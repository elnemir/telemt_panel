//go:build !lite

package customer

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/golang-jwt/jwt/v5"
)

func TestTelegramPKCEBrowserBindingAndVerifiedSubject(t *testing.T) {
	s, _ := serviceFixture(t)
	ctx := context.Background()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	start, e := s.StartTelegram(ctx, "192.0.2.1", "")
	if e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(start.URL)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	if q.Get("scope") != "openid profile" || q.Get("redirect_uri") != "https://panel.example.test/base/api/customer/auth/telegram/callback" || q.Get("code_challenge_method") != "S256" {
		t.Fatal(q)
	}
	var exchanges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/keys" {
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "fixture", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
			return
		}
		exchanges.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "12345" || password != "oidc-fixture-secret" {
			t.Error("bad token client authentication")
			w.WriteHeader(401)
			return
		}
		if e := r.ParseForm(); e != nil {
			t.Error(e)
		}
		challenge := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if r.Method != "POST" || r.PostForm.Get("code") != "provider-code" || r.PostForm.Get("redirect_uri") != q.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(challenge[:]) != q.Get("code_challenge") {
			t.Error("PKCE/token contract mismatch")
			w.WriteHeader(400)
			return
		}
		claims := telegramClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: telegramIssuer, Subject: "987654321", Audience: []string{"12345"}, IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, Nonce: q.Get("nonce")}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "fixture"
		signed, e := token.SignedString(key)
		if e != nil {
			t.Error(e)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": signed, "access_token": "not-persisted"})
	}))
	defer server.Close()
	s.oidc.tokenURL = server.URL + "/token"
	s.oidc.keysURL = server.URL + "/keys"
	if _, _, e = s.CompleteTelegram(ctx, "192.0.2.1", q.Get("state"), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "provider-code"); e == nil {
		t.Fatal("wrong browser accepted")
	}
	if exchanges.Load() != 0 {
		t.Fatal("code exchanged without browser proof")
	}
	raw, session, e := s.CompleteTelegram(ctx, "192.0.2.1", q.Get("state"), start.BrowserToken, "provider-code")
	if e != nil || len(raw) != 43 || !session.Customer.TelegramLinked || session.Customer.Email != "" {
		t.Fatal(session, e)
	}
	if _, _, e = s.CompleteTelegram(ctx, "192.0.2.1", q.Get("state"), start.BrowserToken, "provider-code"); e == nil || exchanges.Load() != 1 {
		t.Fatal("replayed code exchanged", e)
	}
}
func TestTelegramIDTokenRejectsInvalidClaimsAndAlgorithms(t *testing.T) {
	verifier := newTelegramOIDC()
	now := time.Now()
	verifier.now = func() time.Time { return now }
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	other, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	verifier.keys = map[string]*rsa.PublicKey{"fixture": &key.PublicKey}
	verifier.fetched = now
	verifier.attempted = now
	valid := func() telegramClaims {
		return telegramClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: telegramIssuer, Subject: "123", Audience: []string{"12345"}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}, Nonce: "expected-nonce"}
	}
	for _, change := range []func(*telegramClaims){func(c *telegramClaims) { c.Issuer = "https://attacker.test" }, func(c *telegramClaims) { c.Audience = []string{"other"} }, func(c *telegramClaims) { c.Audience = []string{"12345", "other"} }, func(c *telegramClaims) { c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute)) }, func(c *telegramClaims) { c.ExpiresAt = nil }, func(c *telegramClaims) { c.IssuedAt = nil }, func(c *telegramClaims) { c.IssuedAt = jwt.NewNumericDate(now.Add(time.Minute)) }, func(c *telegramClaims) { c.Nonce = "other" }, func(c *telegramClaims) { c.Subject = "" }} {
		claims := valid()
		change(&claims)
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "fixture"
		signed, e := token.SignedString(key)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = verifier.verify(context.Background(), signed, "12345", auth.HashToken("expected-nonce")); e == nil {
			t.Fatal("invalid claims accepted", claims)
		}
	}
	for _, signingKey := range []*rsa.PrivateKey{key, other} {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, valid())
		token.Header["kid"] = "fixture"
		raw, e := token.SignedString(signingKey)
		if e != nil {
			t.Fatal(e)
		}
		subject, e := verifier.verify(context.Background(), raw, "12345", auth.HashToken("expected-nonce"))
		if signingKey == key {
			if e != nil || subject != "123" {
				t.Fatal(subject, e)
			}
		} else if e == nil {
			t.Fatal("wrong signature")
		}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, valid())
	token.Header["kid"] = "fixture"
	raw, _ := token.SignedString([]byte("confusion"))
	if _, e = verifier.verify(context.Background(), raw, "12345", auth.HashToken("expected-nonce")); e == nil {
		t.Fatal("algorithm confusion")
	}
}
