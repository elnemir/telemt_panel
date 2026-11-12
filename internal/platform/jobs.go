package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const jobColumns = "identity_id,node_id,desired_revision,applied_revision,status,attempts,next_attempt,last_code,in_runtime,updated_at,lease_token,applied_secret_hash"

func scanJob(row scanner) (Job, error) {
	var j Job
	e := row.Scan(&j.IdentityID, &j.NodeID, &j.DesiredRevision, &j.AppliedRevision, &j.Status, &j.Attempts, &j.NextAttempt, &j.LastCode, &j.InRuntime, &j.UpdatedAt, &j.leaseToken, &j.previousSecretHash)
	if errors.Is(e, sql.ErrNoRows) {
		e = ErrNotFound
	}
	return j, e
}
func (s *Store) IdentityJobs(ctx context.Context, id string) ([]Job, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT "+jobColumns+" FROM identity_jobs WHERE identity_id=? ORDER BY node_id", id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// A work item contains a single transactionally consistent desired snapshot.
// Plaintext is short lived, private to the worker, and never serialized.
type workItem struct {
	job      Job
	identity Identity
	secret   string
	removing bool
	managed  bool
	period   *PaidPeriod
}

func (s *Store) claimJob(ctx context.Context) (workItem, error) {
	var w workItem
	now := s.now().Unix()
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		j, e := scanJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM identity_jobs WHERE next_attempt<=? AND lease_until<=? ORDER BY next_attempt,identity_id,node_id LIMIT 1", now, now))
		if e != nil {
			return e
		}
		i, e := scanIdentity(tx.QueryRowContext(ctx, "SELECT "+identityColumns+" FROM identities WHERE id=?", j.IdentityID))
		if e != nil {
			return e
		}
		if i.Revision != j.DesiredRevision {
			return ErrSchema
		}
		var sealed []byte
		var state string
		if e = tx.QueryRowContext(ctx, "SELECT secret_cipher FROM identities WHERE id=?", i.ID).Scan(&sealed); e != nil {
			return e
		}
		secret, e := s.decrypt(i.ClusterID+":"+i.ID, sealed)
		if e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, "SELECT state FROM cluster_members WHERE node_id=? AND cluster_id=?", j.NodeID, i.ClusterID).Scan(&state); e != nil {
			return e
		}

		if state == "removing" {
			if e = tx.QueryRowContext(ctx, "SELECT retirement_cipher FROM identity_jobs WHERE identity_id=? AND node_id=?", i.ID, j.NodeID).Scan(&sealed); e != nil {
				return e
			}
			secret, e = s.decrypt(i.ClusterID+":"+i.ID+":"+j.NodeID, sealed)
			if e != nil {
				return e
			}
		}
		j.leaseToken = newID("job_")
		j.Status = "running"
		j.Attempts++
		if _, e = tx.ExecContext(ctx, "UPDATE identity_jobs SET status='running',attempts=?,lease_until=?,lease_token=?,updated_at=? WHERE identity_id=? AND node_id=?", j.Attempts, now+60, j.leaseToken, now, j.IdentityID, j.NodeID); e != nil {
			return e
		}
		w = workItem{job: j, identity: i, secret: secret, removing: state == "removing"}
		var subscriptionID string
		var enabled bool
		e = tx.QueryRowContext(ctx, "SELECT s.id,(s.enabled AND c.enabled) FROM subscriptions s JOIN customers c ON c.id=s.customer_id WHERE s.identity_id=?", i.ID).Scan(&subscriptionID, &enabled)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		w.managed = e == nil
		if w.managed && enabled && !i.Revoked && !w.removing {
			period, e := paidPeriodTx(ctx, tx, subscriptionID, now)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return e
			}
			if e == nil {
				w.period = &period
			}
		}
		return nil
	})
	return w, e
}
func (s *Store) workCurrent(ctx context.Context, w workItem) (bool, error) {
	var count int
	e := s.db.QueryRowContext(ctx, "SELECT count(*) FROM identity_jobs WHERE identity_id=? AND node_id=? AND desired_revision=? AND lease_token=? AND lease_until>?", w.job.IdentityID, w.job.NodeID, w.job.DesiredRevision, w.job.leaseToken, s.now().Unix()).Scan(&count)
	return count == 1, e
}

