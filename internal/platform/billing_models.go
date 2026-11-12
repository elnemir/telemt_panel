package platform

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// JSON decimal strings preserve integer precision in browsers and providers.
// Internal arithmetic and SQLite storage never use floating point.
type PlanTerms struct {
	Currency        string `json:"currency"`
	CurrencyScale   int    `json:"currency_scale"`
	PriceMinor      int64  `json:"price_minor,string"`
	DurationSeconds int64  `json:"duration_seconds,string"`
	QuotaBytes      *int64 `json:"quota_bytes,string"`
	OverrunBytes    *int64 `json:"overrun_bytes,string"`
	NodePolicy      Policy `json:"node_policy"`
}
type Plan struct {
	ID        string    `json:"id"`
	ClusterID string    `json:"cluster_id"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	Revision  int64     `json:"revision"`
	Version   int64     `json:"version"`
	Terms     PlanTerms `json:"terms"`
	CreatedAt int64     `json:"created_at"`
	UpdatedAt int64     `json:"updated_at"`
}
type Customer struct {
	ID             string `json:"id"`
	Email          string `json:"email"`
	EmailVerified  bool   `json:"email_verified"`
	TelegramLinked bool   `json:"telegram_linked"`
	Enabled        bool   `json:"enabled"`
	CreatedAt      int64  `json:"created_at"`
}
type Subscription struct {
	ID         string `json:"id"`
	CustomerID string `json:"customer_id"`
	ClusterID  string `json:"cluster_id"`
	IdentityID string `json:"identity_id"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  int64  `json:"created_at"`
}
type Invoice struct {
	ID             string    `json:"id"`
	Number         int64     `json:"number,string"`
	CustomerID     string    `json:"customer_id"`
	SubscriptionID string    `json:"subscription_id"`
	AccountID      string    `json:"account_id"`
	PlanID         string    `json:"plan_id"`
	PlanVersion    int64     `json:"plan_version"`
	Currency       string    `json:"currency"`
	CurrencyScale  int       `json:"currency_scale"`
	AmountMinor    int64     `json:"amount_minor,string"`
	Test           bool      `json:"test"`
	Status         string    `json:"status"`
	Terms          PlanTerms `json:"terms"`
	CreatedAt      int64     `json:"created_at"`
	PaidAt         *int64    `json:"paid_at"`
}
type PaidPeriod struct {
	ID             string    `json:"id"`
	SubscriptionID string    `json:"subscription_id"`
	InvoiceID      string    `json:"invoice_id"`
	PlanID         string    `json:"plan_id"`
	PlanVersion    int64     `json:"plan_version"`
	StartsAt       int64     `json:"starts_at"`
	EndsAt         int64     `json:"ends_at"`
	Terms          PlanTerms `json:"terms"`
}

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
var billingRequestKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// Bound timestamps to RFC3339's four-digit year range, not a hidden tariff
// duration. The seller explicitly chooses the duration and quota tolerance.
const maxBillingEpoch int64 = 253402300799

func cleanTerms(t PlanTerms, now int64) (PlanTerms, error) {
	t.Currency = strings.ToUpper(strings.TrimSpace(t.Currency))
	if !currencyPattern.MatchString(t.Currency) || t.CurrencyScale < 0 || t.CurrencyScale > 6 || t.PriceMinor <= 0 || t.DurationSeconds <= 0 || now < 0 || t.DurationSeconds > maxBillingEpoch-now {
		return PlanTerms{}, ErrInvalid
	}
	if t.QuotaBytes != nil {
		if *t.QuotaBytes <= 0 || t.OverrunBytes == nil || *t.OverrunBytes < 0 || *t.OverrunBytes > int64(^uint64(0)>>1)-*t.QuotaBytes {
			return PlanTerms{}, ErrInvalid
		}
	} else if t.OverrunBytes != nil {
		return PlanTerms{}, ErrInvalid
	}
	// Billing owns enabled/expiry; operators configure per-node limits here.
	if !t.NodePolicy.Enabled || t.NodePolicy.ExpirationRFC3339 != "" {
		return PlanTerms{}, ErrInvalid
	}
	p, e := cleanPolicy(t.NodePolicy)
	if e != nil {
		return PlanTerms{}, e
	}
	t.NodePolicy = p
	return t, nil
}
func decodeTerms(raw string) (PlanTerms, error) {
	var t PlanTerms
	if json.Unmarshal([]byte(raw), &t) != nil {
		return t, ErrSchema
	}
	return t, nil
}

// Nullable quota fields still must be explicit: omission is not permission for
// unlimited service. Keep the same contract in HTTP and persisted snapshots.
func (t *PlanTerms) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ErrInvalid
	}
	for _, name := range []string{"currency", "currency_scale", "price_minor", "duration_seconds", "quota_bytes", "overrun_bytes", "node_policy"} {
		value, ok := fields[name]
		if !ok {
			return ErrInvalid
		}
		if name != "quota_bytes" && name != "overrun_bytes" && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrInvalid
		}
	}
	type plain PlanTerms
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil {
		return ErrInvalid
	}
	*t = PlanTerms(value)
	return nil
}
