package payments

import (
	"crypto/md5" // Provider legacy wire algorithm, never used for local credentials.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

var merchantPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type Robokassa struct {
	Merchant  string
	Password1 string `json:"-"`
	Password2 string `json:"-"`
	Algorithm string // Explicit MD5 or SHA256, matching merchant settings.
	Test      bool   // Select separate test passwords at configuration loading time.
}

func (r Robokassa) digest(value string) ([]byte, error) {
	var h hash.Hash
	switch r.Algorithm {
	case "MD5":
		h = md5.New()
	case "SHA256":
		h = sha256.New()
	default:
		return nil, ErrInvalid
	}
	_, _ = h.Write([]byte(value))
	return h.Sum(nil), nil
}
func invoiceID(id string) bool {
	n, e := strconv.ParseUint(id, 10, 63)
	return e == nil && n > 0 && strconv.FormatUint(n, 10) == id
}

type Receipt struct {
	Name          string
	Tax           string
	PaymentMethod string
	TaxSystem     string
}

func allowed(value string, choices ...string) bool {
	for _, c := range choices {
		if value == c {
			return true
		}
	}
	return false
}
func (r Receipt) encode(minor int64) (string, error) {
	if utf8.RuneCountInString(r.Name) < 1 || utf8.RuneCountInString(r.Name) > 128 ||
		!allowed(r.Tax, "none", "vat0", "vat5", "vat7", "vat10", "vat20", "vat22", "vat105", "vat107", "vat110", "vat120", "vat122") ||
		!allowed(r.PaymentMethod, "full_prepayment", "full_payment", "prepayment", "advance", "partial_payment", "credit", "credit_payment") ||
		!allowed(r.TaxSystem, "", "osn", "usn_income", "usn_income_outcome", "esn", "patent") {
		return "", ErrInvalid
	}
	amount, err := FormatMinor(minor)
	if err != nil {
		return "", err
	}
	body := map[string]any{"items": []any{map[string]any{"name": r.Name, "quantity": 1, "sum": json.Number(amount), "tax": r.Tax, "payment_method": r.PaymentMethod, "payment_object": "service"}}}
	if r.TaxSystem != "" {
		body["sno"] = r.TaxSystem
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", ErrInvalid
	}
	return url.QueryEscape(string(raw)), nil
}
func (r Robokassa) Checkout(id string, minor int64, description string, receipt *Receipt) (Form, error) {
	amount, err := FormatMinor(minor)
	if err != nil || !invoiceID(id) || !merchantPattern.MatchString(r.Merchant) || r.Password1 == "" || r.Password2 == "" || utf8.RuneCountInString(description) > 100 {
		return Form{}, ErrInvalid
	}
	fields := map[string]string{"MerchantLogin": r.Merchant, "InvId": id, "OutSum": amount, "Description": description, "Culture": "ru", "Encoding": "utf-8"}
	base := r.Merchant + ":" + amount + ":" + id
	if receipt != nil {
		encoded, e := receipt.encode(minor)
		if e != nil {
			return Form{}, e
		}
		fields["Receipt"] = encoded
		base += ":" + encoded
	}
	base += ":" + r.Password1
	sig, err := r.digest(base)
	if err != nil {
		return Form{}, err
	}
	fields["SignatureValue"] = hex.EncodeToString(sig)
	if r.Test {
		fields["IsTest"] = "1"
	} else {
		fields["IsTest"] = "0"
	}
	return Form{Action: "https://auth.robokassa.ru/Merchant/Index.aspx", Method: "POST", Fields: fields}, nil
}
func (r Robokassa) Verify(body []byte) (Verified, error) {
	v, err := parseFields(body)
	if err != nil {
		return Verified{}, err
	}
	if r.Password2 == "" || !invoiceID(v.Get("InvId")) {
		return Verified{}, ErrInvalid
	}
	amount, err := ParseMinor(v.Get("OutSum"))
	if err != nil {
		return Verified{}, err
	}
	base := v.Get("OutSum") + ":" + v.Get("InvId") + ":" + r.Password2
	keys := []string{}
	for key := range v {
		if strings.HasPrefix(key, "Shp_") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		base += ":" + key + "=" + v.Get(key)
	}
	sig, err := r.digest(base)
	if err != nil {
		return Verified{}, err
	}
	if !validHexSignature(v.Get("SignatureValue"), sig) {
		return Verified{}, ErrSignature
	}
	return Verified{Provider: "robokassa", InvoiceRef: v.Get("InvId"), TransactionID: v.Get("InvId"), Currency: "RUB", GrossMinor: amount, Test: r.Test}, nil
}
