package platform

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

const identityColumns = "id,cluster_id,username,policy,revision,revoked,created_at,updated_at,EXISTS(SELECT 1 FROM subscriptions WHERE subscriptions.identity_id=identities.id)"

func scanIdentity(row scanner) (Identity, error) {
	var i Identity
	var policy string
	e := row.Scan(&i.ID, &i.ClusterID, &i.Username, &policy, &i.Revision, &i.Revoked, &i.CreatedAt, &i.UpdatedAt, &i.BillingManaged)
	if errors.Is(e, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	if e != nil {
		return i, e
	}
	if json.Unmarshal([]byte(policy), &i.Policy) != nil {
		return i, ErrSchema
	}
	return i, nil
}
func (s *Store) GetIdentity(ctx context.Context, id string) (Identity, error) {
	return scanIdentity(s.db.QueryRowContext(ctx, "SELECT "+identityColumns+" FROM identities WHERE id=?", id))
}
func (s *Store) ListIdentities(ctx context.Context, clusterID string, offset, limit int) ([]Identity, error) {
	if offset < 0 || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	rows, e := s.db.QueryContext(ctx, "SELECT "+identityColumns+" FROM identities WHERE cluster_id=? ORDER BY username LIMIT ? OFFSET ?", clusterID, limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Identity{}
	for rows.Next() {
		i, e := scanIdentity(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
func (s *Store) RevealIdentitySecret(ctx context.Context, id string) (string, error) {
	var sealed []byte
	var clusterID string
	e := s.db.QueryRowContext(ctx, "SELECT cluster_id,secret_cipher FROM identities WHERE id=?", id).Scan(&clusterID, &sealed)
	if errors.Is(e, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if e != nil {
		return "", e
	}
	return s.decrypt(clusterID+":"+id, sealed)
}
func (s *Store) CreateIdentity(ctx context.Context, clusterID string, clusterRevision int64, in IdentityInput) (Identity, error) {
	if !usernamePattern.MatchString(in.Username) {
		return Identity{}, ErrInvalid
	}
	policy, e := cleanPolicy(in.Policy)
	if e != nil {
		return Identity{}, e
	}
	secret := strings.ToLower(in.Secret)
	if secret == "" {
		secret = randomSecret()
	}
	decoded, e := hex.DecodeString(secret)
	if e != nil || len(decoded) != 16 {
		return Identity{}, ErrInvalid
	}
	now := s.now().Unix()
	i := Identity{ID: newID("i_"), ClusterID: clusterID, Username: in.Username, Policy: policy, Revision: 1, CreatedAt: now, UpdatedAt: now}
	raw, _ := json.Marshal(policy)
	sealed := s.encrypt(clusterID+":"+i.ID, secret)
	e = s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := checkCluster(ctx, tx, clusterID, clusterRevision); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM identities WHERE cluster_id=?", clusterID).Scan(&count); e != nil {
			return e
		}
		if count >= MaxIdentities {
			return ErrConflict
		}
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM identities WHERE cluster_id=? AND (username=? OR secret_hash=?)", clusterID, in.Username, secretHash(secret)).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			return ErrConflict
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO identities(id,cluster_id,username,secret_cipher,secret_hash,policy,revision,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,1,?,?)`, i.ID, clusterID, in.Username, sealed, secretHash(secret), string(raw), now, now); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO identity_credentials(identity_id,secret_hash,revision) VALUES(?,?,1)", i.ID, secretHash(secret)); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO identity_jobs(identity_id,node_id,desired_revision,status,next_attempt,updated_at)
		 SELECT ?,node_id,1,'pending',?,? FROM cluster_members WHERE cluster_id=? AND state='active'`, i.ID, now, now, clusterID); e != nil {
			return e
		}
		return auditTx(ctx, tx, now, "identity.create", i.ID, 1)
	})
	return i, e
}

// UpdateIdentity changes desired policy and its delivery jobs in one commit.
// Rotation does not erase previous remote ownership evidence needed by retries.
func (s *Store) UpdateIdentity(ctx context.Context, id string, revision int64, policy Policy, rotate, revoke bool) error {
	policy, e := cleanPolicy(policy)
	if e != nil {
		return e
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		i, e := scanIdentity(tx.QueryRowContext(ctx, "SELECT "+identityColumns+" FROM identities WHERE id=?", id))
		if e != nil {
			return e
		}
		if revision <= 0 || i.Revision != revision || i.Revoked {
			return ErrConflict
		}
		var billingOwned int
		if e = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM subscriptions WHERE identity_id=?", id).Scan(&billingOwned); e != nil {
			return e
		}
		if billingOwned > 0 && !revoke && (!rotate || !reflect.DeepEqual(policy, i.Policy)) {
			return ErrConflict
		}

		if revoke {
			rotate = true
			policy.Enabled = false
		}
		raw, _ := json.Marshal(policy)
		now := s.now().Unix()
		if _, e := tx.ExecContext(ctx, "UPDATE identities SET policy=?,revoked=?,revision=revision+1,updated_at=? WHERE id=?", string(raw), revoke, now, id); e != nil {
			return e
		}
		if rotate {
			secret := randomSecret()
			if _, e := tx.ExecContext(ctx, "UPDATE identities SET secret_cipher=?,secret_hash=? WHERE id=?", s.encrypt(i.ClusterID+":"+id, secret), secretHash(secret), id); e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, "INSERT INTO identity_credentials(identity_id,secret_hash,revision) VALUES(?,?,?)", id, secretHash(secret), revision+1); e != nil {
				return e
			}
		}
		if _, e := tx.ExecContext(ctx, "UPDATE identity_jobs SET desired_revision=?,status='pending',attempts=0,next_attempt=?,lease_until=0,lease_token='',last_code='',updated_at=? WHERE identity_id=?", revision+1, now, now, id); e != nil {
			return e
		}
		action := "identity.policy"
		if rotate {
			action = "identity.rotate"
		}
		if revoke {
			action = "identity.revoke"
		}
		return auditTx(ctx, tx, now, action, id, revision+1)
	})
}

// Recognize intermediate rotations after a timeout or superseded job. Keeping
// only the last applied hash would strand a node that accepted an intermediate
// secret while a newer desired revision was committed concurrently.
func (s *Store) ownsCredential(ctx context.Context, id, hash string) (bool, error) {
	var count int
	e := s.db.QueryRowContext(ctx, "SELECT count(*) FROM identity_credentials WHERE identity_id=? AND secret_hash=?", id, hash).Scan(&count)
	return count == 1, e
}

// ManagedUser protects node-local CRUD from accidentally overriding desired
// identities. Revoked identities remain reserved until explicit migration.
func (s *Store) ManagedUser(ctx context.Context, nodeID, username string) (bool, error) {
	var count int
	e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM identities i JOIN cluster_members m ON m.cluster_id=i.cluster_id
	 WHERE m.node_id=? AND i.username=?`, nodeID, username).Scan(&count)
	return count > 0, e
}

func (s *Store) DeleteEmptyCluster(ctx context.Context, id string, revision int64) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := checkCluster(ctx, tx, id, revision); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM identities WHERE cluster_id=?)+(SELECT count(*) FROM cluster_members WHERE cluster_id=?)+(SELECT count(*) FROM plans WHERE cluster_id=?)+(SELECT count(*) FROM subscriptions WHERE cluster_id=?)", id, id, id, id).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			return ErrConflict
		}
		if _, e := tx.ExecContext(ctx, "DELETE FROM clusters WHERE id=?", id); e != nil {
			return e
		}
		return auditTx(ctx, tx, s.now().Unix(), "cluster.delete", id, revision)
	})
}
