// Package payments implements provider wire contracts. It neither marks invoices
// paid nor provisions services: callers must durably deduplicate verified events
// and compare invoice merchant, currency and gross amount in one transaction.
package payments

import (
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var ErrInvalid = errors.New("invalid payment data")
var ErrSignature = errors.New("invalid payment signature")
var ErrUnsettled = errors.New("payment is not settled")

// Form contains only public checkout fields; secrets must never appear here.
type Form struct {
	Action string            `json:"action"`
	Method string            `json:"method"`
	Fields map[string]string `json:"fields"`
}
type Verified struct {
	PaidAt        int64 // Signed provider timestamp; zero means unavailable.
	Provider      string
	InvoiceRef    string
	TransactionID string
	Currency      string
	GrossMinor    int64
	NetMinor      int64
	Test          bool
}

var amountPattern = regexp.MustCompile(`^[0-9]{1,17}(\.[0-9]{1,6})?$`)
var referencePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ParseMinor accepts provider decimal amounts without floating point rounding.
// Robokassa uses six decimal places in callbacks, but fractional kopeks are not
// representable by a RUB invoice and must never be rounded into a match.
func ParseMinor(raw string) (int64, error) {
	if !amountPattern.MatchString(raw) {
		return 0, ErrInvalid
	}
	parts := strings.SplitN(raw, ".", 2)
	whole, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || whole > math.MaxInt64/100 {
		return 0, ErrInvalid
	}
	fraction := "00"
	if len(parts) == 2 {
		fraction = parts[1]
		if len(fraction) > 2 {
			if strings.Trim(fraction[2:], "0") != "" {
				return 0, ErrInvalid
			}
			fraction = fraction[:2]
		}
		if len(fraction) == 1 {
			fraction += "0"
		}
	}
	cents, e := strconv.ParseInt(fraction, 10, 64)
	if e != nil || whole*100 > math.MaxInt64-cents {
		return 0, ErrInvalid
	}
	value := whole*100 + cents
	if value <= 0 {
		return 0, ErrInvalid
	}
	return value, nil
}
func FormatMinor(value int64) (string, error) {
	if value <= 0 {
		return "", ErrInvalid
	}
	return fmt.Sprintf("%d.%02d", value/100, value%100), nil
}

func parseFields(body []byte) (url.Values, error) {
	if len(body) == 0 || len(body) > 64*1024 {
		return nil, ErrInvalid
	}
	values, err := url.ParseQuery(string(body))
	if err != nil || len(values) > 64 {
		return nil, ErrInvalid
	}
	for key, items := range values {
		if key == "" || len(key) > 128 || len(items) != 1 || len(items[0]) > 16*1024 {
			return nil, ErrInvalid
		}
	}
	return values, nil
}
func validHexSignature(raw string, want []byte) bool {
	got, err := hex.DecodeString(raw)
	return err == nil && hmac.Equal(got, want)
}
func safeReturnURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
