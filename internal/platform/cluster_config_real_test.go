//go:build !lite

package platform

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/telemt"
)

func TestClusterConfigurationAcrossRealTelemtNodes(t *testing.T) {
	binary := os.Getenv("TELEMT_TEST_BINARY")
	if binary == "" {
		t.Skip("requires isolated real Rust binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	start := func(handshake, keepalive int) *telemt.Client {
		t.Helper()
		freePort := func() int {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			return port
		}
		proxyPort, apiPort := freePort(), freePort()
		for proxyPort == apiPort {
			apiPort = freePort()
		}
		dir := t.TempDir()
		configPath := filepath.Join(dir, "telemt.toml")
		config := fmt.Sprintf(`[general]
use_middle_proxy=false
middle_proxy_nat_probe=false
log_level="silent"
[general.modes]
classic=true
secure=true
tls=false
[server]
port=%d
[[server.listeners]]
ip="127.0.0.1"
[server.api]
enabled=true
read_only=false
listen="127.0.0.1:%d"
whitelist=["127.0.0.1/32"]
auth_header="Bearer cluster-config-fixture"
[censorship]
mask=false
tls_emulation=false
[network]
ipv4=true
ipv6=false
stun_use=false
http_ip_detect_urls=[]
[timeouts]
client_handshake=%d
client_keepalive=%d
[access.users]
seed="77777777777777777777777777777777"
`, proxyPort, apiPort, handshake, keepalive)
		if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		log, err := os.Create(filepath.Join(dir, "process.log"))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, binary, "run", configPath)
		cmd.Dir, cmd.Stdout, cmd.Stderr = dir, log, log
		if err := cmd.Start(); err != nil {
			log.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = log.Close() })
		client := telemt.New(fmt.Sprintf("http://127.0.0.1:%d", apiPort), "Bearer cluster-config-fixture")
		for deadline := time.Now().Add(12 * time.Second); ; time.Sleep(25 * time.Millisecond) {
			if _, _, err := client.GetConfig(ctx); err == nil {
				return client
			}
			if time.Now().After(deadline) {
				raw, _ := os.ReadFile(log.Name())
				t.Fatalf("Telemt did not start: %s", raw)
			}
		}
	}
	source, destination := start(17, 65), start(11, 71)
	store, _, _ := testStore(t)
	cluster, err := store.CreateCluster(ctx, "Real configuration")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMember(ctx, cluster.ID, 1, "source"); err != nil {
		t.Fatal(err)
	}
	sections, _, err := source.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	values, err := ReadClusterConfigValues(sections, []string{"timeouts.client_handshake"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutClusterConfig(ctx, cluster.ID, 2, "source", values); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMember(ctx, cluster.ID, 3, "destination"); err != nil {
		t.Fatal(err)
	}
	clients := map[string]*telemt.Client{"source": source, "destination": destination}
	worker := NewWorker(store, func(ctx context.Context, id string, run func(context.Context, NodeClient) error) error {
		return run(ctx, clients[id])
	})
	for deadline := time.Now().Add(32 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		_, err := worker.processClusterConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		state, err := store.ClusterConfig(ctx, cluster.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Jobs) == 2 && state.Jobs[0].Status == "applied" && state.Jobs[1].Status == "applied" {
			break
		}
		for _, job := range state.Jobs {
			if job.Status == "conflict" {
				_, revision, _ := destination.GetConfig(ctx)
				patch := map[string]any{"timeouts": map[string]any{"client_handshake": 17}}
				validation, validationErr := destination.ValidateClusterConfig(ctx, patch, revision)
				t.Fatalf("configuration conflict: %+v, paths: changed=%v watcher=%v generation=%v, validation=%v",
					job, validation.ChangedPaths, validation.Apply.WatcherPaths, validation.Apply.GenerationPaths, validationErr)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("configuration did not converge: %+v", state.Jobs)
		}
	}
	sections, _, err = destination.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	handshake, _ := readConfigField(sections, "timeouts.client_handshake")
	keepalive, _ := readConfigField(sections, "timeouts.client_keepalive")
	if string(handshake) != "17" || string(keepalive) != "71" {
		t.Fatalf("wrong shared or local value: handshake=%s keepalive=%s", handshake, keepalive)
	}
}
