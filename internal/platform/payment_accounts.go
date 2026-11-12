package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/amirotin/telemt_panel/internal/payments"
)

var ErrPaymentConfiguration = errors.New("payment credentials are unavailable")
var paymentEnvPattern = regexp.MustCompile(`^TELEMT_PAYMENT_[A-Z0-9_]{1,96}$`)

type PaymentAccountConfig struct {
	Merchant              string          `json:"merchant"`
	Algorithm             string          `json:"algorithm"`
	Password1Env          string          `json:"password1_env"`
	Password2Env          string          `json:"password2_env"`
	Wallet                string          `json:"wallet"`
	NotificationSecretEnv string          `json:"notification_secret_env"`
	Receipt               *ServiceReceipt `json:"receipt"`
}
type ServiceReceipt struct {
	Name          string `json:"name"`
	Tax           string `json:"tax"`
	PaymentMethod string `json:"payment_method"`
	TaxSystem     string `json:"tax_system"`
}

func (r *ServiceReceipt) adapter() *payments.Receipt {
	if r == nil {
		return nil
	}
	return &payments.Receipt{Name: r.Name, Tax: r.Tax, PaymentMethod: r.PaymentMethod, TaxSystem: r.TaxSystem}
}

type PaymentAccount struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Provider  string               `json:"provider"`
	Test      bool                 `json:"test"`
	Enabled   bool                 `json:"enabled"`
	Config    PaymentAccountConfig `json:"config"`
	Revision  int64                `json:"revision"`
	CreatedAt int64                `json:"created_at"`
	UpdatedAt int64                `json:"updated_at"`
}

const accountColumns = "id,name,provider,is_test,enabled,config,revision,created_at,updated_at"

