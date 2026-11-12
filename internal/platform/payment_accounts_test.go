//go:build !lite

package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPaymentConnectionWireSettlementAndImmutableConfiguration(t *testing.T) {
	s, p, now := billingFixture(t)
	ctx := context.Background()
	t.Setenv("TELEMT_PAYMENT_TEST_P1", "checkout-secret")
	t.Setenv("TELEMT_PAYMENT_TEST_P2", "notification-secret")
	a, e := s.CreatePaymentAccount(ctx, "Robokassa", "robokassa", false, true, PaymentAccountConfig{Merchant: "test-merchant", Algorithm: "SHA256", Password1Env: "TELEMT_PAYMENT_TEST_P1", Password2Env: "TELEMT_PAYMENT_TEST_P2", Receipt: &ServiceReceipt{Name: "Proxy service", Tax: "none", PaymentMethod: "full_payment"}})
	if e != nil {
		t.Fatal(e)
	}
	invoice, e := s.CreateInvoice(ctx, "customer-a", InvoiceInput{PlanID: p.ID, AccountID: a.ID, RequestKey: "checkout-key-0001"})
	if e != nil {
		t.Fatal(e)
	}
	form, e := s.InvoiceCheckout(ctx, "customer-a", invoice.ID, "AC", "https://example.test/return")
	if e != nil || form.Fields["Receipt"] == "" || form.Action != "https://auth.robokassa.ru/Merchant/Index.aspx" {
		t.Fatal(form, e)
	}
	raw, _ := json.Marshal(form)
	if strings.Contains(string(raw), "checkout-secret") || strings.Contains(string(raw), "notification-secret") {
		t.Fatal("secret exposed")
	}
	if _, e = s.InvoiceCheckout(ctx, "customer-b", invoice.ID, "AC", "https://example.test/return"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if e = s.UpdatePaymentAccount(ctx, a.ID, 1, "Retired checkout", false); e != nil {
		t.Fatal(e)
	}
	if _, e = s.InvoiceCheckout(ctx, "customer-a", invoice.ID, "AC", "https://example.test/return"); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e = s.UpdatePaymentAccount(ctx, a.ID, 1, "Stale", true); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	for _, q := range []string{"UPDATE payment_accounts SET provider='yoomoney' WHERE id=?", "UPDATE payment_accounts SET config='{}' WHERE id=?", "UPDATE payment_accounts SET is_test=1 WHERE id=?"} {
		if _, e = s.db.ExecContext(ctx, q, a.ID); e == nil {
			t.Fatal("mutable merchant", q)
		}
	}
	// Independent wire fixture follows ResultURL: raw OutSum:InvId:Password2.
	id := strconv.FormatInt(invoice.Number, 10)
	sum := sha256.Sum256([]byte("99.000000:" + id + ":notification-secret"))
	body := url.Values{"OutSum": {"99.000000"}, "InvId": {id}, "SignatureValue": {hex.EncodeToString(sum[:])}}.Encode()
	for range 2 {
		ack, e := s.ReceivePayment(ctx, a.ID, []byte(body))
		if e != nil || ack != "OK"+id {
			t.Fatal(ack, e)
		}
	}
	if countTable(t, s, "paid_periods") != 1 {
		t.Fatal("replayed payment")
	}
	var source string
	var at int64
	if e = s.db.QueryRow("SELECT time_source,payment_at FROM payment_events").Scan(&source, &at); e != nil || source != "verified_notification" || at != *now {
		t.Fatal(source, at, e)
	}
	if _, e = s.ReceivePayment(ctx, a.ID, []byte(body+"&InvId="+id)); e == nil {
		t.Fatal("duplicate field accepted")
	}
	if _, e = s.ReceivePayment(ctx, a.ID, []byte(strings.Replace(body, "99.000000", "98.000000", 1))); e == nil {
		t.Fatal("tampered amount")
	}
	if countTable(t, s, "paid_periods") != 1 {
		t.Fatal("bad signature wrote period")
	}
}

func TestYooMoneySignedTimestampAndUnallocatedDuplicateTransfer(t *testing.T) {
	s, p, now := billingFixture(t)
	ctx := context.Background()
	t.Setenv("TELEMT_PAYMENT_YOO_TEST", "fixture-secret")
	a, e := s.CreatePaymentAccount(ctx, "Wallet", "yoomoney", false, true, PaymentAccountConfig{Wallet: "410011234567890", NotificationSecretEnv: "TELEMT_PAYMENT_YOO_TEST"})
	if e != nil {
		t.Fatal(e)
	}
	i, e := s.CreateInvoice(ctx, "customer-a", InvoiceInput{PlanID: p.ID, AccountID: a.ID, RequestKey: "checkout-key-0001"})
	if e != nil {
		t.Fatal(e)
	}
	form, e := s.InvoiceCheckout(ctx, "customer-a", i.ID, "AC", "https://example.test/portal/return")
	if e != nil || form.Fields["label"] != i.ID || form.Fields["sum"] != "99.00" {
		t.Fatal(form, e)
	}
	if _, e = s.InvoiceCheckout(ctx, "customer-a", i.ID, "AC", "javascript:alert(1)"); e == nil {
		t.Fatal("unsafe redirect")
	}
	values := url.Values{"notification_type": {"p2p-incoming"}, "operation_id": {"operation-1"}, "amount": {"98.00"}, "withdraw_amount": {"99.00"}, "currency": {"643"}, "datetime": {time.Unix(*now, 0).UTC().Format(time.RFC3339)}, "sender": {"410099999999"}, "codepro": {"false"}, "unaccepted": {"false"}, "label": {i.ID}}
	sign := func(v url.Values) []byte {
		keys := []string{}
		for k := range v {
			if k != "sign" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		parts := []string{}
		for _, k := range keys {
			parts = append(parts, k+"="+strings.ReplaceAll(url.QueryEscape(v.Get(k)), "+", "%20"))
		}
		h := hmac.New(sha256.New, []byte("fixture-secret"))
		h.Write([]byte(strings.Join(parts, "&")))
		v.Set("sign", hex.EncodeToString(h.Sum(nil)))
		return []byte(v.Encode())
	}
	*now += 120
	if ack, e := s.ReceivePayment(ctx, a.ID, sign(values)); e != nil || ack != "OK" {
		t.Fatal(ack, e)
	}
	periods, e := s.CustomerPeriods(ctx, "customer-a", i.SubscriptionID, 0)
	if e != nil || len(periods) != 1 || periods[0].StartsAt != *now-120 {
		t.Fatal(periods, e)
	}
	values.Set("operation_id", "operation-2")
	if _, e = s.ReceivePayment(ctx, a.ID, sign(values)); e != nil {
		t.Fatal(e)
	}
	if countTable(t, s, "paid_periods") != 1 || countTable(t, s, "billing_ledger") != 2 {
		t.Fatal("second transfer lost")
	}
}

func TestPaymentConnectionValidationAndNoSandboxQuickpay(t *testing.T) {
	s, p, _ := billingFixture(t)
	ctx := context.Background()
	for _, cfg := range []PaymentAccountConfig{{Wallet: "410011234567890", NotificationSecretEnv: "PATH"}, {Wallet: "bad", NotificationSecretEnv: "TELEMT_PAYMENT_YOO_TEST"}, {Wallet: "410011234567890", NotificationSecretEnv: "TELEMT_PAYMENT_YOO_TEST", Merchant: "foreign"}} {
		if _, e := s.CreatePaymentAccount(ctx, "Bad", "yoomoney", false, true, cfg); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	t.Setenv("TELEMT_PAYMENT_YOO_TEST", "")
	a, e := s.CreatePaymentAccount(ctx, "Missing secret", "yoomoney", false, true, PaymentAccountConfig{Wallet: "410011234567890", NotificationSecretEnv: "TELEMT_PAYMENT_YOO_TEST"})
	if e != nil {
		t.Fatal(e)
	}
	i, e := s.CreateInvoice(ctx, "customer-a", InvoiceInput{PlanID: p.ID, AccountID: a.ID, RequestKey: "checkout-key-0001"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.InvoiceCheckout(ctx, "customer-a", i.ID, "AC", "https://example.test/return"); !errors.Is(e, ErrPaymentConfiguration) {
		t.Fatal(e)
	}
	if _, e = s.ReceivePayment(ctx, a.ID, []byte("label=anything")); !errors.Is(e, ErrPaymentConfiguration) {
		t.Fatal(e)
	}
	sandbox, e := s.CreatePaymentAccount(ctx, "Test notifications only", "yoomoney", true, true, a.Config)
	if e != nil {
		t.Fatal(e)
	}
	i, e = s.CreateInvoice(ctx, "customer-a", InvoiceInput{PlanID: p.ID, AccountID: sandbox.ID, RequestKey: "checkout-key-0002"})
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("TELEMT_PAYMENT_YOO_TEST", "configured")
	if _, e = s.InvoiceCheckout(ctx, "customer-a", i.ID, "AC", "https://example.test/return"); !errors.Is(e, ErrInvalid) {
		t.Fatal("live form on sandbox account", e)
	}
}
