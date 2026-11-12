//go:build !lite

package nodeinstall

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/amirotin/telemt_panel/internal/nodeagent"
	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/telemt"
	"github.com/amirotin/telemt_panel/internal/update"
	"golang.org/x/crypto/ssh"
)

//go:embed upgrade.sh
var upgradeScript string

const installedConfigPath = "/var/lib/private/telemt-node/telemt.toml"

func (i *Installer) UpgradeTelemtVersion() string { return i.fileBinary.Version }

// Upgrade uses the installation SSH credential and the existing management
// identity. Neither endpoint may be selected by the browser.
func (i *Installer) Upgrade(ctx context.Context, registry *nodes.Registry, nodeID string, revision uint64,
	req UpgradeRequest, panelVersion string, report func(string)) (state string, err error) {
	node, control, err := registry.Resolve(nodeID)
	if err != nil || node.Revision != revision || !node.ManagedCredentials || node.Binding.URL != node.APIURL {
		return UpgradeFailed, ErrRegistry
	}
	endpoint, err := url.Parse(node.APIURL)
	if err != nil {
		return UpgradeFailed, ErrInvalid
	}
	host, err := netip.ParseAddr(endpoint.Hostname())
	if err != nil || host.Zone() != "" || !host.IsGlobalUnicast() || !req.Valid() {
		return UpgradeFailed, ErrInvalid
	}
	agent, err := registry.AgentClient(node)
	if err != nil {
		return UpgradeFailed, ErrRegistry
	}
	defer agent.Close()

	before, err := readUpgradeSnapshot(ctx, control, agent, node.Binding.ID)
	if err != nil {
		return UpgradeFailed, err
	}
	if !supportsArchitecture(before.arch) {
		return UpgradeFailed, ErrUnsupported
	}
	if req.Telemt && (i.fileBinary.Path == "" || !canUpgradeVersion(before.telemt, i.fileBinary.Version)) {
		return UpgradeFailed, ErrArtifacts
	}
	access := Request{Host: host.String(), SSHPort: req.SSHPort, SSHUser: req.SSHUser, HostFingerprint: req.HostFingerprint}
	client, closeSocket, err := i.connect(ctx, access)
	if err != nil {
		return UpgradeFailed, err
	}
	defer closeSocket()
	dir, err := os.MkdirTemp("", "telemt-node-upgrade-")
	if err != nil {
		return UpgradeFailed, ErrArtifacts
	}
	defer os.RemoveAll(dir)
	report("staging")
	files, err := i.upgradeBundle(req, dir)
	if err != nil {
		return UpgradeFailed, err
	}
	stage, cleanup, err := transferBundle(client, files)
	if err != nil {
		return UpgradeFailed, err
	}
	defer cleanup()
	if !registry.Current(node) {
		return UpgradeFailed, ErrRegistry
	}
	report("restarting")
	command := fmt.Sprintf("/bin/bash -c %s -- %s %s", shellQuote(upgradeScript), shellQuote(stage), shellQuote(before.arch))
	transaction, err := beginRemote(client, command)
	if err != nil {
		var exit *ssh.ExitError
		switch {
		case errors.As(err, &exit) && exit.ExitStatus() == 26:
			return UpgradeUnchanged, nil
		case errors.As(err, &exit) && exit.ExitStatus() == 24:
			return confirmUpgradeRollback(ctx, control, agent, node.Binding.ID, before, report, ErrUpgradeStart)
		case errors.As(err, &exit) && exit.ExitStatus() == 11:
			return UpgradeFailed, ErrUnsupported
		case errors.Is(err, ErrUnconfirmed):
			return UpgradeUnknown, ErrUnconfirmed
		default:
			return UpgradeFailed, ErrApply
		}
	}

	wanted := before
	if req.Telemt {
		wanted.telemt = i.fileBinary.Version
	}
	if req.Agent {
		wanted.agent = panelVersion
	}
	report("checking")
	verified := awaitUpgradeSnapshot(ctx, control, agent, node.Binding.ID, wanted, 40*time.Second)
	current := registry.Current(node)
	if !verified || !current {
		cause := ErrUpgradeCheck
		if !current {
			cause = ErrRegistry
		}
		report("rolling_back")
		if err := transaction.finish(false); err != nil {
			return UpgradeUnknown, ErrUnconfirmed
		}
		return confirmUpgradeRollback(ctx, control, agent, node.Binding.ID, before, report, cause)
	}
	if err := transaction.finish(true); err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) && exit.ExitStatus() == 24 {
			return confirmUpgradeRollback(ctx, control, agent, node.Binding.ID, before, report, ErrUpgradeCheck)
		}
		return UpgradeUnknown, ErrUnconfirmed
	}
	return UpgradeUpdated, nil
}

func canUpgradeVersion(current, candidate string) bool {
	_, _, _, _, oldErr := update.ParseVersion(current)
	_, _, _, _, newErr := update.ParseVersion(candidate)
	return oldErr == nil && newErr == nil && update.CompareVersions(candidate, current) >= 0
}

