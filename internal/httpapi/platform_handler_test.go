//go:build !lite

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirotin/telemt_panel/internal/platform"
	"github.com/amirotin/telemt_panel/internal/subpage"
	"github.com/amirotin/telemt_panel/internal/telemt"
	"github.com/amirotin/telemt_panel/internal/usercopy"
)

func TestPlatformHTTPIsolationRevisionsAndManagedUserProtection(t *testing.T) {
	s, cookie, a, b, upA, upB := nodeTestFixture(t)
	db, e := platform.Open(filepath.Join(t.TempDir(), "platform.sqlite"), bytes.Repeat([]byte{3}, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s.platform = db
	s.cfg.Subpage.Enabled = true
	s.cfg.Subpage.BasePath = "/sub"
	s.cfg.Subpage.Secret = "fixture-subpage-key"
	s.subSvc = subpage.NewService(s.cfg.Subpage.Secret, s.cfg.Subpage.BasePath, s.st)
	request := func(method, path string, body any, revision int64) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		headers := map[string]string{}
		if revision != 0 {
			headers["If-Match"] = fmt.Sprint(revision)
		}
		return doRequest(t, s, cookie, method, path, headers, raw)
	}
	unauth := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauth, httptest.NewRequest("GET", "/api/clusters", nil))
	if unauth.Code != 401 {
		t.Fatal(unauth.Code)
	}
	if w := doRequest(t, s, cookie, "POST", "/api/clusters", map[string]string{"Sec-Fetch-Site": "cross-site"}, []byte(`{"name":"C"}`)); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	w := request("POST", "/api/clusters", map[string]string{"name": "C"}, 0)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var c platform.Cluster
	if e = json.Unmarshal(w.Body.Bytes(), &c); e != nil {
		t.Fatal(e)
	}
	path := "/api/clusters/" + c.ID
	if w = request("POST", path+"/members", map[string]string{"node_id": a.ID}, 0); w.Code != 428 {
		t.Fatal("missing revision", w.Code)
	}
	if w = request("POST", path+"/members", map[string]string{"node_id": a.ID}, 1); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if w = request("POST", path+"/members", map[string]string{"node_id": b.ID}, 1); w.Code != 409 {
		t.Fatal("stale membership", w.Code)
	}
	if w = request("DELETE", "/api/nodes/"+a.ID, nil, int64(a.Revision)); w.Code != 409 || !strings.Contains(w.Body.String(), "node_cluster_owned") {
		t.Fatal(w.Code, w.Body)
	}

	for _, body := range []any{map[string]any{"username": "missing-policy"}, map[string]any{"username": "missing-enabled", "policy": map[string]any{"max_tcp_conns": 7}}} {
		if w = request("POST", path+"/identities", body, 2); w.Code != 400 {
			t.Fatal("required policy not enforced", w.Code, w.Body)
		}
	}
	const name = "cluster-owned"
	w = request("POST", path+"/identities", platform.IdentityInput{Username: name, Policy: platform.Policy{Enabled: true}}, 2)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var identity platform.Identity
	if e = json.Unmarshal(w.Body.Bytes(), &identity); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("secret leaked in identity", w.Body)
	}
	worker := platform.NewWorker(db, s.withPlatformNode)
	if ran, e := worker.ProcessNext(context.Background()); !ran || e != nil {
		t.Fatal(ran, e)
	}
	w = request("GET", "/api/identities/"+identity.ID, nil, 0)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var detail identityDetail
	if e = json.Unmarshal(w.Body.Bytes(), &detail); e != nil {
		t.Fatal(e)
	}
	if len(detail.Jobs) != 1 || detail.Jobs[0].Status != "applied" {
		t.Fatal(w.Body)
	}
	if len(detail.Links) != 0 {
		t.Fatal("cluster published a link before ingress activation")
	}
	if err := db.RecordAppliedIngress(context.Background(), c.ID, 3, "192.0.2.20", 10443); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/api/identities/"+identity.ID, nil, 0)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || len(detail.Links) == 0 ||
		!strings.Contains(detail.Links[0].URL, "port=10443") || !strings.Contains(detail.Links[0].URL, "server=192.0.2.20") {
		t.Fatal("cluster detail missing applied links", w.Code)
	}
	w = request("GET", nodeURL(a.ID, "/users/"+name), nil, 0)
	var managed userResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &managed) != nil || !managed.ClusterManaged ||
		len(managed.Links.Classic) == 0 || !strings.Contains(managed.Links.Classic[0], "server=192.0.2.20") {
		t.Fatal("node detail bypasses the cluster ingress", w.Code)
	}
	sub := request("GET", nodeURL(a.ID, "/users/"+name+"/sublink"), nil, 0)
	var link sublinkResponse
	if sub.Code != 200 || json.Unmarshal(sub.Body.Bytes(), &link) != nil {
		t.Fatal("subscription URL unavailable", sub.Code)
	}
	public := httptest.NewRecorder()
	s.SubscriptionHandler().ServeHTTP(public, httptest.NewRequest("GET", pathOf(t, link.URL), nil))
	if public.Code != 200 || !strings.Contains(public.Body.String(), "server=192.0.2.20") ||
		!strings.Contains(public.Body.String(), "port=10443") {
		t.Fatal("public subscription missing applied ingress", public.Code)
	}
	w = request("GET", nodeURL(a.ID, "/users/alice"), nil, 0)
	var local userResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &local) != nil || local.ClusterManaged ||
		strings.Contains(w.Body.String(), "server=192.0.2.20") {
		t.Fatal("local user incorrectly uses cluster ingress", w.Code)
	}
	for _, op := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/users/" + name, map[string]any{"enabled": false}},
		{"DELETE", "/users/" + name, nil},
		{"POST", "/users/" + name + "/rotate-secret", nil},
		{"PUT", "/users/" + name + "/enabled", map[string]any{"enabled": false}},
		{"POST", "/users", map[string]any{"username": name}},
	} {
		w = request(op.method, nodeURL(a.ID, op.path), op.body, 0)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "user_cluster_owned") {
			t.Fatal(op, w.Code, w.Body)
		}
	}
	// Same username on an independent node remains independently editable.
	if w = request("POST", nodeURL(b.ID, "/users"), map[string]any{"username": name}, 0); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w = request("PATCH", nodeURL(b.ID, "/users/"+name), map[string]any{"enabled": false}, 0); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	client := clusterProtectedCopyClient{Client: telemt.New(upA.URL, ""), state: db, node: a.ID}
	if _, e = client.PatchUserWithRevision(context.Background(), name, map[string]any{"enabled": false}, "anything"); e == nil {
		t.Fatal("copy bypassed cluster owner")
	}
	var _ usercopy.Client = client
	aUsers, e := telemt.New(upA.URL, "").Users(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	aUser, ok := findUser(aUsers, name)
	if !ok || !aUser.Enabled {
		t.Fatal("A changed", aUser)
	}
	bUsers, e := telemt.New(upB.URL, "").Users(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	bUser, ok := findUser(bUsers, name)
	if !ok || bUser.Enabled {
		t.Fatal("B did not change", bUser)
	}
	// Global routes never become reachable through node scope.
	if w = request("GET", nodeURL(a.ID, "/clusters"), nil, 0); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w = request("PUT", "/api/identities/"+identity.ID, map[string]any{"policy": identity.Policy, "rotate_secret": true}, 1); w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	if w = request("PUT", "/api/identities/"+identity.ID, map[string]any{"policy": identity.Policy}, 1); w.Code != 409 {
		t.Fatal("stale identity", w.Code)
	}
	if ran, e := worker.ProcessNext(context.Background()); !ran || e != nil {
		t.Fatal(ran, e)
	}
}
