package config

import (
	"strings"
	"testing"
)

func TestPanelOverridesPreserveExplicitFalseAndRejectOtherKeys(t *testing.T) {
	base := &Config{Geography: GeographyConfig{BuildTimeoutSecs: 10}}
	base.CustomerPortal.Email.Enabled = true
	next, err := ApplyPanelOverrides(base, []byte("[customer_portal.email]\nenabled = false\n"))
	if err != nil || next.CustomerPortal.Email.Enabled {
		t.Fatalf("explicit false not applied: %v", err)
	}
	raw, err := EncodePanelOverrides(base, next)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := ApplyPanelOverrides(base, raw)
	if err != nil || reopened.CustomerPortal.Email.Enabled {
		t.Fatalf("explicit false not persisted: %v", err)
	}
	if strings.Contains(string(raw), "session_ttl") || strings.Contains(string(raw), "build_timeout_secs") {
		t.Fatal("unchanged fields were persisted")
	}
	for _, raw := range []string{
		"[auth]\ndisabled = true",
		"[host.commands.panel]\nrestart = ['synthetic-value']",
		"[customer_portal]\nenabled = true",
		"[auth]\nsession_ttl = 'synthetic-value'",
	} {
		if _, err := ApplyPanelOverrides(base, []byte(raw)); err == nil || strings.Contains(err.Error(), "synthetic-value") {
			t.Fatalf("expected sanitized rejection, got %v", err)
		}
	}
}