func (i *Installer) upgradeBundle(req UpgradeRequest, dir string) (bundle, error) {
	files := bundle{files: make(map[string]string), binaries: make(map[string]string)}
	if req.Telemt {
		path, err := i.fileBinary.stage(dir)
		if err != nil {
			return bundle{}, err
		}
		files.binaries["telemt"] = path
	}
	if req.Agent {
		// The running inode is the same build as the current panel version,
		// even when an installer has staged a replacement executable.
		files.binaries["node-runtime"] = "/proc/self/exe"
	}
	var checksums strings.Builder
	for name, path := range files.binaries {
		digest, err := upgradeChecksum(path)
		if err != nil {
			return bundle{}, err
		}
		fmt.Fprintf(&checksums, "%x  %s\n", digest, name)
	}
	files.files["checksums"] = checksums.String()
	return files, nil
}

func upgradeChecksum(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrArtifacts
	}
	defer f.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, io.LimitReader(f, maxBundleBinarySize+1))
	if err != nil || size == 0 || size > maxBundleBinarySize {
		return nil, ErrArtifacts
	}
	return digest.Sum(nil), nil
}

type upgradeSnapshot struct {
	telemt, agent, arch, revision string
	readOnly                      bool
	caps                          upgradeCapabilities
}

type upgradeCapabilities struct {
	ConfigFile bool `json:"config_file"`
	Start      bool `json:"start_telemt"`
	Stop       bool `json:"stop_telemt"`
	Restart    bool `json:"restart_telemt"`
	LogTail    bool `json:"log_tail"`
	LogStream  bool `json:"log_stream"`
}

func readUpgradeSnapshot(ctx context.Context, control *telemt.Client, agent *nodeagent.Client, agentID string) (upgradeSnapshot, error) {
	health, err := control.Health(ctx)
	if err != nil || health.Status != "ok" {
		return upgradeSnapshot{}, ErrHealth
	}
	info, err := control.SystemInfo(ctx)
	if err != nil || info.Version == "" || info.TargetOS != "linux" || info.ConfigPath != installedConfigPath {
		return upgradeSnapshot{}, ErrUnsupported
	}
	var identity struct {
		ID      string `json:"agent_id"`
		Version string `json:"version"`
	}
	if err := readUpgradeAgent(ctx, agent, "/v1/agent/health", &identity); err != nil || identity.ID != agentID || identity.Version == "" {
		return upgradeSnapshot{}, ErrHealth
	}
	var host struct {
		Caps *upgradeCapabilities `json:"caps"`
	}
	if err := readUpgradeAgent(ctx, agent, "/v1/agent/host", &host); err != nil || host.Caps == nil {
		return upgradeSnapshot{}, ErrHealth
	}
	snapshot := upgradeSnapshot{telemt: strings.TrimPrefix(info.Version, "v"), agent: identity.Version, arch: info.TargetArch, readOnly: health.ReadOnly, caps: *host.Caps}
	if snapshot.caps.ConfigFile {
		var config struct {
			Protocol int    `json:"protocol"`
			Revision string `json:"revision"`
		}
		if err := readUpgradeAgent(ctx, agent, "/v1/agent/config", &config); err != nil || config.Protocol != 1 || config.Revision == "" {
			return upgradeSnapshot{}, ErrManagement
		}
		snapshot.revision = config.Revision
	}
	return snapshot, nil
}

func readUpgradeAgent(ctx context.Context, agent *nodeagent.Client, path string, into any) error {
	res, err := agent.Request(ctx, http.MethodGet, path, "")
	if err != nil {
		return err
	}
	limit := 2 << 20
	if path == "/v1/agent/config" {
		limit = nodeagent.MaxConfigResponse
	}
	raw, err := nodeagent.ReadLimitedBody(res, limit)
	defer clear(raw)
	if err != nil || res.StatusCode != http.StatusOK {
		return ErrHealth
	}
	return json.Unmarshal(raw, into)
}

func awaitUpgradeSnapshot(ctx context.Context, control *telemt.Client, agent *nodeagent.Client, agentID string, wanted upgradeSnapshot, limit time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	wanted.telemt = strings.TrimPrefix(wanted.telemt, "v")
	for {
		probeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		got, err := readUpgradeSnapshot(probeCtx, control, agent, agentID)
		stop()
		if err == nil && got == wanted {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}

func confirmUpgradeRollback(ctx context.Context, control *telemt.Client, agent *nodeagent.Client, agentID string,
	before upgradeSnapshot, report func(string), cause error) (string, error) {
	report("rolling_back")
	if !awaitUpgradeSnapshot(ctx, control, agent, agentID, before, 30*time.Second) {
		return UpgradeUnknown, ErrUnconfirmed
	}
	return UpgradeRolledBack, cause
}
