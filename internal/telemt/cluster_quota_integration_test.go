package telemt

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This optional acceptance runs a real Rust proxy and a loopback fake DC.
// The Linux wrapper disables container networking; no Telegram traffic is sent.
func TestRealClusterQuotaNativeAndControlAPI(t *testing.T) {
	binary := os.Getenv("TELEMT_TEST_BINARY")
	if binary == "" {
		t.Skip("TELEMT_TEST_BINARY is required for real Rust acceptance")
	}
	port := func() int {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return p
	}
	dc, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	payloads := make(chan int64, 8)
	go func() {
		for {
			c, e := dc.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				header := make([]byte, 64)
				if _, e := io.ReadFull(c, header); e != nil {
					return
				}
				n, _ := io.Copy(io.Discard, c)
				payloads <- n
			}()
		}
	}()
	apiPort, proxyPort := port(), port()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "telemt.toml")
	const secret = "55555555555555555555555555555555"
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
auth_header="Bearer quota-fixture"
[censorship]
mask=false
tls_emulation=false
[network]
ipv4=true
ipv6=false
stun_use=false
http_ip_detect_urls=[]
[dc_overrides]
"2"=["%s"]
[access.users]
alice="%s"
[access.user_cluster_quota]
alice=true
`, proxyPort, apiPort, dc.Addr(), secret)
	if e = os.WriteFile(configPath, []byte(config), 0600); e != nil {
		t.Fatal(e)
	}
	client := New(fmt.Sprintf("http://127.0.0.1:%d", apiPort), "Bearer quota-fixture")
	start := func() func() {
		log, e := os.Create(filepath.Join(dir, fmt.Sprintf("process-%d.log", time.Now().UnixNano())))
		if e != nil {
			t.Fatal(e)
		}
		cmd := exec.Command(binary, "run", configPath)
		cmd.Dir = dir
		cmd.Stdout = log
		cmd.Stderr = log
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stop := func() { cmd.Process.Kill(); <-done; log.Close() }
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if _, _, e = client.ClusterQuotaWithRevision(context.Background(), "alice"); e == nil {
				return stop
			}
			select {
			case exit := <-done:
				log.Close()
				raw, _ := os.ReadFile(log.Name())
				t.Fatalf("Rust process exited %v: %s", exit, raw)
			default:
			}
			time.Sleep(25 * time.Millisecond)
		}
		stop()
		raw, _ := os.ReadFile(log.Name())
		t.Fatalf("Rust API not ready: %v: %s", e, raw)
		return nil
	}
	stop := start()
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	ctx := context.Background()
	status, revision, e := client.ClusterQuotaWithRevision(ctx, "alice")
	if e != nil || !status.Required || status.Grant != nil || status.AvailableBytes != 0 {
		t.Fatal(status, e)
	}
	// Discovery metadata must stay usable when a config field/route is added.
	for _, path := range []string{"/v1/capabilities", "/v1/config/schema"} {
		if _, _, e = client.call(ctx, "GET", path, nil); e != nil {
			t.Fatal(path, e)
		}
	}
	connect := func(payload int) net.Conn {
		conn, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		header := bytes.Repeat([]byte{0x5a}, 64)
		if _, e = rand.Read(header[8:56]); e != nil {
			t.Fatal(e)
		}
		secretBytes, _ := hex.DecodeString(secret)
		keyInput := append(append([]byte{}, header[8:40]...), secretBytes...)
		key := sha256.Sum256(keyInput)
		block, _ := aes.NewCipher(key[:])
		stream := cipher.NewCTR(block, header[40:56])
		pad := make([]byte, 64)
		stream.XORKeyStream(pad, pad)
		plain := make([]byte, 64)
		copy(plain[56:60], []byte{0xee, 0xee, 0xee, 0xee})
		plain[60] = 2
		for n := 56; n < 64; n++ {
			header[n] = plain[n] ^ pad[n]
		}
		if _, e = conn.Write(header); e != nil {
			t.Fatal(e)
		}
		if payload > 0 {
			data := bytes.Repeat([]byte{0x42}, payload)
			stream.XORKeyStream(data, data)
			if _, e = conn.Write(data); e != nil {
				t.Fatal(e)
			}
		}
		return conn
	}
	// No grant must reject before a DC connection is established.
	denied := connect(0)
	buf := make([]byte, 1)
	if _, e = denied.Read(buf); e == nil {
		t.Fatal("unfunded user remained open")
	}
	denied.Close()
	select {
	case n := <-payloads:
		t.Fatal("unfunded DC connection", n)
	default:
	}
	now := time.Now().Unix()
	grant := ClusterQuotaGrant{BootID: status.BootID, PeriodID: "p_fixture", Sequence: 1, GrantedBytes: 100, PeriodStartsAt: now - 1, PeriodEndsAt: now + 3600, ValidUntil: now + 50}
	if _, _, e = client.ApplyClusterQuota(ctx, "alice", status.SecretHash, "stale", grant); e == nil {
		t.Fatal("stale revision accepted")
	}
	if _, _, e = client.ApplyClusterQuota(ctx, "alice", "wrong", revision, grant); e == nil {
		t.Fatal("wrong credential accepted")
	}
	applied, _, e := client.ApplyClusterQuota(ctx, "alice", status.SecretHash, revision, grant)
	if e != nil || applied.AvailableBytes != 100 {
		t.Fatal(applied, e)
	}
	active := connect(300)
	defer active.Close()
	select {
	case n := <-payloads:
		if n != 100 {
			t.Fatalf("native forwarded %d, grant 100", n)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("native DC relay did not settle")
	}
	observed, _, e := client.ClusterQuotaWithRevision(ctx, "alice")
	if e != nil || observed.ChargedBytes != 100 || observed.AvailableBytes != 0 {
		t.Fatal(observed, e)
	}
	replay, _, e := client.ApplyClusterQuota(ctx, "alice", status.SecretHash, revision, grant)
	if e != nil || replay.AvailableBytes != 0 {
		t.Fatal("replay credited bytes", replay, e)
	}
	// Resetting old local counters cannot replenish the managed debit.
	if _, e = client.ResetQuotaWithRevision(ctx, "alice", revision); e != nil {
		t.Fatal(e)
	}
	afterReset, _, e := client.ClusterQuotaWithRevision(ctx, "alice")
	if e != nil || afterReset.ChargedBytes != 100 || afterReset.AvailableBytes != 0 {
		t.Fatal(afterReset, e)
	}
	grant.Sequence = 2
	grant.GrantedBytes = 200
	if _, _, e = client.ApplyClusterQuota(ctx, "alice", status.SecretHash, revision, grant); e != nil {
		t.Fatal(e)
	}
	second := connect(300)
	defer second.Close()
	select {
	case n := <-payloads:
		if n != 100 {
			t.Fatalf("refill forwarded %d, expected additional 100", n)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("refill did not settle")
	}
	// A rolling allocation pauses the SAME native connection until refill;
	// it must neither forward unreserved bytes nor force a reconnect.
	grant.Sequence = 3
	grant.GrantedBytes = 250
	grant.Refillable = true
	if _, _, e = client.ApplyClusterQuota(ctx, "alice", status.SecretHash, revision, grant); e != nil {
		t.Fatal(e)
	}
	rolling := connect(300)
	defer rolling.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, _, err := client.ClusterQuotaWithRevision(ctx, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if current.ChargedBytes == 250 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rolling debit did not pause", current)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case n := <-payloads:
		t.Fatal("rolling grant closed connection before refill", n)
	default:
	}
	grant.Sequence = 4
	grant.GrantedBytes = 400
	grant.Refillable = false
	if _, _, e = client.ApplyClusterQuota(ctx, "alice", status.SecretHash, revision, grant); e != nil {
		t.Fatal(e)
	}
	select {
	case n := <-payloads:
		if n != 200 {
			t.Fatalf("rolling connection forwarded %d, expected 200", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("same native connection did not resume")
	}
	stop()
	stop = nil
	stop = start()
	restarted, rev, e := client.ClusterQuotaWithRevision(ctx, "alice")
	if e != nil || restarted.BootID == status.BootID || restarted.AvailableBytes != 0 {
		t.Fatal(restarted, e)
	}
	_, _, e = client.ApplyClusterQuota(ctx, "alice", restarted.SecretHash, rev, grant)
	var apiErr *APIError
	if !errors.As(e, &apiErr) || apiErr.Status != 409 {
		t.Fatal("old boot accepted", e)
	}
}
