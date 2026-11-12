//go:build !lite

package platform

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/amirotin/telemt_panel/internal/telemt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Optional integration: real API/runtime on two processes and a local fake DC.
// No wall-clock injection into runtime grants and no mocked provisioning calls.
func TestPaidQuotaAcrossTwoRealTelemtNodes(t *testing.T) {
	binary := os.Getenv("TELEMT_TEST_BINARY")
	if binary == "" {
		t.Skip("requires isolated real Rust binary")
	}
	s, p, now := billingFixture(t)
	*now = time.Now().Unix()
	s.now = time.Now
	ctx := context.Background()
	quota, overrun := int64(1000), int64(50)
	p.Terms.QuotaBytes = &quota
	p.Terms.OverrunBytes = &overrun
	if e := s.UpdatePlan(ctx, p.ID, p.Revision, p.Name, true, p.Terms); e != nil {
		t.Fatal(e)
	}
	// The invoice snapshots the current version, even though p identifies v1.
	invoice := quote(t, s, p, "", "two-real-nodes-checkout")
	if _, e := s.SettlePayment(ctx, "yoo", verified(invoice, "two-real-nodes-payment", *now)); e != nil {
		t.Fatal(e)
	}
	dc, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer dc.Close()
	forwarded := make(chan int64, 8)
	go func() {
		for {
			conn, e := dc.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(20 * time.Second))
				header := make([]byte, 64)
				if _, e := io.ReadFull(conn, header); e != nil {
					return
				}
				n, _ := io.Copy(io.Discard, conn)
				forwarded <- n
			}()
		}
	}()
	clients := map[string]*telemt.Client{}
	ports := map[string]int{}
	freePort := func() int {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return p
	}
	for _, id := range []string{"node-a", "node-b"} {
		apiPort, proxyPort := freePort(), freePort()
		ports[id] = proxyPort
		dir := t.TempDir()
		path := filepath.Join(dir, "telemt.toml")
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
auth_header="Bearer two-node-fixture"
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
seed="77777777777777777777777777777777"
`, proxyPort, apiPort, dc.Addr())
		if e = os.WriteFile(path, []byte(config), 0600); e != nil {
			t.Fatal(e)
		}
		log, e := os.Create(filepath.Join(dir, "process.log"))
		if e != nil {
			t.Fatal(e)
		}
		command := exec.Command(binary, "run", path)
		command.Dir = dir
		command.Stdout = log
		command.Stderr = log
		if e = command.Start(); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { command.Process.Kill(); command.Wait(); log.Close() })
		client := telemt.New(fmt.Sprintf("http://127.0.0.1:%d", apiPort), "Bearer two-node-fixture")
		clients[id] = client
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, _, e = client.UserPolicySnapshotsWithRevision(ctx); e == nil {
				break
			}
			if time.Now().After(deadline) {
				raw, _ := os.ReadFile(log.Name())
				t.Fatalf("node not ready %v %s", e, raw)
			}
			time.Sleep(25 * time.Millisecond)
		}
		cluster, e := s.GetCluster(ctx, p.ClusterID)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.AddMember(ctx, p.ClusterID, cluster.Revision, id); e != nil {
			t.Fatal(e)
		}
	}
	worker := NewWorker(s, func(ctx context.Context, id string, fn func(context.Context, NodeClient) error) error {
		return fn(ctx, clients[id])
	})
	deadline := time.Now().Add(25 * time.Second)
	var sub Subscription
	steps := 0
	step := func() {
		t.Helper()
		var e error
		if sub.IdentityID != "" && steps%2 == 0 {
			id, err := s.GetIdentity(ctx, sub.IdentityID)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.RetryIdentity(ctx, id.ID, id.Revision); err != nil {
				t.Fatal(err)
			}
		}
		steps++
		if _, e = worker.ProcessNext(ctx); e != nil {
			t.Fatal(e)
		}
		sub, e = s.CustomerSubscription(ctx, "customer-a", invoice.SubscriptionID)
		if e != nil {
			t.Fatal(e)
		}
	}
	for {
		step()
		access, e := s.CustomerServiceAccess(ctx, "customer-a", invoice.SubscriptionID)
		if e != nil {
			t.Fatal(e)
		}
		ready := len(access.Nodes) == 2
		for _, n := range access.Nodes {
			ready = ready && n.Status == "configured"
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			jobs, _ := s.IdentityJobs(ctx, sub.IdentityID)
			t.Fatalf("provisioning failed: %+v %+v", access, jobs)
		}
		time.Sleep(30 * time.Millisecond)
	}
	identity, e := s.GetIdentity(ctx, sub.IdentityID)
	if e != nil {
		t.Fatal(e)
	}
	secret, e := s.RevealIdentitySecret(ctx, identity.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"node-a", "node-b"} {
		conn := realNativePayload(t, ports[id], secret, 2000)
		defer conn.Close()
	}
	completed := 0
	var total int64
	deadline = time.Now().Add(15 * time.Second)
	for completed < 2 {
		step()
		select {
		case n := <-forwarded:
			total += n
			completed++
		case <-time.After(30 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("relay did not finish: connections=%d bytes=%d", completed, total)
		}
	}
	if total != 1050 {
		t.Fatalf("cluster cap multiplied or lost: forwarded=%d want1050", total)
	}
	// Report final debit from both nodes, preserving the already committed cap.
	for range 4 {
		step()
	}
	periods, e := s.CustomerPeriods(ctx, "customer-a", sub.ID, 0)
	if e != nil || len(periods) != 1 {
		t.Fatal(periods, e)
	}
	usage, e := s.CustomerQuotaUsage(ctx, "customer-a", sub.ID, periods[0].ID)
	if e != nil || usage.GrantedBytes != 1050 || usage.ObservedBytes != 1050 || usage.UnknownBytes != 0 {
		t.Fatal(usage, e)
	}
}
func realNativePayload(t *testing.T, port int, secret string, payload int) net.Conn {
	t.Helper()
	conn, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	header := bytes.Repeat([]byte{0x5a}, 64)
	if _, e = rand.Read(header[8:56]); e != nil {
		t.Fatal(e)
	}
	secretBytes, _ := hex.DecodeString(secret)
	key := sha256.Sum256(append(append([]byte{}, header[8:40]...), secretBytes...))
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
	data := bytes.Repeat([]byte{0x42}, payload)
	stream.XORKeyStream(data, data)
	if _, e = conn.Write(data); e != nil {
		t.Fatal(e)
	}
	return conn
}
