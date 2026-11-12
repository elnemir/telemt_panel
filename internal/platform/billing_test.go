//go:build !lite

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/payments"
)

func billingFixture(t *testing.T) (*Store, Plan, *int64) {
	t.Helper()
	s, _, _ := testStore(t)
	now := int64(2000000000)
	s.now = func() time.Time { return time.Unix(now, 0) }
	ctx := context.Background()
	c, e := s.CreateCluster(ctx, "Paid cluster")
	if e != nil {
		t.Fatal(e)
	}
	quota, overrun := int64(1073741824), int64(1048576)
	p, e := s.CreatePlan(ctx, c.ID, "Service", true, PlanTerms{Currency: "RUB", CurrencyScale: 2, PriceMinor: 9900, DurationSeconds: 86400, QuotaBytes: &quota, OverrunBytes: &overrun, NodePolicy: Policy{Enabled: true}})
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"customer-a", "customer-b"} {
		if _, e = s.db.ExecContext(ctx, "INSERT INTO customers(id,email,email_verified,created_at,updated_at) VALUES(?,?,1,?,?)", id, id+"@example.test", now, now); e != nil {
			t.Fatal(e)
		}
	}
	for _, a := range []struct {
		id, provider string
		test         bool
	}{{"yoo", "yoomoney", false}, {"robo", "robokassa", false}, {"sandbox", "robokassa", true}} {
		if _, e = s.db.ExecContext(ctx, "INSERT INTO payment_accounts(id,name,provider,is_test,enabled,config,revision,created_at,updated_at) VALUES(?,?,?,?,1,'{}',1,?,?)", a.id, a.id, a.provider, a.test, now, now); e != nil {
			t.Fatal(e)
		}
	}
	return s, p, &now
}
func quote(t *testing.T, s *Store, p Plan, sub, key string) Invoice {
	t.Helper()
	i, e := s.CreateInvoice(context.Background(), "customer-a", InvoiceInput{SubscriptionID: sub, PlanID: p.ID, AccountID: "yoo", RequestKey: key})
	if e != nil {
		t.Fatal(e)
	}
	return i
}
func verified(i Invoice, tx string, at int64) payments.Verified {
	return payments.Verified{Provider: "yoomoney", InvoiceRef: i.ID, TransactionID: tx, Currency: i.Currency, GrossMinor: i.AmountMinor, NetMinor: i.AmountMinor - 100, PaidAt: at, Test: i.Test}
}
func countTable(t *testing.T, s *Store, table string) int {
	t.Helper()
	var count int
	if e := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); e != nil {
		t.Fatal(e)
	}
	return count
}
func TestConcurrentPaymentReplayCreatesOnePeriodAndLedgerEntry(t *testing.T) {
	s, p, now := billingFixture(t)
	ctx := context.Background()
	i := quote(t, s, p, "", "checkout-key-0001")
	payment := verified(i, "provider-transaction", *now)
	var fresh atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			result, e := s.SettlePayment(ctx, "yoo", payment)
			if e != nil {
				t.Error(e)
				return
			}
			if !result.Replay {
				fresh.Add(1)
			}
		})
	}
	wg.Wait()
	if fresh.Load() != 1 || countTable(t, s, "paid_periods") != 1 || countTable(t, s, "billing_ledger") != 1 || countTable(t, s, "payment_events") != 1 {
		t.Fatal("duplicated settlement", fresh.Load())
	}
	current, e := s.CurrentPaidPeriod(ctx, "customer-a", i.SubscriptionID)
	if e != nil || current.StartsAt != *now || current.EndsAt != *now+86400 {
		t.Fatal(current, e)
	}
	if countTable(t, s, "identities") != 0 {
		t.Fatal("payment activated an unbudgeted proxy identity")
	}
	payment.GrossMinor++
	if _, e = s.SettlePayment(ctx, "yoo", payment); !errors.Is(e, ErrConflict) {
		t.Fatal("changed replay accepted", e)
	}
	if _, e = s.CurrentPaidPeriod(ctx, "customer-b", i.SubscriptionID); !errors.Is(e, ErrNotFound) {
		t.Fatal("cross-customer period exposed", e)
	}
}
func TestEarlyRenewalAndTariffVersionApplyOnlyToNextPeriod(t *testing.T) {
	s, p, now := billingFixture(t)
	ctx := context.Background()
	i := quote(t, s, p, "", "checkout-key-0001")
	if _, e := s.SettlePayment(ctx, "yoo", verified(i, "one", *now)); e != nil {
		t.Fatal(e)
	}
	first, e := s.CurrentPaidPeriod(ctx, "customer-a", i.SubscriptionID)
	if e != nil {
		t.Fatal(e)
	}
	oldQuote := quote(t, s, p, i.SubscriptionID, "checkout-key-0002")
	nextTerms := p.Terms
	nextTerms.PriceMinor = 19900
	nextTerms.DurationSeconds = 172800
	q := int64(12345)
	nextTerms.QuotaBytes = &q
	if e = s.UpdatePlan(ctx, p.ID, 1, "Changed service", true, nextTerms); e != nil {
		t.Fatal(e)
	}
	p, e = s.GetPlan(ctx, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	nextQuote := quote(t, s, p, i.SubscriptionID, "checkout-key-0003")
	if oldQuote.AmountMinor != 9900 || nextQuote.AmountMinor != 19900 || oldQuote.PlanVersion != 1 || nextQuote.PlanVersion != 2 {
		t.Fatal(oldQuote, nextQuote)
	}
	*now += 3600
	if _, e = s.SettlePayment(ctx, "yoo", verified(oldQuote, "two", *now)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SettlePayment(ctx, "yoo", verified(nextQuote, "three", *now)); e != nil {
		t.Fatal(e)
	}
	periods, e := s.CustomerPeriods(ctx, "customer-a", i.SubscriptionID, 0)
	if e != nil || len(periods) != 3 {
		t.Fatal(periods, e)
	}
	if periods[1].StartsAt != first.EndsAt || periods[1].PlanVersion != 1 || periods[0].StartsAt != periods[1].EndsAt || periods[0].PlanVersion != 2 || *periods[0].Terms.QuotaBytes != 12345 {
		t.Fatal(periods)
	}
	current, e := s.CurrentPaidPeriod(ctx, "customer-a", i.SubscriptionID)
	if e != nil || current.ID != first.ID || *current.Terms.QuotaBytes != 1073741824 {
		t.Fatal("current period changed", current, e)
	}
	*now = periods[0].StartsAt
	current, e = s.CurrentPaidPeriod(ctx, "customer-a", i.SubscriptionID)
	if e != nil || current.PlanVersion != 2 || *current.Terms.QuotaBytes != 12345 {
		t.Fatal("new quota carried prior remainder", current, e)
	}
	// Expired subscriptions restart at the authenticated provider timestamp.
	*now = periods[0].EndsAt + 3600
	late := quote(t, s, p, i.SubscriptionID, "checkout-key-0004")
	paymentAt := *now
	*now += 60
	if _, e = s.SettlePayment(ctx, "yoo", verified(late, "four", paymentAt)); e != nil {
		t.Fatal(e)
	}
	current, e = s.CurrentPaidPeriod(ctx, "customer-a", i.SubscriptionID)
	if e != nil || current.StartsAt != paymentAt {
		t.Fatal(current, e)
	}
}
func TestDistinctConcurrentPaymentsAppendWithoutOverlap(t *testing.T) {
	s, p, now := billingFixture(t)
	i := quote(t, s, p, "", "checkout-key-0001")
	j := quote(t, s, p, i.SubscriptionID, "checkout-key-0002")
	var wg sync.WaitGroup
	for _, invoice := range []Invoice{i, j} {
		wg.Go(func() {
			_, e := s.SettlePayment(context.Background(), "yoo", verified(invoice, invoice.ID, *now))
			if e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	periods, e := s.CustomerPeriods(context.Background(), "customer-a", i.SubscriptionID, 0)
	if e != nil || len(periods) != 2 || periods[0].StartsAt != periods[1].EndsAt {
		t.Fatal(periods, e)
	}
}
func TestSecondTransferForSameInvoiceIsRecordedWithoutDoubleService(t *testing.T) {
	s, p, now := billingFixture(t)
	i := quote(t, s, p, "", "checkout-key-0001")
	ctx := context.Background()
	if _, e := s.SettlePayment(ctx, "yoo", verified(i, "first-transfer", *now)); e != nil {
		t.Fatal(e)
	}
	second := verified(i, "second-transfer", *now)
	result, e := s.SettlePayment(ctx, "yoo", second)
	if e != nil || !result.Unallocated || result.PeriodID != "" {
		t.Fatal(result, e)
	}
	replay, e := s.SettlePayment(ctx, "yoo", second)
	if e != nil || !replay.Replay || !replay.Unallocated {
		t.Fatal(replay, e)
	}
	records, e := s.ListBillingPayments(ctx, 0)
	if e != nil || len(records) != 2 {
		t.Fatal(records, e)
	}
	unallocated := 0
	for _, record := range records {
		if record.Kind == "unallocated_payment" {
			unallocated++
		}
		if record.InvoiceID != i.ID || record.GrossMinor != 9900 || record.NetMinor == nil || *record.NetMinor != 9800 {
			t.Fatal(record)
		}
	}
	if unallocated != 1 {
		t.Fatal("unallocated money hidden")
	}
	if countTable(t, s, "payment_events") != 2 || countTable(t, s, "billing_ledger") != 2 || countTable(t, s, "paid_periods") != 1 {
		t.Fatal("money lost or service duplicated")
	}
}
func TestSettlementRollbackAndImmutableFinancialRecords(t *testing.T) {
	s, p, now := billingFixture(t)
	ctx := context.Background()
	i := quote(t, s, p, "", "checkout-key-0001")
	if _, e := s.db.ExecContext(ctx, "CREATE TRIGGER reject_ledger BEFORE INSERT ON billing_ledger BEGIN SELECT RAISE(ABORT,'injected ledger failure'); END"); e != nil {
		t.Fatal(e)
	}
	payment := verified(i, "payment", *now)
	if _, e := s.SettlePayment(ctx, "yoo", payment); e == nil {
		t.Fatal("expected rollback")
	}
	for _, table := range []string{"payment_events", "paid_periods", "billing_ledger"} {
		if countTable(t, s, table) != 0 {
			t.Fatal("partial settlement", table)
		}
	}
	got, e := s.CustomerInvoice(ctx, "customer-a", i.ID)
	if e != nil || got.Status != "pending" {
		t.Fatal(got, e)
	}
	if _, e = s.db.ExecContext(ctx, "DROP TRIGGER reject_ledger"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SettlePayment(ctx, "yoo", payment); e != nil {
		t.Fatal(e)
	}
	for _, statement := range []string{"UPDATE plan_versions SET terms='{}'", "DELETE FROM plan_versions", "UPDATE paid_periods SET ends_at=ends_at+1", "DELETE FROM payment_events", "UPDATE billing_ledger SET amount_minor=1", "UPDATE invoices SET amount_minor=1", "UPDATE invoices SET status='pending'", "DELETE FROM invoices"} {
		if _, e = s.db.ExecContext(ctx, statement); e == nil {
			t.Fatal("mutable financial record", statement)
		}
	}
}
func TestInvoiceScopeIdempotencyAndPaymentGates(t *testing.T) {
	s, p, now := billingFixture(t)
	ctx := context.Background()
	i := quote(t, s, p, "", "checkout-key-0001")
	same := quote(t, s, p, "", "checkout-key-0001")
	if same.ID != i.ID || same.SubscriptionID != i.SubscriptionID {
		t.Fatal("invoice replay duplicated")
	}
	if _, e := s.CreateInvoice(ctx, "customer-a", InvoiceInput{PlanID: p.ID, AccountID: "robo", RequestKey: "checkout-key-0001"}); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if _, e := s.CreateInvoice(ctx, "customer-b", InvoiceInput{SubscriptionID: i.SubscriptionID, PlanID: p.ID, AccountID: "yoo", RequestKey: "checkout-key-0002"}); !errors.Is(e, ErrNotFound) {
		t.Fatal("cross-customer renewal", e)
	}
	if _, e := s.CustomerInvoice(ctx, "customer-b", i.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	for _, mutate := range []func(*payments.Verified){func(p *payments.Verified) { p.GrossMinor++ }, func(p *payments.Verified) { p.Currency = "KZT" }, func(p *payments.Verified) { p.Test = true }, func(p *payments.Verified) { p.Provider = "robokassa" }, func(p *payments.Verified) { p.PaidAt = *now + 301 }} {
		invalid := verified(i, "payment", *now)
		mutate(&invalid)
		if _, e := s.SettlePayment(ctx, "yoo", invalid); e == nil {
			t.Fatal("accepted mismatched payment", invalid)
		}
	}
	if countTable(t, s, "payment_events") != 0 {
		t.Fatal("rejected payment persisted as settled")
	}
	// Account disable only stops new checkouts. In-flight verified money survives.
	if _, e := s.db.ExecContext(ctx, "UPDATE payment_accounts SET enabled=0 WHERE id='yoo'"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SettlePayment(ctx, "yoo", verified(i, "payment", *now)); e != nil {
		t.Fatal(e)
	}
}
func TestSandboxDoesNotCreateRealLedgerPeriodOrUser(t *testing.T) {
	s, p, _ := billingFixture(t)
	ctx := context.Background()
	i, e := s.CreateInvoice(ctx, "customer-a", InvoiceInput{PlanID: p.ID, AccountID: "sandbox", RequestKey: "checkout-key-0001"})
	if e != nil {
		t.Fatal(e)
	}
	v := payments.Verified{Provider: "robokassa", InvoiceRef: "1", TransactionID: "1", Currency: "RUB", GrossMinor: i.AmountMinor, Test: true}
	result, e := s.SettlePayment(ctx, "sandbox", v)
	if e != nil || !result.Test || result.PeriodID != "" {
		t.Fatal(result, e)
	}
	for _, table := range []string{"paid_periods", "billing_ledger", "identities"} {
		if countTable(t, s, table) != 0 {
			t.Fatal("test payment activated real state", table)
		}
	}
	saved, e := s.CustomerInvoice(ctx, "customer-a", i.ID)
	if e != nil || saved.Status != "test_paid" {
		t.Fatal(saved, e)
	}
}
func TestTermsRequireExplicitToleranceAndPreserveLargeIntegers(t *testing.T) {
	q := int64(9007199254740993)
	zero := int64(0)
	terms := PlanTerms{Currency: "RUB", CurrencyScale: 2, PriceMinor: q, DurationSeconds: 86400, QuotaBytes: &q, NodePolicy: Policy{Enabled: true}}
	if _, e := cleanTerms(terms, 2000000000); !errors.Is(e, ErrInvalid) {
		t.Fatal("implicit tolerance", e)
	}
	terms.OverrunBytes = &zero
	if _, e := cleanTerms(terms, 2000000000); e != nil {
		t.Fatal(e)
	}
	data, e := json.Marshal(terms)
	if e != nil || !strings.Contains(string(data), `"9007199254740993"`) {
		t.Fatal(string(data), e)
	}
	var decoded PlanTerms
	if e = json.Unmarshal(data, &decoded); e != nil || decoded.PriceMinor != q || *decoded.QuotaBytes != q {
		t.Fatal(decoded, e)
	}
}

func TestTariffJSONRequiresExplicitQuotaChoice(t *testing.T) {
	for _, raw := range []string{
		`{"currency":"RUB","currency_scale":2,"price_minor":"99","duration_seconds":"10","node_policy":{"enabled":true}}`,
		`{"currency":"RUB","currency_scale":null,"price_minor":"99","duration_seconds":"10","quota_bytes":null,"overrun_bytes":null,"node_policy":{"enabled":true}}`,
	} {
		var terms PlanTerms
		if json.Unmarshal([]byte(raw), &terms) == nil {
			t.Fatal("implicit terms", raw)
		}
	}
	s, p, _ := billingFixture(t)
	if e := s.DeleteEmptyCluster(context.Background(), p.ClusterID, 1); !errors.Is(e, ErrConflict) {
		t.Fatal("deleted tariff cluster", e)
	}
}
