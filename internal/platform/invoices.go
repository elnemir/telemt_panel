package platform

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
)

const invoiceColumns = "id,number,customer_id,subscription_id,account_id,plan_id,plan_version,currency,currency_scale,amount_minor,is_test,status,terms,created_at,paid_at"

func scanInvoice(row scanner) (Invoice, error) {
	var i Invoice
	var raw string
	e := row.Scan(&i.ID, &i.Number, &i.CustomerID, &i.SubscriptionID, &i.AccountID, &i.PlanID, &i.PlanVersion, &i.Currency, &i.CurrencyScale, &i.AmountMinor, &i.Test, &i.Status, &raw, &i.CreatedAt, &i.PaidAt)
	if errors.Is(e, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	if e != nil {
		return i, e
	}
	i.Terms, e = decodeTerms(raw)
	return i, e
}

// Customer identity comes from a verified session, never from the request body.
func (s *Store) CustomerInvoice(ctx context.Context, customerID, id string) (Invoice, error) {
	return scanInvoice(s.db.QueryRowContext(ctx, "SELECT "+invoiceColumns+" FROM invoices WHERE id=? AND customer_id=?", id, customerID))
}
func (s *Store) ListCustomerInvoices(ctx context.Context, customerID string, offset int) ([]Invoice, error) {
	if offset < 0 {
		return nil, ErrInvalid
	}
	rows, e := s.db.QueryContext(ctx, "SELECT "+invoiceColumns+" FROM invoices WHERE customer_id=? ORDER BY number DESC LIMIT 100 OFFSET ?", customerID, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Invoice{}
	for rows.Next() {
		i, e := scanInvoice(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

type InvoiceInput struct {
	SubscriptionID string `json:"subscription_id"`
	PlanID         string `json:"plan_id"`
	AccountID      string `json:"account_id"`
	RequestKey     string `json:"request_key"`
}

func (s *Store) CreateInvoice(ctx context.Context, customerID string, in InvoiceInput) (Invoice, error) {
	if customerID == "" || in.PlanID == "" || in.AccountID == "" || !billingRequestKeyPattern.MatchString(in.RequestKey) {
		return Invoice{}, ErrInvalid
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var invoice Invoice
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		var eligible bool
		e := tx.QueryRowContext(ctx, "SELECT enabled=1 AND (email_verified=1 OR telegram_subject IS NOT NULL) FROM customers WHERE id=?", customerID).Scan(&eligible)
		if errors.Is(e, sql.ErrNoRows) || e == nil && !eligible {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		var oldHash, oldID string
		e = tx.QueryRowContext(ctx, "SELECT id,request_hash FROM invoices WHERE customer_id=? AND request_key=?", customerID, in.RequestKey).Scan(&oldID, &oldHash)
		if e == nil {
			if oldHash != hash {
				return ErrConflict
			}
			invoice, e = scanInvoice(tx.QueryRowContext(ctx, "SELECT "+invoiceColumns+" FROM invoices WHERE id=?", oldID))
			return e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		plan, e := scanPlan(tx.QueryRowContext(ctx, "SELECT "+planColumns+planFrom+" WHERE p.id=? AND p.enabled=1", in.PlanID))
		if e != nil {
			return e
		}
		var accountTest, accountEnabled bool
		var provider string
		e = tx.QueryRowContext(ctx, "SELECT provider,is_test,enabled FROM payment_accounts WHERE id=?", in.AccountID).Scan(&provider, &accountTest, &accountEnabled)
		if errors.Is(e, sql.ErrNoRows) || e == nil && !accountEnabled {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		// These adapters implement RUB with two fractional digits. Other tariff
		// currencies remain valid catalog data but cannot use an incompatible rail.
		if (provider != "robokassa" && provider != "yoomoney") || plan.Terms.Currency != "RUB" || plan.Terms.CurrencyScale != 2 {
			return ErrInvalid
		}
		now := s.now().Unix()
		subscriptionID := in.SubscriptionID
		if subscriptionID == "" {
			subscriptionID = newID("sub_")
			if _, e = tx.ExecContext(ctx, "INSERT INTO subscriptions(id,customer_id,cluster_id,created_at,updated_at) VALUES(?,?,?,?,?)", subscriptionID, customerID, plan.ClusterID, now, now); e != nil {
				return e
			}
		} else {
			var cluster string
			var enabled bool
			e = tx.QueryRowContext(ctx, "SELECT cluster_id,enabled FROM subscriptions WHERE id=? AND customer_id=?", subscriptionID, customerID).Scan(&cluster, &enabled)
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			if e != nil {
				return e
			}
			if !enabled || cluster != plan.ClusterID {
				return ErrConflict
			}
		}
		terms, _ := json.Marshal(plan.Terms)
		var number int64
		if e = tx.QueryRowContext(ctx, "UPDATE billing_sequences SET value=value+1 WHERE name='invoice' AND value<? RETURNING value", math.MaxInt64).Scan(&number); e != nil {
			return e
		}
		invoice = Invoice{ID: newID("inv_"), Number: number, CustomerID: customerID, SubscriptionID: subscriptionID, AccountID: in.AccountID, PlanID: plan.ID, PlanVersion: plan.Version, Currency: plan.Terms.Currency, CurrencyScale: plan.Terms.CurrencyScale, AmountMinor: plan.Terms.PriceMinor, Test: accountTest, Status: "pending", Terms: plan.Terms, CreatedAt: now}
		if _, e = tx.ExecContext(ctx, `INSERT INTO invoices(id,number,customer_id,subscription_id,account_id,plan_id,plan_version,currency,currency_scale,amount_minor,is_test,status,terms,request_key,request_hash,created_at)
   VALUES(?,?,?,?,?,?,?,?,?,?,?,'pending',?,?,?,?)`, invoice.ID, number, customerID, subscriptionID, in.AccountID, plan.ID, plan.Version, invoice.Currency, invoice.CurrencyScale, invoice.AmountMinor, accountTest, string(terms), in.RequestKey, hash, now); e != nil {
			return e
		}
		return auditTx(ctx, tx, now, "invoice.create", invoice.ID, plan.Version)
	})
	return invoice, e
}
