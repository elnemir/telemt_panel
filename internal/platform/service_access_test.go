//go:build !lite

package platform

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/amirotin/telemt_panel/internal/telemt"
)

func TestCustomerAccessRequiresOwnCurrentLeaseAndEncryptedLinks(t *testing.T) {
	s, w, _, i, now := managedWorkerFixture(t)
	ctx := context.Background()
	stepManaged(t, w, now)
	stepManaged(t, w, now)
	sub, e := s.CustomerSubscription(ctx, "customer-a", i.SubscriptionID)
	if e != nil {
		t.Fatal(e)
	}
	access, e := s.CustomerServiceAccess(ctx, "customer-a", sub.ID)
	if e != nil || access.Status != "configured" || len(access.Nodes) != 1 || len(access.Nodes[0].Links) != 1 || access.Usage == nil || access.Usage.LastReportedAt == nil {
		t.Fatal(access, e)
	}
	if _, e = s.CustomerServiceAccess(ctx, "customer-b", sub.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("foreign service exposed", e)
	}
	secret, e := s.RevealIdentitySecret(ctx, sub.IdentityID)
	if e != nil {
		t.Fatal(e)
	}
	var sealed []byte
	if e = s.db.QueryRow("SELECT links_cipher FROM identity_jobs WHERE identity_id=?", sub.IdentityID).Scan(&sealed); e != nil || len(sealed) == 0 || bytes.Contains(sealed, []byte(secret)) || bytes.Contains(sealed, []byte("tg://")) {
		t.Fatal("links not encrypted", e)
	}
	*now = access.Nodes[0].BudgetUntil
	stale, e := s.CustomerServiceAccess(ctx, "customer-a", sub.ID)
	if e != nil || stale.Status == "configured" || len(stale.Nodes[0].Links) != 0 || stale.Nodes[0].Status != "unavailable" {
		t.Fatal(stale, e)
	}
	// Unknown old-node usage is retained after its lease expires.
	if stale.Usage.GrantedBytes != access.Usage.GrantedBytes || stale.Usage.UnknownBytes != access.Usage.UnknownBytes {
		t.Fatal("expired lease reclaimed usage", stale)
	}
	stepManaged(t, w, now)
	restored, e := s.CustomerServiceAccess(ctx, "customer-a", sub.ID)
	if e != nil || restored.Status != "configured" {
		t.Fatal(restored, e)
	}
	id, e := s.GetIdentity(ctx, sub.IdentityID)
	if e != nil || !id.BillingManaged {
		t.Fatal(id, e)
	}
	if e = s.UpdateIdentity(ctx, id.ID, id.Revision, id.Policy, false, true); e != nil {
		t.Fatal(e)
	}
	revoked, e := s.CustomerServiceAccess(ctx, "customer-a", sub.ID)
	if e != nil || revoked.Status != "disabled" || len(revoked.Nodes) != 0 {
		t.Fatal(revoked, e)
	}
}
func TestConnectionLinksRejectForeignSecretsSchemesAndDuplicateParameters(t *testing.T) {
	secret := strings.Repeat("a", 32)
	base := "tg://proxy?server=proxy.example&port=443&secret="
	raw := []string{base + secret, "javascript:alert(1)?secret=" + secret, "https://phishing.example/?secret=" + secret, base + strings.Repeat("b", 32), base + secret + "&secret=" + secret, base + secret + "&extra=1", strings.Replace(base+secret, "port=443", "port=0", 1), base + secret}
	tls := base + "ee" + secret + "6578616d706c652e6f7267"
	got := connectionLinks(telemt.UserLinks{Classic: raw, TLS: []string{tls}}, secret)
	if len(got) != 2 || got[0].Mode != "faketls" || got[1].URL != base+secret {
		t.Fatal(got)
	}
}
