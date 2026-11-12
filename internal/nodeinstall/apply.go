//go:build !lite

package nodeinstall

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/amirotin/telemt_panel/internal/nodeagent"
	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/telemt"
)

// Apply rechecks the preview and holds the remote installation open until
// both APIs have responded and the registry update is visible.
func (i *Installer) Apply(ctx context.Context, plan Plan, registry *nodes.Registry, report func(string)) (string, error) {
	client, closeSocket, err := i.connect(ctx, plan.Request)
	if err != nil {
		return "", err
	}
	defer closeSocket()
	verified, err := preflight(client, plan.Request)
	if err != nil {
		return "", err
	}
	if verified != plan {
		return "", ErrInvalid
	}
	report("downloading")
	dir, err := os.MkdirTemp("", "telemt-node-install-")
	if err != nil {
		return "", ErrArtifacts
	}
	defer os.RemoveAll(dir)
	binary, err := i.loadBinary(ctx, plan.Architecture, dir)
	if err != nil {
		return "", ErrArtifacts
	}
	runtimePath, err := os.Executable()
	if err != nil {
		return "", ErrArtifacts
	}
	files, err := newBundle(plan, binary, runtimePath)
	if err != nil {
		return "", err
	}

	report("installing")
	name, err := smallCommand(client, "umask 077; /usr/bin/mktemp -d /var/tmp/telemt-install.XXXXXXXXXX", nil)
	stage := strings.TrimSpace(name)
	if err != nil || !stagePattern.MatchString(stage) {
		return "", ErrApply
	}
	defer func() { _, _ = smallCommand(client, "/bin/rm -rf -- "+shellQuote(stage), nil) }()
	reader, writer := io.Pipe()
	defer reader.Close()
	go func() { _ = writer.CloseWithError(files.archive(writer)) }()
	_, err = smallCommand(client, "/bin/tar -x --no-same-owner --no-same-permissions -C "+shellQuote(stage), reader)
	_ = reader.Close()
	if err != nil {
		return "", ErrApply
	}
	remote, err := begin(client, stage, plan.Request)
	if err != nil {
		return "", err
	}

	report("checking")
	in := nodes.Input{Name: plan.Name, APIURL: plan.APIURL, Enabled: true}
	if plan.WithAgent {
		in.Binding = nodeagent.Binding{URL: plan.APIURL, ID: files.agentID}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	err = checkAPIs(checkCtx, in, files.credentials)
	cancel()
	if err != nil || ctx.Err() != nil {
		if rollbackErr := remote.finish(false); rollbackErr != nil {
			return "", ErrUnconfirmed
		}
		return "", ErrHealth
	}
	n, publishErr := registry.CreateManaged(in, files.credentials)
	if n.ID == "" {
		if rollbackErr := remote.finish(false); rollbackErr != nil {
			return "", ErrUnconfirmed
		}
		return "", ErrRegistry
	}
	if err := remote.finish(true); err != nil || publishErr != nil {
		// The registry write or SSH commit may already have happened. Keep
		// the identity and show the uncertain outcome rather than retrying.
		return n.ID, ErrUnconfirmed
	}
	return n.ID, nil
}

func checkAPIs(ctx context.Context, in nodes.Input, credentials nodes.ManagedCredentials) error {
	trust, err := credentials.TLS()
	if err != nil {
		return err
	}
	control := telemt.NewIsolatedTLS(in.APIURL, credentials.Control, trust)
	defer control.CloseIdleConnections()
	var agent *nodeagent.Client
	if in.Binding.URL != "" {
		agent, err = nodeagent.NewManagedClient(in.Binding, credentials.Agent, trust)
		if err != nil {
			return err
		}
		defer agent.Close()
	}
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		err := probeAPIs(probeCtx, control, agent)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ErrHealth
		case <-time.After(time.Second):
		}
	}
}

func probeAPIs(ctx context.Context, control *telemt.Client, agent *nodeagent.Client) error {
	health, err := control.Health(ctx)
	if err != nil || health.Status != "ok" || health.ReadOnly {
		return ErrHealth
	}
	if agent != nil {
		response, err := agent.Request(ctx, http.MethodGet, "/v1/agent/health", "")
		if err != nil {
			return ErrHealth
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return ErrHealth
		}
	}
	return nil
}
