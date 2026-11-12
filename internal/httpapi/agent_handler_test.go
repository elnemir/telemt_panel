package httpapi

import (
	"context"
	"errors"
	"github.com/amirotin/telemt_panel/internal/host"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/host/hosttest"
	"github.com/amirotin/telemt_panel/internal/nodeagent"
	"github.com/amirotin/telemt_panel/internal/store"
)

const testAgentToken = "Bearer local-test-agent-authorization-123456789"

func agentFixture(t *testing.T, id string) (*Server, *hosttest.Runner) {
	t.Helper()
	s, _, _, runner := newRestartTestServer(t)
	s.cfg.Agent.Enabled = true
	s.cfg.Agent.ID = id
	s.cfg.Agent.AuthEnv = "TEST_AGENT_TOKEN"
	s.cfg.Auth.Disabled = true
	s.cfg.Host.PanelService = "telemt-agent"
	t.Setenv("TEST_AGENT_TOKEN", testAgentToken)
	state, err := store.NewMemory(filepath.Join(t.TempDir(), "agent-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	s.agentJournal, err = nodeagent.OpenJournal(state)
	if err != nil {
		t.Fatal(err)
	}
	return s, runner
}
func TestAgentAuthenticationReplayAndBinding(t *testing.T) {
	s, runner := agentFixture(t, "test-agent-node-01")
	h := s.Handler()
	key := nodeagent.NewCommandKey()
	request := func(path, token, id, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", token)
		r.Header.Set(nodeagent.IdentityHeader, id)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, c := range []struct {
		token, id string
		status    int
	}{{"", "test-agent-node-01", 401}, {testAgentToken, "test-agent-node-02", 409}, {testAgentToken, "test-agent-node-01", 202}, {testAgentToken, "test-agent-node-01", 202}} {
		w := request("/v1/agent/service/restart", c.token, c.id, "")
		if w.Code != c.status {
			t.Fatal(w.Code, w.Body)
		}
	}
	if len(runner.CallsSnapshot()) != 1 {
		t.Fatal("replayed command")
	}
	if w := request("/v1/agent/service/stop", testAgentToken, s.cfg.Agent.ID, ""); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request("/v1/agent/service/restart", testAgentToken, s.cfg.Agent.ID, `{"command":"bad"}`); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request("/v1/agent/service/restart", testAgentToken, s.cfg.Agent.ID, strings.Repeat(" ", 4097)); w.Code != 413 {
		t.Fatal(w.Code, w.Body)
	}
	s.cfg.Host.PanelService = "telemt.service"
	if w := request("/v1/agent/service/restart", testAgentToken, s.cfg.Agent.ID, ""); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request("/api/auth/login", testAgentToken, s.cfg.Agent.ID, ""); w.Code != 404 {
		t.Fatal("panel API exposed", w.Code)
	}
	if len(runner.CallsSnapshot()) != 1 {
		t.Fatal("unexpected host action")
	}
}
func TestNodeAgentRoutingAndReplay(t *testing.T) {
	s, cookie, a, b, _, _ := nodeTestFixture(t)
	agentA, runnerA := agentFixture(t, "test-agent-node-01")
	agentB, runnerB := agentFixture(t, "test-agent-node-02")
	as := httptest.NewServer(agentA.Handler())
	defer as.Close()
	bs := httptest.NewServer(agentB.Handler())
	defer bs.Close()
	a.Binding = nodeagent.Binding{URL: as.URL, ID: agentA.cfg.Agent.ID, AuthEnv: "TEST_AGENT_TOKEN"}
	b.Binding = nodeagent.Binding{URL: bs.URL, ID: agentB.cfg.Agent.ID, AuthEnv: "TEST_AGENT_TOKEN"}
	if _, err := s.nodes.Replace(a.ID, a.Revision, a.Input); err != nil {
		t.Fatal(err)
	}
	if _, err := s.nodes.Replace(b.ID, b.Revision, b.Input); err != nil {
		t.Fatal(err)
	}
	key := nodeagent.NewCommandKey()
	for range 2 {
		w := doRequest(t, s, cookie, http.MethodPost, nodeURL(a.ID, "/telemt/restart"), map[string]string{"Idempotency-Key": key}, nil)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if w := doRequest(t, s, cookie, "GET", nodeURL(a.ID, "/agent/commands/"+key), nil, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":202`) {
		t.Fatal(w.Code, w.Body)
	}
	if w := doRequest(t, s, cookie, "GET", nodeURL(b.ID, "/agent/commands/"+key), nil, nil); w.Code != 404 {
		t.Fatal("foreign command visible", w.Code, w.Body)
	}
	if len(runnerA.CallsSnapshot()) != 1 || len(runnerB.CallsSnapshot()) != 0 {
		t.Fatal("cross-node or repeated command")
	}
	t.Setenv("TEST_AGENT_TOKEN", "")
	if w := doRequest(t, s, cookie, "POST", nodeURL(b.ID, "/telemt/restart"), nil, nil); w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
	if w := doRequest(t, s, cookie, "GET", nodeURL(b.ID, "/users"), nil, nil); w.Code != 200 {
		t.Fatal("agent failure blocked API", w.Code, w.Body)
	}
	if len(runnerB.CallsSnapshot()) != 0 {
		t.Fatal("missing token ran action")
	}
}

func TestNodeAgentLogStreamClosesOnLogout(t *testing.T) {
	s, cookie, a, _, _, _ := nodeTestFixture(t)
	agent, _ := agentFixture(t, "test-agent-node-01")
	stopped := make(chan struct{})
	agent.logSrc = &hosttest.LogSource{KindValue: host.LogKindJournald, CapsValue: host.LogCaps{CanStream: true}, StreamFunc: func(ctx context.Context, _ string) (<-chan host.LogEvent, error) {
		ch := make(chan host.LogEvent)
		go func() { <-ctx.Done(); close(ch); close(stopped) }()
		return ch, nil
	}}
	as := httptest.NewServer(agent.Handler())
	defer as.Close()
	a.Binding = nodeagent.Binding{URL: as.URL, ID: agent.cfg.Agent.ID, AuthEnv: "TEST_AGENT_TOKEN"}
	if _, err := s.nodes.Replace(a.ID, a.Revision, a.Input); err != nil {
		t.Fatal(err)
	}
	if w := doRequest(t, s, cookie, "HEAD", nodeURL(a.ID, "/events/logs?service=telemt"), nil, nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	panel := httptest.NewServer(s.Handler())
	defer panel.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", panel.URL+nodeURL(a.ID, "/events/logs?service=telemt"), nil)
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	if w := doRequest(t, s, cookie, "POST", "/api/auth/logout", nil, nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	_, err = io.ReadAll(res.Body)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("remote stream survived logout")
	}
}
