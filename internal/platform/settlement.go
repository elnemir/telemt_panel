package platform

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/amirotin/telemt_panel/internal/payments"
)

// Settlement is reached only after the provider adapter has verified the wire
// signature. It does not provision a user or alter an active quota generation.
type Settlement struct {
	InvoiceID   string `json:"invoice_id"`
	EventID     string `json:"event_id"`
	PeriodID    string `json:"period_id"`
	Replay      bool   `json:"replay"`
	Unallocated bool   `json:"unallocated"`
	Test        bool   `json:"test"`
}

func (s *Store) SettlePayment(ctx context.Context, accountID string, p payments.Verified) (Settlement, error) {
	if accountID == "" || p.TransactionID == "" || len(p.TransactionID) > 128 || len(p.InvoiceRef) > 128 || p.InvoiceRef == "" || p.GrossMinor <= 0 || p.NetMinor < 0 || p.NetMinor > p.GrossMinor || p.PaidAt < 0 {
		return Settlement{}, ErrInvalid
	}
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	var result Settlement
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		var provider string
		var test bool
		e := tx.QueryRowContext(ctx, "SELECT provider,is_test FROM payment_accounts WHERE id=?", accountID).Scan(&provider, &test)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if provider != p.Provider || test != p.Test {
			return ErrConflict
		}
		// Disabling a checkout account must not discard an already accepted payment.
		var existingHash string
		e = tx.QueryRowContext(ctx, "SELECT id,invoice_id,fingerprint,is_test FROM payment_events WHERE account_id=? AND external_id=?", accountID, p.TransactionID).Scan(&result.EventID, &result.InvoiceID, &existingHash, &result.Test)
		if e == nil {
			if existingHash != fingerprint {
				return ErrConflict
			}
			result.Replay = true
			if !result.Test {
				var kind string
				var period sql.NullString
				if e = tx.QueryRowContext(ctx, "SELECT kind,period_id FROM billing_ledger WHERE event_id=?", result.EventID).Scan(&kind, &period); e != nil {
					return ErrSchema
				}
				result.Unallocated = kind == "unallocated_payment"
				result.PeriodID = period.String
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		query := "SELECT " + invoiceColumns + " FROM invoices WHERE account_id=? AND id=?"
		var ref any = p.InvoiceRef
		if provider == "robokassa" {
			n, err := strconv.ParseInt(p.InvoiceRef, 10, 64)
			if err != nil || n <= 0 || strconv.FormatInt(n, 10) != p.InvoiceRef {
				return ErrInvalid
			}
			query = "SELECT " + invoiceColumns + " FROM invoices WHERE account_id=? AND number=?"
			ref = n
		}
		invoice, e := scanInvoice(tx.QueryRowContext(ctx, query, accountID, ref))
		if e != nil {
			return e
		}
		if invoice.Test != p.Test || invoice.AmountMinor != p.GrossMinor || invoice.Currency != p.Currency || invoice.CurrencyScale != 2 {
			return ErrConflict
		}
		now := s.now().Unix()
		paymentAt := p.PaidAt
		source := "provider"
		if paymentAt == 0 {
			paymentAt = now
			source = "verified_notification"
		}
		if paymentAt < invoice.CreatedAt-300 || paymentAt > now+300 || paymentAt < 0 || paymentAt >= maxBillingEpoch {
			return ErrInvalid
		}
		eventID := newID("pay_")
		var net any
		if p.NetMinor > 0 {
			net = p.NetMinor
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO payment_events(id,account_id,external_id,invoice_id,fingerprint,gross_minor,net_minor,currency,is_test,payment_at,time_source,received_at)
   VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, eventID, accountID, p.TransactionID, invoice.ID, fingerprint, p.GrossMinor, net, p.Currency, p.Test, paymentAt, source, now); e != nil {
			return e
		}
		result = Settlement{InvoiceID: invoice.ID, EventID: eventID, Test: p.Test}
		if p.Test {
			if invoice.Status == "pending" {
				_, e = tx.ExecContext(ctx, "UPDATE invoices SET status='test_paid',paid_at=? WHERE id=?", paymentAt, invoice.ID)
			}
			return e
		}
		kind := "service_payment"
		var periodID any
		if invoice.Status == "paid" {
			// A distinct real transfer for the same invoice is money received, not a
			// retry. Record it visibly without silently granting another service period
			// or inventing a refund/credit-spending policy.
			kind = "unallocated_payment"
			result.Unallocated = true
		} else if invoice.Status != "pending" {
			return ErrConflict
		} else {
			var lastEnd int64
			if e = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(ends_at),0) FROM paid_periods WHERE subscription_id=?", invoice.SubscriptionID).Scan(&lastEnd); e != nil {
				return e
			}
			starts := max(paymentAt, lastEnd)
			if invoice.Terms.DurationSeconds <= 0 || starts > maxBillingEpoch-invoice.Terms.DurationSeconds {
				return ErrInvalid
			}
			ends := starts + invoice.Terms.DurationSeconds
			id := newID("period_")
			terms, _ := json.Marshal(invoice.Terms)
			if _, e = tx.ExecContext(ctx, "INSERT INTO paid_periods(id,subscription_id,invoice_id,plan_id,plan_version,starts_at,ends_at,terms,created_at) VALUES(?,?,?,?,?,?,?,?,?)", id, invoice.SubscriptionID, invoice.ID, invoice.PlanID, invoice.PlanVersion, starts, ends, string(terms), now); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "UPDATE invoices SET status='paid',paid_at=? WHERE id=? AND status='pending'", paymentAt, invoice.ID); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "UPDATE subscriptions SET entitlement_next_attempt=? WHERE id=?", now, invoice.SubscriptionID); e != nil {
				return e
			}
			periodID = id
			result.PeriodID = id
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO billing_ledger(id,customer_id,subscription_id,invoice_id,kind,event_id,period_id,amount_minor,currency,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)", newID("entry_"), invoice.CustomerID, invoice.SubscriptionID, invoice.ID, kind, eventID, periodID, p.GrossMinor, p.Currency, now); e != nil {
			return e
		}
		return auditTx(ctx, tx, now, "payment."+strings.TrimSuffix(kind, "_payment"), invoice.ID, invoice.PlanVersion)
	})
	return result, e
}

