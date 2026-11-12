//go:build !lite

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amirotin/telemt_panel/internal/telemt"
)

type nodeFixture struct {
	users            map[string]telemt.UserPolicySnapshot
	revision, writes int
	loseResponse     bool
	beforeWrite      func()
	unavailable      bool
}

func newNodeFixture() *nodeFixture {
	return &nodeFixture{users: map[string]telemt.UserPolicySnapshot{"bootstrap": {UserInfo: telemt.UserInfo{Username: "bootstrap", Enabled: true}, Limits: &telemt.UserLimitState{Desired: telemt.UserLimitSnapshot{MaxTCPConns: telemt.InheritedUserLimit{Origin: "unlimited"}, MaxUniqueIPs: telemt.InheritedUserLimit{Origin: "unlimited"}}}}}, revision: 1}
}
func (n *nodeFixture) UserPolicySnapshotsWithRevision(context.Context) ([]telemt.UserPolicySnapshot, string, error) {
	if n.unavailable {
		return nil, "", errors.New("offline")
	}
	users := []telemt.UserPolicySnapshot{}
	for _, u := range n.users {
		users = append(users, u)
	}
	return users, fmt.Sprint(n.revision), nil
}
func (n *nodeFixture) CreateUserWithRevision(ctx context.Context, r telemt.CreateUserRequest, rev string) (telemt.UserInfo, error) {
	if _, exists := n.users[r.Username]; exists {
		return telemt.UserInfo{}, ErrConflict
	}
	p := Policy{Enabled: r.Enabled == nil || *r.Enabled, MaxTCPConns: r.MaxTCPConns, MaxUniqueIPs: r.MaxUniqueIPs, RateLimitUpBps: r.RateLimitUpBps, RateLimitDownBps: r.RateLimitDownBps, ExpirationRFC3339: r.ExpirationRFC3339, UserAdTag: r.UserAdTag}
	return n.PatchUserWithRevision(ctx, r.Username, policyPatch(p, r.Secret), rev)
}
func (n *nodeFixture) PatchUserWithRevision(_ context.Context, name string, patch map[string]any, rev string) (telemt.UserInfo, error) {
	if n.beforeWrite != nil {
		fn := n.beforeWrite
		n.beforeWrite = nil
		fn()
	}
	if rev != fmt.Sprint(n.revision) {
		return telemt.UserInfo{}, ErrConflict
	}
	raw, _ := json.Marshal(patch)
	var fields struct {
		Secret       string  `json:"secret"`
		Enabled      bool    `json:"enabled"`
		MaxTCPConns  *uint64 `json:"max_tcp_conns"`
		MaxUniqueIPs *uint64 `json:"max_unique_ips"`
		RateUp       *uint64 `json:"rate_limit_up_bps"`
		RateDown     *uint64 `json:"rate_limit_down_bps"`
		Expiration   string  `json:"expiration_rfc3339"`
		AdTag        string  `json:"user_ad_tag"`
	}
	if e := json.Unmarshal(raw, &fields); e != nil {
		return telemt.UserInfo{}, e
	}
	u := n.users[name]
	u.Username = name
	u.Enabled = fields.Enabled
	u.InRuntime = fields.Enabled
	u.RateLimitUpBps = fields.RateUp
	u.RateLimitDownBps = fields.RateDown
	u.ExpirationRFC3339 = fields.Expiration
	u.UserAdTag = fields.AdTag
	global := uint64(999)
	u.MaxTCPConns = &global
	u.MaxUniqueIPs = &global
	u.Limits = &telemt.UserLimitState{Desired: telemt.UserLimitSnapshot{MaxTCPConns: telemt.InheritedUserLimit{ConfiguredOverride: fields.MaxTCPConns, GlobalValue: 999, Origin: "global_default"}, MaxUniqueIPs: telemt.InheritedUserLimit{ConfiguredOverride: fields.MaxUniqueIPs, GlobalValue: 999, Origin: "global_default"}}}
	u.Links = telemt.UserLinks{Classic: []string{"tg://proxy?server=example.org&port=443&secret=" + fields.Secret}}
	n.users[name] = u
	n.writes++
	n.revision++
	if n.loseResponse {
		n.loseResponse = false
		return telemt.UserInfo{}, errors.New("response lost after commit")
	}
	return u.UserInfo, nil
}
func identityFixture(t *testing.T) (*Store, Cluster, Identity, *nodeFixture, *Worker, *int64) {
	t.Helper()
	s, _, _ := testStore(t)
	now := int64(2000000000)
	s.now = func() time.Time { return time.Unix(now, 0) }
	ctx := context.Background()
	c, e := s.CreateCluster(ctx, "Cluster")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AddMember(ctx, c.ID, c.Revision, "n1"); e != nil {
		t.Fatal(e)
	}
	c, e = s.GetCluster(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	i, e := s.CreateIdentity(ctx, c.ID, c.Revision, IdentityInput{Username: "alice", Policy: Policy{Enabled: true}})
	if e != nil {
		t.Fatal(e)
	}
	n := newNodeFixture()
	w := NewWorker(s, func(ctx context.Context, id string, run func(context.Context, NodeClient) error) error {
		if id != "n1" {
			t.Fatal("cross-node delivery", id)
		}
		return run(ctx, n)
	})
	return s, c, i, n, w, &now
}
func process(t *testing.T, w *Worker) {
	t.Helper()
	ran, e := w.ProcessNext(context.Background())
	if !ran || e != nil {
		t.Fatal(ran, e)
	}
}
func job(t *testing.T, s *Store, id string) Job {
	t.Helper()
	jobs, e := s.IdentityJobs(context.Background(), id)
	if e != nil || len(jobs) != 1 {
		t.Fatal(jobs, e)
	}
	return jobs[0]
}

func TestLostResponseReobservesWithoutDuplicatingWrite(t *testing.T) {
	s, _, i, n, w, now := identityFixture(t)
	n.loseResponse = true
	process(t, w)
	j := job(t, s, i.ID)
	if j.Status != "pending" || j.AppliedRevision != 0 {
		t.Fatal(j)
	}
	*now += 10
	process(t, w)
	j = job(t, s, i.ID)
	if j.Status != "applied" || j.AppliedRevision != 1 || n.writes != 1 {
		t.Fatal(j, n.writes)
	}
	// Effective defaults differ from desired nil, but do not trigger perpetual writes.
	*now += 61
	process(t, w)
	if n.writes != 1 {
		t.Fatal("adopted inherited limits", n.writes)
	}
}
func TestForeignUserIsNotOverwritten(t *testing.T) {
	s, _, i, n, w, _ := identityFixture(t)
	_, e := n.PatchUserWithRevision(context.Background(), "alice", policyPatch(Policy{Enabled: true}, strings.Repeat("f", 32)), "1")
	if e != nil {
		t.Fatal(e)
	}
	process(t, w)
	j := job(t, s, i.ID)
	if j.Status != "conflict" || j.LastCode != "foreign_user" || n.writes != 1 {
		t.Fatal(j, n.writes)
	}
}
func TestRotationSupersedingAcceptedWriteAndRevoke(t *testing.T) {
	s, _, i, n, w, now := identityFixture(t)
	ctx := context.Background()
	process(t, w)
	if e := s.UpdateIdentity(ctx, i.ID, 1, i.Policy, true, false); e != nil {
		t.Fatal(e)
	}
	n.beforeWrite = func() {
		if e := s.UpdateIdentity(ctx, i.ID, 2, i.Policy, true, false); e != nil {
			t.Fatal(e)
		}
	}
	ran, e := w.ProcessNext(ctx)
	if !ran || !errors.Is(e, ErrConflict) {
		t.Fatal("stale completion accepted", ran, e)
	}
	if j := job(t, s, i.ID); j.DesiredRevision != 3 || j.AppliedRevision != 1 {
		t.Fatal(j)
	}
	// The intermediate secret is recognized even though its completion was fenced.
	process(t, w)
	if j := job(t, s, i.ID); j.AppliedRevision != 3 || j.Status != "applied" {
		t.Fatal(j)
	}
	if e := s.UpdateIdentity(ctx, i.ID, 3, i.Policy, false, true); e != nil {
		t.Fatal(e)
	}
	process(t, w)
	if n.users["alice"].Enabled {
		t.Fatal("revoke left access enabled")
	}
	if j := job(t, s, i.ID); j.AppliedRevision != 4 || j.InRuntime {
		t.Fatal(j)
	}
	*now += 61
	process(t, w)
	if n.writes != 4 {
		t.Fatal("unexpected retry", n.writes)
	}
}
func TestRestartRecoversLeaseAndEncryptedIdentity(t *testing.T) {
	s, path, key := testStore(t)
	ctx := context.Background()
	now := int64(2000000000)
	s.now = func() time.Time { return time.Unix(now, 0) }
	c, e := s.CreateCluster(ctx, "C")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AddMember(ctx, c.ID, 1, "node"); e != nil {
		t.Fatal(e)
	}
	i, e := s.CreateIdentity(ctx, c.ID, 2, IdentityInput{Username: "a", Policy: Policy{Enabled: true}})
	if e != nil {
		t.Fatal(e)
	}
	old, e := s.claimJob(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return time.Unix(now, 0) }
	if _, e = reopened.claimJob(ctx); !errors.Is(e, ErrNotFound) {
		t.Fatal("stole live lease", e)
	}
	now += 61
	next, e := reopened.claimJob(ctx)
	if e != nil || next.secret != old.secret {
		t.Fatal(e)
	}
	if e = reopened.finishJob(ctx, old, outcome{status: "applied"}); !errors.Is(e, ErrConflict) {
		t.Fatal("old lease completed", e)
	}
	if e = reopened.finishJob(ctx, next, outcome{status: "pending", code: "restart"}); e != nil {
		t.Fatal(e)
	}
	if j := job(t, reopened, i.ID); j.AppliedRevision != 0 {
		t.Fatal(j)
	}
}
func TestConcurrentClaimsHaveOneOwner(t *testing.T) {
	s, _, _, _, _, _ := identityFixture(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			_, e := s.claimJob(context.Background())
			if e == nil {
				wins.Add(1)
			} else if !errors.Is(e, ErrNotFound) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
}
func TestMembershipRemovalFencesRunningEnable(t *testing.T) {
	s, c, i, n, w, _ := identityFixture(t)
	ctx := context.Background()
	n.beforeWrite = func() {
		if e := s.RemoveMember(ctx, c.ID, c.Revision, "n1"); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := w.ProcessNext(ctx); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	process(t, w)
	if n.users["alice"].Enabled {
		t.Fatal("removing node remains enabled")
	}
	members, e := s.ListMembers(ctx, c.ID)
	if e != nil || len(members) != 1 || members[0].State != "removing" {
		t.Fatal(members, e)
	}
	if j := job(t, s, i.ID); j.Status != "applied" {
		t.Fatal(j)
	}
	// Membership remains reserved pending the explicit detach barrier.
	if e = s.AddMember(ctx, c.ID, c.Revision+1, "n1"); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
}

func TestDetachRetirementCredentialFencesUnknownEarlierCreate(t *testing.T) {
	s, c, i, n, w, _ := identityFixture(t)
	ctx := context.Background()
	old, e := s.claimJob(ctx)
	if e != nil {
		t.Fatal(e)
	}
	// This request may already be in transit but has not committed remotely.
	staleRevision := fmt.Sprint(n.revision)
	if e = s.RemoveMember(ctx, c.ID, c.Revision, "n1"); e != nil {
		t.Fatal(e)
	}
	process(t, w)
	if n.users[i.Username].Enabled {
		t.Fatal("retirement tombstone enabled")
	}
	if _, e = n.CreateUserWithRevision(ctx, telemt.CreateUserRequest{Username: i.Username, Secret: old.secret}, staleRevision); e == nil {
		t.Fatal("late create accepted")
	}
	if _, e = n.PatchUserWithRevision(ctx, i.Username, policyPatch(i.Policy, old.secret), staleRevision); e == nil {
		t.Fatal("late enable accepted")
	}
	if ran, e := w.ProcessNext(ctx); ran || e != nil {
		t.Fatal(ran, e)
	}
	members, e := s.ListMembers(ctx, c.ID)
	if e != nil || len(members) != 0 {
		t.Fatal("membership not released", members, e)
	}
	if owned, e := s.ManagedUser(ctx, "n1", i.Username); e != nil || owned {
		t.Fatal(owned, e)
	}
}
func TestUnavailableNodeRetainsRemovalOwnership(t *testing.T) {
	s, c, i, n, w, _ := identityFixture(t)
	ctx := context.Background()
	process(t, w)
	if e := s.RemoveMember(ctx, c.ID, c.Revision, "n1"); e != nil {
		t.Fatal(e)
	}
	n.unavailable = true
	process(t, w)
	if j := job(t, s, i.ID); j.Status != "unavailable" {
		t.Fatal(j)
	}
	if _, e := w.ProcessNext(ctx); e != nil {
		t.Fatal(e)
	}
	members, e := s.ListMembers(ctx, c.ID)
	if e != nil || len(members) != 1 || members[0].State != "removing" {
		t.Fatal(members, e)
	}
}

func TestLegacyPolicySnapshotDoesNotCreateUnverifiableAccount(t *testing.T) {
	s, _, i, n, w, _ := identityFixture(t)
	n.users["bootstrap"] = telemt.UserPolicySnapshot{UserInfo: telemt.UserInfo{Username: "bootstrap"}}
	process(t, w)
	if j := job(t, s, i.ID); j.Status != "conflict" || j.LastCode != "policy_snapshot_unsupported" || n.writes != 0 {
		t.Fatal(j, n.writes)
	}
}
