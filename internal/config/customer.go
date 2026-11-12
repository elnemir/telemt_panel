package config

import (
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

type CustomerPortalConfig struct {
	Enabled   bool                   `toml:"enabled"`
	PublicURL string                 `toml:"public_url"`
	Email     CustomerEmailConfig    `toml:"email"`
	Telegram  CustomerTelegramConfig `toml:"telegram"`
}
type CustomerEmailConfig struct {
	Enabled     bool   `toml:"enabled"`
	Host        string `toml:"host"`
	Port        int    `toml:"port"`
	Mode        string `toml:"mode"`
	From        string `toml:"from"`
	Username    string `toml:"username"`
	PasswordEnv string `toml:"password_env"`
	CAFile      string `toml:"ca_file"`
}
type CustomerTelegramConfig struct {
	Enabled   bool   `toml:"enabled"`
	ClientID  string `toml:"client_id"`
	SecretEnv string `toml:"client_secret_env"`
}

var customerSecretEnv = regexp.MustCompile(`^TELEMT_CUSTOMER_[A-Z0-9_]{1,96}$`)
var telegramClientID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

func (c CustomerPortalConfig) Validate(basePath string, platformEnabled, agentEnabled bool) error {
	if !c.Enabled {
		return nil
	}
	if !platformEnabled || agentEnabled {
		return fmt.Errorf("customer_portal requires the central platform")
	}
	if !c.Email.Enabled && !c.Telegram.Enabled {
		return fmt.Errorf("customer_portal requires an enabled login method")
	}
	u, e := url.Parse(c.PublicURL)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.TrimSuffix(u.Path, "/") != basePath {
		return fmt.Errorf("customer_portal.public_url must include the panel base_path without query or fragment")
	}
	local := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		local = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return fmt.Errorf("customer_portal.public_url must use HTTPS (HTTP allowed only on loopback)")
	}
	if c.Email.Enabled {
		if c.Email.CAFile != "" && !filepath.IsAbs(c.Email.CAFile) {
			return fmt.Errorf("customer_portal.email.ca_file must be an absolute PEM certificate path")
		}
		for _, r := range c.Email.From {
			if r < 33 || r > 126 {
				return fmt.Errorf("customer_portal.email.from must use ASCII")
			}
		}
		a, e := mail.ParseAddress(c.Email.From)
		if e != nil || a.Address != c.Email.From || strings.ContainsAny(c.Email.From, "\r\n") || len(c.Email.From) > 254 {
			return fmt.Errorf("customer_portal.email.from must be a bare mailbox")
		}
		if c.Email.Host == "" || len(c.Email.Host) > 253 || strings.ContainsAny(c.Email.Host, " /\\\r\n\t") || c.Email.Port < 1 || c.Email.Port > 65535 || (c.Email.Mode != "tls" && c.Email.Mode != "starttls") {
			return fmt.Errorf("customer_portal.email requires SMTP host, port and tls/starttls mode")
		}
		if c.Email.Username == "" || len(c.Email.Username) > 256 || strings.ContainsAny(c.Email.Username, "\r\n\x00") || !customerSecretEnv.MatchString(c.Email.PasswordEnv) {
			return fmt.Errorf("customer_portal.email requires username and TELEMT_CUSTOMER_* password_env")
		}
	}
	if c.Telegram.Enabled && (!telegramClientID.MatchString(c.Telegram.ClientID) || !customerSecretEnv.MatchString(c.Telegram.SecretEnv)) {
		return fmt.Errorf("customer_portal.telegram requires client_id and TELEMT_CUSTOMER_* client_secret_env")
	}
	return nil
}
