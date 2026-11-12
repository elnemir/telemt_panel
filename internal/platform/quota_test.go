//go:build !lite

package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
)

func quotaFixture(t *testing.T) (*Store, Plan, Invoice, PaidPeriod, Identity, *int64) {
	t.Helper()
	ctx := context.Background()
	s, p, now := billingFixture(t)
	q, o := int64(1000), int64(50)
	p.Terms.QuotaBytes = &q
	p.Terms.OverrunBytes = &o
	if e := s.UpdatePlan(ctx, p.ID, p.Revision, p.Name, true, p.Terms); e != nil {
		t.Fatal(e)
	}
	p, e := s.GetPlan(ctx, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, node := range []string{"node-a", "node-b"} {
		c, e := s.GetCluster(ctx, p.ClusterID)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.AddMember(ctx, c.ID, c.Revision, node); e != nil {
			t.Fatal(e)
		}
	}
	c, e := s.GetCluster(ctx, p.ClusterID)
	if e != nil {
		t.Fatal(e)
	}
	id, e := s.CreateIdentity(ctx, c.ID, c.Revision, IdentityInput{Username: "paid", Policy: Policy{Enabled: true}})
	if e != nil {
		t.Fatal(e)
	}
	i := quote(t, s, p, "", "quota-checkout-0001")
	if _, e = s.SettlePayment(ctx, "yoo", verified(i, "quota-payment-1", *now)); e != nil {
		t.Fatal(e)
	}
	period, e := s.CurrentPaidPeriod(ctx, "customer-a", i.SubscriptionID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec("UPDATE subscriptions SET identity_id=? WHERE id=?", id.ID, i.SubscriptionID); e != nil {
		t.Fatal(e)
	}
	return s, p, i, period, id, now
}

const bootA = "11111111111111111111111111111111"
const bootB = "22222222222222222222222222222222"

func reserve(t *testing.T, s *Store, p PaidPeriod, node, boot string, expected, target int64) QuotaGrant {
	t.Helper()
	g, e := s.ReserveQuota(context.Background(), QuotaRequest{p.ID, node, boot, expected, target})
	if e != nil {
		t.Fatal(e)
	}
	return g
}
func TestQuotaConcurrentNodesAndBootsNeverMultiplyPeriodCap(t *testing.T) {
	s, _, i, p, _, now := quotaFixture(t)
	var wg sync.WaitGroup
	for n := range 32 {
		wg.Go(func() {
			node := "node-a"
			if n%2 == 1 {
				node = "node-b"
			}
			g, e := s.ReserveQuota(context.Background(), QuotaRequest{p.ID, node, fmt.Sprintf("%032x", n), 0, 100})
			if e != nil {
				t.Error(e)
				return
			}
			if g.GrantedBytes > 100 || g.ValidUntil != *now+60 {
				t.Error(g)
			}
		})
	}
	wg.Wait()
	u, e := s.CustomerQuotaUsage(context.Background(), "customer-a", i.SubscriptionID, p.ID)
	if e != nil || u.GrantedBytes != 1050 || u.ObservedBytes != 0 || u.UnknownBytes != 1050 || u.Allocations != 32 || u.LastReportedAt != nil {
		t.Fatal(u, e)
	}
	if _, e = s.CustomerQuotaUsage(context.Background(), "customer-b", i.SubscriptionID, p.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("foreign usage", e)
	}
}
func TestQuotaCumulativeRetryCASReportsAndNoReclaim(t *testing.T) {
	s, _, i, p, _, _ := quotaFixture(t)
	ctx := context.Background()
	a := reserve(t, s, p, "node-a", bootA, 0, 700)
	retry := reserve(t, s, p, "node-a", bootA, 0, 700)
	if retry.GrantedBytes != 700 || retry.Sequence <= a.Sequence {
		t.Fatal(retry)
	}
	if _, e := s.ReserveQuota(ctx, QuotaRequest{p.ID, "node-a", bootA, 0, 900}); !errors.Is(e, ErrConflict) {
		t.Fatal("stale CAS", e)
	}
	b := reserve(t, s, p, "node-b", bootB, 0, 700)
	if b.GrantedBytes != 350 {
		t.Fatal(b)
	}
	for _, used := range []int64{100, 100, 500} {
		if e := s.ObserveQuota(ctx, p.ID, "node-a", bootA, a.Sequence, used); e != nil {
			t.Fatal(e)
		}
	}
	for _, v := range []struct{ seq, used int64 }{{retry.Sequence, 499}, {retry.Sequence, 701}, {retry.Sequence + 1, 600}} {
		if e := s.ObserveQuota(ctx, p.ID, "node-a", bootA, v.seq, v.used); !errors.Is(e, ErrConflict) {
			t.Fatal(v, e)
		}
	}
	// An acknowledged usage report and a fresh process never release the first
	// process's reserved bytes; it could still be transmitting after the report.
	next := reserve(t, s, p, "node-a", "33333333333333333333333333333333", 0, 700)
	if next.GrantedBytes != 0 {
		t.Fatal("reclaimed unknown budget", next)
	}
	u, e := s.CustomerQuotaUsage(ctx, "customer-a", i.SubscriptionID, p.ID)
	if e != nil || u.GrantedBytes != 1050 || u.ObservedBytes != 500 || u.UnknownBytes != 550 || u.LastReportedAt == nil {
		t.Fatal(u, e)
	}
	for _, q := range []string{`UPDATE quota_allocations SET granted_bytes=granted_bytes-1 WHERE granted_bytes>0`, `UPDATE quota_allocations SET observed_bytes=0 WHERE observed_bytes>0`, `DELETE FROM quota_allocations`} {
		if _, e = s.db.Exec(q); e == nil {
			t.Fatal("reclaimed allocation via SQL", q)
		}
	}
}
func TestQuotaScopesExpiryRenewalAndDisabledEntitlements(t *testing.T) {
	s, plan, i, p, id, now := quotaFixture(t)
	ctx := context.Background()
	old := reserve(t, s, p, "node-a", bootA, 0, 700)
	if _, e := s.ReserveQuota(ctx, QuotaRequest{p.ID, "foreign-node", bootA, 0, 1}); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	nextInvoice := quote(t, s, plan, i.SubscriptionID, "quota-checkout-0002")
	if _, e := s.SettlePayment(ctx, "yoo", verified(nextInvoice, "quota-payment-2", *now)); e != nil {
		t.Fatal(e)
	}
	var nextPeriod string
	if e := s.db.QueryRow("SELECT id FROM paid_periods WHERE invoice_id=?", nextInvoice.ID).Scan(&nextPeriod); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReserveQuota(ctx, QuotaRequest{nextPeriod, "node-a", bootA, 0, 1}); !errors.Is(e, ErrNotFound) {
		t.Fatal("future period spent", e)
	}
	*now = p.EndsAt - 5
	near := reserve(t, s, p, "node-a", bootA, 700, 700)
	if near.ValidUntil != p.EndsAt {
		t.Fatal(near)
	}
	*now = p.EndsAt
	if _, e := s.ReserveQuota(ctx, QuotaRequest{p.ID, "node-a", bootA, 700, 700}); !errors.Is(e, ErrNotFound) {
		t.Fatal("expired renewed", e)
	}
	fresh, e := s.ReserveQuota(ctx, QuotaRequest{nextPeriod, "node-a", bootA, 0, 900})
	if e != nil || fresh.GrantedBytes != 900 || fresh.Sequence <= near.Sequence {
		t.Fatal(fresh, e)
	}
	if e = s.ObserveQuota(ctx, p.ID, "node-a", bootA, old.Sequence, 600); e != nil {
		t.Fatal("late old-period report", e)
	}
	for _, field := range []struct{ table, key, value string }{{"customers", "id", "customer-a"}, {"subscriptions", "id", i.SubscriptionID}} {
		if _, e = s.db.Exec("UPDATE "+field.table+" SET enabled=0 WHERE "+field.key+"=?", field.value); e != nil {
			t.Fatal(e)
		}
		if _, e = s.ReserveQuota(ctx, QuotaRequest{nextPeriod, "node-a", bootA, 900, 900}); !errors.Is(e, ErrNotFound) {
			t.Fatal("disabled renewed", e)
		}
		if _, e = s.db.Exec("UPDATE "+field.table+" SET enabled=1 WHERE "+field.key+"=?", field.value); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.UpdateIdentity(ctx, id.ID, id.Revision, Policy{Enabled: false}, false, true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReserveQuota(ctx, QuotaRequest{nextPeriod, "node-a", bootA, 900, 900}); !errors.Is(e, ErrNotFound) {
		t.Fatal("disabled identity renewed", e)
	}
}
func TestQuotaAllocationSurvivesReopenAndUnsentGrant(t *testing.T) {
	s, _, _, p, _, _ := quotaFixture(t)
	ctx := context.Background()
	a := reserve(t, s, p, "node-a", bootA, 0, 1000)
	// Reopen the actual SQLite file using its configured key. No delivery/usage
	// acknowledgement is needed to retain the committed reservation.
	var seq int
	var name, path string
	if e := s.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); e != nil {
		t.Fatal(e)
	}
	clock := s.now
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	key := make([]byte, 32)
	for n := range key {
		key[n] = 7
	}
	reopened, e := Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	reopened.now = clock
	b := reserve(t, reopened, p, "node-a", bootB, 0, 1000)
	if b.GrantedBytes != 50 || b.Sequence <= a.Sequence {
		t.Fatal(b)
	}
	replay := reserve(t, reopened, p, "node-a", bootA, 0, 1000)
	if replay.GrantedBytes != 1000 {
		t.Fatal(replay)
	}
	if e = reopened.ObserveQuota(ctx, p.ID, "node-a", bootA, a.Sequence, 999); e != nil {
		t.Fatal(e)
	}
}
func TestQuotaFailsClosedAtSequenceAndInventoryBounds(t *testing.T) {
	s, _, _, p, _, _ := quotaFixture(t)
	ctx := context.Background()
	a := reserve(t, s, p, "node-a", bootA, 0, 100)
	if _, e := s.db.Exec("UPDATE quota_sequences SET sequence=?", int64(math.MaxInt64)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReserveQuota(ctx, QuotaRequest{p.ID, "node-a", bootA, 100, 200}); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	var granted int64
	if e := s.db.QueryRow("SELECT granted_bytes FROM quota_allocations").Scan(&granted); e != nil || granted != 100 {
		t.Fatal("partial failed commit", granted, e)
	}
	if e := s.transaction(ctx, func(tx *sql.Tx) error {
		for n := range MaxPeriodAllocations - 1 {
			if _, e := tx.Exec(`INSERT INTO quota_allocations(period_id,node_id,boot_id,identity_id,granted_bytes,sequence,valid_until,updated_at) VALUES(?,?,?,?,0,1,1,1)`, p.ID, "node-b", fmt.Sprintf("%032x", n), a.IdentityID); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReserveQuota(ctx, QuotaRequest{p.ID, "node-b", bootB, 0, 1}); !errors.Is(e, ErrConflict) {
		t.Fatal("unbounded process inventory", e)
	}
	for _, bad := range []QuotaRequest{{p.ID, "node-a", "invalid", 0, 1}, {p.ID, "node-a", bootA, -1, 1}, {p.ID, "node-a", bootA, 2, 1}} {
		if _, e := s.ReserveQuota(ctx, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal(bad, e)
		}
	}
}
