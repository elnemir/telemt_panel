package platform

import "context"

// PaymentRecord is an administrator read model; signatures, fingerprints and
// credential values never enter this response. An unallocated transfer remains
// visible for reconciliation without silently assigning another service period.
type PaymentRecord struct {
	ID         string `json:"id"`
	AccountID  string `json:"account_id"`
	Provider   string `json:"provider"`
	ExternalID string `json:"external_id"`
	CustomerID string `json:"customer_id"`
	InvoiceID  string `json:"invoice_id"`
	GrossMinor int64  `json:"gross_minor,string"`
	NetMinor   *int64 `json:"net_minor,string"`
	Currency   string `json:"currency"`
	Test       bool   `json:"test"`
	Kind       string `json:"kind"`
	PaymentAt  int64  `json:"payment_at"`
	TimeSource string `json:"time_source"`
	ReceivedAt int64  `json:"received_at"`
}

func (s *Store) ListBillingInvoices(ctx context.Context, offset int) ([]Invoice, error) {
	if offset < 0 {
		return nil, ErrInvalid
	}
	rows, e := s.db.QueryContext(ctx, "SELECT "+invoiceColumns+" FROM invoices ORDER BY number DESC LIMIT 100 OFFSET ?", offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Invoice{}
	for rows.Next() {
		v, e := scanInvoice(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) ListBillingPayments(ctx context.Context, offset int) ([]PaymentRecord, error) {
	if offset < 0 {
		return nil, ErrInvalid
	}
	rows, e := s.db.QueryContext(ctx, `SELECT e.id,e.account_id,a.provider,e.external_id,i.customer_id,e.invoice_id,e.gross_minor,e.net_minor,e.currency,e.is_test,COALESCE(l.kind,'test_payment'),e.payment_at,e.time_source,e.received_at
 FROM payment_events e JOIN payment_accounts a ON a.id=e.account_id JOIN invoices i ON i.id=e.invoice_id LEFT JOIN billing_ledger l ON l.event_id=e.id
 ORDER BY e.received_at DESC,e.id DESC LIMIT 100 OFFSET ?`, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []PaymentRecord{}
	for rows.Next() {
		var p PaymentRecord
		if e = rows.Scan(&p.ID, &p.AccountID, &p.Provider, &p.ExternalID, &p.CustomerID, &p.InvoiceID, &p.GrossMinor, &p.NetMinor, &p.Currency, &p.Test, &p.Kind, &p.PaymentAt, &p.TimeSource, &p.ReceivedAt); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
