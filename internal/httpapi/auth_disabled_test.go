package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDisabledAuthenticationAPI(t *testing.T) {
	s := newTestServer(t)
	s.cfg.Auth.Disabled = true
	h := s.Handler()
	request := func(method, path, site string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(`{}`))
		r.Header.Set("Sec-Fetch-Site", site)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/api/auth/me", "")
	var me meResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &me) != nil || !me.AuthDisabled || me.Username != "anonymous" || len(me.Passkeys) != 0 {
		t.Fatalf("me = %d %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("anonymous cookie issued")
	}
	for _, endpoint := range []struct{ method, path string }{
		{"POST", "/api/auth/login"}, {"POST", "/api/auth/logout"},
		{"GET", "/api/auth/admins"}, {"POST", "/api/auth/admins"}, {"PUT", "/api/auth/password"}, {"POST", "/api/auth/admins/test/delete"},
		{"GET", "/api/auth/sessions"}, {"DELETE", "/api/auth/sessions"}, {"DELETE", "/api/auth/sessions/test"},
		{"POST", "/api/auth/webauthn/register/begin"}, {"POST", "/api/auth/webauthn/register/finish"},
		{"POST", "/api/auth/webauthn/login/begin"}, {"POST", "/api/auth/webauthn/login/finish"}, {"DELETE", "/api/auth/webauthn/credentials/test"},
	} {
		w := request(endpoint.method, endpoint.path, "same-origin")
		if w.Code != 403 || !strings.Contains(w.Body.String(), "auth_disabled") {
			t.Errorf("%s %s: %d %s", endpoint.method, endpoint.path, w.Code, w.Body.String())
		}
	}
	if w := request("GET", "/api/settings/branding", ""); w.Code != 200 {
		t.Fatalf("ordinary API blocked: %d %s", w.Code, w.Body.String())
	}
	if w := request("PUT", "/api/settings/branding", "cross-site"); w.Code != 403 || !strings.Contains(w.Body.String(), "csrf_rejected") {
		t.Fatalf("CSRF bypass: %d", w.Code)
	}
	if w := request("GET", "/api/auth/methods", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"auth_disabled":true`) {
		t.Fatal(w.Body.String())
	}
	sessions, err := s.st.ListSessions()
	if err != nil || len(sessions) != 0 {
		t.Fatal("anonymous requests created sessions", err)
	}
	s.cfg.Auth.Disabled = false
	if w := request("GET", "/api/auth/me", ""); w.Code != 401 {
		t.Fatalf("re-enabled authentication did not reject anonymous request: %d", w.Code)
	}
}
