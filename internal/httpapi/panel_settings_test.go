package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirotin/telemt_panel/internal/config"
	"github.com/amirotin/telemt_panel/internal/store"
)

func TestPanelSettingsLifecycle(t *testing.T) {
	s := newTestServer(t)
	s.cfg.DataDir = t.TempDir()
	s.cfg.Geography.BuildTimeoutSecs = 10
	s.cfg.Telemt.AuthHeader = "private-test-header"
	s.SetPanelSettingsBase(s.cfg)
	state, err := store.NewState(filepath.Join(s.cfg.DataDir, "panel-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	replaceTestServerStore(s, state)
	defer s.sessions.Close()
	handler := s.Handler()
	_, cookie := login(t, handler, s.cfg.Auth.Username, testPassword)

	const path = "/api/settings/panel-config"
	expect := func(method, path string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := accessRequest(t, s, cookie, method, path, body)
		if w.Code != status {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, w.Code, status, w.Body)
		}
		return w
	}
	viewOf := func(w *httptest.ResponseRecorder) panelSettingsView {
		t.Helper()
		var v panelSettingsView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(w.Body.String(), "private-test-header") || strings.Contains(w.Body.String(), testPasswordHash) {
			t.Fatal("private configuration in response")
		}
		return v
	}

	anonymous := accessRequest(t, s, nil, http.MethodGet, path, nil)
	if anonymous.Code != 401 {
		t.Fatalf("anonymous get = %d", anonymous.Code)
	}
	csrf := mutating(http.MethodPut, path, cookie)
	csrf.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, csrf)
	if w.Code != 403 {
		t.Fatalf("cross-site save = %d", w.Code)
	}

	initial := viewOf(expect("GET", path, nil, 200))
	if initial.RestartRequired || !initial.Writable || !initial.SavedValid || initial.Active.MapTimeoutSecs != 10 {
		t.Fatalf("unexpected startup settings: %+v", initial)
	}
	body := map[string]any{"revision": initial.Revision, "settings": map[string]any{"map_timeout_secs": 30, "session_ttl": "48h"}}
	expect("POST", path+"/preview", body, 200)
	file := filepath.Join(s.cfg.DataDir, config.PanelOverridesName)
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("preview wrote settings")
	}
	bad := expect("PUT", path, map[string]any{"revision": initial.Revision, "settings": map[string]any{"map_timeout_secs": 121}}, 422)
	if !strings.Contains(bad.Body.String(), "panel_config_map_timeout_secs") {
		t.Fatalf("expected field-level error, got %s", bad.Body)
	}
	expect("PUT", path, map[string]any{"revision": initial.Revision, "settings": map[string]any{"auth_disabled": true}}, 400)
	saved := viewOf(expect("PUT", path, body, 200))
	if !saved.RestartRequired || saved.Revision == initial.Revision || saved.Active != initial.Active ||
		saved.Saved.MapTimeoutSecs != 30 || saved.Saved.SessionTTL != "48h0m0s" {
		t.Fatalf("unexpected result: %+v", saved)
	}
	expect("PUT", path, body, 409)

	raw, err := config.ReadPanelOverrides(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	next, err := config.ApplyPanelOverrides(s.cfg, raw)
	if err != nil || config.EditablePanelSettings(next) != saved.Saved || next.Telemt.AuthHeader != s.cfg.Telemt.AuthHeader {
		t.Fatalf("saved override does not reproduce the proposed settings: %v", err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("override permissions are not 0600")
	}

	reset := viewOf(expect("PUT", path, map[string]any{"revision": saved.Revision, "settings": map[string]any{}}, 200))
	if reset.RestartRequired || reset.Saved != reset.Startup {
		t.Fatalf("reset did not inherit the startup configuration: %+v", reset)
	}
}
