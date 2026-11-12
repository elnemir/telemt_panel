// Package customer owns customer authentication independently of administrator
// sessions and Telemt credentials. Platform owns the durable transactions.
package customer

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/config"
	"github.com/amirotin/telemt_panel/internal/platform"
	"github.com/amirotin/telemt_panel/internal/ratelimit"
)

var ErrUnavailable = errors.New("customer authentication method unavailable")
var ErrDelivery = errors.New("verification message could not be delivered")
var ErrLimited = errors.New("customer authentication rate limited")
var ErrInput = errors.New("invalid customer authentication input")

type Mailer interface {
	Send(context.Context, string, string) error
}
type Service struct {
	Store     *platform.Store
	Config    config.CustomerPortalConfig
	mail      Mailer
	limiter   *ratelimit.Window
	global    *ratelimit.Window
	ips       *ratelimit.Window
	expensive chan struct{}
	oidc      *telegramOIDC
}

func New(st *platform.Store, cfg config.CustomerPortalConfig, basePath string) (*Service, error) {
	if st == nil || !cfg.Enabled {
		return nil, ErrUnavailable
	}
	if e := cfg.Validate(basePath, true, false); e != nil {
		return nil, e
	}
	s := &Service{Store: st, Config: cfg, mail: &smtpMailer{cfg: cfg.Email}, limiter: ratelimit.New(5, 15*time.Minute, nil), global: ratelimit.New(60, time.Minute, nil), ips: ratelimit.New(30, 15*time.Minute, nil), expensive: make(chan struct{}, 4), oidc: newTelegramOIDC()}
	return s, nil
}
func (s *Service) Close()            { s.limiter.Close(); s.global.Close(); s.ips.Close() }
func (s *Service) PortalURL() string { return strings.TrimRight(s.Config.PublicURL, "/") + "/portal" }
func (s *Service) CallbackURL() string {
	return strings.TrimRight(s.Config.PublicURL, "/") + "/api/customer/auth/telegram/callback"
}
func secretEnv(name string) (string, error) {
	if !strings.HasPrefix(name, "TELEMT_CUSTOMER_") {
		return "", ErrUnavailable
	}
	v, ok := os.LookupEnv(name)
	if !ok || v == "" || len(v) > 4096 || strings.ContainsAny(v, "\r\n\x00") {
		return "", ErrUnavailable
	}
	return v, nil
}
func (s *Service) EmailAvailable() bool {
	if !s.Config.Email.Enabled {
		return false
	}
	_, e := secretEnv(s.Config.Email.PasswordEnv)
	return e == nil
}
func (s *Service) TelegramAvailable() bool {
	if !s.Config.Telegram.Enabled {
		return false
	}
	_, e := secretEnv(s.Config.Telegram.SecretEnv)
	return e == nil
}
func (s *Service) admit(ip, email string) (func(), error) {
	// Bound all attempts, not only wrong passwords. The shared key bounds the
	// limiter's map and total password/mail work before user-controlled keys enter.
	if !s.global.AllowAndCount("global") {
		return nil, ErrLimited
	}
	if !s.ips.AllowAndCount(ip) || (email != "" && !s.limiter.AllowAndCount(auth.HashToken(email))) {
		return nil, ErrLimited
	}
	select {
	case s.expensive <- struct{}{}:
		return func() { <-s.expensive }, nil
	default:
		return nil, ErrLimited
	}
}
func validPassword(p string) bool { return len(p) >= 12 && len(p) <= 72 }
func (s *Service) Login(ctx context.Context, ip, email, password string) (string, platform.CustomerSession, error) {
	if !s.Config.Email.Enabled {
		return "", platform.CustomerSession{}, ErrUnavailable
	}
	email, e := platform.NormalizeCustomerEmail(email)
	if e != nil || !validPassword(password) {
		return "", platform.CustomerSession{}, ErrInput
	}
	release, e := s.admit(ip, email)
	if e != nil {
		return "", platform.CustomerSession{}, e
	}
	defer release()
	c, e := s.Store.CustomerCredentials(ctx, email)
	if e != nil && !errors.Is(e, platform.ErrNotFound) {
		return "", platform.CustomerSession{}, e
	}
	name, hash := "\x00", ""
	if e == nil && c.Customer.Enabled && c.Customer.EmailVerified && c.PasswordHash != "" {
		name, hash = email, c.PasswordHash
	}
	if !auth.VerifyCredentials(name, hash, email, password) {
		return "", platform.CustomerSession{}, platform.ErrCustomerAuth
	}
	return s.newSession(ctx, c)
}
func (s *Service) newSession(ctx context.Context, c platform.CustomerCredential) (string, platform.CustomerSession, error) {
	token, e := auth.NewToken()
	if e != nil {
		return "", platform.CustomerSession{}, e
	}
	session, e := s.Store.CreateCustomerSession(ctx, c.Customer.ID, c.AuthRevision, auth.HashToken(token))
	if e != nil {
		return "", session, e
	}
	return token, session, nil
}
func (s *Service) SendEmailChallenge(ctx context.Context, ip, purpose, email, password, sessionHash string) error {
	if !s.EmailAvailable() {
		return ErrUnavailable
	}
	email, e := platform.NormalizeCustomerEmail(email)
	if e != nil {
		return ErrInput
	}
	if purpose != "reset" && !validPassword(password) {
		return ErrInput
	}
	release, e := s.admit(ip, email)
	if e != nil {
		return e
	}
	defer release()
	hash := ""
	if purpose != "reset" {
		hash, e = auth.HashPassword(password)
		if e != nil {
			return ErrInput
		}
	}
	token, e := auth.NewToken()
	if e != nil {
		return e
	}
	e = s.Store.PrepareEmailChallenge(ctx, purpose, email, auth.HashToken(token), hash, sessionHash)
	// Anonymous flows do not disclose whether the mailbox already has an account.
	if purpose == "register" && errors.Is(e, platform.ErrConflict) || purpose == "reset" && errors.Is(e, platform.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	link := s.PortalURL() + "#" + url.Values{"email_token": {token}, "purpose": {purpose}}.Encode()
	if e = s.mail.Send(ctx, email, link); e != nil {
		return ErrDelivery
	}
	return nil
}
func (s *Service) CompleteEmail(ctx context.Context, ip, token, password string) (string, platform.CustomerSession, error) {
	if !s.Config.Email.Enabled {
		return "", platform.CustomerSession{}, ErrUnavailable
	}
	if len(token) != 43 || password != "" && !validPassword(password) {
		return "", platform.CustomerSession{}, ErrInput
	}
	release, e := s.admit(ip, "proof:"+token)
	if e != nil {
		return "", platform.CustomerSession{}, e
	}
	defer release()
	hash := ""
	if password != "" {
		hash, e = auth.HashPassword(password)
		if e != nil {
			return "", platform.CustomerSession{}, ErrInput
		}
	}
	c, e := s.Store.CompleteEmailChallenge(ctx, auth.HashToken(token), hash)
	if e != nil {
		return "", platform.CustomerSession{}, e
	}
	return s.newSession(ctx, c)
}
