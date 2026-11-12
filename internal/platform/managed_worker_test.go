//go:build !lite

package platform

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/amirotin/telemt_panel/internal/subpage"
	"github.com/amirotin/telemt_panel/internal/telemt"
)

type managedFixture struct {
	*nodeFixture
	states         map[string]telemt.ClusterQuotaStatus
	incarnation    int
	loseGrantReply bool
}

func newManagedFixture() *managedFixture {
	return &managedFixture{nodeFixture: newNodeFixture(), states: map[string]telemt.ClusterQuotaStatus{}}
}
func (n *managedFixture) reset(name string) {
	n.incarnation++
	user := n.users[name]
	secret, _ := subpage.ExtractSecret(user.Links)
	n.states[name] = telemt.ClusterQuotaStatus{Username: name, Required: true, BootID: fmt.Sprintf("%032x", n.incarnation), SecretHash: secretHash(secret), Enabled: user.Enabled}
}
func (n *managedFixture) CreateManagedUserWithRevision(ctx context.Context, r telemt.CreateUserRequest, rev string) (telemt.UserInfo, error) {
	out, e := n.nodeFixture.CreateUserWithRevision(ctx, r, rev)
	if e != nil {
		return out, e
	}
	yes := true
	u := n.users[r.Username]
	u.ClusterQuota = &yes
	n.users[r.Username] = u
	n.reset(r.Username)
	return out, nil
}
func (n *managedFixture) PatchUserWithRevision(ctx context.Context, name string, p map[string]any, rev string) (telemt.UserInfo, error) {
	old := n.users[name]
	secret, _ := subpage.ExtractSecret(old.Links)
	out, e := n.nodeFixture.PatchUserWithRevision(ctx, name, p, rev)
	if e != nil {
		return out, e
	}
	u := n.users[name]
	u.ClusterQuota = old.ClusterQuota
	n.users[name] = u
	next, _ := subpage.ExtractSecret(u.Links)
	if secret != next {
		n.reset(name)
	}
	return out, nil
}
func (n *managedFixture) ClusterQuotaWithRevision(_ context.Context, name string) (telemt.ClusterQuotaStatus, string, error) {
	s, ok := n.states[name]
	if !ok {
		return s, "", ErrNotFound
	}
	s.Enabled = n.users[name].Enabled
	return s, fmt.Sprint(n.revision), nil
}
func (n *managedFixture) ApplyClusterQuota(_ context.Context, name, hash, rev string, g telemt.ClusterQuotaGrant) (telemt.ClusterQuotaStatus, string, error) {
	s := n.states[name]
	if rev != fmt.Sprint(n.revision) || s.BootID != g.BootID || s.SecretHash != hash {
		return s, "", ErrConflict
	}
	if s.Grant != nil && s.Grant.Sequence >= g.Sequence && !reflect.DeepEqual(*s.Grant, g) {
		return s, "", ErrConflict
	}
	if s.Grant == nil || s.Grant.PeriodID != g.PeriodID {
		s.ChargedBytes = 0
	}
	s.Grant = &g
	s.AvailableBytes = g.GrantedBytes - s.ChargedBytes
	n.states[name] = s
	if n.loseGrantReply {
		n.loseGrantReply = false
		return s, "", errors.New("reply lost after grant commit")
	}
	return s, rev, nil
}
func managedWorkerFixture(t *testing.T) (*Store, *Worker, *managedFixture, Invoice, *int64) {
	t.Helper()
	s, p, now := billingFixture(t)
	ctx := context.Background()
	c, e := s.GetCluster(ctx, p.ClusterID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AddMember(ctx, c.ID, c.Revision, "node-a"); e != nil {
		t.Fatal(e)
	}
	invoice := quote(t, s, p, "", "managed-checkout-0001")
	if _, e = s.SettlePayment(ctx, "yoo", verified(invoice, "managed-payment", *now)); e != nil {
		t.Fatal(e)
	}
	node := newManagedFixture()
	worker := NewWorker(s, func(ctx context.Context, id string, fn func(context.Context, NodeClient) error) error {
		if id != "node-a" {
			return ErrNotFound
		}
		return fn(ctx, node)
	})
	return s, worker, node, invoice, now
}
func stepManaged(t *testing.T, w *Worker, now *int64) {
	t.Helper()
	*now += 12
	if _, e := w.ProcessNext(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestManagedProvisioningNeverEnablesBeforeConfirmedGrant(t *testing.T) {
	s, w, n, i, now := managedWorkerFixture(t)
	ctx := context.Background()
	stepManaged(t, w, now)
	sub, e := s.CustomerSubscription(ctx, "customer-a", i.SubscriptionID)
	if e != nil {
		t.Fatal(e)
	}
	id, e := s.GetIdentity(ctx, sub.IdentityID)
	if e != nil {
		t.Fatal(e)
	}
	if n.users[id.Username].Enabled || n.states[id.Username].Grant != nil {
		t.Fatal("created enabled or funded before gate readback")
	}
	n.loseGrantReply = true
	stepManaged(t, w, now)
	if n.users[id.Username].Enabled {
		t.Fatal("enabled after unconfirmed grant")
	}
	period, e := s.CurrentPaidPeriod(ctx, "customer-a", sub.ID)
	if e != nil {
		t.Fatal(e)
	}
	before, e := s.CustomerQuotaUsage(ctx, "customer-a", sub.ID, period.ID)
	if e != nil || before.GrantedBytes != 1<<20 {
		t.Fatal(before, e)
	}
	stepManaged(t, w, now)
	after, e := s.CustomerQuotaUsage(ctx, "customer-a", sub.ID, period.ID)
	if e != nil || after.GrantedBytes != before.GrantedBytes || !n.users[id.Username].Enabled {
		t.Fatal(after, e)
	}
	state := n.states[id.Username]
	state.ChargedBytes = 1 << 20
	state.AvailableBytes = 0
	n.states[id.Username] = state
	stepManaged(t, w, now)
	usage, e := s.CustomerQuotaUsage(ctx, "customer-a", sub.ID, period.ID)
	if e != nil || usage.ObservedBytes != 1<<20 || usage.GrantedBytes != 2<<20 {
		t.Fatal(usage, e)
	}
	// A restarted accounting incarnation receives new reserved credit; the old
	// allocation and its reported/unknown usage remain in the period total.
	n.reset(id.Username)
	stepManaged(t, w, now)
	usage, e = s.CustomerQuotaUsage(ctx, "customer-a", sub.ID, period.ID)
	if e != nil || usage.GrantedBytes != 3<<20 || usage.Allocations != 2 {
		t.Fatal(usage, e)
	}
	*now = period.EndsAt
	stepManaged(t, w, now)
	if n.users[id.Username].Enabled {
		t.Fatal("expired service still enabled")
	}
	if _, e = s.ReserveQuota(ctx, QuotaRequest{period.ID, "node-a", n.states[id.Username].BootID, 1 << 20, 2 << 20}); !errors.Is(e, ErrNotFound) {
		t.Fatal("expired allocation", e)
	}
}
func TestManagedUnsupportedNodeStaysDisabled(t *testing.T) {
	s, w, n, i, now := managedWorkerFixture(t)
	ctx := context.Background()
	if _, e := s.ReconcileEntitlement(ctx); e != nil {
		t.Fatal(e)
	}
	sub, _ := s.CustomerSubscription(ctx, "customer-a", i.SubscriptionID)
	id, _ := s.GetIdentity(ctx, sub.IdentityID)
	// An old client implementation has no budget methods. It must not fall back
	// to ordinary synchronization and create an enabled user.
	old := NewWorker(s, func(ctx context.Context, _ string, fn func(context.Context, NodeClient) error) error {
		return fn(ctx, n.nodeFixture)
	})
	stepManaged(t, old, now)
	if _, exists := n.users[id.Username]; exists {
		t.Fatal("legacy fallback created paid user")
	}
	*now += 300
	stepManaged(t, w, now)
	u := n.users[id.Username]
	no := false
	u.ClusterQuota = &no
	n.users[id.Username] = u
	stepManaged(t, w, now)
	if n.users[id.Username].Enabled {
		t.Fatal("ignored managed flag enabled")
	}
	jobs, e := s.IdentityJobs(ctx, id.ID)
	if e != nil || jobs[0].LastCode != "managed_quota_unsupported" {
		t.Fatal(jobs, e)
	}
	if e = s.UpdateIdentity(ctx, id.ID, id.Revision, id.Policy, false, false); !errors.Is(e, ErrConflict) {
		t.Fatal("billing policy editable outside paid period", e)
	}
}
