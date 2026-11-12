package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/amirotin/telemt_panel/internal/config"
	"github.com/amirotin/telemt_panel/internal/hub"
	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/store"
	"github.com/amirotin/telemt_panel/internal/telemt"
	"github.com/amirotin/telemt_panel/internal/telemt/telemttest"
)

func TestStandaloneIsolationAndRegistry(t *testing.T) {
	var calls atomic.Int32
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer forbidden.Close()
	st, err := store.NewMemory("")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{ControlPlaneOnly: true, Auth: config.AuthConfig{Username: "admin", PasswordHash: testPasswordHash}, Host: config.HostConfig{ServiceManager: "systemd"}, Privileges: config.PrivilegesConfig{Mode: "direct"}}
	tc := telemt.New(forbidden.URL, "")
	hb := hub.New(hub.Config{}, tc, st)
	defer hb.Close()
	registry, err := nodes.Open(st, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	s := New(cfg, tc, st, hb, "test", EngineOptions{Nodes: registry})
	defer s.CloseNodeRuntimes()
	defer s.limiter.Stop()
	defer s.subLimiter.Stop()
	defer s.quotaResets.Close()
	defer s.sessions.Close()
	defer s.geoip.Close()
	defer s.geography.Close()
	_, cookie := login(t, s.Handler(), "admin", testPassword)
	for _, p := range []string{"/api/users", "/api/telemt/config", "/api/events?topics=users", "/api/snapshot?topics=stats", "/api/history?metric=traffic&range=1h", "/api/settings/quota-schedule", "/api/updates", "/api/nodes/default/manage/users"} {
		w := doRequest(t, s, cookie, "GET", p, nil, nil)
		if w.Code != 404 || !strings.Contains(w.Body.String(), "node_not_found") {
			t.Fatal(p, w.Code, w.Body)
		}
	}
	for _, p := range []string{"/api/telemt/restart", "/api/telemt/start", "/api/telemt/stop", "/api/updates/telemt/apply"} {
		if w := doRequest(t, s, cookie, "POST", p, nil, []byte(`{}`)); w.Code != 404 {
			t.Fatal(p, w.Code, w.Body)
		}
	}
	for _, p := range []string{"/api/auth/me", "/api/nodes", "/api/host", "/api/health"} {
		if w := doRequest(t, s, cookie, "GET", p, nil, nil); w.Code != 200 {
			t.Fatal(p, w.Code, w.Body)
		}
	}
	if s.privilegesMode != "manual" || s.svcMgr.Kind() != "none" || s.logSrc.Kind() != "none" {
		t.Fatal("local host activated")
	}
	if len(registry.List()) != 0 || calls.Load() != 0 {
		t.Fatal("default leaked", calls.Load())
	}
	upstream := telemttest.New(telemttest.Scenario{})
	defer upstream.Close()
	n, err := registry.Create(nodes.Input{Name: "remote", APIURL: upstream.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	w := doRequest(t, s, cookie, "GET", nodeURL(n.ID, "/users"), nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "alice") {
		t.Fatal(w.Code, w.Body)
	}
	if w := doRequest(t, s, cookie, "GET", "/api/users", nil, nil); w.Code != 404 {
		t.Fatal("new node silently became default")
	}
}
