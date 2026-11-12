//go:build !lite

package nodeinstall

import (
	"archive/tar"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/update"
)

type releaseAsset struct {
	architecture string
	digest       string
}

// Official baseline Linux static archives, checked against the release assets.
var releaseAssets = map[string]releaseAsset{
	"x86_64":  {"amd64", "f1591635849e0aad31f147692b21726da2f49499aae3ece79f8760c92925133a"},
	"aarch64": {"arm64", "c80b0159b41cf410b2a87cf62676d67a16a537bd7207602b60dd9ce56e14d1f6"},
}

func supportsArchitecture(remote string) bool {
	return runtime.GOOS == "linux" && releaseAssets[remote].architecture == runtime.GOARCH
}

func releaseBinary(ctx context.Context, architecture, dir string) (string, error) {
	asset, ok := releaseAssets[architecture]
	if !ok {
		return "", ErrArtifacts
	}
	link := "https://github.com/telemt/telemt/releases/download/" + Release +
		"/telemt-" + architecture + "-linux-musl.tar.gz"
	return update.FetchVerifiedBinary(ctx, link, asset.digest, dir)
}

type bundle struct {
	files       map[string]string
	binaries    map[string]string
	credentials nodes.ManagedCredentials
	agentID     string
}

func newBundle(plan Plan, telemtPath, runtimePath string) (bundle, error) {
	cert, key, err := issueTLS(plan.Host)
	if err != nil {
		return bundle{}, ErrArtifacts
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	defer clear(key)
	defer clear(keyPEM)
	b := bundle{credentials: nodes.ManagedCredentials{Control: "Bearer " + token(32), Certificate: cert},
		binaries: map[string]string{"telemt": telemtPath, "node-runtime": runtimePath},
		files:    map[string]string{"preflight": preflightScript, "gateway.crt": string(certPEM), "gateway.key": string(keyPEM)}}
	b.files["telemt.toml"] = fmt.Sprintf(telemtConfig, plan.ProxyPort, strconv.Quote(b.credentials.Control), token(16))
	b.files["telemt.service"] = telemtUnit
	agentFlag := ""
	if plan.WithAgent {
		b.agentID = token(16)
		b.credentials.Agent = "Bearer " + token(32)
		b.files["agent.toml"] = fmt.Sprintf(agentConfig, b.agentID)
		b.files["agent.env"] = "TELEMT_AGENT_AUTH=" + strconv.Quote(b.credentials.Agent) + "\n"
		b.files["telemt-agent.service"] = agentUnit
		agentFlag = " --agent"
	}
	b.files["telemt-node-gateway.service"] = fmt.Sprintf(gatewayUnit, plan.ManagementPort, plan.PanelPeer, agentFlag)
	return b, nil
}

func token(size int) string {
	raw := make([]byte, size)
	_, _ = rand.Read(raw)
	defer clear(raw)
	return hex.EncodeToString(raw)
}

func issueTLS(host string) ([]byte, []byte, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	cert := &x509.Certificate{
		SerialNumber: serial, NotBefore: time.Now().Add(-5 * time.Minute),
		NotAfter: time.Now().AddDate(5, 0, 0), IPAddresses: []net.IP{net.ParseIP(host)},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &private.PublicKey, private)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	return der, key, err
}

func (b bundle) archive(output io.Writer) error {
	tw := tar.NewWriter(output)
	for name, content := range b.files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content))}); err != nil {
			return err
		}
		if _, err := io.WriteString(tw, content); err != nil {
			return err
		}
	}
	for name, path := range b.binaries {
		if err := writeBinary(tw, name, path); err != nil {
			return err
		}
	}
	return tw.Close()
}

func writeBinary(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 128<<20 {
		return ErrArtifacts
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0700, Size: info.Size()}); err != nil {
		return err
	}
	_, err = io.CopyN(tw, f, info.Size())
	return err
}

const telemtConfig = `[general.modes]
classic = false
secure = false
tls = true

[server]
port = %d

[server.api]
enabled = true
listen = "127.0.0.1:9091"
whitelist = ["127.0.0.1/32"]
auth_header = %s
read_only = false

[access.users]
initial = "%s"
`

const telemtUnit = `[Unit]
Description=Telemt node
After=network-online.target
Wants=network-online.target

[Service]
Type=exec
DynamicUser=yes
StateDirectory=telemt-node
StateDirectoryMode=0700
WorkingDirectory=/var/lib/telemt-node
LoadCredential=telemt.toml:/opt/telemt-node/telemt.toml
ExecStartPre=/bin/sh -c 'test -e /var/lib/telemt-node/telemt.toml || install -m 0600 %d/telemt.toml /var/lib/telemt-node/telemt.toml'
# DynamicUser makes /var/lib/telemt-node a symlink. Telemt rejects symlink components.
ExecStart=/opt/telemt-node/telemt /var/lib/private/telemt-node/telemt.toml
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
UMask=0077
Restart=on-failure
RestartSec=3s

[Install]
WantedBy=multi-user.target
`

const gatewayUnit = `[Unit]
Description=Telemt node HTTPS management
After=telemt.service

[Service]
Type=exec
DynamicUser=yes
LoadCredential=gateway.crt:/opt/telemt-node/gateway.crt
LoadCredential=gateway.key:/opt/telemt-node/gateway.key
ExecStart=/opt/telemt-node/node-runtime node-gateway --listen=:%d --peer=%s --cert=%%d/gateway.crt --key=%%d/gateway.key%s
CapabilityBoundingSet=
NoNewPrivileges=yes
UMask=0077
Restart=on-failure
RestartSec=3s

[Install]
WantedBy=multi-user.target
`

const agentUnit = `[Unit]
Description=Telemt node host agent
After=telemt.service

[Service]
Type=exec
DynamicUser=yes
StateDirectory=telemt-agent
StateDirectoryMode=0700
SupplementaryGroups=systemd-journal
EnvironmentFile=/opt/telemt-node/agent.env
LoadCredential=agent.toml:/opt/telemt-node/agent.toml
ExecStart=/opt/telemt-node/node-runtime --config %d/agent.toml
CapabilityBoundingSet=
NoNewPrivileges=yes
UMask=0077
Restart=on-failure
RestartSec=3s

[Install]
WantedBy=multi-user.target
`

const agentConfig = `listen = "127.0.0.1:9092"
data_dir = "/var/lib/telemt-agent"

[agent]
enabled = true
id = "%s"
authorization_env = "TELEMT_AGENT_AUTH"

[telemt]
url = "http://127.0.0.1:9091"

[host]
service_manager = "systemd"
log_source = "journald"
telemt_service = "telemt"
panel_service = "telemt-agent"

[privileges]
mode = "manual"

[store]
driver = "memory"
`
