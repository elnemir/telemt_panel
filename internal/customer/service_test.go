//go:build !lite

package customer

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/config"
	"github.com/amirotin/telemt_panel/internal/platform"
)

type capturedMail struct {
	to, link string
	fail     bool
}

func (m *capturedMail) Send(_ context.Context, to, link string) error {
	m.to, m.link = to, link
	if m.fail {
		return ErrDelivery
	}
	return nil
}
func serviceFixture(t *testing.T) (*Service, *capturedMail) {
	t.Helper()
	st, e := platform.Open(filepath.Join(t.TempDir(), "platform.sqlite"), bytes.Repeat([]byte{5}, 32))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := config.CustomerPortalConfig{Enabled: true, PublicURL: "https://panel.example.test/base", Email: config.CustomerEmailConfig{Enabled: true, Host: "smtp.example.test", Port: 465, Mode: "tls", From: "noreply@example.test", Username: "sender", PasswordEnv: "TELEMT_CUSTOMER_SMTP_TEST"}, Telegram: config.CustomerTelegramConfig{Enabled: true, ClientID: "12345", SecretEnv: "TELEMT_CUSTOMER_OIDC_TEST"}}
	t.Setenv(cfg.Email.PasswordEnv, "smtp-fixture-secret")
	t.Setenv(cfg.Telegram.SecretEnv, "oidc-fixture-secret")
	s, e := New(st, cfg, "/base")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	mail := &capturedMail{}
	s.mail = mail
	return s, mail
}
func mailToken(t *testing.T, m *capturedMail) string {
	t.Helper()
	u, e := url.Parse(m.link)
	if e != nil {
		t.Fatal(e)
	}
	q, e := url.ParseQuery(u.Fragment)
	if e != nil {
		t.Fatal(e)
	}
	token := q.Get("email_token")
	if len(token) != 43 || u.Path != "/base/portal" {
		t.Fatal("invalid email link")
	}
	return token
}

const customerPassword = "Correct-password-123!"

