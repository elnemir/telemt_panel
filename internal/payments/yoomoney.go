package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var walletPattern = regexp.MustCompile(`^[0-9]{11,20}$`)

type YooMoney struct {
	Wallet             string
	NotificationSecret string `json:"-"`
}

func (y YooMoney) Checkout(reference string, minor int64, method, returnURL string) (Form, error) {
	amount, err := FormatMinor(minor)
	if err != nil || y.NotificationSecret == "" || !walletPattern.MatchString(y.Wallet) || !referencePattern.MatchString(reference) || (method != "PC" && method != "AC") || !safeReturnURL(returnURL) {
		return Form{}, ErrInvalid
	}
	return Form{Action: "https://yoomoney.ru/quickpay/confirm", Method: "POST", Fields: map[string]string{"receiver": y.Wallet, "quickpay-form": "button", "paymentType": method, "sum": amount, "label": reference, "successURL": returnURL}}, nil
}

func yooSignature(values url.Values, secret string) []byte {
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "sign" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var canonical strings.Builder
	for i, key := range keys {
		if i > 0 {
			canonical.WriteByte('&')
		}
		canonical.WriteString(key)
		canonical.WriteByte('=')
		canonical.WriteString(strings.ReplaceAll(url.QueryEscape(values.Get(key)), "+", "%20"))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical.String()))
	return mac.Sum(nil)
}
func (y YooMoney) Verify(body []byte) (Verified, error) {
	v, err := parseFields(body)
	if err != nil {
		return Verified{}, err
	}
	if y.NotificationSecret == "" || !validHexSignature(v.Get("sign"), yooSignature(v, y.NotificationSecret)) {
		return Verified{}, ErrSignature
	}
	if v.Get("notification_type") != "p2p-incoming" && v.Get("notification_type") != "card-incoming" {
		return Verified{}, ErrInvalid
	}
	if v.Get("currency") != "643" || !referencePattern.MatchString(v.Get("label")) || !referencePattern.MatchString(v.Get("operation_id")) {
		return Verified{}, ErrInvalid
	}
	if v.Get("codepro") != "false" || v.Get("unaccepted") != "false" {
		return Verified{}, ErrUnsettled
	}
	// Test notifications must never be confused with a real transfer.
	test := v.Get("test_notification")
	if test != "" && test != "false" && test != "true" {
		return Verified{}, ErrInvalid
	}
	gross, e := ParseMinor(v.Get("withdraw_amount"))
	if e != nil {
		return Verified{}, e
	}
	net, e := ParseMinor(v.Get("amount"))
	if e != nil || net > gross {
		return Verified{}, ErrInvalid
	}
	paidAt, e := time.Parse(time.RFC3339Nano, v.Get("datetime"))
	if e != nil || paidAt.Unix() <= 0 {
		return Verified{}, ErrInvalid
	}
	return Verified{Provider: "yoomoney", InvoiceRef: v.Get("label"), TransactionID: v.Get("operation_id"), Currency: "RUB", GrossMinor: gross, NetMinor: net, PaidAt: paidAt.Unix(), Test: test == "true"}, nil
}
