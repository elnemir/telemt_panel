//go:build !lite

package platform

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestEntitlementCreatesOneIdentityAndAppliesPaidSnapshotAtBoundary(t *testing.T) {
	s, p, now := billingFixture(t)
	ctx := context.Background()
	invoice := quote(t, s, p, "", "entitlement-checkout-0001")
	if ran, e := s.ReconcileEntitlement(ctx); e != nil || ran {
		t.Fatal("unpaid provisioned", ran, e)
	}
	if _, e := s.SettlePayment(ctx, "yoo", verified(invoice, "entitlement-payment", *now)); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, e := s.ReconcileEntitlement(ctx); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	sub, e := s.CustomerSubscription(ctx, "customer-a", invoice.SubscriptionID)
	if e != nil || sub.IdentityID == "" {
		t.Fatal(sub, e)
	}
	identity, e := s.GetIdentity(ctx, sub.IdentityID)
	if e != nil || !identity.Policy.Enabled || identity.Username != sub.ID || countTable(t, s, "identities") != 1 {
		t.Fatal(identity, e)
	}
	secret, e := s.RevealIdentitySecret(ctx, identity.ID)
	if e != nil || len(secret) != 32 {
		t.Fatal(e)
	}
	// Editing the current tariff must not mutate this immutable paid period.
	nextTerms := p.Terms
	rate := uint64(1000000)
	nextTerms.NodePolicy.RateLimitDownBps = &rate
	if e = s.UpdatePlan(ctx, p.ID, p.Revision, p.Name, true, nextTerms); e != nil {
		t.Fatal(e)
	}
	*now += 31
	if _, e = s.ReconcileEntitlement(ctx); e != nil {
		t.Fatal(e)
	}
	same, _ := s.GetIdentity(ctx, identity.ID)
	if !reflect.DeepEqual(same.Policy, identity.Policy) {
		t.Fatal("paid terms mutated", same)
	}
	renewed := quote(t, s, p, sub.ID, "entitlement-checkout-0002")
	if _, e = s.SettlePayment(ctx, "yoo", verified(renewed, "entitlement-renewal", *now)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReconcileEntitlement(ctx); e != nil {
		t.Fatal(e)
	}
	same, _ = s.GetIdentity(ctx, identity.ID)
	if same.Policy.RateLimitDownBps != nil {
		t.Fatal("next tariff applied early")
	}
	first, e := s.CurrentPaidPeriod(ctx, "customer-a", sub.ID)
	if e != nil {
		t.Fatal(e)
	}
	*now = first.EndsAt
	if _, e = s.ReconcileEntitlement(ctx); e != nil {
		t.Fatal(e)
	}
	changed, _ := s.GetIdentity(ctx, identity.ID)
	if changed.Policy.RateLimitDownBps == nil || *changed.Policy.RateLimitDownBps != rate || changed.Revision <= identity.Revision {
		t.Fatal(changed)
	}
	second, e := s.CurrentPaidPeriod(ctx, "customer-a", sub.ID)
	if e != nil {
		t.Fatal(e)
	}
	*now = second.EndsAt
	if _, e = s.ReconcileEntitlement(ctx); e != nil {
		t.Fatal(e)
	}
	expired, _ := s.GetIdentity(ctx, identity.ID)
	if expired.Policy.Enabled {
		t.Fatal("expired enabled")
	}
	if _, e = s.CurrentPaidPeriod(ctx, "customer-a", sub.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
}
func TestEntitlementDoesNotUndoCustomerDisableOrIdentityRevoke(t *testing.T) {
	s, p, now := billingFixture(t)
	ctx := context.Background()
	invoice := quote(t, s, p, "", "entitlement-checkout-0001")
	if _, e := s.SettlePayment(ctx, "yoo", verified(invoice, "one", *now)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReconcileEntitlement(ctx); e != nil {
		t.Fatal(e)
	}
	sub, _ := s.CustomerSubscription(ctx, "customer-a", invoice.SubscriptionID)
	id, _ := s.GetIdentity(ctx, sub.IdentityID)
	if _, e := s.db.Exec("UPDATE customers SET enabled=0 WHERE id='customer-a'"); e != nil {
		t.Fatal(e)
	}
	*now += 31
	if _, e := s.ReconcileEntitlement(ctx); e != nil {
		t.Fatal(e)
	}
	disabled, _ := s.GetIdentity(ctx, id.ID)
	if disabled.Policy.Enabled {
		t.Fatal("disabled customer enabled")
	}
	if _, e := s.db.Exec("UPDATE customers SET enabled=1 WHERE id='customer-a'"); e != nil {
		t.Fatal(e)
	}
	*now += 31
	if _, e := s.ReconcileEntitlement(ctx); e != nil {
		t.Fatal(e)
	}
	enabled, _ := s.GetIdentity(ctx, id.ID)
	if !enabled.Policy.Enabled {
		t.Fatal("paid customer not restored")
	}
	if e := s.UpdateIdentity(ctx, id.ID, enabled.Revision, enabled.Policy, false, true); e != nil {
		t.Fatal(e)
	}
	*now += 31
	if _, e := s.ReconcileEntitlement(ctx); e != nil {
		t.Fatal(e)
	}
	revoked, _ := s.GetIdentity(ctx, id.ID)
	if !revoked.Revoked || revoked.Policy.Enabled {
		t.Fatal("revocation undone")
	}
}
