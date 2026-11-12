//go:build !lite

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/amirotin/telemt_panel/internal/ingress"
	"github.com/amirotin/telemt_panel/internal/platform"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
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
}
