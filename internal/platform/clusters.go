package platform

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type scanner interface{ Scan(...any) error }

func scanCluster(row scanner) (Cluster, error) {
	var c Cluster
	e := row.Scan(&c.ID, &c.Name, &c.Revision, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		e = ErrNotFound
	}
	return c, e
}

const clusterColumns = "id,name,revision,created_at,updated_at"

func (s *Store) ListClusters(ctx context.Context) ([]Cluster, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT "+clusterColumns+" FROM clusters ORDER BY created_at,id LIMIT ?", MaxClusters)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Cluster{}
	for rows.Next() {
		c, e := scanCluster(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) GetCluster(ctx context.Context, id string) (Cluster, error) {
	return scanCluster(s.db.QueryRowContext(ctx, "SELECT "+clusterColumns+" FROM clusters WHERE id=?", id))
}
func (s *Store) CreateCluster(ctx context.Context, name string) (Cluster, error) {
	name, e := cleanName(name)
	if e != nil {
		return Cluster{}, e
	}
	now := s.now().Unix()
	c := Cluster{ID: newID("c_"), Name: name, Revision: 1, CreatedAt: now, UpdatedAt: now}
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM clusters").Scan(&count); e != nil {
			return e
		}
		if count >= MaxClusters {
			return ErrConflict
		}
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM clusters WHERE name_key=?", strings.ToLower(name)).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			return ErrConflict
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO clusters(id,name,name_key,revision,created_at,updated_at) VALUES(?,?,?,1,?,?)", c.ID, c.Name, strings.ToLower(name), now, now); e != nil {
			return e
		}
		return auditTx(ctx, tx, now, "cluster.create", c.ID, 1)
	})
	return c, e
}
func checkCluster(ctx context.Context, tx *sql.Tx, id string, revision int64) (Cluster, error) {
	c, e := scanCluster(tx.QueryRowContext(ctx, "SELECT "+clusterColumns+" FROM clusters WHERE id=?", id))
	if e != nil {
		return c, e
	}
	if revision <= 0 || c.Revision != revision {
		return c, ErrConflict
	}
	return c, nil
}
func bumpCluster(ctx context.Context, tx *sql.Tx, id string, now int64) error {
	_, e := tx.ExecContext(ctx, "UPDATE clusters SET revision=revision+1,updated_at=? WHERE id=?", now, id)
	return e
}
func auditTx(ctx context.Context, tx *sql.Tx, now int64, action, id string, revision int64) error {
	_, e := tx.ExecContext(ctx, "INSERT INTO platform_audit(created_at,action,subject,revision) VALUES(?,?,?,?)", now, action, id, revision)
	return e
}
func (s *Store) RenameCluster(ctx context.Context, id string, revision int64, name string) error {
	name, e := cleanName(name)
	if e != nil {
		return e
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := checkCluster(ctx, tx, id, revision); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM clusters WHERE name_key=? AND id<>?", strings.ToLower(name), id).Scan(&count); e != nil {
			return e
		}
		if count > 0 {
			return ErrConflict
		}
		if _, e := tx.ExecContext(ctx, "UPDATE clusters SET name=?,name_key=?,revision=revision+1,updated_at=? WHERE id=?", name, strings.ToLower(name), s.now().Unix(), id); e != nil {
			return e
		}
		return auditTx(ctx, tx, s.now().Unix(), "cluster.rename", id, revision+1)
	})
}
func (s *Store) ListMembers(ctx context.Context, clusterID string) ([]Member, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT node_id,cluster_id,state,weight,drain FROM cluster_members WHERE cluster_id=? ORDER BY node_id", clusterID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if e := rows.Scan(&m.NodeID, &m.ClusterID, &m.State, &m.Weight, &m.Drain); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) NodeCluster(ctx context.Context, nodeID string) (string, error) {
	var id string
	e := s.db.QueryRowContext(ctx, "SELECT cluster_id FROM cluster_members WHERE node_id=?", nodeID).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	return id, e
}
func (s *Store) AddMember(ctx context.Context, clusterID string, revision int64, nodeID string) error {
	if nodeID == "" || len(nodeID) > 128 {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := checkCluster(ctx, tx, clusterID, revision); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM cluster_members WHERE node_id=? OR cluster_id=?", nodeID, clusterID).Scan(&count); e != nil {
			return e
		}
		if count >= MaxMembers {
			return ErrConflict
		}
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM cluster_members WHERE node_id=?", nodeID).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			return ErrConflict
		}
		now := s.now().Unix()
		if _, e := tx.ExecContext(ctx, "INSERT INTO cluster_members(node_id,cluster_id,state,created_at) VALUES(?,?,'active',?)", nodeID, clusterID, now); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO identity_jobs(identity_id,node_id,desired_revision,status,next_attempt,updated_at)
		 SELECT id,?,revision,'pending',?,? FROM identities WHERE cluster_id=? AND revoked=0`, nodeID, now, now, clusterID); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO cluster_config_jobs(node_id,desired_revision,next_attempt)
			SELECT ?,revision,? FROM cluster_config_templates WHERE cluster_id=?`, nodeID, now, clusterID); e != nil {
			return e
		}
		if e := bumpCluster(ctx, tx, clusterID, now); e != nil {
			return e
		}
		return auditTx(ctx, tx, now, "cluster.member.add", nodeID, revision+1)
	})
}

// Removal first makes every owned account unavailable on this node. The worker
// removes membership only after all revocations have been confirmed.
func (s *Store) RemoveMember(ctx context.Context, clusterID string, revision int64, nodeID string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := checkCluster(ctx, tx, clusterID, revision); e != nil {
			return e
		}
		now := s.now().Unix()
		r, e := tx.ExecContext(ctx, "UPDATE cluster_members SET state='removing',drain=1 WHERE cluster_id=? AND node_id=? AND state='active'", clusterID, nodeID)
		if e != nil {
			return e
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return ErrConflict
		}

		// A unique retirement credential forces a source-revision change, even
		// when the account is already disabled. This fences older unknown
		// writes before ownership can be released.
		rows, e := tx.QueryContext(ctx, "SELECT identity_id FROM identity_jobs WHERE node_id=?", nodeID)
		if e != nil {
			return e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, id := range ids {
			secret := randomSecret()
			hash := secretHash(secret)
			if _, e = tx.ExecContext(ctx, "UPDATE identity_jobs SET retirement_cipher=?,retirement_hash=?,status='pending',lease_token='',lease_until=0,next_attempt=?,updated_at=? WHERE identity_id=? AND node_id=?", s.encrypt(clusterID+":"+id+":"+nodeID, secret), hash, now, now, id, nodeID); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO identity_credentials(identity_id,secret_hash,revision) SELECT id,?,revision FROM identities WHERE id=?", hash, id); e != nil {
				return e
			}
		}

		if e = bumpCluster(ctx, tx, clusterID, now); e != nil {
			return e
		}
		return auditTx(ctx, tx, now, "cluster.member.remove_requested", nodeID, revision+1)
	})
}
