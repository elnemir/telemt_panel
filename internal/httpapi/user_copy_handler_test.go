package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/amirotin/telemt_panel/internal/nodeagent"
	"github.com/amirotin/telemt_panel/internal/store"
	"github.com/amirotin/telemt_panel/internal/usercopy"
)

func TestUserCopyHTTPAuthScopeConflictAndReplay(t *testing.T) {
	s, cookie, a, b, _, _ := nodeTestFixture(t)
	state, e := store.NewState(filepath.Join(t.TempDir(), "copy.json"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = state.Close() })
	s.userCopies, e = usercopy.New(state, s.withCopyNode)
	if e != nil {
		t.Fatal(e)
	}
	request := func(method, path string, body any, key string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		headers := map[string]string{}
		if key != "" {
			headers["Idempotency-Key"] = key
		}
		return doRequest(t, s, cookie, method, path, headers, raw)
	}
	const username = "copy-source"
	w := request("POST", nodeURL(a.ID, "/users"), map[string]any{"username": username, "secret": "0123456789abcdef0123456789abcdef", "max_tcp_conns": 7}, "")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	selection := usercopy.Selection{SourceID: a.ID, Username: username, Targets: []string{b.ID, "default"}, Options: usercopy.Options{Limits: true, Enabled: true}}
	raw, _ := json.Marshal(selection)
	unauth := httptest.NewRecorder()
	unauthRequest := httptest.NewRequest("POST", "/api/user-copies/prepare", nil)
	unauthRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	s.Handler().ServeHTTP(unauth, unauthRequest)
	if unauth.Code != 401 {
		t.Fatal(unauth.Code)
	}
	if w = doRequest(t, s, cookie, "POST", "/api/user-copies/prepare", map[string]string{"Sec-Fetch-Site": "cross-site"}, raw); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	w = request("POST", "/api/user-copies/prepare", selection, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var preview usercopy.Preview
	_ = json.Unmarshal(w.Body.Bytes(), &preview)
	if preview.Fields.Secret != "" || len(preview.Targets) != 2 {
		t.Fatal(preview)
	}
	key := nodeagent.NewCommandKey()
	body := map[string]string{"token": preview.Token}
	w = request("POST", "/api/user-copies", body, key)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var result usercopy.Result
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	for _, target := range result.Targets {
		if target.State != "persisted" {
			t.Fatal(target)
		}
	}
	if w = request("POST", "/api/user-copies", body, key); w.Code != 200 {
		t.Fatal("replay", w.Code, w.Body)
	}
	if w = request("GET", "/api/user-copies/"+key, nil, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	selection.Targets = []string{b.ID}
	w = request("POST", "/api/user-copies/prepare", selection, "")
	_ = json.Unmarshal(w.Body.Bytes(), &preview)
	if w.Code != 200 || preview.Targets[0].State != "skipped" {
		t.Fatal(w.Code, w.Body)
	}
	// A node-scoped operation cannot escape into the global transfer API.
	if w = request("POST", nodeURL(a.ID, "/user-copies/prepare"), selection, ""); w.Code != 404 {
		t.Fatal("copy incorrectly remapped into node scope", w.Code)
	}
}
