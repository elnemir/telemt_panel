package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/store"
	"github.com/amirotin/telemt_panel/internal/telemt"
	"github.com/amirotin/telemt_panel/internal/telemt/telemttest"
)

func nodeTestFixture(t *testing.T) (*Server, *http.Cookie, nodes.Node, nodes.Node, *telemttest.Server, *telemttest.Server) {
	t.Helper()
	def := telemttest.New(telemttest.Scenario{})
	a := telemttest.New(telemttest.Scenario{})
	b := telemttest.New(telemttest.Scenario{})
	t.Cleanup(def.Close)
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	s, cookie := newTelemtInfoTestServer(t, telemt.New(def.URL, ""))
	s.cfg.Telemt.URL = def.URL
	var err error
	s.nodes, err = nodes.Open(s.st, def.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.CloseNodeRuntimes()
		s.nodes.Close()
		s.quotaResets.Close()
		s.sessions.Close()
		s.geoip.Close()
	})
	an, err := s.nodes.Create(nodes.Input{Name: "A", APIURL: a.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	bn, err := s.nodes.Create(nodes.Input{Name: "B", APIURL: b.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return s, cookie, an, bn, a, b
}

func nodeURL(id, path string) string { return "/api/nodes/" + id + "/manage" + path }

func TestNodeManagementMutationsAndDefaultIsolation(t *testing.T) {
	s, cookie, a, b, _, upB := nodeTestFixture(t)
	req := func(id, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		return doRequest(t, s, cookie, method, nodeURL(id, path), headers, []byte(body))
	}
	for _, id := range []string{a.ID, b.ID} {
		w := req(id, "POST", "/users", `{"username":"same"}`, nil)
		if w.Code != 201 {
			t.Fatalf("create %d %s", w.Code, w.Body)
		}
	}
	w := req(a.ID, "PATCH", "/users/same", `{"max_tcp_conns":7}`, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"max_tcp_conns":7`) {
		t.Fatal(w.Code, w.Body)
	}
	w = req(a.ID, "PUT", "/users/same/enabled", `{"enabled":false}`, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = req(b.ID, "GET", "/users/same", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"max_tcp_conns":7`) || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatal("B changed", w.Body)
	}
	w = req("default", "GET", "/users/same", "", nil)
	if w.Code != 404 {
		t.Fatal("default received user", w.Code)
	}
	for _, path := range []string{"/users/same/rotate-secret", "/users/alice/reset-quota"} {
		w = req(a.ID, "POST", path, "", nil)
		if w.Code != 200 {
			t.Fatal(path, w.Code, w.Body)
		}
	}
	w = req(a.ID, "GET", "/telemt/config", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var config telemtConfigView
	if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	_, beforeB, err := telemt.New(upB.URL, "").GetConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w = req(a.ID, "PATCH", "/telemt/config?reload=instant", `{"sections":{"general":{"log_level":"debug"}}}`, map[string]string{"If-Match": config.Revision})
	if w.Code != 202 {
		t.Fatal("config patch", w.Code, w.Body)
	}
	w = req(a.ID, "PATCH", "/telemt/config?reload=instant", `{"sections":{"general":{"log_level":"warn"}}}`, map[string]string{"If-Match": config.Revision})
	if w.Code != 409 {
		t.Fatal("stale config revision accepted", w.Code, w.Body)
	}
	_, afterB, err := telemt.New(upB.URL, "").GetConfig(context.Background())
	if err != nil || beforeB != afterB {
		t.Fatal("B revision changed", err)
	}
	w = req(a.ID, "DELETE", "/users/same", "", nil)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	w = req(b.ID, "GET", "/users/same", "", nil)
	if w.Code != 200 {
		t.Fatal("B user deleted")
	}
	for _, path := range []string{"/telemt/restart", "/updates/telemt/apply", "/users/alice/sublink"} {
		w = req(a.ID, "POST", path, `{}`, nil)
		if w.Code != 501 {
			t.Fatal("host/subscription escape", path, w.Code)
		}
	}
	w = req(a.ID, "GET", "/settings/tls", "", nil)
	if w.Code != 404 {
		t.Fatal("global settings exposed", w.Code)
	}
	w = req(a.ID, "GET", "/host", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"restart_telemt":true`) || strings.Contains(w.Body.String(), "manual_commands") {
		t.Fatal("host leaked", w.Body)
	}
	upB.SetScenario(telemttest.Scenario{ReadOnly: true})
	w = req(b.ID, "POST", "/users", `{"username":"denied"}`, nil)
	if w.Code != 403 {
		t.Fatal("read-only bypass", w.Code, w.Body)
	}
	w = req(a.ID, "POST", "/users", `{"username":"allowed"}`, nil)
	if w.Code != 201 {
		t.Fatal("read-only leaked to A", w.Code, w.Body)
	}
}

func TestNodeManagementHistoryPartitionsAndLifecycle(t *testing.T) {
	s, cookie, a, b, _, _ := nodeTestFixture(t)
	now := time.Now().Unix()
	for i, n := range []nodes.Node{a, b} {
		rt, release, err := acquireTestNode(t, s, n.ID)
		if err != nil {
			t.Fatal(err)
		}
		st := rt.server.st
		value := float64(101 + i)
		if err := st.RecordMetric("connections", store.MetricPoint{TS: now, Value: value}); err != nil {
			t.Fatal(err)
		}
		if err := st.AppendHistoryEvent(store.HistoryEvent{TS: time.Now(), Category: store.StorageEvents, Kind: "test", Entity: "same", State: n.Name}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSetting("test.scope", n.Name); err != nil {
			t.Fatal(err)
		}
		first := store.UserTrafficSnapshot{ObservedAt: now - 30, SourceStartedAt: now - 60, TelemetryEnabled: true, Users: []store.UserTrafficObservation{{Username: "same", RawOctets: 0}}}
		if _, err := st.ApplyUserTrafficSnapshot(first); err != nil {
			t.Fatal(err)
		}
		first.ObservedAt = now
		first.Users[0].RawOctets = uint64(100 + i)
		if _, err := st.ApplyUserTrafficSnapshot(first); err != nil {
			t.Fatal(err)
		}
		release()
	}
	for _, n := range []nodes.Node{a, b} {
		w := doRequest(t, s, cookie, "GET", nodeURL(n.ID, "/history/events?range=1h"), nil, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"`+n.Name+`"`) {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := doRequest(t, s, cookie, "POST", nodeURL(a.ID, "/traffic/reset"), nil, []byte(`{"confirm":true}`))
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	rt, release, err := acquireTestNode(t, s, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := rt.server.st.UserTrafficSummaries()
	release()
	if err != nil || summaries["same"].ObservedTotalBytes != 101 {
		t.Fatal("B history reset", summaries, err)
	}
	if metrics, err := s.st.MetricRange("connections", 0); err != nil || len(metrics) != 0 {
		t.Fatal("default history contaminated", metrics, err)
	}
	// Updating metadata keeps the partition but retires runtime caches/replay.
	rt, release, err = acquireTestNode(t, s, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldScope := rt.scope
	oldStore := rt.server.st
	release()
	input := a.Input
	input.Name = "A renamed"
	if _, err := s.nodes.Replace(a.ID, 1, input); err != nil {
		t.Fatal(err)
	}
	s.refreshNode(a.ID)
	rt, release, err = acquireTestNode(t, s, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rt.scope == oldScope || rt.server.st != oldStore {
		t.Fatal("incorrect runtime/store lifetime")
	}
	release()
	if err := s.nodes.Delete(a.ID, 2); err != nil {
		t.Fatal(err)
	}
	s.refreshNode(a.ID)
	w = doRequest(t, s, cookie, "GET", nodeURL(a.ID, "/history?metric=connections&range=1h"), nil, nil)
	if w.Code != 404 {
		t.Fatal("deleted history fallback", w.Code)
	}
}

func TestNodeManagementSSECursorAndDisable(t *testing.T) {
	s, cookie, a, b, _, _ := nodeTestFixture(t)
	for _, id := range []string{a.ID, b.ID} {
		rt, release, err := acquireTestNode(t, s, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rt.server.hub.Snapshot(context.Background(), []string{"users"}); err != nil {
			t.Fatal(err)
		}
		release()
	}
	live := httptest.NewServer(s.Handler())
	defer live.Close()
	open := func(id, cursor string) (*http.Response, context.CancelFunc) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", live.URL+nodeURL(id, "/events?topics=users"), nil)
		req.AddCookie(cookie)
		if cursor != "" {
			req.Header.Set("Last-Event-ID", cursor)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			cancel()
			t.Fatal(res.StatusCode)
		}
		return res, cancel
	}
	readID := func(res *http.Response) string {
		t.Helper()
		scanner := bufio.NewScanner(res.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "id: ") {
				return strings.TrimPrefix(scanner.Text(), "id: ")
			}
		}
		t.Fatal("no SSE id", scanner.Err())
		return ""
	}
	first, cancelA := open(a.ID, "")
	cursorA := readID(first)
	if !strings.HasPrefix(cursorA, a.ID+":") {
		t.Fatal(cursorA)
	}
	first.Body.Close()
	cancelA()
	second, cancelB := open(b.ID, cursorA)
	defer cancelB()
	defer second.Body.Close()
	cursorB := readID(second)
	if !strings.HasPrefix(cursorB, b.ID+":") {
		t.Fatal("cross-node cursor", cursorB)
	}
	in := b.Input
	in.Enabled = false
	if _, err := s.nodes.Replace(b.ID, 1, in); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.refreshNode(b.ID); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE lease blocked retirement")
	}
	w := doRequest(t, s, cookie, "GET", nodeURL(b.ID, "/snapshot?topics=users"), nil, nil)
	if w.Code != 409 {
		t.Fatal("disabled fallback", w.Code)
	}
	w = doRequest(t, s, cookie, "GET", nodeURL(a.ID, "/snapshot?topics=users"), nil, nil)
	if w.Code != 200 {
		t.Fatal("A stopped with B", w.Code, w.Body)
	}
}

func TestNodeManagementUnknownScopeNoAllocation(t *testing.T) {
	s, cookie, _, _, _, _ := nodeTestFixture(t)
	for i := range 20 {
		w := doRequest(t, s, cookie, "GET", nodeURL(fmt.Sprint(i), "/snapshot?topics=users"), nil, nil)
		if w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
	if len(s.nodeEntries) != 0 {
		t.Fatal("unknown IDs allocated runtime state")
	}
}

func acquireTestNode(t *testing.T, s *Server, id string) (*nodeRuntime, func(), error) {
	t.Helper()
	rt, release, err := s.acquireNodeRuntime(id)
	if err != nil {
		return nil, nil, err
	}
	once := sync.OnceFunc(release)
	t.Cleanup(once)
	return rt, once, nil
}

func TestNodeManagementSQLiteReopenAndPolicies(t *testing.T) {
	if store.Variant == "lite" {
		t.Skip("SQLite is not compiled into lite")
	}
	s, _, a, b, _, _ := nodeTestFixture(t)
	root := t.TempDir()
	s.cfg.DataDir = filepath.Join(root, "state")
	s.cfg.Store.Driver = "sqlite"
	s.cfg.Store.Path = filepath.Join(root, "legacy.sqlite")
	now := time.Now().Add(-10 * time.Minute).Truncate(5 * time.Minute).Unix()
	for i, n := range []nodes.Node{a, b} {
		rt, release, err := acquireTestNode(t, s, n.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.server.st.SetSetting("node-test", n.Name); err != nil {
			t.Fatal(err)
		}
		if err := rt.server.st.RecordMetric("connections", store.MetricPoint{TS: now, Value: float64(500 + i)}); err != nil {
			t.Fatal(err)
		}
		if !rt.server.st.StateDurable() || !rt.server.st.Info().Durable {
			t.Fatal("not durable")
		}
		release()
	}
	s.CloseNodeRuntimes()
	for i, n := range []nodes.Node{a, b} {
		reopened, err := s.openNodeStore(n.ID)
		if err != nil {
			t.Fatal(err)
		}
		value, found, err := reopened.GetSetting("node-test")
		if err != nil || !found || value != n.Name {
			t.Fatal("wrong node state", value, err)
		}
		points, err := reopened.MetricRange("connections", now-300)
		if err != nil {
			t.Fatal(err)
		}
		found = false
		for _, p := range points {
			if p.TS == now && p.Max == float64(500+i) {
				found = true
			}
		}
		if !found {
			t.Fatal("wrong or lost persisted history", n.ID, points)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNodeManagementAuthAndPartitionFailure(t *testing.T) {
	s, cookie, a, b, _, _ := nodeTestFixture(t)
	h := s.Handler()
	for _, authenticated := range []bool{false, true} {
		r := httptest.NewRequest("POST", nodeURL(a.ID, "/users"), strings.NewReader(`{"username":"blocked"}`))
		if authenticated {
			r.AddCookie(cookie)
		} else {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expected := 401
		if authenticated {
			expected = 403
		}
		if w.Code != expected {
			t.Fatal("management auth/CSRF", w.Code, w.Body)
		}
	}
	if len(s.nodeEntries) != 0 {
		t.Fatal("unauthorized request created a runtime")
	}
	s.cfg.DataDir = t.TempDir()
	dir := filepath.Join(s.cfg.DataDir, "nodes", a.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "panel-state.json"), []byte("broken json"), 0600); err != nil {
		t.Fatal(err)
	}
	w := doRequest(t, s, cookie, "GET", nodeURL(a.ID, "/history/events"), nil, nil)
	if w.Code != 503 {
		t.Fatal("corrupt partition did not fail closed", w.Code, w.Body)
	}
	w = doRequest(t, s, cookie, "GET", nodeURL(b.ID, "/users"), nil, nil)
	if w.Code != 200 {
		t.Fatal("failed partition affected B", w.Code, w.Body)
	}
	s.CloseNodeRuntimes()
	if _, _, err := s.acquireNodeRuntime(b.ID); err != nodes.ErrDisabled {
		t.Fatal("runtime reopened after shutdown", err)
	}
}

func TestNodeManagementSlowNodeDoesNotBlockPeer(t *testing.T) {
	s, cookie, _, b, _, _ := nodeTestFixture(t)
	started := make(chan struct{})
	unblocked := make(chan struct{})
	var once sync.Once
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-unblocked:
		case <-r.Context().Done():
		}
		w.WriteHeader(503)
	}))
	defer slow.Close()
	defer close(unblocked)
	a, err := s.nodes.Create(nodes.Input{Name: "slow", APIURL: slow.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); doRequest(t, s, cookie, "GET", nodeURL(a.ID, "/users"), nil, nil) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow request did not start")
	}
	peer := make(chan *httptest.ResponseRecorder, 1)
	go func() { peer <- doRequest(t, s, cookie, "GET", nodeURL(b.ID, "/users"), nil, nil) }()
	select {
	case w := <-peer:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow A blocked B")
	}
	in := a.Input
	in.Enabled = false
	if _, err := s.nodes.Replace(a.ID, a.Revision, in); err != nil {
		t.Fatal(err)
	}
	retired := make(chan struct{})
	go func() { s.refreshNode(a.ID); close(retired) }()
	select {
	case <-retired:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not retire slow node")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retired request did not finish")
	}
}

func TestNodeManagementBasePath(t *testing.T) {
	s, cookie, a, _, _, _ := nodeTestFixture(t)
	s.cfg.BasePath = "/panel"
	for _, path := range []string{"/users", "/telemt/config", "/history/events?range=24h"} {
		w := doRequest(t, s, cookie, "GET", "/panel"+nodeURL(a.ID, path), nil, nil)
		if w.Code != 200 || w.Header().Get("X-Telemt-Node") != a.ID {
			t.Fatal("base path scope", path, w.Code, w.Body)
		}
	}
	w := doRequest(t, s, cookie, "GET", nodeURL(a.ID, "/users"), nil, nil)
	if w.Code != 404 {
		t.Fatal("unprefixed request accepted", w.Code)
	}
}
