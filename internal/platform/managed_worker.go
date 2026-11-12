package platform

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"reflect"

	"github.com/amirotin/telemt_panel/internal/subpage"
	"github.com/amirotin/telemt_panel/internal/telemt"
)

type managedNodeClient interface {
	NodeClient
	CreateManagedUserWithRevision(context.Context, telemt.CreateUserRequest, string) (telemt.UserInfo, error)
	ClusterQuotaWithRevision(context.Context, string) (telemt.ClusterQuotaStatus, string, error)
	ApplyClusterQuota(context.Context, string, string, string, telemt.ClusterQuotaGrant) (telemt.ClusterQuotaStatus, string, error)
}

func (w *Worker) reconcileManaged(ctx context.Context, client NodeClient, work workItem, users []telemt.UserPolicySnapshot, revision string) outcome {
	unavailable := outcome{status: "unavailable", code: "managed_quota_unavailable"}
	c, ok := client.(managedNodeClient)
	if !ok {
		return unavailable
	}
	user, exists := findSnapshot(users, work.identity.Username)
	p := work.identity.Policy
	p.Enabled = work.period != nil && !work.removing && !work.identity.Revoked && p.Enabled
	if work.period != nil && p.Enabled {
		p = work.period.Terms.NodePolicy
	}
	if exists {
		secret, ok := subpage.ExtractSecret(user.Links)
		if !ok {
			return outcome{status: "conflict", code: "secret_unavailable"}
		}
		owned, e := w.store.ownsCredential(ctx, work.identity.ID, secretHash(secret))
		if e != nil {
			return unavailable
		}
		if !owned {
			return outcome{status: "conflict", code: "foreign_user"}
		}
		if !hasPolicySnapshot(user) {
			return outcome{status: "conflict", code: "policy_snapshot_unsupported"}
		}
		// Correct credential/policy while disabled before any new budget can be used.
		if secretHash(secret) != secretHash(work.secret) {
			disabled := p
			disabled.Enabled = false
			return w.managedPatch(ctx, c, work, disabled, revision, false)
		}
	}
	current, e := w.store.workCurrent(ctx, work)
	if e != nil {
		return unavailable
	}
	if !current {
		return outcome{status: "pending", code: "superseded"}
	}
	if !exists {
		disabled := false
		_, e = c.CreateManagedUserWithRevision(ctx, telemt.CreateUserRequest{Username: work.identity.Username, Secret: work.secret, Enabled: &disabled, MaxTCPConns: p.MaxTCPConns, MaxUniqueIPs: p.MaxUniqueIPs, RateLimitUpBps: p.RateLimitUpBps, RateLimitDownBps: p.RateLimitDownBps, UserAdTag: p.UserAdTag}, revision)
		if e != nil {
			return outcome{status: "pending", code: "write_unconfirmed"}
		}
		return outcome{status: "pending", code: "managed_runtime_pending"}
	}
	if !p.Enabled {
		if matchesPolicy(user, p) {
			return outcome{status: "applied", code: "persisted", inRuntime: user.InRuntime}
		}
		return w.managedPatch(ctx, c, work, p, revision, true)
	}
	if user.ClusterQuota == nil || !*user.ClusterQuota {
		if user.Enabled {
			disabled := p
			disabled.Enabled = false
			return w.managedPatch(ctx, c, work, disabled, revision, false)
		}
		return outcome{status: "conflict", code: "managed_quota_unsupported"}
	}
	state, quotaRevision, e := c.ClusterQuotaWithRevision(ctx, work.identity.Username)
	if e != nil {
		return unavailable
	}
	if !state.Required || !validBootID(state.BootID) || state.Username != work.identity.Username || state.SecretHash != secretHash(work.secret) || quotaRevision == "" {
		return outcome{status: "conflict", code: "managed_quota_owner_mismatch"}
	}
	var used int64
	if state.Grant != nil {
		g := state.Grant
		if g.BootID != state.BootID || g.Sequence > math.MaxInt64 || state.ChargedBytes > math.MaxInt64 {
			return outcome{status: "conflict", code: "managed_quota_invalid_report"}
		}
		e = w.store.ObserveQuota(ctx, g.PeriodID, work.job.NodeID, state.BootID, int64(g.Sequence), int64(state.ChargedBytes))
		if e != nil {
			return outcome{status: "conflict", code: "managed_quota_unknown_grant"}
		}
		if g.PeriodID == work.period.ID {
			used = int64(state.ChargedBytes)
		}
	}
	var allocated int64
	e = w.store.db.QueryRowContext(ctx, "SELECT granted_bytes FROM quota_allocations WHERE period_id=? AND node_id=? AND boot_id=?", work.period.ID, work.job.NodeID, state.BootID).Scan(&allocated)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return unavailable
	}
	// A bounded initial window limits credit lost on crash. Refill remains a
	// cumulative CAS, including grants committed but never delivered.
	chunk := int64(1 << 20)
	if work.period.Terms.QuotaBytes != nil {
		var members int64
		if e = w.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM cluster_members WHERE cluster_id=? AND state='active'", work.identity.ClusterID).Scan(&members); e != nil || members <= 0 {
			return unavailable
		}
		chunk = min(chunk, max(int64(1), *work.period.Terms.QuotaBytes/members))
	}
	target := max(allocated, used+min(chunk, math.MaxInt64-used))
	current, e = w.store.workCurrent(ctx, work)
	if e != nil {
		return unavailable
	}
	if !current {
		return outcome{status: "pending", code: "superseded"}
	}
	grant, e := w.store.ReserveQuota(ctx, QuotaRequest{work.period.ID, work.job.NodeID, state.BootID, allocated, target})
	if e != nil {
		return outcome{status: "pending", code: "managed_quota_reservation_pending"}
	}
	// Leave five seconds for ordinary clock skew; never extend a period deadline.
	until := min(w.store.now().Unix()+QuotaLeaseSeconds-5, grant.PeriodEndsAt)
	wire := telemt.ClusterQuotaGrant{Refillable: grant.Refillable, BootID: grant.BootID, PeriodID: grant.PeriodID, Sequence: uint64(grant.Sequence), GrantedBytes: uint64(grant.GrantedBytes), PeriodStartsAt: grant.PeriodStartsAt, PeriodEndsAt: grant.PeriodEndsAt, ValidUntil: until}
	applied, newRevision, e := c.ApplyClusterQuota(ctx, work.identity.Username, grant.SecretHash, quotaRevision, wire)
	if e != nil {
		return outcome{status: "pending", code: "managed_quota_apply_unconfirmed"}
	}
	if !applied.Required || applied.BootID != grant.BootID || applied.SecretHash != grant.SecretHash || !reflect.DeepEqual(applied.Grant, &wire) {
		return outcome{status: "conflict", code: "managed_quota_readback_mismatch"}
	}
	if applied.ChargedBytes > math.MaxInt64 {
		return outcome{status: "conflict", code: "managed_quota_invalid_report"}
	}
	if e = w.store.ObserveQuota(ctx, grant.PeriodID, grant.NodeID, grant.BootID, grant.Sequence, int64(applied.ChargedBytes)); e != nil {
		return outcome{status: "conflict", code: "managed_quota_invalid_report"}
	}
	if grant.GrantedBytes <= int64(applied.ChargedBytes) {
		return outcome{status: "applied", code: "managed_quota_empty", inRuntime: user.InRuntime}
	}
	out := outcome{status: "applied", inRuntime: user.InRuntime, links: connectionLinks(user.Links, work.secret)}
	if !matchesPolicy(user, p) {
		out = w.managedPatch(ctx, c, work, p, newRevision, true)
	}
	if out.status == "applied" {
		out.code = "managed_quota_active"
		out.budgetPeriod = grant.PeriodID
		out.budgetUntil = until
	}
	return out
}
func (w *Worker) managedPatch(ctx context.Context, c NodeClient, work workItem, policy Policy, revision string, complete bool) outcome {
	current, e := w.store.workCurrent(ctx, work)
	if e != nil || !current {
		return outcome{status: "pending", code: "superseded"}
	}
	if _, e = c.PatchUserWithRevision(ctx, work.identity.Username, policyPatch(policy, work.secret), revision); e != nil {
		return outcome{status: "pending", code: "write_unconfirmed"}
	}
	users, _, e := c.UserPolicySnapshotsWithRevision(ctx)
	if e != nil {
		return outcome{status: "unavailable", code: "readback_unavailable"}
	}
	user, exists := findSnapshot(users, work.identity.Username)
	secret, ok := subpage.ExtractSecret(user.Links)
	if !exists || !ok || secretHash(secret) != secretHash(work.secret) || !matchesPolicy(user, policy) {
		return outcome{status: "pending", code: "readback_mismatch"}
	}
	if !complete {
		return outcome{status: "pending", code: "managed_runtime_pending"}
	}
	out := outcome{status: "applied", code: "persisted", inRuntime: user.InRuntime}
	if policy.Enabled {
		out.links = connectionLinks(user.Links, work.secret)
	}
	return out
}