const periodColumns = "id,subscription_id,invoice_id,plan_id,plan_version,starts_at,ends_at,terms"

func scanPeriod(row scanner) (PaidPeriod, error) {
	var p PaidPeriod
	var raw string
	e := row.Scan(&p.ID, &p.SubscriptionID, &p.InvoiceID, &p.PlanID, &p.PlanVersion, &p.StartsAt, &p.EndsAt, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if e != nil {
		return p, e
	}
	p.Terms, e = decodeTerms(raw)
	return p, e
}
func (s *Store) CurrentPaidPeriod(ctx context.Context, customerID, subscriptionID string) (PaidPeriod, error) {
	now := s.now().Unix()
	return scanPeriod(s.db.QueryRowContext(ctx, "SELECT "+periodColumns+" FROM paid_periods WHERE subscription_id IN (SELECT id FROM subscriptions WHERE id=? AND customer_id=?) AND starts_at<=? AND ends_at>? ORDER BY starts_at DESC LIMIT 1", subscriptionID, customerID, now, now))
}
func (s *Store) CustomerPeriods(ctx context.Context, customerID, subscriptionID string, offset int) ([]PaidPeriod, error) {
	if offset < 0 {
		return nil, ErrInvalid
	}
	rows, e := s.db.QueryContext(ctx, "SELECT "+periodColumns+" FROM paid_periods WHERE subscription_id IN (SELECT id FROM subscriptions WHERE id=? AND customer_id=?) ORDER BY starts_at DESC LIMIT 100 OFFSET ?", subscriptionID, customerID, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []PaidPeriod{}
	for rows.Next() {
		p, e := scanPeriod(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
