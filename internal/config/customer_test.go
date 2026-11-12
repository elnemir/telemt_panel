package config

import "testing"

func TestCustomerPortalConfigurationBoundary(t *testing.T) {
	valid := CustomerPortalConfig{Enabled: true, PublicURL: "https://panel.example.test/panel", Email: CustomerEmailConfig{Enabled: true, Host: "smtp.example.test", Port: 465, Mode: "tls", From: "sender@example.test", Username: "sender", PasswordEnv: "TELEMT_CUSTOMER_SMTP"}}
	if e := valid.Validate("/panel", true, false); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*CustomerPortalConfig){func(c *CustomerPortalConfig) { c.PublicURL = "http://panel.example.test/panel" }, func(c *CustomerPortalConfig) { c.PublicURL = "https://panel.example.test/wrong" }, func(c *CustomerPortalConfig) { c.PublicURL = "https://user:pass@panel.example.test/panel" }, func(c *CustomerPortalConfig) { c.PublicURL = "https://panel.example.test/panel?next=bad" }, func(c *CustomerPortalConfig) { c.Email.Mode = "plain" }, func(c *CustomerPortalConfig) { c.Email.PasswordEnv = "PATH" }, func(c *CustomerPortalConfig) { c.Email.From = "Sender <sender@example.test>" }, func(c *CustomerPortalConfig) { c.Email.CAFile = "relative.pem" }, func(c *CustomerPortalConfig) { c.Email.Enabled = false }} {
		c := valid
		change(&c)
		if e := c.Validate("/panel", true, false); e == nil {
			t.Fatal("invalid config accepted", c)
		}
	}
	for _, flags := range [][2]bool{{false, false}, {true, true}} {
		if e := valid.Validate("/panel", flags[0], flags[1]); e == nil {
			t.Fatal("invalid platform mode")
		}
	}
	local := valid
	local.PublicURL = "http://localhost:48180/panel"
	if e := local.Validate("/panel", true, false); e != nil {
		t.Fatal(e)
	}
	telegram := CustomerPortalConfig{Enabled: true, PublicURL: "https://panel.example.test/panel", Telegram: CustomerTelegramConfig{Enabled: true, ClientID: "12345", SecretEnv: "TELEMT_CUSTOMER_OIDC"}}
	if e := telegram.Validate("/panel", true, false); e != nil {
		t.Fatal(e)
	}
}
