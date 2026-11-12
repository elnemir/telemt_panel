package platform

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

// QuotaLeaseSeconds bounds availability after the control plane disappears.
// It does not reclaim any allocation: a node may have spent its entire grant.
const QuotaLeaseSeconds int64 = 60
const MaxPeriodAllocations = 4096

type QuotaRequest struct {
	PeriodID        string
	NodeID          string
	BootID          string
	ExpectedGranted int64
	TargetGranted   int64
}

type QuotaGrant struct {
	Refillable       bool   `json:"refillable"`
	PeriodID         string `json:"period_id"`
	NodeID           string `json:"node_id"`
	BootID           string `json:"boot_id"`
	IdentityID       string `json:"identity_id"`
	Username         string `json:"username"`
	IdentityRevision int64  `json:"identity_revision,string"`
	SecretHash       string `json:"secret_hash"`
	Sequence         int64  `json:"sequence,string"`
	GrantedBytes     int64  `json:"granted_bytes,string"`
	ObservedBytes    int64  `json:"observed_bytes,string"`
	PeriodStartsAt   int64  `json:"period_starts_at"`
	PeriodEndsAt     int64  `json:"period_ends_at"`
	ValidUntil       int64  `json:"valid_until"`
}

type QuotaUsage struct {
	PeriodID      string `json:"period_id"`
	QuotaBytes    *int64 `json:"quota_bytes,string"`
	OverrunBytes  *int64 `json:"overrun_bytes,string"`
	GrantedBytes  int64  `json:"granted_bytes,string"`
	ObservedBytes int64  `json:"observed_bytes,string"`
	// Unknown includes unspent reservations and traffic not yet reported.
	UnknownBytes   int64  `json:"unknown_bytes,string"`
	Allocations    int    `json:"allocations"`
	LastReportedAt *int64 `json:"last_reported_at"`
}

func validBootID(id string) bool {
	if len(id) != 32 || strings.ToLower(id) != id {
		return false
	}
	_, e := hex.DecodeString(id)
	return e == nil
}

// ReserveQuota commits a cumulative grant before the caller can publish it.
// It is internal orchestration, not a customer-controlled allocation API.
func (s *Store) ReserveQuota(ctx context.Context, in QuotaRequest) (QuotaGrant, error) {
	var out QuotaGrant
	if in.PeriodID == "" || in.NodeID == "" || !validBootID(in.BootID) || in.ExpectedGranted < 0 || in.TargetGranted < in.ExpectedGranted {
		return out, ErrInvalid
	}
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		now := s.now().Unix()
		var termsJSON, policyJSON string
		out = QuotaGrant{PeriodID: in.PeriodID, NodeID: in.NodeID, BootID: in.BootID}
		// Membership, customer and entitlement are checked in the same transaction
		// as the reservation. A customer cannot spend a future paid period early.
		e := tx.QueryRowContext(ctx, `SELECT i.id,i.username,i.revision,i.secret_hash,i.policy,p.starts_at,p.ends_at,p.terms
   FROM paid_periods p JOIN subscriptions s ON s.id=p.subscription_id
   JOIN customers c ON c.id=s.customer_id JOIN identities i ON i.id=s.identity_id
   JOIN cluster_members m ON m.cluster_id=s.cluster_id AND m.node_id=?
   WHERE p.id=? AND s.enabled=1 AND c.enabled=1 AND i.revoked=0 AND i.cluster_id=s.cluster_id
   AND m.state='active' AND p.starts_at<=? AND p.ends_at>?`, in.NodeID, in.PeriodID, now, now).
			Scan(&out.IdentityID, &out.Username, &out.IdentityRevision, &out.SecretHash, &policyJSON, &out.PeriodStartsAt, &out.PeriodEndsAt, &termsJSON)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		var policy Policy
		if e = json.Unmarshal([]byte(policyJSON), &policy); e != nil {
			return e
		}
		if !policy.Enabled {
			return ErrConflict
		}
		terms, e := decodeTerms(termsJSON)
		if e != nil {
			return e
		}
		if _, e = cleanTerms(terms, 0); e != nil {
			return ErrSchema
		}
		// An identity with a different expiration is not a valid billing projection.
		if policy.ExpirationRFC3339 != "" {
			return ErrConflict
		}
		var identityID string
		e = tx.QueryRowContext(ctx, `SELECT identity_id,granted_bytes,observed_bytes FROM quota_allocations WHERE period_id=? AND node_id=? AND boot_id=?`, in.PeriodID, in.NodeID, in.BootID).Scan(&identityID, &out.GrantedBytes, &out.ObservedBytes)
		fresh := errors.Is(e, sql.ErrNoRows)
		if e != nil && !fresh {
			return e
		}
		if !fresh && identityID != out.IdentityID {
			return ErrConflict
		}
		if in.ExpectedGranted != out.GrantedBytes && in.TargetGranted != out.GrantedBytes {
			return ErrConflict
		}
		if in.TargetGranted < out.GrantedBytes {
			return ErrConflict
		}
		var total int64
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(granted_bytes),0),COUNT(*) FROM quota_allocations WHERE period_id=?`, in.PeriodID).Scan(&total, &count); e != nil {
			return e
		}
		if fresh && count >= MaxPeriodAllocations {
			return ErrConflict
		}
		cap := int64(math.MaxInt64)
		if terms.QuotaBytes != nil {
			cap = *terms.QuotaBytes + *terms.OverrunBytes
		}
		if total > cap {
			return ErrConflict
		}
		increase := min(in.TargetGranted-out.GrantedBytes, cap-total)
		out.GrantedBytes += increase
		out.Refillable = total+increase < cap
		// Sequence is per identity/node across ALL boots and periods, preventing a
		// delayed older grant from overwriting a newer period on a live process.
		var seq int64
		e = tx.QueryRowContext(ctx, `SELECT sequence FROM quota_sequences WHERE identity_id=? AND node_id=?`, out.IdentityID, in.NodeID).Scan(&seq)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if seq == math.MaxInt64 {
			return ErrConflict
		}
		out.Sequence = seq + 1
		out.ValidUntil = min(now+QuotaLeaseSeconds, out.PeriodEndsAt)
		if _, e = tx.ExecContext(ctx, `INSERT INTO quota_sequences(identity_id,node_id,sequence) VALUES(?,?,?) ON CONFLICT(identity_id,node_id) DO UPDATE SET sequence=excluded.sequence`, out.IdentityID, in.NodeID, out.Sequence); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO quota_allocations(period_id,node_id,boot_id,identity_id,granted_bytes,sequence,valid_until,updated_at)
   VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(period_id,node_id,boot_id) DO UPDATE SET granted_bytes=excluded.granted_bytes,sequence=excluded.sequence,valid_until=excluded.valid_until,updated_at=excluded.updated_at`, in.PeriodID, in.NodeID, in.BootID, out.IdentityID, out.GrantedBytes, out.Sequence, out.ValidUntil, now)
		return e
	})
	return out, e
}

