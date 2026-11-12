package platform

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/amirotin/telemt_panel/internal/subpage"
	"github.com/amirotin/telemt_panel/internal/telemt"
)

type NodeClient interface {
	UserPolicySnapshotsWithRevision(context.Context) ([]telemt.UserPolicySnapshot, string, error)
	CreateUserWithRevision(context.Context, telemt.CreateUserRequest, string) (telemt.UserInfo, error)
	PatchUserWithRevision(context.Context, string, map[string]any, string) (telemt.UserInfo, error)
}
type WithNode func(context.Context, string, func(context.Context, NodeClient) error) error

// One worker belongs to one central panel process. Serial execution bounds load
// and keeps a newer job from racing a still-running callback for the same node.
type Worker struct {
	store    *Store
	withNode WithNode
	mu       sync.Mutex
}

func NewWorker(store *Store, withNode WithNode) *Worker {
	return &Worker{store: store, withNode: withNode}
}
func (w *Worker) Run(ctx context.Context, onError func(error)) {
	timer := time.NewTicker(workerPoll)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		ran, e := w.ProcessNext(ctx)
		if e != nil && !errors.Is(e, ErrConflict) && !errors.Is(e, context.Canceled) && onError != nil {
			onError(e)
		}
		if ran && e == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (w *Worker) ProcessNext(ctx context.Context) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Bounded entitlement projection also runs when no node jobs are due.
	for range 8 {
		ran, e := w.store.ReconcileEntitlement(ctx)
		if e != nil {
			return false, e
		}
		if !ran {
			break
		}
	}
	if e := w.store.finalizeRemovals(ctx); e != nil {
		return false, e
	}
	work, e := w.store.claimJob(ctx)
	if errors.Is(e, ErrNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	operation, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := outcome{status: "unavailable", code: "node_unavailable"}
	e = w.withNode(operation, work.job.NodeID, func(ctx context.Context, c NodeClient) error {
		result = w.reconcile(ctx, c, work)
		return nil
	})
	if e != nil {
		result = outcome{status: "unavailable", code: "node_unavailable"}
	}
	// Even on shutdown, persist a bounded outcome. If this fails, the lease
	// expires durably and the next process re-observes remote state.
	finish, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelFinish()
	return true, w.store.finishJob(finish, work, result)
}
func findSnapshot(users []telemt.UserPolicySnapshot, name string) (telemt.UserPolicySnapshot, bool) {
	for _, u := range users {
		if u.Username == name {
			return u, true
		}
	}
	return telemt.UserPolicySnapshot{}, false
}
func (w *Worker) reconcile(ctx context.Context, c NodeClient, work workItem) outcome {
	unknown := outcome{status: "unavailable", code: "readback_unavailable"}
	users, revision, e := c.UserPolicySnapshotsWithRevision(ctx)
	if e != nil {
		return unknown
	}
	if revision == "" {
		return outcome{status: "conflict", code: "revision_unavailable"}
	}

	// Refuse a legacy/partial response before creating an account whose
	// configured overrides could not subsequently be verified.
	if len(users) == 0 {
		return outcome{status: "conflict", code: "policy_snapshot_unsupported"}
	}
	for _, profile := range users {
		if !hasPolicySnapshot(profile) {
			return outcome{status: "conflict", code: "policy_snapshot_unsupported"}
		}
	}
	if work.managed {
		return w.reconcileManaged(ctx, c, work, users, revision)
	}
	u, exists := findSnapshot(users, work.identity.Username)
	p := work.identity.Policy
	if work.removing || work.identity.Revoked {
		p.Enabled = false
	}
	// A disabled tombstone is still created when absent. Its credential
	// revision fences a formerly unknown create before membership release.
	if exists {
		secret, ok := subpage.ExtractSecret(u.Links)
		if !ok {
			return outcome{status: "conflict", code: "secret_unavailable"}
		}
		owned, e := w.store.ownsCredential(ctx, work.identity.ID, secretHash(secret))
		if e != nil {
			return unknown
		}
		if !owned {
			return outcome{status: "conflict", code: "foreign_user"}
		}
		if !hasPolicySnapshot(u) {
			return outcome{status: "conflict", code: "policy_snapshot_unsupported"}
		}
		if secretHash(secret) == secretHash(work.secret) && matchesPolicy(u, p) {
			return outcome{status: "applied", code: "persisted", inRuntime: u.InRuntime}
		}
	}
	current, e := w.store.workCurrent(ctx, work)
	if e != nil {
		return unknown
	}
	if !current {
		return outcome{status: "pending", code: "superseded"}
	}
	if exists {
		_, e = c.PatchUserWithRevision(ctx, work.identity.Username, policyPatch(p, work.secret), revision)
	} else {
		_, e = c.CreateUserWithRevision(ctx, telemt.CreateUserRequest{Username: work.identity.Username, Secret: work.secret, Enabled: &p.Enabled, MaxTCPConns: p.MaxTCPConns, MaxUniqueIPs: p.MaxUniqueIPs, RateLimitUpBps: p.RateLimitUpBps, RateLimitDownBps: p.RateLimitDownBps, ExpirationRFC3339: p.ExpirationRFC3339, UserAdTag: p.UserAdTag}, revision)
	}
	// A failed write can have committed remotely. Never repeat it without a new
	// observation, and never expose upstream bodies (which may contain secrets).
	if e != nil {
		return outcome{status: "pending", code: "write_unconfirmed"}
	}
	users, _, e = c.UserPolicySnapshotsWithRevision(ctx)
	if e != nil {
		return unknown
	}
	u, exists = findSnapshot(users, work.identity.Username)
	if !exists {
		return outcome{status: "pending", code: "readback_mismatch"}
	}
	secret, ok := subpage.ExtractSecret(u.Links)
	if !ok || secretHash(secret) != secretHash(work.secret) || !matchesPolicy(u, p) {
		return outcome{status: "pending", code: "readback_mismatch"}
	}
	return outcome{status: "applied", code: "persisted", inRuntime: u.InRuntime}
}
func matchesPolicy(u telemt.UserPolicySnapshot, p Policy) bool {
	if !hasPolicySnapshot(u) {
		return false
	}
	observed := Policy{Enabled: u.Enabled, MaxTCPConns: u.Limits.Desired.MaxTCPConns.ConfiguredOverride, MaxUniqueIPs: u.Limits.Desired.MaxUniqueIPs.ConfiguredOverride, RateLimitUpBps: u.RateLimitUpBps, RateLimitDownBps: u.RateLimitDownBps, ExpirationRFC3339: u.ExpirationRFC3339, UserAdTag: u.UserAdTag}
	observed, e := cleanPolicy(observed)
	return e == nil && reflect.DeepEqual(observed, p)
}
func policyPatch(p Policy, secret string) map[string]any {
	nullable := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	return map[string]any{"secret": secret, "enabled": p.Enabled, "max_tcp_conns": p.MaxTCPConns, "max_unique_ips": p.MaxUniqueIPs, "rate_limit_up_bps": p.RateLimitUpBps, "rate_limit_down_bps": p.RateLimitDownBps, "expiration_rfc3339": nullable(p.ExpirationRFC3339), "user_ad_tag": nullable(p.UserAdTag)}
}

func hasPolicySnapshot(u telemt.UserPolicySnapshot) bool {
	if u.Limits == nil {
		return false
	}
	valid := func(origin string) bool {
		return origin == "user_override" || origin == "global_default" || origin == "unlimited"
	}
	return valid(u.Limits.Desired.MaxTCPConns.Origin) && valid(u.Limits.Desired.MaxUniqueIPs.Origin)
}
