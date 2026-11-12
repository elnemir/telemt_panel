package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/store"
)

func TestAdminAccountsLifecycle(t *testing.T) {
	srv := newTestServer(t)
	path := filepath.Join(t.TempDir(), "panel-state.json")
	openState := func(configHash string) *store.Memory {
		t.Helper()
		st, err := store.NewState(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.BindPasswordAuth(srv.cfg.Auth.Username, configHash); err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := openState(srv.cfg.Auth.PasswordHash)
	t.Cleanup(func() { _ = st.Close() })
	replaceTestServerStore(srv, st)
	t.Cleanup(func() { srv.sessions.Close() })
	handler := srv.Handler()

	signIn := func(name, password string, expected int) *http.Cookie {
		t.Helper()
		w, cookie := login(t, handler, name, password)
		if w.Code != expected || expected == 204 && cookie == nil {
			t.Fatalf("login %q: status %d, want %d", name, w.Code, expected)
		}
		return cookie
	}
	request := func(cookie *http.Cookie, method, path string, data adminChangeRequest, expected int) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s %s: %d, want %d (%s)", method, path, w.Code, expected, w.Body)
		}
		return w
	}

	owner := signIn("admin", testPassword, 204)
	const name = "managed.admin"
	const initialPassword = "synthetic-initial-password"
	const nextPassword = "synthetic-next-password"
	create := adminChangeRequest{Username: name, CurrentPassword: testPassword, NewPassword: initialPassword}
	request(nil, "GET", "/api/auth/admins", adminChangeRequest{}, 401)
	request(owner, "POST", "/api/auth/admins", adminChangeRequest{Username: name, CurrentPassword: "wrong", NewPassword: initialPassword}, 403)
	request(owner, "POST", "/api/auth/admins", create, 204)
	request(owner, "POST", "/api/auth/admins", create, 409)
	list := request(owner, "GET", "/api/auth/admins", adminChangeRequest{}, 200)
	var accounts adminAccountsResponse
	if err := json.Unmarshal(list.Body.Bytes(), &accounts); err != nil {
		t.Fatal(err)
	}
	if !accounts.Writable || len(accounts.Accounts) != 2 || accounts.Accounts[0] != (adminAccountInfo{"admin", true, true}) ||
		accounts.Accounts[1] != (adminAccountInfo{name, false, false}) {
		t.Fatalf("unexpected accounts: %+v", accounts)
	}
	if strings.Contains(list.Body.String(), "password") {
		t.Fatal("account list includes password data")
	}

	managed := signIn(name, initialPassword, 204)
	another := signIn(name, initialPassword, 204)
	managedID, ownerID := auth.HashToken(managed.Value), auth.HashToken(owner.Value)
	me := request(managed, "GET", "/api/auth/me", adminChangeRequest{}, 200)
	if !strings.Contains(me.Body.String(), `"username":"managed.admin"`) || !strings.Contains(me.Body.String(), `"passkey_management":false`) {
		t.Fatalf("wrong identity: %s", me.Body)
	}
	sessions := request(managed, "GET", "/api/auth/sessions", adminChangeRequest{}, 200)
	if !strings.Contains(sessions.Body.String(), managedID) || strings.Contains(sessions.Body.String(), ownerID) {
		t.Fatal("sessions are not scoped to their administrator")
	}
	request(managed, "DELETE", "/api/auth/sessions/"+ownerID, adminChangeRequest{}, 404)
	request(managed, "DELETE", "/api/auth/sessions", adminChangeRequest{}, 204)
	request(another, "GET", "/api/auth/me", adminChangeRequest{}, 401)
	request(owner, "GET", "/api/auth/me", adminChangeRequest{}, 200)
	request(managed, "POST", "/api/auth/webauthn/register/begin", adminChangeRequest{}, 403)
	request(managed, "DELETE", "/api/auth/webauthn/credentials/anything", adminChangeRequest{}, 403)
	request(managed, "POST", "/api/auth/admins/"+name+"/delete", adminChangeRequest{CurrentPassword: initialPassword}, 403)
	request(managed, "POST", "/api/auth/admins/admin/delete", adminChangeRequest{CurrentPassword: initialPassword}, 403)

	csrf := httptest.NewRequest("PUT", "/api/auth/password", strings.NewReader(`{"current_password":"irrelevant","new_password":"synthetic-password"}`))
	csrf.AddCookie(managed)
	csrf.Header.Set("Sec-Fetch-Site", "cross-site")
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, csrf)
	if denied.Code != 403 {
		t.Fatalf("cross-site password change = %d", denied.Code)
	}

	stream, release, err := srv.sessions.Track(t.Context(), managedID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	request(managed, "PUT", "/api/auth/password", adminChangeRequest{CurrentPassword: initialPassword, NewPassword: "short"}, 400)
	request(managed, "PUT", "/api/auth/password", adminChangeRequest{CurrentPassword: initialPassword, NewPassword: nextPassword}, 204)
	if stream.Err() == nil {
		t.Fatal("password change did not cancel stream")
	}
	request(managed, "GET", "/api/auth/me", adminChangeRequest{}, 401)
	signIn(name, initialPassword, 401)
	managed = signIn(name, nextPassword, 204)

	// A disk reopen preserves the account and its scoped session; exports omit
	// the local authentication data, including managed session token hashes.
	reopened := openState(srv.cfg.Auth.PasswordHash)
	if !auth.VerifyPassword(reopened.AdminPassword(name, "admin", srv.cfg.Auth.PasswordHash), nextPassword) {
		t.Fatal("managed password did not survive reopen")
	}
	id := auth.HashToken(managed.Value)
	if session, ok, err := reopened.GetSession(id); err != nil || !ok || session.Username != name {
		t.Fatal("managed session did not survive reopen")
	}
	portable, err := reopened.ExportData()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := portable.Sessions[id]; ok {
		t.Fatal("portable export included a managed session")
	}

	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	request(owner, "POST", "/api/auth/admins/"+name+"/delete", adminChangeRequest{CurrentPassword: testPassword}, 204)
	request(managed, "GET", "/api/auth/me", adminChangeRequest{}, 401)
	signIn(name, nextPassword, 401)
	request(owner, "PUT", "/api/auth/password", adminChangeRequest{CurrentPassword: testPassword, NewPassword: nextPassword}, 204)
	request(owner, "GET", "/api/auth/me", adminChangeRequest{}, 401)
	owner = signIn("admin", nextPassword, 204)
	signIn("admin", testPassword, 401)

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	afterChange := openState(srv.cfg.Auth.PasswordHash)
	if !auth.VerifyPassword(afterChange.AdminPassword("admin", "admin", srv.cfg.Auth.PasswordHash), nextPassword) {
		t.Fatal("primary override did not survive reopen")
	}
	_ = afterChange.Close()
	recoveryHash, err := auth.HashPassword("synthetic-recovery-password")
	if err != nil {
		t.Fatal(err)
	}
	recovered := openState(recoveryHash)
	defer recovered.Close()
	if recovered.AdminPassword("admin", "admin", recoveryHash) != recoveryHash {
		t.Fatal("changing TOML credentials did not restore configuration precedence")
	}
	if _, ok, _ := recovered.GetSession(auth.HashToken(owner.Value)); ok {
		t.Fatal("config recovery left a previous primary session active")
	}
}