// ObserveQuota records monotonic process-local usage. Even a final report does
// not release the unspent allocation: another delayed I/O may still commit.
func (s *Store) ObserveQuota(ctx context.Context, periodID, nodeID, bootID string, sequence, used int64) error {
	if !validBootID(bootID) || sequence <= 0 || used < 0 {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var granted, observed, currentSequence int64
		e := tx.QueryRowContext(ctx, `SELECT granted_bytes,observed_bytes,sequence FROM quota_allocations WHERE period_id=? AND node_id=? AND boot_id=?`, periodID, nodeID, bootID).Scan(&granted, &observed, &currentSequence)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if sequence > currentSequence || used > granted || used < observed {
			return ErrConflict
		}
		_, e = tx.ExecContext(ctx, `UPDATE quota_allocations SET observed_bytes=?,reported_at=? WHERE period_id=? AND node_id=? AND boot_id=?`, used, s.now().Unix(), periodID, nodeID, bootID)
		return e
	})
}

func (s *Store) CustomerQuotaUsage(ctx context.Context, customerID, subscriptionID, periodID string) (QuotaUsage, error) {
	out := QuotaUsage{PeriodID: periodID}
	// A single SQL snapshot owns both scope and totals; unknown objects are 404.
	var termsJSON string
	e := s.db.QueryRowContext(ctx, `SELECT p.terms,COALESCE(SUM(q.granted_bytes),0),COALESCE(SUM(q.observed_bytes),0),COUNT(q.boot_id),MAX(q.reported_at)
  FROM paid_periods p JOIN subscriptions s ON s.id=p.subscription_id LEFT JOIN quota_allocations q ON q.period_id=p.id
  WHERE p.id=? AND s.id=? AND s.customer_id=? GROUP BY p.id`, periodID, subscriptionID, customerID).
		Scan(&termsJSON, &out.GrantedBytes, &out.ObservedBytes, &out.Allocations, &out.LastReportedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if e != nil {
		return out, e
	}
	terms, e := decodeTerms(termsJSON)
	if e != nil {
		return out, e
	}
	out.QuotaBytes = terms.QuotaBytes
	out.OverrunBytes = terms.OverrunBytes
	out.UnknownBytes = out.GrantedBytes - out.ObservedBytes
	return out, nil
}
