//go:build !lite

package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

func TestBillingMigrationPreservesD1AndRejectsWrongKeyBeforeDDL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "platform.sqlite")
	key := bytes.Repeat([]byte{9}, 32)
	u := url.URL{Scheme: "file", Path: path}
	db, e := sqlitedriver.Open(u.String())
	if e != nil {
		t.Fatal(e)
	}
	old, e := newStore(db, key)
	if e != nil {
		t.Fatal(e)
	}
	e = old.transaction(ctx, func(tx *sql.Tx) error {
		for _, statement := range strings.Split(schema, "-- statement") {
			if strings.TrimSpace(statement) != "" {
				if _, e := tx.ExecContext(ctx, statement); e != nil {
					return e
				}
			}
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO platform_meta(key,value) VALUES('encryption_key_id',?)", old.keyID); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "PRAGMA user_version=1")
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	c, e := old.CreateCluster(ctx, "Existing cluster")
	if e != nil {
		t.Fatal(e)
	}
	i, e := old.CreateIdentity(ctx, c.ID, c.Revision, IdentityInput{Username: "existing", Policy: Policy{Enabled: true}})
	if e != nil {
		t.Fatal(e)
	}
	if e = old.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = Open(path, bytes.Repeat([]byte{8}, 32)); !errors.Is(e, ErrKey) {
		t.Fatal(e)
	}
	db, e = sqlitedriver.Open(u.String())
	if e != nil {
		t.Fatal(e)
	}
	var version, tables int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 1 {
		t.Fatal(version, e)
	}
	if e = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='invoices'").Scan(&tables); e != nil || tables != 0 {
		t.Fatal(tables, e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	current, e := Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	defer current.Close()
	if e = current.db.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 6 {
		t.Fatal(version, e)
	}
	got, e := current.GetIdentity(ctx, i.ID)
	if e != nil || got.Username != "existing" || got.ClusterID != c.ID {
		t.Fatal(got, e)
	}
	if countTable(t, current, "invoices") != 0 {
		t.Fatal("migration created financial data")
	}
}

func TestBillingSettlementSurvivesRestartAndReplay(t *testing.T) {
	s, p, now := billingFixture(t)
	var path string
	var seq int
	var name string
	if e := s.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); e != nil {
		t.Fatal(e)
	}
	i := quote(t, s, p, "", "checkout-key-0001")
	payment := verified(i, "persisted-payment", *now)
	first, e := s.SettlePayment(context.Background(), "yoo", payment)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(path, bytes.Repeat([]byte{7}, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return time.Unix(*now+60, 0) }
	again, e := reopened.SettlePayment(context.Background(), "yoo", payment)
	if e != nil || !again.Replay || again.PeriodID != first.PeriodID || again.EventID != first.EventID {
		t.Fatal(again, e)
	}
	if countTable(t, reopened, "paid_periods") != 1 || countTable(t, reopened, "billing_ledger") != 1 {
		t.Fatal("restart duplicated payment")
	}
}

func TestCustomerMigrationPreservesVersionTwoFinancialHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v2.sqlite")
	key := bytes.Repeat([]byte{6}, 32)
	u := url.URL{Scheme: "file", Path: path}
	db, e := sqlitedriver.Open(u.String())
	if e != nil {
		t.Fatal(e)
	}
	old, e := newStore(db, key)
	if e != nil {
		t.Fatal(e)
	}
	e = old.transaction(ctx, func(tx *sql.Tx) error {
		for _, definition := range []string{schema, billingSchema} {
			for _, statement := range strings.Split(definition, "-- statement") {
				if strings.TrimSpace(statement) != "" {
					if _, e := tx.ExecContext(ctx, statement); e != nil {
						return e
					}
				}
			}
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO platform_meta(key,value) VALUES('encryption_key_id',?)", old.keyID); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "PRAGMA user_version=2")
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	c, e := old.CreateCluster(ctx, "Existing paid cluster")
	if e != nil {
		t.Fatal(e)
	}
	p, e := old.CreatePlan(ctx, c.ID, "Existing tariff", true, PlanTerms{Currency: "RUB", CurrencyScale: 2, PriceMinor: 9900, DurationSeconds: 86400, NodePolicy: Policy{Enabled: true}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = old.db.Exec("INSERT INTO customers(id,email,email_verified,created_at,updated_at) VALUES('customer-a','customer@example.test',1,1,1)"); e != nil {
		t.Fatal(e)
	}
	a, e := old.CreatePaymentAccount(ctx, "Existing account", "yoomoney", false, true, PaymentAccountConfig{Wallet: "410011234567890", NotificationSecretEnv: "TELEMT_PAYMENT_EXISTING"})
	if e != nil {
		t.Fatal(e)
	}
	invoice, e := old.CreateInvoice(ctx, "customer-a", InvoiceInput{PlanID: p.ID, AccountID: a.ID, RequestKey: "migration-invoice-key-0001"})
	if e != nil {
		t.Fatal(e)
	}
	payment := verified(invoice, "existing-transfer", time.Now().Unix())
	// Populate the historical v2 record directly: the current settlement
	// function now also wakes v4 entitlement jobs and must not run on v2.
	raw, _ := json.Marshal(payment)
	fingerprint := sha256.Sum256(raw)
	terms, _ := json.Marshal(invoice.Terms)
	const historicalPeriod = "period_before_migration"
	e = old.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := tx.Exec(`INSERT INTO payment_events(id,account_id,external_id,invoice_id,fingerprint,gross_minor,net_minor,currency,is_test,payment_at,time_source,received_at) VALUES('old-event',?,?,?,?,?,?,?,0,?,'provider',?)`, a.ID, payment.TransactionID, invoice.ID, hex.EncodeToString(fingerprint[:]), payment.GrossMinor, payment.NetMinor, payment.Currency, payment.PaidAt, payment.PaidAt); e != nil {
			return e
		}
		if _, e := tx.Exec(`INSERT INTO paid_periods(id,subscription_id,invoice_id,plan_id,plan_version,starts_at,ends_at,terms,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, historicalPeriod, invoice.SubscriptionID, invoice.ID, p.ID, p.Version, payment.PaidAt, payment.PaidAt+86400, string(terms), payment.PaidAt); e != nil {
			return e
		}
		if _, e := tx.Exec(`UPDATE invoices SET status='paid',paid_at=? WHERE id=?`, payment.PaidAt, invoice.ID); e != nil {
			return e
		}
		_, e := tx.Exec(`INSERT INTO billing_ledger(id,customer_id,subscription_id,invoice_id,kind,event_id,period_id,amount_minor,currency,created_at) VALUES('old-ledger','customer-a',?,?,'service_payment','old-event',?,9900,'RUB',?)`, invoice.SubscriptionID, invoice.ID, historicalPeriod, payment.PaidAt)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = old.Close(); e != nil {
		t.Fatal(e)
	}
	current, e := Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	defer current.Close()
	got, e := current.CustomerInvoice(ctx, "customer-a", invoice.ID)
	if e != nil || got.Status != "paid" || got.AmountMinor != 9900 {
		t.Fatal(got, e)
	}
	replay, e := current.SettlePayment(ctx, a.ID, payment)
	if e != nil || !replay.Replay || replay.PeriodID != historicalPeriod {
		t.Fatal(replay, e)
	}
	customer, e := current.CustomerCredentials(ctx, "customer@example.test")
	if e != nil || customer.AuthRevision != 1 || !customer.Customer.EmailVerified {
		t.Fatal(customer, e)
	}
	if countTable(t, current, "billing_ledger") != 1 || countTable(t, current, "customer_sessions") != 0 {
		t.Fatal("migration rewrote history")
	}
}
