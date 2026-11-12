//go:build !lite

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/config"
	"github.com/amirotin/telemt_panel/internal/customer"
	"github.com/amirotin/telemt_panel/internal/platform"
)

func TestCustomerHTTPIsolatesAdminCustomersAndFinancialObjects(t *testing.T) {
	s := newTestServer(t)
	_, adminCookie := login(t, s.Handler(), "admin", testPassword)
	db, e := platform.Open(filepath.Join(t.TempDir(), "platform.sqlite"), bytes.Repeat([]byte{4}, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s.platform = db
	cfg := config.CustomerPortalConfig{Enabled: true, PublicURL: "https://panel.example.test", Email: config.CustomerEmailConfig{Enabled: true, Host: "smtp.example.test", Port: 465, Mode: "tls", From: "sender@example.test", Username: "sender", PasswordEnv: "TELEMT_CUSTOMER_HTTP_SMTP"}, Telegram: config.CustomerTelegramConfig{Enabled: true, ClientID: "12345", SecretEnv: "TELEMT_CUSTOMER_HTTP_OIDC"}}
	service, e := customer.New(db, cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
	s.customer = service
	// SMTP is deliberately unconfigured: no test may send email to a live server.
	t.Setenv(cfg.Email.PasswordEnv, "")
	t.Setenv(cfg.Telegram.SecretEnv, "fixture-only-secret")
	password := "Customer-password-123!"
	hash, e := auth.HashPassword(password)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	for _, email := range []string{"alice@example.test", "bob@example.test"} {
		token, e := auth.NewToken()
		if e != nil {
			t.Fatal(e)
		}
		if e = db.PrepareEmailChallenge(ctx, "register", email, auth.HashToken(token), hash, ""); e != nil {
			t.Fatal(e)
		}
		if _, e = db.CompleteEmailChallenge(ctx, auth.HashToken(token), ""); e != nil {
			t.Fatal(e)
		}
	}
	request := func(cookie *http.Cookie, method, path string, body any, cross bool) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if cross {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	customerLogin := func(email string) *http.Cookie {
		t.Helper()
		w := request(nil, "POST", "/api/customer/auth/login", map[string]string{"email": email, "password": password}, false)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "hash") {
			t.Fatal("credential exposed")
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == customerCookieName {
				if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
					t.Fatal(c)
				}
				return c
			}
		}
		t.Fatal("missing cookie")
		return nil
	}
	alice := customerLogin("alice@example.test")
	bob := customerLogin("bob@example.test")
	for _, cookie := range []*http.Cookie{nil, adminCookie} {
		if w := request(cookie, "GET", "/api/customer/auth/me", nil, false); w.Code != 401 {
			t.Fatal("admin crossed customer boundary", w.Code, w.Body)
		}
	}
	if w := request(alice, "GET", "/api/billing/invoices", nil, false); w.Code != 401 {
		t.Fatal("customer crossed admin boundary", w.Code, w.Body)
	}
	if w := request(nil, "POST", "/api/customer/auth/login", map[string]string{"email": "alice@example.test", "password": password}, true); w.Code != 403 {
		t.Fatal("login CSRF", w.Code)
	}
	if w := request(alice, "POST", "/api/customer/auth/logout", nil, true); w.Code != 403 {
		t.Fatal("logout CSRF", w.Code)
	}
	if w := request(nil, "POST", "/api/customer/auth/register", map[string]string{"email": "new@example.test", "password": password}, false); w.Code != 503 {
		t.Fatal("fake delivery", w.Code, w.Body)
	}
	c, e := db.CreateCluster(ctx, "Customer cluster")
	if e != nil {
		t.Fatal(e)
	}
	terms := platform.PlanTerms{Currency: "RUB", CurrencyScale: 2, PriceMinor: 9900, DurationSeconds: 86400, NodePolicy: platform.Policy{Enabled: true}}
	plan, e := db.CreatePlan(ctx, c.ID, "Paid service", true, terms)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.CreatePlan(ctx, c.ID, "Hidden service", false, terms)
	if e != nil {
		t.Fatal(e)
	}
	a, e := db.CreatePaymentAccount(ctx, "Live merchant", "robokassa", false, true, platform.PaymentAccountConfig{Merchant: "fixture", Algorithm: "SHA256", Password1Env: "TELEMT_PAYMENT_CUSTOMER_HTTP_1", Password2Env: "TELEMT_PAYMENT_CUSTOMER_HTTP_2"})
	if e != nil {
		t.Fatal(e)
	}
	sandbox, e := db.CreatePaymentAccount(ctx, "Sandbox", "robokassa", true, true, a.Config)
	if e != nil {
		t.Fatal(e)
	}
	w := request(alice, "GET", "/api/customer/plans", nil, false)
	if w.Code != 200 || strings.Contains(w.Body.String(), "Hidden service") {
		t.Fatal(w.Code, w.Body)
	}
	w = request(alice, "GET", "/api/customer/payment-options", nil, false)
	if w.Code != 200 || strings.Contains(w.Body.String(), "TELEMT_PAYMENT") || strings.Contains(w.Body.String(), sandbox.ID) {
		t.Fatal("merchant settings leaked", w.Code, w.Body)
	}
	input := platform.InvoiceInput{PlanID: plan.ID, AccountID: a.ID, RequestKey: "customer-invoice-key-0001"}
	w = request(alice, "POST", "/api/customer/invoices", input, false)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var invoice platform.Invoice
	if json.Unmarshal(w.Body.Bytes(), &invoice) != nil {
		t.Fatal(w.Body)
	}
	w = request(alice, "POST", "/api/customer/invoices", input, false)
	if w.Code != 201 || !strings.Contains(w.Body.String(), invoice.ID) {
		t.Fatal("invoice replay", w.Code, w.Body)
	}
	for _, path := range []string{"/api/customer/invoices/" + invoice.ID, "/api/customer/subscriptions/" + invoice.SubscriptionID, "/api/customer/subscriptions/" + invoice.SubscriptionID + "/periods"} {
		if w = request(bob, "GET", path, nil, false); w.Code != 404 {
			t.Fatal("cross-customer read", path, w.Code, w.Body)
		}
		if w = request(alice, "GET", path, nil, false); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w = request(alice, "GET", "/api/customer/subscriptions/"+invoice.SubscriptionID, nil, false)
	var detail struct {
		AccessStatus   string                 `json:"access_status"`
		Access         platform.ServiceAccess `json:"access"`
		UsageAvailable bool                   `json:"usage_available"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.AccessStatus != "unpaid" || detail.Access.Status != "unpaid" || detail.UsageAvailable || detail.Access.Nodes == nil {
		t.Fatal("unpaid access contract", w.Code, w.Body)
	}
	if w = request(bob, "GET", "/api/customer/invoices", nil, false); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Code, w.Body)
	}
	input.SubscriptionID = invoice.SubscriptionID
	input.RequestKey = "customer-invoice-key-0002"
	if w = request(bob, "POST", "/api/customer/invoices", input, false); w.Code != 404 {
		t.Fatal("cross-customer renewal", w.Code, w.Body)
	}
	input.SubscriptionID = ""
	input.AccountID = sandbox.ID
	if w = request(alice, "POST", "/api/customer/invoices", input, false); w.Code != 404 {
		t.Fatal("sandbox offered as live checkout", w.Code, w.Body)
	}
	t.Setenv(a.Config.Password1Env, "checkout-fixture-secret")
	t.Setenv(a.Config.Password2Env, "callback-fixture-secret")
	endpoint := "/api/customer/invoices/" + invoice.ID + "/checkout"
	if w = request(bob, "POST", endpoint, map[string]string{"method": "AC"}, false); w.Code != 404 {
		t.Fatal("cross-customer checkout", w.Code, w.Body)
	}
	if w = request(alice, "POST", endpoint, map[string]string{"method": "AC", "return_url": "https://attacker.test"}, false); w.Code != 400 {
		t.Fatal("browser redirect accepted", w.Code)
	}
	if w = request(alice, "POST", endpoint, map[string]string{"method": "AC"}, false); w.Code != 200 || !strings.Contains(w.Body.String(), "https://auth.robokassa.ru/Merchant/Index.aspx") || strings.Contains(w.Body.String(), "fixture-secret") {
		t.Fatal(w.Code, w.Body)
	}
	// Starting Telegram only constructs a URL; the test does not contact Telegram.
	if w = request(alice, "POST", "/api/customer/auth/telegram/start", map[string]bool{"link": true}, false); w.Code != 200 || !strings.Contains(w.Body.String(), "code_challenge_method=S256") {
		t.Fatal(w.Code, w.Body)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == customerOIDCCookieName && (!cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode) {
			t.Fatal(cookie)
		}
	}
	if w = request(alice, "POST", "/api/customer/auth/logout", nil, false); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if w = request(alice, "GET", "/api/customer/auth/me", nil, false); w.Code != 401 {
		t.Fatal("revoked customer session", w.Code)
	}
	if w = request(bob, "GET", "/api/customer/auth/me", nil, false); w.Code != 200 {
		t.Fatal("other customer revoked", w.Code)
	}
	// Disabling administrator authentication does not disable customer isolation.
	s.cfg.Auth.Disabled = true
	if w = request(nil, "GET", "/api/customer/invoices", nil, false); w.Code != 401 {
		t.Fatal("anonymous customer access", w.Code, w.Body)
	}
}