func scanPaymentAccount(row scanner) (PaymentAccount, error) {
	var a PaymentAccount
	var raw string
	e := row.Scan(&a.ID, &a.Name, &a.Provider, &a.Test, &a.Enabled, &raw, &a.Revision, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if e != nil {
		return a, e
	}
	if json.Unmarshal([]byte(raw), &a.Config) != nil {
		return a, ErrSchema
	}
	return a, nil
}
func (s *Store) GetPaymentAccount(ctx context.Context, id string) (PaymentAccount, error) {
	return scanPaymentAccount(s.db.QueryRowContext(ctx, "SELECT "+accountColumns+" FROM payment_accounts WHERE id=?", id))
}
func (s *Store) ListPaymentAccounts(ctx context.Context) ([]PaymentAccount, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT "+accountColumns+" FROM payment_accounts ORDER BY created_at,id LIMIT 64")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []PaymentAccount{}
	for rows.Next() {
		a, e := scanPaymentAccount(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func validatePaymentAccount(a PaymentAccount) error {
	c := a.Config
	switch a.Provider {
	case "robokassa":
		if !paymentEnvPattern.MatchString(c.Password1Env) || !paymentEnvPattern.MatchString(c.Password2Env) || c.Password1Env == c.Password2Env || c.Wallet != "" || c.NotificationSecretEnv != "" {
			return ErrInvalid
		}
		_, e := (payments.Robokassa{Merchant: c.Merchant, Algorithm: c.Algorithm, Password1: "validation", Password2: "validation", Test: a.Test}).Checkout("1", 100, "Validation", c.Receipt.adapter())
		if e != nil {
			return ErrInvalid
		}
	case "yoomoney":
		if !paymentEnvPattern.MatchString(c.NotificationSecretEnv) || c.Merchant != "" || c.Algorithm != "" || c.Password1Env != "" || c.Password2Env != "" || c.Receipt != nil {
			return ErrInvalid
		}
		_, e := (payments.YooMoney{Wallet: c.Wallet, NotificationSecret: "validation"}).Checkout("validation", 100, "AC", "https://example.invalid/return")
		if e != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func (s *Store) CreatePaymentAccount(ctx context.Context, name, provider string, test, enabled bool, config PaymentAccountConfig) (PaymentAccount, error) {
	name, e := cleanName(name)
	if e != nil {
		return PaymentAccount{}, e
	}
	now := s.now().Unix()
	a := PaymentAccount{ID: newID("merchant_"), Name: name, Provider: provider, Test: test, Enabled: enabled, Config: config, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if e = validatePaymentAccount(a); e != nil {
		return PaymentAccount{}, e
	}
	raw, _ := json.Marshal(config)
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM payment_accounts").Scan(&count); e != nil {
			return e
		}
		if count >= 64 {
			return ErrConflict
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO payment_accounts("+accountColumns+") VALUES(?,?,?,?,?,?,1,?,?)", a.ID, a.Name, a.Provider, a.Test, a.Enabled, string(raw), now, now); e != nil {
			return e
		}
		return auditTx(ctx, tx, now, "payment_account.create", a.ID, 1)
	})
	return a, e
}
func (s *Store) UpdatePaymentAccount(ctx context.Context, id string, revision int64, name string, enabled bool) error {
	name, e := cleanName(name)
	if e != nil {
		return e
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		a, e := scanPaymentAccount(tx.QueryRowContext(ctx, "SELECT "+accountColumns+" FROM payment_accounts WHERE id=?", id))
		if e != nil {
			return e
		}
		if revision != a.Revision {
			return ErrConflict
		}
		now := s.now().Unix()
		if _, e = tx.ExecContext(ctx, "UPDATE payment_accounts SET name=?,enabled=?,revision=revision+1,updated_at=? WHERE id=?", name, enabled, now, id); e != nil {
			return e
		}
		return auditTx(ctx, tx, now, "payment_account.update", id, revision+1)
	})
}
func paymentSecret(name string) (string, error) {
	if !paymentEnvPattern.MatchString(name) {
		return "", ErrPaymentConfiguration
	}
	value, ok := os.LookupEnv(name)
	if !ok || value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
		return "", ErrPaymentConfiguration
	}
	return value, nil
}
func (a PaymentAccount) robokassa(callbackOnly bool) (payments.Robokassa, error) {
	r := payments.Robokassa{Merchant: a.Config.Merchant, Algorithm: a.Config.Algorithm, Test: a.Test}
	var e error
	r.Password2, e = paymentSecret(a.Config.Password2Env)
	if e != nil {
		return r, e
	}
	if !callbackOnly {
		r.Password1, e = paymentSecret(a.Config.Password1Env)
	}
	return r, e
}
func (a PaymentAccount) yoomoney() (payments.YooMoney, error) {
	secret, e := paymentSecret(a.Config.NotificationSecretEnv)
	return payments.YooMoney{Wallet: a.Config.Wallet, NotificationSecret: secret}, e
}

// Checkout is scoped to a customer and an immutable invoice. Returning a signed
// form neither sends money nor changes payment/service status.
func (s *Store) InvoiceCheckout(ctx context.Context, customerID, invoiceID, method, returnURL string) (payments.Form, error) {
	i, e := s.CustomerInvoice(ctx, customerID, invoiceID)
	if e != nil {
		return payments.Form{}, e
	}
	if i.Status != "pending" {
		return payments.Form{}, ErrConflict
	}
	a, e := s.GetPaymentAccount(ctx, i.AccountID)
	if e != nil {
		return payments.Form{}, e
	}
	if !a.Enabled || a.Test != i.Test || i.Currency != "RUB" || i.CurrencyScale != 2 {
		return payments.Form{}, ErrConflict
	}
	switch a.Provider {
	case "robokassa":
		r, e := a.robokassa(false)
		if e != nil {
			return payments.Form{}, e
		}
		return r.Checkout(strconv.FormatInt(i.Number, 10), i.AmountMinor, "Telemt invoice "+strconv.FormatInt(i.Number, 10), a.Config.Receipt.adapter())
	case "yoomoney":
		// Quickpay has no sandbox checkout. Its test notifications are a separate flow.
		if a.Test {
			return payments.Form{}, ErrInvalid
		}
		y, e := a.yoomoney()
		if e != nil {
			return payments.Form{}, e
		}
		return y.Checkout(i.ID, i.AmountMinor, method, returnURL)
	}
	return payments.Form{}, ErrSchema
}

// ReceivePayment is the only wire-to-ledger entry point. No unverified form
// fields or browser redirects may call SettlePayment directly.
func (s *Store) ReceivePayment(ctx context.Context, accountID string, body []byte) (string, error) {
	a, e := s.GetPaymentAccount(ctx, accountID)
	if e != nil {
		return "", e
	}
	var verified payments.Verified
	switch a.Provider {
	case "robokassa":
		r, err := a.robokassa(true)
		if err != nil {
			return "", err
		}
		verified, e = r.Verify(body)
	case "yoomoney":
		y, err := a.yoomoney()
		if err != nil {
			return "", err
		}
		verified, e = y.Verify(body)
	default:
		return "", ErrSchema
	}
	if e != nil {
		return "", e
	}
	if _, e = s.SettlePayment(ctx, accountID, verified); e != nil {
		return "", e
	}
	if a.Provider == "robokassa" {
		return "OK" + verified.InvoiceRef, nil
	}
	return "OK", nil
}
