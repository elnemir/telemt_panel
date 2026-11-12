package customer

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/platform"
	"github.com/golang-jwt/jwt/v5"
)

const telegramIssuer = "https://oauth.telegram.org"

var ErrTelegram = errors.New("Telegram authentication failed")

type telegramOIDC struct {
	client    *http.Client
	tokenURL  string
	keysURL   string
	now       func() time.Time
	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetched   time.Time
	attempted time.Time
}

func newTelegramOIDC() *telegramOIDC {
	return &telegramOIDC{client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, tokenURL: telegramIssuer + "/token", keysURL: telegramIssuer + "/.well-known/jwks.json", now: time.Now}
}
func readProviderJSON(response *http.Response, out any) error {
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrTelegram
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(raw) > 65536 || json.Unmarshal(raw, out) != nil {
		return ErrTelegram
	}
	return nil
}
func (t *telegramOIDC) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if kid == "" || len(kid) > 128 {
		return nil, ErrTelegram
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if key := t.keys[kid]; key != nil && now.Sub(t.fetched) < 5*time.Minute {
		return key, nil
	}
	// Unknown attacker-controlled key IDs cannot force a JWKS request per login.
	if !t.attempted.IsZero() && now.Sub(t.attempted) < time.Minute {
		return nil, ErrTelegram
	}
	t.attempted = now
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, t.keysURL, nil)
	if e != nil {
		return nil, ErrTelegram
	}
	response, e := t.client.Do(req)
	if e != nil {
		return nil, ErrTelegram
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if readProviderJSON(response, &set) != nil || len(set.Keys) > 32 {
		return nil, ErrTelegram
	}
	keys := map[string]*rsa.PublicKey{}
	for _, key := range set.Keys {
		if key.Kty != "RSA" || (key.Use != "" && key.Use != "sig") || (key.Alg != "" && key.Alg != "RS256") || key.Kid == "" || len(key.Kid) > 128 {
			continue
		}
		n, e := base64.RawURLEncoding.DecodeString(key.N)
		if e != nil || len(n) < 256 || len(n) > 1024 {
			continue
		}
		exponent, e := base64.RawURLEncoding.DecodeString(key.E)
		if e != nil || len(exponent) < 1 || len(exponent) > 4 {
			continue
		}
		number := new(big.Int).SetBytes(exponent).Int64()
		modulus := new(big.Int).SetBytes(n)
		if number < 3 || number > 2147483647 || number%2 == 0 || modulus.BitLen() < 2048 {
			continue
		}
		if keys[key.Kid] != nil {
			return nil, ErrTelegram
		}
		keys[key.Kid] = &rsa.PublicKey{N: modulus, E: int(number)}
	}
	if len(keys) == 0 {
		return nil, ErrTelegram
	}
	t.keys = keys
	t.fetched = now
	key := keys[kid]
	if key == nil {
		return nil, ErrTelegram
	}
	return key, nil
}

type telegramClaims struct {
	jwt.RegisteredClaims
	Nonce string `json:"nonce"`
}

func (t *telegramOIDC) verify(ctx context.Context, raw, clientID, nonceHash string) (string, error) {
	if len(raw) > 32768 {
		return "", ErrTelegram
	}
	claims := telegramClaims{}
	token, e := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, ErrTelegram
		}
		return t.key(ctx, kid)
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(telegramIssuer), jwt.WithAudience(clientID), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(30*time.Second), jwt.WithTimeFunc(t.now))
	if e != nil || !token.Valid || claims.IssuedAt == nil || claims.Subject == "" || len(claims.Audience) != 1 || claims.Audience[0] != clientID || claims.Nonce == "" || subtle.ConstantTimeCompare([]byte(auth.HashToken(claims.Nonce)), []byte(nonceHash)) != 1 {
		return "", ErrTelegram
	}
	return claims.Subject, nil
}
func (t *telegramOIDC) exchange(ctx context.Context, clientID, secret, redirect, code, verifier, nonceHash string) (string, error) {
	values := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {clientID}, "code_verifier": {verifier}}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL, strings.NewReader(values.Encode()))
	if e != nil {
		return "", ErrTelegram
	}
	req.SetBasicAuth(clientID, secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, e := t.client.Do(req)
	if e != nil {
		return "", ErrTelegram
	}
	var body struct {
		IDToken string `json:"id_token"`
	}
	if readProviderJSON(response, &body) != nil || body.IDToken == "" {
		return "", ErrTelegram
	}
	return t.verify(ctx, body.IDToken, clientID, nonceHash)
}

type TelegramStart struct {
	URL          string
	BrowserToken string
}

func (s *Service) StartTelegram(ctx context.Context, ip, sessionHash string) (TelegramStart, error) {
	if !s.TelegramAvailable() {
		return TelegramStart{}, ErrUnavailable
	}
	release, e := s.admit(ip, "")
	if e != nil {
		return TelegramStart{}, e
	}
	defer release()
	values := make([]string, 4)
	for i := range values {
		values[i], e = auth.NewToken()
		if e != nil {
			return TelegramStart{}, e
		}
	}
	state, browser, verifier, nonce := values[0], values[1], values[2], values[3]
	if e = s.Store.PutCustomerOIDCState(ctx, auth.HashToken(state), auth.HashToken(browser), verifier, auth.HashToken(nonce), sessionHash); e != nil {
		return TelegramStart{}, e
	}
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {s.Config.Telegram.ClientID}, "redirect_uri": {s.CallbackURL()}, "response_type": {"code"}, "scope": {"openid profile"}, "state": {state}, "nonce": {nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	return TelegramStart{URL: telegramIssuer + "/auth?" + q.Encode(), BrowserToken: browser}, nil
}
func (s *Service) CompleteTelegram(ctx context.Context, ip, state, browser, code string) (string, platform.CustomerSession, error) {
	if !s.Config.Telegram.Enabled {
		return "", platform.CustomerSession{}, ErrUnavailable
	}
	if len(state) != 43 || len(browser) != 43 || len(code) < 1 || len(code) > 4096 {
		return "", platform.CustomerSession{}, ErrInput
	}
	release, e := s.admit(ip, "")
	if e != nil {
		return "", platform.CustomerSession{}, e
	}
	defer release()
	secret, e := secretEnv(s.Config.Telegram.SecretEnv)
	if e != nil {
		return "", platform.CustomerSession{}, e
	}
	flow, e := s.Store.TakeCustomerOIDCState(ctx, auth.HashToken(state), auth.HashToken(browser))
	if e != nil {
		return "", platform.CustomerSession{}, e
	}
	subject, e := s.oidc.exchange(ctx, s.Config.Telegram.ClientID, secret, s.CallbackURL(), code, flow.Verifier, flow.NonceHash)
	if e != nil {
		return "", platform.CustomerSession{}, e
	}
	var c platform.CustomerCredential
	if flow.SessionHash != "" {
		c, e = s.Store.LinkCustomerTelegram(ctx, flow.SessionHash, subject)
	} else {
		c, e = s.Store.TelegramCustomer(ctx, subject)
	}
	if e != nil {
		return "", platform.CustomerSession{}, e
	}
	return s.newSession(ctx, c)
}
