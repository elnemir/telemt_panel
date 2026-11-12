package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/amirotin/telemt_panel/internal/nodes"
)

func TestNodeAPIAuthCRUDAndScopedReads(t *testing.T) {
	var callsA, callsB atomic.Int32
	makeUpstream := func(marker string, calls *atomic.Int32) *httptest.Server {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+marker {
				t.Error("wrong node credential")
				w.WriteHeader(401)
				return
			}
			switch r.URL.Path {
			case "/v1/health":
				fmt.Fprintf(w, `{"ok":true,"data":{"status":"%s","read_only":false}}`, marker)
			case "/v1/users":
				fmt.Fprintf(w, `{"ok":true,"revision":"%s","data":[{"username":"same","enabled":true,"current_connections":1}]}`, marker)
			case "/v1/config":
				fmt.Fprintf(w, `{"ok":true,"revision":"%s","data":{"general":{"log_level":"%s"}}}`, marker, marker)
			default:
				w.WriteHeader(501)
			}
		}))
		t.Cleanup(ts.Close)
		return ts
	}
	a := makeUpstream("A", &callsA)
	b := makeUpstream("B", &callsB)
	t.Setenv("NODE_API_A", "Bearer A")
	t.Setenv("NODE_API_B", "Bearer B")
	s := newTestServer(t)
	var err error
	s.nodes, err = nodes.Open(s.st, a.URL, "Bearer A")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.nodes.Close)
	h := s.Handler()
	_, cookie := login(t, h, "admin", testPassword)
	request := func(method, path, body, match string, authenticated, csrf bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if authenticated {
			r.AddCookie(cookie)
		}
		if csrf {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		}
		if match != "" {
			r.Header.Set("If-Match", match)
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/nodes", "", "", false, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	payload := fmt.Sprintf(`{"name":"B","api_url":%q,"enabled":true,"auth_header_env":"NODE_API_B","labels":{"region":"b"}}`, b.URL)
	if w := request("POST", "/api/nodes", payload, "", true, false); w.Code != 403 {
		t.Fatal("CSRF", w.Code)
	}
	if w := request("POST", "/api/nodes", `{"name":"X","api_url":"http://example.test","enabled":true,"auth_header":"secret"}`, "", true, true); w.Code != 400 {
		t.Fatal("unknown fields", w.Code)
	}
	w := request("POST", "/api/nodes", payload, "", true, true)
	if w.Code != 201 || w.Header().Get("ETag") != `"1"` {
		t.Fatal(w.Code, w.Body.String())
	}
	var n nodes.Node
	if err := json.Unmarshal(w.Body.Bytes(), &n); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "Bearer") {
		t.Fatal("credential response")
	}
	for _, kind := range []string{"health", "users", "config"} {
		w = request("GET", "/api/nodes/"+n.ID+"/"+kind, "", "", true, false)
		if w.Code != 200 {
			t.Fatal(kind, w.Code, w.Body.String())
		}
		var result struct {
			NodeID   string          `json:"node_id"`
			Revision string          `json:"revision"`
			Data     json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.NodeID != n.ID {
			t.Fatal("scope missing")
		}
		if kind != "health" && result.Revision != "B" {
			t.Fatal("cross-node revision")
		}
		if strings.Contains(string(result.Data), "sub_url") || strings.Contains(string(result.Data), "ip_history") {
			t.Fatal("legacy enrichment leaked")
		}
	}
	if callsA.Load() != 0 || callsB.Load() != 3 {
		t.Fatal("incorrect target", callsA.Load(), callsB.Load())
	}
	w = request("GET", "/api/nodes/default/health", "", "", true, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"A"`) {
		t.Fatal("default changed", w.Body.String())
	}
	for _, path := range []string{"/api/nodes/missing/health", "/api/nodes/" + n.ID + "/events", "/api/nodes/" + n.ID + "/traffic/summary"} {
		w = request("GET", path, "", "", true, false)
		if w.Code != 404 || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal("fallback", path, w.Code)
		}
	}
	for _, match := range []string{"", `"`, "*", `W/"1"`, "1,2", "2"} {
		w = request("PUT", "/api/nodes/"+n.ID, payload, match, true, true)
		if w.Code < 400 {
			t.Fatal("invalid precondition accepted", match)
		}
	}
	w = request("DELETE", "/api/nodes/default", "", "1", true, true)
	if w.Code != 409 {
		t.Fatal("default deleted", w.Code)
	}
	disabled := strings.Replace(payload, `"enabled":true`, `"enabled":false`, 1)
	w = request("PUT", "/api/nodes/"+n.ID, disabled, `"1"`, true, true)
	if w.Code != 200 || w.Header().Get("ETag") != `"2"` {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/api/nodes/"+n.ID+"/health", "", "", true, false)
	if w.Code != 409 || callsB.Load() != 3 {
		t.Fatal("disabled node called")
	}
	w = request("DELETE", "/api/nodes/"+n.ID, "", "2", true, true)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/api/nodes/"+n.ID+"/users", "", "", true, false)
	if w.Code != 404 {
		t.Fatal("deleted node fallback")
	}
}

func TestNodeReadDiscardsRetiredSnapshotAndRedactsErrors(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, `{"ok":true,"data":{"status":"old"}}`)
	}))
	defer up.Close()
	s := newTestServer(t)
	s.nodes, _ = nodes.Open(s.st, "", "")
	t.Cleanup(s.nodes.Close)
	n, _ := s.nodes.Create(nodes.Input{Name: "slow", APIURL: up.URL, Enabled: true})
	h := s.Handler()
	_, cookie := login(t, h, "admin", testPassword)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("GET", "/api/nodes/"+n.ID+"/health", nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		done <- w
	}()
	<-started
	if err := s.nodes.Delete(n.ID, 1); err != nil {
		t.Fatal(err)
	}
	close(release)
	if w := <-done; w.Code != 409 || strings.Contains(w.Body.String(), `"old"`) {
		t.Fatal("retired result escaped", w.Code)
	}
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"ok":false,"error":{"code":"internal","message":"private-secret"}}`)
	}))
	defer failed.Close()
	n, _ = s.nodes.Create(nodes.Input{Name: "failed", APIURL: failed.URL, Enabled: true})
	req := httptest.NewRequest("GET", "/api/nodes/"+n.ID+"/health", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 502 || strings.Contains(w.Body.String(), "private-secret") || strings.Contains(w.Body.String(), failed.URL) {
		t.Fatal("error not redacted", w.Body.String())
	}
}
