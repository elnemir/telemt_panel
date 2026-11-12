package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/nodeinstall"
	"github.com/amirotin/telemt_panel/internal/nodes"
)

type testNodeInstaller struct {
	applies atomic.Int32
	release chan struct{}
}

func (i *testNodeInstaller) Preview(_ context.Context, request nodeinstall.Request) (nodeinstall.Plan, error) {
	return nodeinstall.Plan{Request: request, Release: nodeinstall.Release, Architecture: "x86_64",
		PanelPeer: "192.0.2.10", APIURL: "https://192.0.2.20:9443"}, nil
}

func (i *testNodeInstaller) Apply(ctx context.Context, _ nodeinstall.Plan, _ *nodes.Registry, progress func(string)) (string, error) {
	i.applies.Add(1)
	progress("checking")
	select {
	case <-i.release:
		return "n_test", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestNodeInstallAPIAuthAndSingleApply(t *testing.T) {
	s := newTestServer(t)
	installer := &testNodeInstaller{release: make(chan struct{})}
	s.nodeInstallation = newNodeInstallJobs(context.Background(), installer)
	t.Cleanup(s.nodeInstallation.close)
	h := s.Handler()
	_, cookie := login(t, h, "admin", testPassword)
	request := nodeinstall.Request{Name: "New node", Host: "192.0.2.20", SSHPort: 22, SSHUser: "root",
		HostFingerprint: "SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ProxyPort: 443, ManagementPort: 9443}
	send := func(method, path string, auth *http.Cookie, payload any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, mutatingJSON(t, method, path, auth, payload))
		return w
	}
	const preview = "/api/node-installs/preview"
	if w := send("POST", preview, nil, request); w.Code != 401 {
		t.Fatalf("anonymous preview returned %d", w.Code)
	}
	if w := send("POST", preview, cookie, map[string]string{"ssh_password": "not accepted"}); w.Code != 400 {
		t.Fatalf("unexpected credential field returned %d", w.Code)
	}
	w := send("POST", preview, cookie, request)
	var job nodeInstallJob
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &job) != nil || job.State != "prepared" || job.ID == "" {
		t.Fatalf("preview status %d, body %s", w.Code, w.Body)
	}
	id := job.ID
	for range 2 {
		if w := send("POST", "/api/node-installs/"+id+"/apply", cookie, nil); w.Code != 202 {
			t.Fatalf("apply status %d: %s", w.Code, w.Body)
		}
	}
	close(installer.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w := send("GET", "/api/node-installs/"+id, cookie, nil)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &job) != nil {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		if job.State == "installed" {
			if job.NodeID != "n_test" || installer.applies.Load() != 1 {
				t.Fatal("incorrect job result or duplicate apply")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
}
