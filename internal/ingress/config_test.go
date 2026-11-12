package ingress

import (
	"strings"
	"testing"
)

func fixture() Config {
	return Config{Domain: "proxy.example.test", NativePort: 8443, WebPort: 443, NativeBalance: "leastconn", Targets: []Target{{NodeID: "n-b", NativeAddress: "192.0.2.2:443", WebAddress: "192.0.2.2:18080", Weight: 20}, {NodeID: "n-a", NativeAddress: "[2001:db8::1]:443", WebAddress: "[2001:db8::1]:18080", Weight: 10, Drain: true}}}
}
func TestRenderPreservesProtocolAndAffinity(t *testing.T) {
	c := fixture()
	raw, e := c.Render()
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"mode tcp", "send-proxy-v2", "retries 0", "alpn h2,http/1.1", "http-request del-header X-Forwarded-For", "http-request set-header X-Forwarded-For %[src]", "cookie TELEMT_INGRESS insert indirect nocache httponly secure", "weight 0", "init-state fully-down"} {
		if !strings.Contains(raw, s) {
			t.Fatal("missing contract", s)
		}
	}
	c.Targets[0], c.Targets[1] = c.Targets[1], c.Targets[0]
	other, _ := c.Render()
	if raw != other {
		t.Fatal("non-deterministic")
	}
	if strings.Contains(raw, "set-path") || strings.Contains(raw, "set-query") || strings.Contains(raw, "option redispatch") {
		t.Fatal("carrier replay/rewrite")
	}
}
func TestRejectInjectionAndAmbiguousEndpoints(t *testing.T) {
	for _, mutate := range []func(*Config){func(c *Config) { c.Domain = "example.test\nfrontend injected" }, func(c *Config) { c.Domain = "EXAMPLE.test" }, func(c *Config) { c.NativeBalance = "roundrobin\nanything" }, func(c *Config) { c.WebPort = c.NativePort }, func(c *Config) { c.Targets[0].NativeAddress = "server.example:443" }, func(c *Config) { c.Targets[0].NativeAddress = "[fe80::1%en0]:443" }, func(c *Config) { c.Targets[0].WebAddress = "0.0.0.0:80" }, func(c *Config) { c.Targets[0].Weight = 0 }, func(c *Config) { c.Targets[0].Weight = 257 }, func(c *Config) { c.Targets[1].NodeID = c.Targets[0].NodeID }, func(c *Config) { c.Targets = nil }} {
		c := fixture()
		mutate(&c)
		if _, e := c.Render(); e == nil {
			t.Fatal("invalid config accepted", c)
		}
	}
}