type outcome struct {
	status, code string
	inRuntime    bool
	links        []ConnectionLink
	budgetPeriod string
	budgetUntil  int64
}

func (s *Store) finishJob(ctx context.Context, w workItem, o outcome) error {
	switch o.status {
	case "applied", "conflict", "unavailable", "pending":
	default:
		return ErrInvalid
	}
	now := s.now().Unix()
	delay := int64(5) << min(w.job.Attempts-1, 6)
	delay = min(delay, 300)
	applied := w.job.AppliedRevision
	hash := w.job.previousSecretHash
	if o.status == "applied" {
		applied = w.job.DesiredRevision
		hash = secretHash(w.secret)
		delay = 60
		if w.managed && w.period != nil && !w.removing {
			delay = 2
		}
	}
	var sealed []byte
	if len(o.links) > 0 {
		raw, e := json.Marshal(o.links)
		if e != nil {
			return e
		}
		sealed = s.encrypt("links:"+w.identity.ID+":"+w.job.NodeID, string(raw))
	}
	// Conditional completion cannot publish a superseded revision or lease.
	r, e := s.db.ExecContext(ctx, `UPDATE identity_jobs SET status=?,last_code=?,in_runtime=?,applied_revision=?,applied_secret_hash=?,next_attempt=?,lease_until=0,lease_token='',updated_at=?,links_cipher=?,budget_period_id=?,budget_until=?
 WHERE identity_id=? AND node_id=? AND desired_revision=? AND lease_token=?`, o.status, o.code, o.inRuntime, applied, hash, now+delay, now, sealed, o.budgetPeriod, o.budgetUntil, w.job.IdentityID, w.job.NodeID, w.job.DesiredRevision, w.job.leaseToken)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// Retry schedules an observation; it never executes a mutation on the request
// goroutine or steals a live lease. Revisions fence stale UI actions.
func (s *Store) RetryIdentity(ctx context.Context, id string, revision int64) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		i, e := scanIdentity(tx.QueryRowContext(ctx, "SELECT "+identityColumns+" FROM identities WHERE id=?", id))
		if e != nil {
			return e
		}
		if i.Revision != revision {
			return ErrConflict
		}
		now := s.now().Unix()
		_, e = tx.ExecContext(ctx, "UPDATE identity_jobs SET next_attempt=?,attempts=0 WHERE identity_id=? AND lease_until<=?", now, id, now)
		return e
	})
}

// Retry delay and drift polling are operational scheduling, not tariff periods.
const workerPoll = time.Second

// Finalize only after readback of a unique per-node retirement credential.
// Unlike an unchanged disabled flag, this changes the remote source revision
// and fences writes carrying an older If-Match. The single process worker
// serializes observation and release; registry membership remains reserved on
// any conflict, timeout or superseding desired change.
func (s *Store) finalizeRemovals(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		rows, e := tx.QueryContext(ctx, `SELECT m.node_id,m.cluster_id FROM cluster_members m WHERE m.state='removing' AND NOT EXISTS
   (SELECT 1 FROM identity_jobs j WHERE j.node_id=m.node_id AND (j.status<>'applied' OR j.retirement_hash='' OR j.applied_secret_hash<>j.retirement_hash OR j.applied_revision<>j.desired_revision))
   AND NOT EXISTS (SELECT 1 FROM cluster_config_jobs j WHERE j.node_id=m.node_id AND j.lease_until>?)`, s.now().Unix())
		if e != nil {
			return e
		}
		type removal struct{ node, cluster string }
		list := []removal{}
		for rows.Next() {
			var r removal
			if e = rows.Scan(&r.node, &r.cluster); e != nil {
				rows.Close()
				return e
			}
			list = append(list, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, r := range list {
			if _, e = tx.ExecContext(ctx, "DELETE FROM identity_jobs WHERE node_id=?", r.node); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "DELETE FROM cluster_members WHERE node_id=?", r.node); e != nil {
				return e
			}
			if e = bumpCluster(ctx, tx, r.cluster, s.now().Unix()); e != nil {
				return e
			}
			if e = auditTx(ctx, tx, s.now().Unix(), "cluster.member.detached", r.node, 0); e != nil {
				return e
			}
		}
		return nil
	})
}
