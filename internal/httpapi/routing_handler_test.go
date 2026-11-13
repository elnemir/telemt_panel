//go:build !lite

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/ingress"
	"github.com/amirotin/telemt_panel/internal/nodeinstall"
	"github.com/amirotin/telemt_panel/internal/platform"
)

func TestRoutingHTTPAuthCASAndExport(t *testing.T) {
	s, cookie, a, _, _, _ := nodeTestFixture(t)
	db, e := platform.Open(filepath.Join(t.TempDir(), "platform.sqlite"), bytes.Repeat([]byte{9}, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s.platform = db
	c, e := db.CreateCluster(context.Background(), "Routing")
	if e != nil {
		t.Fatal(e)
	}
	if e = db.AddMember(context.Background(), c.ID, 1, a.ID); e != nil {
		t.Fatal(e)
	}
	path := "/api/clusters/" + c.ID + "/routing"
	cfg := ingress.Config{Domain: "proxy.example.test", NativePort: 8443, WebPort: 443, NativeBalance: "leastconn", Targets: []ingress.Target{{NodeID: a.ID, NativeAddress: "192.0.2.1:443", WebAddress: "192.0.2.1:18080", Weight: 1}}}
	raw, _ := json.Marshal(cfg)
	anon := httptest.NewRecorder()
	s.Handler().ServeHTTP(anon, httptest.NewRequest("GET", path, nil))
	if anon.Code != 401 {
		t.Fatal(anon.Code)
	}
	suggestions := doRequest(t, s, cookie, "GET", path+"/suggestions", nil, nil)
	var candidates []routingNodeSuggestion
	if suggestions.Code != 200 || suggestions.Header().Get("Cache-Control") != "no-store" ||
		json.Unmarshal(suggestions.Body.Bytes(), &candidates) != nil || len(candidates) != 1 ||
		candidates[0].NodeID != a.ID || candidates[0].Status != "read" {
		t.Fatal("suggestions must only include active members", suggestions.Code, suggestions.Body)
	}
	if w := doRequest(t, s, cookie, "PUT", path, map[string]string{"Sec-Fetch-Site": "cross-site", "If-Match": "2"}, raw); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	for _, item := range [][2]int{{0, 428}, {1, 409}, {2, 204}} {
		rev, want := item[0], item[1]
		headers := map[string]string{}
		if rev != 0 {
			headers["If-Match"] = fmt.Sprint(rev)
		}
		w := doRequest(t, s, cookie, "PUT", path, headers, raw)
		if w.Code != want {
			t.Fatal(rev, w.Code, w.Body)
		}
	}
	w := doRequest(t, s, cookie, "GET", path+"/export", map[string]string{"If-Match": "3"}, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "send-proxy-v2") || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header(), w.Body)
	}
	status := doRequest(t, s, cookie, "GET", path+"/install", nil, nil)
	if status.Code != 200 || status.Body.String() != "{\"available\":false}\n" {
		t.Fatal(status.Code, status.Body)
	}

	runner := &testIngressInstaller{inputs: make(chan string, 2), proceed: make(chan struct{})}
	s.nodeInstallation = newNodeInstallJobs(nil, runner)
	defer s.nodeInstallation.close()
	input := nodeinstall.IngressRequest{Host: "192.0.2.10", SSHPort: 22, SSHUser: "root",
		HostFingerprint: "SHA256:" + strings.Repeat("A", 43), RequestID: strings.Repeat("a", 32)}
	body, _ := json.Marshal(input)
	installPath := path + "/install"
	for _, tt := range []struct {
		headers map[string]string
		body    []byte
		code    int
	}{
		{nil, body, 428},
		{map[string]string{"If-Match": "2"}, body, 409},
		{map[string]string{"If-Match": "3", "Sec-Fetch-Site": "cross-site"}, body, 403},
		{map[string]string{"If-Match": "3"}, []byte(strings.Replace(string(body), "192.0.2.10", "localhost", 1)), 400},
		{map[string]string{"If-Match": "3"}, body, 202},
		{map[string]string{"If-Match": "3"}, body, 202},
	} {
		result := doRequest(t, s, cookie, "POST", installPath, tt.headers, tt.body)
		if result.Code != tt.code {
			t.Fatalf("apply: got %d, want %d (%s)", result.Code, tt.code, result.Body.String())
		}
	}
	select {
	case config := <-runner.inputs:
		if config != w.Body.String() {
			t.Fatal("deployed configuration differs from export")
		}
	case <-time.After(time.Second):
		t.Fatal("installer did not receive the configuration")
	}
	close(runner.proceed)
	status = waitForIngressJob(t, s, cookie, installPath, "applied")
	if !strings.Contains(status.Body.String(), `"max_connections":2048`) ||
		status.Header().Get("Cache-Control") != "no-store" ||
		strings.Contains(status.Body.String(), input.HostFingerprint) {
		t.Fatal(status.Body, status.Header())
	}
	select {
	case <-runner.inputs:
		t.Fatal("a retry started a second installation")
	default:
	}

	// A route edited while the remote host validates must never be activated.
	runner.proceed = make(chan struct{})
	input.RequestID = strings.Repeat("b", 32)
	body, _ = json.Marshal(input)
	result := doRequest(t, s, cookie, "POST", installPath, map[string]string{"If-Match": "3"}, body)
	if result.Code != 202 {
		t.Fatal(result.Code, result.Body)
	}
	select {
	case <-runner.inputs:
	case <-time.After(time.Second):
		t.Fatal("installer did not start validating")
	}
	if result := doRequest(t, s, cookie, "PUT", path, map[string]string{"If-Match": "3"}, raw); result.Code != 204 {
		t.Fatal(result.Code, result.Body)
	}
	close(runner.proceed)
	status = waitForIngressJob(t, s, cookie, installPath, "failed")
	if !strings.Contains(status.Body.String(), string(nodeinstall.ErrIngressStale)) {
		t.Fatal(status.Body)
	}
}

type testIngressInstaller struct {
	NodeInstaller
	inputs  chan string
	proceed chan struct{}
}

func (f *testIngressInstaller) InstallIngress(ctx context.Context, _ nodeinstall.IngressRequest, config, _ string, _ int64, current func(context.Context) bool, report func(string)) (int, error) {
	report("validating")
	f.inputs <- config
	select {
	case <-f.proceed:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	if !current(ctx) {
		return 0, nodeinstall.ErrIngressStale
	}
	return 2048, nil
}

func waitForIngressJob(t *testing.T, s *Server, cookie *http.Cookie, path, state string) *httptest.ResponseRecorder {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		status := doRequest(t, s, cookie, "GET", path, nil, nil)
		var value clusterIngressStatus
		if status.Code == 200 && json.Unmarshal(status.Body.Bytes(), &value) == nil && value.Job != nil && value.Job.State == state {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("installation did not reach %s", state)
	return nil
}