func registerCustomer(t *testing.T, s *Service, m *capturedMail, email string) (string, platform.CustomerSession) {
	t.Helper()
	if e := s.SendEmailChallenge(context.Background(), "192.0.2.1", "register", email, customerPassword, ""); e != nil {
		t.Fatal(e)
	}
	token, session, e := s.CompleteEmail(context.Background(), "192.0.2.1", mailToken(t, m), "")
	if e != nil {
		t.Fatal(e)
	}
	return token, session
}
func TestEmailServiceProofLoginRecoveryAndNoAdminSession(t *testing.T) {
	s, m := serviceFixture(t)
	ctx := context.Background()
	raw, session := registerCustomer(t, s, m, "Client@example.test")
	if session.Customer.Email != "client@example.test" || !session.Customer.EmailVerified || len(raw) != 43 {
		t.Fatal(session)
	}
	c, e := s.Store.CustomerCredentials(ctx, "client@example.test")
	if e != nil || strings.Contains(c.PasswordHash, customerPassword) || !auth.VerifyPassword(c.PasswordHash, customerPassword) {
		t.Fatal("password not hashed", e)
	}
	if _, _, e = s.CompleteEmail(ctx, "192.0.2.1", mailToken(t, m), ""); !errors.Is(e, platform.ErrCustomerAuth) {
		t.Fatal("proof replay", e)
	}
	if _, _, e = s.Login(ctx, "192.0.2.1", "client@example.test", "Wrong-password-123!"); !errors.Is(e, platform.ErrCustomerAuth) {
		t.Fatal(e)
	}
	newToken, logged, e := s.Login(ctx, "192.0.2.1", "client@example.test", customerPassword)
	if e != nil || logged.Customer.ID != session.Customer.ID || newToken == raw {
		t.Fatal(logged, e)
	}
	// SMTP configuration loss must not block existing password authentication.
	t.Setenv(s.Config.Email.PasswordEnv, "")
	if _, _, e = s.Login(ctx, "192.0.2.1", "client@example.test", customerPassword); e != nil {
		t.Fatal("SMTP disabled login", e)
	}
	if e = s.SendEmailChallenge(ctx, "192.0.2.1", "register", "other@example.test", customerPassword, ""); !errors.Is(e, ErrUnavailable) {
		t.Fatal("fake email success", e)
	}
}
func TestEmailDeliveryFailureAndAnonymousAccountResponses(t *testing.T) {
	s, m := serviceFixture(t)
	ctx := context.Background()
	m.fail = true
	if e := s.SendEmailChallenge(ctx, "192.0.2.1", "register", "retry@example.test", customerPassword, ""); !errors.Is(e, ErrDelivery) {
		t.Fatal(e)
	}
	old := mailToken(t, m)
	m.fail = false
	if e := s.SendEmailChallenge(ctx, "192.0.2.1", "register", "retry@example.test", customerPassword, ""); e != nil {
		t.Fatal(e)
	}
	fresh := mailToken(t, m)
	if fresh == old {
		t.Fatal("token reused")
	}
	if _, _, e := s.CompleteEmail(ctx, "192.0.2.1", old, ""); !errors.Is(e, platform.ErrCustomerAuth) {
		t.Fatal(e)
	}
	if _, _, e := s.CompleteEmail(ctx, "192.0.2.1", fresh, ""); e != nil {
		t.Fatal(e)
	}
	m.link = ""
	if e := s.SendEmailChallenge(ctx, "192.0.2.1", "register", "retry@example.test", customerPassword, ""); e != nil || m.link != "" {
		t.Fatal("existing registration disclosed or sent new proof", e)
	}
	if e := s.SendEmailChallenge(ctx, "192.0.2.1", "reset", "missing@example.test", "", ""); e != nil || m.link != "" {
		t.Fatal(e)
	}
}
func TestRecoveryInvalidatesOldCookiesAndChangesPassword(t *testing.T) {
	s, m := serviceFixture(t)
	ctx := context.Background()
	raw, session := registerCustomer(t, s, m, "reset@example.test")
	if e := s.SendEmailChallenge(ctx, "192.0.2.1", "reset", "reset@example.test", "", ""); e != nil {
		t.Fatal(e)
	}
	newRaw, changed, e := s.CompleteEmail(ctx, "192.0.2.1", mailToken(t, m), "Replacement-password-123!")
	if e != nil || newRaw == raw || changed.Customer.ID != session.Customer.ID {
		t.Fatal(changed, e)
	}
	if _, e = s.Store.ResolveCustomerSession(ctx, auth.HashToken(raw)); !errors.Is(e, platform.ErrCustomerAuth) {
		t.Fatal(e)
	}
	if _, _, e = s.Login(ctx, "192.0.2.1", "reset@example.test", customerPassword); !errors.Is(e, platform.ErrCustomerAuth) {
		t.Fatal("old password accepted", e)
	}
	if _, _, e = s.Login(ctx, "192.0.2.1", "reset@example.test", "Replacement-password-123!"); e != nil {
		t.Fatal(e)
	}
}
func TestCustomerAuthBoundsAndRateLimit(t *testing.T) {
	s, _ := serviceFixture(t)
	ctx := context.Background()
	for _, password := range []string{"short", strings.Repeat("a", 73)} {
		if _, _, e := s.Login(ctx, "192.0.2.1", "none@example.test", password); !errors.Is(e, ErrInput) {
			t.Fatal(e)
		}
	}
	for range 5 {
		if _, _, e := s.Login(ctx, "192.0.2.1", "none@example.test", customerPassword); !errors.Is(e, platform.ErrCustomerAuth) {
			t.Fatal(e)
		}
	}
	if _, _, e := s.Login(ctx, "192.0.2.2", "none@example.test", customerPassword); !errors.Is(e, ErrLimited) {
		t.Fatal("per-account bound bypassed", e)
	}
}
