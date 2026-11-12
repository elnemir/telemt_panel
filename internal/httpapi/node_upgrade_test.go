package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/nodeagent"
	"github.com/amirotin/telemt_panel/internal/nodeinstall"
	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/store"
)

type testNodeUpgrader struct {
	testNodeInstaller
	calls   atomic.Int32
	target  chan string
	release chan struct{}
}

func (*testNodeUpgrader) UpgradeTelemtVersion() string { return "3.5.17" }

func (u *testNodeUpgrader) Upgrade(ctx context.Context, _ *nodes.Registry, id string, _ uint64,
	_ nodeinstall.UpgradeRequest, _ string, progress func(string)) (string, error) {
	u.calls.Add(1)
	u.target <- id
	progress("checking")
	select {
	case <-u.release:
		return nodeinstall.UpgradeRolledBack, nil
	case <-ctx.Done():
		return nodeinstall.UpgradeUnknown, nodeinstall.ErrUnconfirmed
	}
}

func TestNodeUpgradeAdminScopeAndWriteFence(t *testing.T) {
	s := newTestServer(t)
	state, err := store.NewMemory(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	registry, err := nodes.OpenWithKey(state, "", "", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	tls := httptest.NewTLSServer(http.NotFoundHandler())
	defer tls.Close()
	s.nodes = registry
	node, err := registry.CreateManaged(nodes.Input{
		Name: "Managed", APIURL: tls.URL, Enabled: true,
		Binding: nodeagent.Binding{URL: tls.URL, ID: "managed-upgrade-fixture"},
	}, nodes.ManagedCredentials{
		Control: strings.Repeat("c", 32), Agent: strings.Repeat("a", 32), Certificate: tls.Certificate().Raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	upgrader := &testNodeUpgrader{target: make(chan string, 2), release: make(chan struct{})}
	s.nodeInstallation = newNodeInstallJobs(context.Background(), upgrader)
	defer s.nodeInstallation.close()
	h := s.Handler()
	_, cookie := login(t, h, "admin", testPassword)
	send := func(method, path string, auth *http.Cookie, payload any) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, mutatingJSON(t, method, path, auth, payload))
		return w
	}
	path := "/api/nodes/" + node.ID + "/upgrade"
	input := nodeinstall.UpgradeRequest{RequestID: strings.Repeat("1", 32), SSHPort: 22, SSHUser: "root",
		HostFingerprint: "SHA256:" + strings.Repeat("a", 43), Telemt: true, Agent: true}
	if got := send("POST", path, nil, input); got.Code != 401 {
		t.Fatalf("anonymous upgrade: %d", got.Code)
	}
	if got := send("POST", path, cookie, map[string]any{"host": "192.0.2.99"}); got.Code != 400 {
		t.Fatalf("unknown target field: %d", got.Code)
	}
	release, ok := s.nodeInstallation.beginNodeWrite(node.ID)
	if !ok {
		t.Fatal("node write rejected while idle")
	}
	blocked := send("POST", path, cookie, input)
	release()
	if blocked.Code != 409 {
		t.Fatalf("upgrade while a write is active: %d", blocked.Code)
	}
	for range 2 {
		if got := send("POST", path, cookie, input); got.Code != 202 {
			t.Fatalf("upgrade acceptance: %d, %s", got.Code, got.Body)
		}
	}
	select {
	case id := <-upgrader.target:
		if id != node.ID {
			t.Fatalf("wrong node: %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade did not start")
	}
	if got := send("POST", nodeURL(node.ID, "/telemt/restart"), cookie, nil); got.Code != 409 {
		t.Fatalf("node write while upgrading: %d", got.Code)
	}
	if releaseOther, ok := s.nodeInstallation.beginNodeWrite("another-node"); !ok {
		t.Fatal("unrelated node should remain writable")
	} else {
		releaseOther()
	}
	close(upgrader.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w := send("GET", path, cookie, nil)
		var status nodeUpgradeStatus
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Job == nil || !status.Available {
			t.Fatalf("upgrade status: %d, %s", w.Code, w.Body)
		}
		if status.Job.State == nodeinstall.UpgradeRolledBack {
			if upgrader.calls.Load() != 1 || status.Job.RequestID != input.RequestID ||
				strings.Contains(w.Body.String(), input.HostFingerprint) {
				t.Fatalf("upgrade repeated or returned SSH data: %s", w.Body)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("rollback status did not arrive")
}
