package payments

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestExactAmounts(t *testing.T) {
	for raw, want := range map[string]int64{"1": 100, "1.2": 120, "100.000000": 10000, "0.01": 1, "92233720368547758.07": 9223372036854775807} {
		got, e := ParseMinor(raw)
		if e != nil || got != want {
			t.Fatal(raw, got, e)
		}
	}
	for _, bad := range []string{"0", "-1", "1e2", "1,00", "1.001", "92233720368547758.08", " 1.00", "NaN", "1.0000001"} {
		if _, e := ParseMinor(bad); e == nil {
			t.Fatal(bad)
		}
	}
}
func TestYooMoneyPublishedVectorAndTampering(t *testing.T) {
	// Published provider HMAC-SHA256 vector, including the obsolete field when
	// present. It is signed as data, never used as an authentication fallback.
	raw := "notification_type=p2p-incoming&operation_id=441361714955017004&amount=98.00&withdraw_amount=100.00&currency=643&datetime=2013-12-26T08%3A28%3A34Z&sender=41000000000&codepro=false&label=ML23045&unaccepted=false&sha1_hash=ac13833bd6ba9eff1fa9e4bed76f3d6ebb57f6c0&sign=a452af731650e2c5b39abcdc7c28dd27db7b3b654c2230ad2c386e64afb98605"
	y := YooMoney{Wallet: "41000000000", NotificationSecret: "secret123"}
	got, e := y.Verify([]byte(raw))
	if e != nil || got.GrossMinor != 10000 || got.NetMinor != 9800 || got.InvoiceRef != "ML23045" || got.Test {
		t.Fatal(got, e)
	}
	for _, bad := range []string{strings.Replace(raw, "100.00", "10.00", 1), raw + "&amount=98.00", strings.Split(raw, "&sign=")[0]} {
		if _, e := y.Verify([]byte(bad)); e == nil {
			t.Fatal("accepted altered/duplicate/unsigned payload")
		}
	}
	v, _ := url.ParseQuery(raw)
	v.Set("unaccepted", "true")
	v.Set("sign", hex.EncodeToString(yooSignature(v, y.NotificationSecret)))
	if _, e := y.Verify([]byte(v.Encode())); !errors.Is(e, ErrUnsettled) {
		t.Fatal(e)
	}
	v.Set("unaccepted", "false")
	v.Set("test_notification", "true")
	v.Set("sign", hex.EncodeToString(yooSignature(v, y.NotificationSecret)))
	got, e = y.Verify([]byte(v.Encode()))
	if e != nil || !got.Test {
		t.Fatal(got, e)
	}
	form, e := y.Checkout("invoice_123", 10000, "AC", "https://panel.example/customer/invoices")
	if e != nil || form.Fields["quickpay-form"] != "button" || form.Fields["sum"] != "100.00" {
		t.Fatal(form, e)
	}
	if _, e := y.Checkout("invoice_123", 10000, "AC", "javascript:alert(1)"); e == nil {
		t.Fatal("unsafe return URL")
	}
}
func TestRobokassaVectorsAndReceipt(t *testing.T) {
	r := Robokassa{Merchant: "demo", Password1: "password_1", Password2: "password_2", Algorithm: "SHA256", Test: true}
	f, e := r.Checkout("12", 99000, "Service", nil)
	if e != nil || f.Fields["SignatureValue"] != "4cfecd5dae8217e8cd7a78b23f8dc56e5ec4766c6a548bbed5e1ac0c41c4b325" || f.Fields["IsTest"] != "1" {
		t.Fatal(f, e)
	}
	raw := "OutSum=100.000000&InvId=450009&Shp_oplata=1&Shp_login=Vasya&SignatureValue=025e4f4f5385f685e8b04a9fcf7021ff3084f89d0afbadd977b3609c09d31fc9"
	got, e := r.Verify([]byte(raw))
	if e != nil || got.GrossMinor != 10000 || !got.Test {
		t.Fatal(got, e)
	}
	for _, bad := range []string{raw + "&InvId=450009", strings.Replace(raw, "Vasya", "Other", 1), strings.Replace(raw, "100.000000", "100.000001", 1)} {
		if _, e := r.Verify([]byte(bad)); e == nil {
			t.Fatal("altered callback accepted")
		}
	}
	r.Algorithm = "MD5"
	f, e = r.Checkout("12", 99000, "Service", nil)
	if e != nil || f.Fields["SignatureValue"] != "6c9d6eb96b82e643b343c26a85349a37" {
		t.Fatal(f, e)
	}
	f, e = r.Checkout("12", 12345, "Service", &Receipt{Name: "Subscription", Tax: "none", PaymentMethod: "full_payment"})
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := url.QueryUnescape(f.Fields["Receipt"])
	if e != nil || !json.Valid([]byte(decoded)) || !strings.Contains(decoded, `"sum":123.45`) {
		t.Fatal(decoded, e)
	}
	if strings.Contains(decoded, "password") {
		t.Fatal("secret in receipt")
	}
	if _, e := r.Checkout("12", 12345, "Service", &Receipt{Name: "Subscription"}); e == nil {
		t.Fatal("invented missing fiscal settings")
	}
}
