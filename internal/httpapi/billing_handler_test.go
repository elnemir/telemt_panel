//go:build !lite

package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/platform"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

func TestBillingHTTPAdminContractAndSignedPublicCallback(t *testing.T) {
	s := newTestServer(t)
	_, cookie := login(t, s.Handler(), "admin", testPassword)
	if cookie == nil {
		t.Fatal("login")
	}
	path := filepath.Join(t.TempDir(), "platform.sqlite")
	db, e := platform.Open(path, bytes.Repeat([]byte{3}, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s.platform = db
	request := func(method, path string, body any, rev int64) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		headers := map[string]string{}
		if rev != 0 {
			headers["If-Match"] = fmt.Sprint(rev)
		}
		return doRequest(t, s, cookie, method, path, headers, raw)
	}
	for _, path := range []string{"/api/billing/plans", "/api/billing/payment-accounts", "/api/billing/invoices", "/api/billing/payments"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	c, e := db.CreateCluster(context.Background(), "Billing cluster")
	if e != nil {
		t.Fatal(e)
	}
	terms := platform.PlanTerms{Currency: "RUB", CurrencyScale: 2, PriceMinor: 9900, DurationSeconds: 86400, NodePolicy: platform.Policy{Enabled: true}}
	input := map[string]any{"cluster_id": c.ID, "name": "Test tariff", "enabled": true, "terms": terms}
	raw, _ := json.Marshal(input)
	if w := doRequest(t, s, cookie, "POST", "/api/billing/plans", map[string]string{"Sec-Fetch-Site": "cross-site"}, raw); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	w := request("POST", "/api/billing/plans", input, 0)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var p platform.Plan
	if e = json.Unmarshal(w.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(w.Body.String(), `"price_minor":"9900"`) {
		t.Fatal("imprecise money JSON")
	}
	updated := map[string]any{"name": "Updated tariff", "enabled": false, "terms": terms}
	planPath := "/api/billing/plans/" + p.ID
	if w = request("PUT", planPath, updated, 0); w.Code != 428 {
		t.Fatal(w.Code, w.Body)
	}
	if w = request("PUT", planPath, updated, 1); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if w = request("PUT", planPath, updated, 1); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	updated["enabled"] = true
	if w = request("PUT", planPath, updated, 2); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	t.Setenv("TELEMT_PAYMENT_HTTP_P1", "checkout-fixture-secret")
	t.Setenv("TELEMT_PAYMENT_HTTP_P2", "callback-fixture-secret")
	input = map[string]any{"name": "Merchant", "provider": "robokassa", "test": false, "enabled": true, "config": platform.PaymentAccountConfig{Merchant: "demo", Algorithm: "SHA256", Password1Env: "TELEMT_PAYMENT_HTTP_P1", Password2Env: "TELEMT_PAYMENT_HTTP_P2"}}
	w = request("POST", "/api/billing/payment-accounts", input, 0)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var a platform.PaymentAccount
	if e = json.Unmarshal(w.Body.Bytes(), &a); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(w.Body.String(), "fixture-secret") {
		t.Fatal("secret exposed")
	}
	accountPath := "/api/billing/payment-accounts/" + a.ID
	if w = request("PUT", accountPath, map[string]any{"name": "Wrong", "enabled": true, "provider": "yoomoney"}, 1); w.Code != 400 {
		t.Fatal("mutable provider", w.Code)
	}
	// Test-only verified customer fixture. No bypass is exposed as an HTTP API.
	u := url.URL{Scheme: "file", Path: path}
	seed, e := sqlitedriver.Open(u.String())
	if e != nil {
		t.Fatal(e)
	}
	_, e = seed.Exec("INSERT INTO customers(id,email,email_verified,created_at,updated_at) VALUES('customer','test@example.test',1,?,?)", time.Now().Unix(), time.Now().Unix())
	if e != nil {
		t.Fatal(e)
	}
	if e = seed.Close(); e != nil {
		t.Fatal(e)
	}
	invoice, e := db.CreateInvoice(context.Background(), "customer", platform.InvoiceInput{PlanID: p.ID, AccountID: a.ID, RequestKey: "http-invoice-key-0001"})
	if e != nil {
		t.Fatal(e)
	}
	// Disable new checkout; already-paid money must still be settled.
	if w = request("PUT", accountPath, map[string]any{"name": "Old account", "enabled": false}, 1); w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	id := strconv.FormatInt(invoice.Number, 10)
	sum := sha256.Sum256([]byte("99.00:" + id + ":callback-fixture-secret"))
	form := url.Values{"OutSum": {"99.00"}, "InvId": {id}, "SignatureValue": {hex.EncodeToString(sum[:])}}.Encode()
	callback := func(path, body, kind string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", kind)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	endpoint := "/api/payments/" + a.ID + "/notify"
	if w = callback(endpoint, strings.Replace(form, "99.00", "98.00", 1), "application/x-www-form-urlencoded"); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	for range 2 {
		if w = callback(endpoint, form, "application/x-www-form-urlencoded; charset=UTF-8"); w.Code != 200 || w.Body.String() != "OK"+id {
			t.Fatal(w.Code, w.Body)
		}
	}
	periods, e := db.CustomerPeriods(context.Background(), "customer", invoice.SubscriptionID, 0)
	if e != nil || len(periods) != 1 {
		t.Fatal(periods, e)
	}
	for _, invalid := range []struct {
		path, body, kind string
		code             int
	}{{endpoint, form, "application/json", 415}, {endpoint + "?InvId=" + id, form, "application/x-www-form-urlencoded", 400}, {endpoint, form + "&InvId=" + id, "application/x-www-form-urlencoded", 400}, {endpoint, strings.Repeat("x", 65537), "application/x-www-form-urlencoded", 400}} {
		if w = callback(invalid.path, invalid.body, invalid.kind); w.Code != invalid.code {
			t.Fatal(w.Code, w.Body)
		}
	}
	for _, path := range []string{"/api/billing/invoices", "/api/billing/payments"} {
		w = request("GET", path, nil, 0)
		if w.Code != 200 || strings.Contains(w.Body.String(), "fingerprint") || strings.Contains(w.Body.String(), "fixture-secret") {
			t.Fatal(w.Code, w.Body)
		}
		var records []json.RawMessage
		if json.Unmarshal(w.Body.Bytes(), &records) != nil || len(records) != 1 {
			t.Fatal(w.Body)
		}
		if w = request("GET", path+"?offset=-1", nil, 0); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	t.Setenv("TELEMT_PAYMENT_HTTP_P2", "")
	if w = callback(endpoint, form, "application/x-www-form-urlencoded"); w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
}
