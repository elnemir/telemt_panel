package httpapi

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amirotin/telemt_panel/internal/branding"
	"github.com/amirotin/telemt_panel/internal/nodes"
	"github.com/amirotin/telemt_panel/internal/subpage"
	"github.com/amirotin/telemt_panel/internal/telemt/telemttest"
)

func TestSubscriptionAppearancePreviewSaveAndConflict(t *testing.T) {
	s, cookie := newSubpageTestServer(t, true)
	const path = "/api/settings/subscription"
	h := s.Handler()
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest("GET", path, nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("appearance API was public: %d", unauth.Code)
	}
	initial := doRequest(t, s, cookie, "GET", path, nil, nil)
	var draft subpage.AppearanceSnapshot
	if initial.Code != 200 || json.Unmarshal(initial.Body.Bytes(), &draft) != nil || draft.Revision == "" {
		t.Fatalf("appearance read: %d %s", initial.Code, initial.Body)
	}
	link := getSublink(t, h, cookie)
	draft.Settings.Language, draft.Settings.Theme, draft.Settings.Accent = "ru", "light", "violet"
	draft.Settings.RU.Title = "Моя подписка"
	draft.Settings.RU.Description = "<script>alert(1)</script>"
	draft.Settings.RU.Instructions = "Настройка\nTelegram"
	draft.Settings.RU.SupportLabel = "Написать"
	draft.Settings.SupportURL = "https://example.test/support"
	draft.Settings.Hidden = []string{"qr", "manual"}
	draft.Settings.Connections = []string{"classic"}
	raw, _ := json.Marshal(draft.Settings)
	preview := doRequest(t, s, cookie, "POST", path+"/preview?state=expired", nil, raw)
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "demo") ||
		strings.Contains(preview.Body.String(), "alice") || !strings.Contains(preview.Body.String(), "status-expired") ||
		strings.Contains(preview.Body.String(), draft.Settings.RU.Description) ||
		!strings.Contains(preview.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("invalid preview: %d", preview.Code)
	}
	unchanged := doRequest(t, s, cookie, "GET", path, nil, nil)
	if unchanged.Body.String() != initial.Body.String() {
		t.Fatal("preview persisted the draft")
	}
	body, _ := json.Marshal(draft)
	put := doRequest(t, s, cookie, "PUT", path, nil, body)
	if put.Code != 200 || put.Body.String() == initial.Body.String() {
		t.Fatalf("appearance save: %d %s", put.Code, put.Body)
	}
	conflict := doRequest(t, s, cookie, "PUT", path, nil, body)
	if conflict.Code != 409 || !strings.Contains(conflict.Body.String(), "subscription_settings_conflict") {
		t.Fatalf("stale write accepted: %d", conflict.Code)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", pathOf(t, link.URL), nil)
	req.Header.Set("Accept-Language", "en")
	s.SubscriptionHandler().ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("public page after save: %d", w.Code)
	}
	html := w.Body.String()
	for _, want := range []string{`lang="ru"`, "Моя подписка", "alice", "Настройка\nTelegram", "&lt;script&gt;", "https://example.test/support", "--acc:#624098"} {
		if !strings.Contains(html, want) {
			t.Errorf("public page missing %q", want)
		}
	}
	for _, unwanted := range []string{draft.Settings.RU.Description, `class="card qr-card"`, `class="manual"`} {
		if strings.Contains(html, unwanted) {
			t.Errorf("public page contains %q", unwanted)
		}
	}
	draft.Settings.SupportURL = "javascript:alert(1)"
	bad, _ := json.Marshal(draft.Settings)
	if invalid := doRequest(t, s, cookie, "POST", path+"/preview", nil, bad); invalid.Code != 400 {
		t.Fatalf("unsafe URL accepted: %d", invalid.Code)
	}
}

func TestRegisteredNodeSubscriptionRoutingAndRotation(t *testing.T) {
	s, cookie := newSubpageTestServer(t, true)
	s.cfg.ControlPlaneOnly = true
	s.branding = branding.New(s.st, "", t.TempDir())
	var logo bytes.Buffer
	if err := png.Encode(&logo, image.NewNRGBA(image.Rect(0, 0, 32, 16))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.branding.SaveUpload("Team", logo.Bytes()); err != nil {
		t.Fatal(err)
	}
	var err error
	s.nodes, err = nodes.Open(s.st, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.nodes.Close)
	t.Cleanup(s.CloseNodeRuntimes)
	register := func(name string) nodes.Node {
		upstream := telemttest.New(telemttest.Scenario{})
		t.Cleanup(upstream.Close)
		node, err := s.nodes.Create(nodes.Input{Name: name, APIURL: upstream.URL, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return node
	}
	a, b := register("a"), register("b")
	get := func(method, id string) string {
		t.Helper()
		w := doRequest(t, s, cookie, method, nodeURL(id, "/users/alice/sublink"), nil, nil)
		var response sublinkResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || !response.Enabled {
			t.Fatalf("sublink request: %d %s", w.Code, w.Body)
		}
		return pathOf(t, response.URL)
	}
	fetch := func(path string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		s.SubscriptionHandler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != status {
			t.Fatalf("public response: %d want %d", w.Code, status)
		}
		if status == 200 && (!strings.Contains(w.Body.String(), "alice") ||
			!strings.Contains(w.Body.String(), `class="page-logo"`) || !strings.Contains(w.Body.String(), "data:image/png;base64,")) {
			t.Fatal("public response missing subscriber or shared logo")
		}
	}
	linkA, linkB := get("GET", a.ID), get("GET", b.ID)
	if !strings.Contains(linkA, a.ID+".") || !strings.Contains(linkB, b.ID+".") || linkA == linkB {
		t.Fatal("links were not scoped to different nodes")
	}
	fetch(linkA, 200)
	fetch(linkB, 200)
	fetch(strings.Replace(linkA, a.ID, b.ID, 1), 404)
	rotated := get("POST", a.ID)
	fetch(linkA, 404)
	fetch(rotated, 200)
	fetch(linkB, 200)
	fetch("/sub/invalid-legacy-link", 404)
}
