//go:build !lite

package nodeinstall

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/amirotin/telemt_panel/internal/nodeagent"
	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/store"
	"golang.org/x/crypto/ssh"
)

func key(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func startTestSSH(t *testing.T, hostKey, userKey ssh.Signer, execute func(string, ssh.Channel) uint32) int {
	t.Helper()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(conn ssh.ConnMetadata, public ssh.PublicKey) (*ssh.Permissions, error) {
		if conn.User() != "root" || !bytes.Equal(public.Marshal(), userKey.PublicKey().Marshal()) {
			return nil, errors.New("unknown account")
		}
		return nil, nil
	}}
	cfg.AddHostKey(hostKey)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer raw.Close()
				conn, channels, requests, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					if channel.ChannelType() != "session" {
						_ = channel.Reject(ssh.UnknownChannelType, "session required")
						continue
					}
					session, reqs, err := channel.Accept()
					if err != nil {
						return
					}
					go func() {
						defer session.Close()
						for req := range reqs {
							var payload struct{ Command string }
							if req.Type != "exec" || ssh.Unmarshal(req.Payload, &payload) != nil {
								_ = req.Reply(false, nil)
								continue
							}
							_ = req.Reply(true, nil)
							exit := execute(payload.Command, session)
							_, _ = session.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exit}))
							return
						}
					}()
				}
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestSSHHostKeyVerifiedBeforeRemoteCommand(t *testing.T) {
	hostKey, loginKey, wrongKey := key(t), key(t), key(t)
	var commands atomic.Int32
	port := startTestSSH(t, hostKey, loginKey, func(_ string, _ ssh.Channel) uint32 {
		commands.Add(1)
		return 0
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	i := Installer{signer: loginKey}
	r := Request{Host: "127.0.0.1", SSHPort: port, SSHUser: "root", HostFingerprint: ssh.FingerprintSHA256(wrongKey.PublicKey())}
	if _, _, err := i.connect(ctx, r); !errors.Is(err, ErrHostKey) || commands.Load() != 0 {
		t.Fatalf("untrusted server accepted: %v", err)
	}
	r.HostFingerprint = ssh.FingerprintSHA256(hostKey.PublicKey())
	conn, closeSocket, err := i.connect(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSocket()
	if _, err := smallCommand(conn, "/bin/true", nil); err != nil || commands.Load() != 1 {
		t.Fatalf("authorized SSH request failed: %v", err)
	}
}

func TestFileBinaryStagingAndPlan(t *testing.T) {
	payload := []byte("test artifact")
	file := filepath.Join(t.TempDir(), "trusted-artifact")
	if err := os.WriteFile(file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	artifact := FileBinary{Path: file, SHA256: fmt.Sprintf("%x", sha256.Sum256(payload)), Version: "3.5.17-file.1"}
	plan := artifact.configure(Plan{Request: Request{WithAgent: true}, Release: Release})
	if !plan.ConfigFiles || plan.Release != artifact.Version || artifact.configure(Plan{}).ConfigFiles {
		t.Fatal("artifact must be selected only for agent installations")
	}
	staged, err := artifact.stage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(staged); err != nil || !bytes.Equal(raw, payload) {
		t.Fatalf("staged artifact differs: %v", err)
	}
	if err := os.WriteFile(file, []byte("different artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.stage(t.TempDir()); !errors.Is(err, ErrArtifacts) {
		t.Fatalf("incorrect digest was accepted: %v", err)
	}
	files, err := newBundle(Plan{Request: Request{Host: "127.0.0.1", WithAgent: true}, ConfigFiles: true}, staged, staged)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files.files["agent.toml"], `config_path = "/var/lib/private/telemt-node/telemt.toml"`) ||
		!strings.Contains(files.files["telemt-agent.service"], "User=telemt\nStateDirectory=telemt-agent telemt-node\n") {
		t.Fatal("file editor must operate on the proxy's live state directory")
	}
}

// This fixture speaks real SSH and TLS. Remote systemctl and the Telemt
// application are emulated. It verifies the transaction and restart of the
// registry without giving a test process remote root access.
func TestInstallTransactionWithSSHAndTLS(t *testing.T) {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if !supportsArchitecture(arch) {
		t.Skip("requires a supported Linux installer binary")
	}
	for _, scenario := range []struct {
		name                                 string
		withAgent, rejectRegistry, rejectTLS bool
	}{
		{name: "control"},
		{name: "with agent", withAgent: true},
		{name: "abort on duplicate registry URL", withAgent: true, rejectRegistry: true},
		{name: "abort when TLS activity is disabled", rejectTLS: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			withAgent := scenario.withAgent
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			var committed, aborted atomic.Bool
			files := make(map[string]string)
			var stateMu atomic.Bool
			httpServer := &http.Server{}
			t.Cleanup(func() { _ = httpServer.Close() })
			hostKey, loginKey := key(t), key(t)
			port := startTestSSH(t, hostKey, loginKey, func(command string, channel ssh.Channel) uint32 {
				switch {
				case strings.HasPrefix(command, "/bin/bash -s --"):
					_, _ = io.Copy(io.Discard, channel)
					_, _ = io.WriteString(channel, arch+"\n127.0.0.1\n")
				case strings.HasPrefix(command, "umask 077; /usr/bin/mktemp "):
					_, _ = io.WriteString(channel, "/var/tmp/telemt-install.ABCDEFGHIJ\n")
				case strings.HasPrefix(command, "/bin/tar "):
					tr := tar.NewReader(channel)
					for {
						entry, err := tr.Next()
						if err == io.EOF {
							break
						}
						if err != nil {
							return 1
						}
						if entry.Name == "telemt" || entry.Name == "node-runtime" {
							_, _ = io.Copy(io.Discard, tr)
							continue
						}
						raw, err := io.ReadAll(tr)
						if err != nil {
							return 1
						}
						files[entry.Name] = string(raw)
					}
					stateMu.Store(true)
				case strings.HasPrefix(command, "/bin/bash -c "):
					if !stateMu.Load() {
						return 1
					}
					cert, err := tls.X509KeyPair([]byte(files["gateway.crt"]), []byte(files["gateway.key"]))
					if err != nil {
						return 1
					}
					var cfg struct {
						Server struct {
							API struct {
								Auth        string `toml:"auth_header"`
								RuntimeEdge bool   `toml:"runtime_edge_enabled"`
							}
						}
					}
					if _, err := toml.Decode(files["telemt.toml"], &cfg); err != nil {
						return 1
					}
					if !cfg.Server.API.RuntimeEdge {
						t.Error("SSH installation must enable TLS telemetry in server.api")
						return 1
					}
					var agentCfg struct{ Agent struct{ ID string } }
					if withAgent {
						if _, err := toml.Decode(files["agent.toml"], &agentCfg); err != nil || agentCfg.Agent.ID == "" {
							return 1
						}
					}
					control := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("Authorization") != cfg.Server.API.Auth {
							w.WriteHeader(401)
							return
						}
						switch r.URL.Path {
						case "/v1/health":
							_, _ = io.WriteString(w, `{"ok":true,"data":{"status":"ok","read_only":false}}`)
						case "/v1/runtime/tls-fingerprints":
							if scenario.rejectTLS {
								_, _ = io.WriteString(w, `{"ok":true,"data":{"enabled":false,"reason":"feature_disabled","data":null}}`)
								return
							}
							_, _ = io.WriteString(w, `{"ok":true,"data":{"enabled":true,"data":{"retention_secs":3600,"by_fingerprint":[],"by_ip":[],"by_cidr":[],"by_user":[]}}}`)
						default:
							w.WriteHeader(404)
						}
					})
					var agent http.Handler
					if withAgent {
						agent = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							env := strings.TrimSpace(files["agent.env"])
							if r.Header.Get(nodeagent.IdentityHeader) != agentCfg.Agent.ID ||
								env != "TELEMT_AGENT_AUTH=\""+r.Header.Get("Authorization")+"\"" {
								w.WriteHeader(401)
								return
							}
							w.Header().Set(nodeagent.IdentityHeader, agentCfg.Agent.ID)
							switch r.URL.Path {
							case "/v1/agent/host":
								_, _ = io.WriteString(w, `{"caps":{"start_telemt":true,"stop_telemt":true,"restart_telemt":true,"log_tail":true,"log_stream":true}}`)
							case "/v1/agent/service":
								_, _ = io.WriteString(w, `{"status":"running"}`)
							case "/v1/agent/config":
								_, _ = io.WriteString(w, `{"protocol":1,"revision":"fixture"}`)
							default:
								w.WriteHeader(404)
							}
						})
					}
					httpServer.Handler = gatewayHandler(netip.MustParseAddr("127.0.0.1"), control, agent)
					httpServer.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
					go func() { _ = httpServer.ServeTLS(listener, "", "") }()
					_, _ = io.WriteString(channel, "ready\n")
					line, _ := io.ReadAll(channel)
					committed.Store(string(line) == "commit\n")
					aborted.Store(string(line) == "abort\n")
				case strings.HasPrefix(command, "/bin/rm "):
				default:
					return 1
				}
				return 0
			})

			dir := t.TempDir()
			state, err := store.NewState(filepath.Join(dir, "panel.json"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			stateKey := bytes.Repeat([]byte{7}, 32)
			registry, err := nodes.OpenWithKey(state, "", "", stateKey)
			if err != nil {
				t.Fatal(err)
			}
			defer registry.Close()
			i := Installer{signer: loginKey, loadBinary: func(_ context.Context, _, dir string) (string, error) {
				f := filepath.Join(dir, "telemt")
				return f, os.WriteFile(f, []byte("fixture"), 0700)
			}}
			r := Request{Name: "Fixture", Host: "127.0.0.1", SSHPort: port, SSHUser: "root", HostFingerprint: ssh.FingerprintSHA256(hostKey.PublicKey()),
				ProxyPort: 443, ManagementPort: listener.Addr().(*net.TCPAddr).Port, WithAgent: withAgent}
			plan := Plan{Request: r, Architecture: arch, PanelPeer: "127.0.0.1", Release: Release, APIURL: "https://" + listener.Addr().String()}
			if withAgent {
				path := filepath.Join(dir, "approved")
				artifact := []byte("fixture")
				if err := os.WriteFile(path, artifact, 0700); err != nil {
					t.Fatal(err)
				}
				i.fileBinary = FileBinary{Path: path, SHA256: fmt.Sprintf("%x", sha256.Sum256(artifact)), Version: Release}
				plan = i.fileBinary.configure(plan)
			}
			if scenario.rejectRegistry {
				if _, err := registry.Create(nodes.Input{Name: "Existing", APIURL: plan.APIURL, Enabled: true}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			id, err := i.Apply(ctx, plan, registry, func(string) {})
			if scenario.rejectTLS {
				if !errors.Is(err, ErrTLSActivity) || id != "" || committed.Load() || !aborted.Load() || len(registry.List()) != 0 {
					t.Fatalf("unavailable TLS activity did not abort remote installation: %v", err)
				}
				return
			}
			if scenario.rejectRegistry {
				if !errors.Is(err, ErrRegistry) || id != "" || committed.Load() || !aborted.Load() || len(registry.List()) != 1 {
					t.Fatalf("registry rejection did not abort remote installation: %v", err)
				}
				return
			}
			if err != nil || id == "" || !committed.Load() {
				t.Fatalf("installation failed: %v", err)
			}

			raw, _, _ := state.GetSetting("nodes.registry.v1")
			if strings.Contains(raw, "Bearer ") || strings.Contains(raw, "PRIVATE KEY") {
				t.Fatal("plaintext credential persisted")
			}
			reopened, err := nodes.OpenWithKey(state, "", "", stateKey)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			node, client, err := reopened.Resolve(id)
			if err != nil || !node.ManagedCredentials {
				t.Fatalf("installed node credentials not persisted: %v", err)
			}
			if health, err := client.Health(ctx); err != nil || health.Status != "ok" {
				t.Fatalf("persisted TLS connection failed: %v", err)
			}
			if withAgent {
				agent, err := reopened.AgentClient(node)
				if err != nil {
					t.Fatal(err)
				}
				defer agent.Close()
				if err := probeAPIs(ctx, client, agent, false); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
