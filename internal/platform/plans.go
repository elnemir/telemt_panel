package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const planColumns = "p.id,p.cluster_id,p.name,p.enabled,p.revision,p.current_version,v.terms,p.created_at,p.updated_at"
const planFrom = " FROM plans p JOIN plan_versions v ON v.plan_id=p.id AND v.version=p.current_version"

func scanPlan(row scanner) (Plan, error) {
	var p Plan
	var terms string
	e := row.Scan(&p.ID, &p.ClusterID, &p.Name, &p.Enabled, &p.Revision, &p.Version, &terms, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if e != nil {
		return p, e
	}
	p.Terms, e = decodeTerms(terms)
	return p, e
}
func (s *Store) GetPlan(ctx context.Context, id string) (Plan, error) {
	return scanPlan(s.db.QueryRowContext(ctx, "SELECT "+planColumns+planFrom+" WHERE p.id=?", id))
}
func (s *Store) ListPlans(ctx context.Context, clusterID string, publicOnly bool) ([]Plan, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT "+planColumns+planFrom+" WHERE (?='' OR p.cluster_id=?) AND (?=0 OR p.enabled=1) ORDER BY p.created_at,p.id LIMIT 1000", clusterID, clusterID, publicOnly)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		p, e := scanPlan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) CreatePlan(ctx context.Context, clusterID, name string, enabled bool, terms PlanTerms) (Plan, error) {
	name, e := cleanName(name)
	if e != nil {
		return Plan{}, e
	}
	now := s.now().Unix()
	terms, e = cleanTerms(terms, now)
	if e != nil {
		return Plan{}, e
	}
	p := Plan{ID: newID("plan_"), ClusterID: clusterID, Name: name, Enabled: enabled, Revision: 1, Version: 1, Terms: terms, CreatedAt: now, UpdatedAt: now}
	raw, _ := json.Marshal(terms)
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := scanCluster(tx.QueryRowContext(ctx, "SELECT "+clusterColumns+" FROM clusters WHERE id=?", clusterID)); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM plans").Scan(&count); e != nil {
			return e
		}
		if count >= 1000 {
			return ErrConflict
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO plans(id,cluster_id,name,enabled,revision,current_version,created_at,updated_at) VALUES(?,?,?,?,1,1,?,?)", p.ID, clusterID, name, enabled, now, now); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO plan_versions(plan_id,version,terms,created_at) VALUES(?,1,?,?)", p.ID, string(raw), now); e != nil {
			return e
		}
		return auditTx(ctx, tx, now, "plan.create", p.ID, 1)
	})
	return p, e
}
func (s *Store) UpdatePlan(ctx context.Context, id string, revision int64, name string, enabled bool, terms PlanTerms) error {
	name, e := cleanName(name)
	if e != nil {
		return e
	}
	now := s.now().Unix()
	terms, e = cleanTerms(terms, now)
	if e != nil {
		return e
	}
	raw, _ := json.Marshal(terms)
	return s.transaction(ctx, func(tx *sql.Tx) error {
		p, e := scanPlan(tx.QueryRowContext(ctx, "SELECT "+planColumns+planFrom+" WHERE p.id=?", id))
		if e != nil {
			return e
		}
		if revision != p.Revision {
			return ErrConflict
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO plan_versions(plan_id,version,terms,created_at) VALUES(?,?,?,?)", id, p.Version+1, string(raw), now); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "UPDATE plans SET name=?,enabled=?,revision=revision+1,current_version=current_version+1,updated_at=? WHERE id=?", name, enabled, now, id); e != nil {
			return e
		}
		return auditTx(ctx, tx, now, "plan.version", id, revision+1)
	})
}
