package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/amirotin/telemt_panel/internal/ingress"
)

type Routing struct {
	Revision int64           `json:"revision"`
	Config   *ingress.Config `json:"config"`
}

func (s *Store) GetRouting(ctx context.Context, clusterID string) (Routing, error) {
	var out Routing
	var raw string
	e := s.db.QueryRowContext(ctx, "SELECT revision,routing FROM clusters WHERE id=?", clusterID).Scan(&out.Revision, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if e != nil {
		return out, e
	}
	if raw != "{}" {
		var cfg ingress.Config
		if json.Unmarshal([]byte(raw), &cfg) != nil {
			return out, ErrSchema
		}
		out.Config = &cfg
	}
	return out, nil
}
func (s *Store) PutRouting(ctx context.Context, clusterID string, revision int64, cfg ingress.Config) error {
	if cfg.Validate() != nil {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := checkCluster(ctx, tx, clusterID, revision); e != nil {
			return e
		}
		// The input is a complete routing inventory for active members. Membership
		// changes invalidate export until the operator reviews the new inventory.
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM cluster_members WHERE cluster_id=? AND state='active'", clusterID).Scan(&count); e != nil {
			return e
		}
		if count != len(cfg.Targets) {
			return ErrConflict
		}
		for _, t := range cfg.Targets {
			r, e := tx.ExecContext(ctx, "UPDATE cluster_members SET weight=?,drain=? WHERE cluster_id=? AND node_id=? AND state='active'", t.Weight, t.Drain, clusterID, t.NodeID)
			if e != nil {
				return e
			}
			if n, _ := r.RowsAffected(); n != 1 {
				return ErrConflict
			}
		}
		raw, e := json.Marshal(cfg)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE clusters SET routing=? WHERE id=?", string(raw), clusterID); e != nil {
			return e
		}
		now := s.now().Unix()
		if e = bumpCluster(ctx, tx, clusterID, now); e != nil {
			return e
		}
		return auditTx(ctx, tx, now, "cluster.routing.update", clusterID, revision+1)
	})
}

// ExportRouting returns an atomic inventory snapshot. It makes no claim that
// a saved policy was validated by HAProxy or applied on an ingress host.
func (s *Store) ExportRouting(ctx context.Context, clusterID string, revision int64) (string, error) {
	var rendered string
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := checkCluster(ctx, tx, clusterID, revision); e != nil {
			return e
		}
		var raw string
		if e := tx.QueryRowContext(ctx, "SELECT routing FROM clusters WHERE id=?", clusterID).Scan(&raw); e != nil {
			return e
		}
		var cfg ingress.Config
		if json.Unmarshal([]byte(raw), &cfg) != nil || cfg.Validate() != nil {
			return ErrConflict
		}
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM cluster_members WHERE cluster_id=? AND state='active'", clusterID).Scan(&count); e != nil {
			return e
		}
		if count != len(cfg.Targets) {
			return ErrConflict
		}
		for _, t := range cfg.Targets {
			var weight int
			var drain bool
			if e := tx.QueryRowContext(ctx, "SELECT weight,drain FROM cluster_members WHERE cluster_id=? AND node_id=? AND state='active'", clusterID, t.NodeID).Scan(&weight, &drain); e != nil {
				if errors.Is(e, sql.ErrNoRows) {
					return ErrConflict
				}
				return e
			}
			if weight != t.Weight || drain != t.Drain {
				return ErrConflict
			}
		}
		var e error
		rendered, e = cfg.Render()
		return e
	})
	return rendered, e
}
