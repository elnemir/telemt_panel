//go:build !lite

package platform

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) (*Store, string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "platform.sqlite")
	key := bytes.Repeat([]byte{7}, 32)
	s, err := Open(path, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path, key
}
func TestEncryptionIsOwnerBoundAndKeyValidatedOnReopen(t *testing.T) {
	s, path, key := testStore(t)
	const secret = "0123456789abcdef0123456789abcdef"
	a := s.encrypt("identity-a", secret)
	b := s.encrypt("identity-a", secret)
	if bytes.Equal(a, b) || bytes.Contains(a, []byte(secret)) {
		t.Fatal("encryption not randomized")
	}
	if got, e := s.decrypt("identity-a", a); e != nil || got != secret {
		t.Fatal(e)
	}
	if _, e := s.decrypt("identity-b", a); !errors.Is(e, ErrKey) {
		t.Fatal("ciphertext reassigned to another owner", e)
	}
	a[len(a)-1] ^= 1
	if _, e := s.decrypt("identity-a", a); !errors.Is(e, ErrKey) {
		t.Fatal("tamper accepted", e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(path, bytes.Repeat([]byte{8}, 32)); !errors.Is(e, ErrKey) {
		t.Fatal("wrong key accepted", e)
	}
	reopened, e := Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
}
func TestTransactionsRollbackAndConstraintsRemainEnabled(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	sentinel := errors.New("stop")
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "INSERT INTO clusters(id,name,name_key,revision,created_at,updated_at) VALUES('c1','Cluster','cluster',1,1,1)"); e != nil {
			return e
		}
		return sentinel
	})
	if !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	var count int
	if e = s.db.QueryRowContext(ctx, "SELECT count(*) FROM clusters").Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
	if _, e = s.db.ExecContext(ctx, "INSERT INTO cluster_members(node_id,cluster_id,state,created_at) VALUES('node','missing','active',1)"); e == nil {
		t.Fatal("foreign key not enforced")
	}
	var mode string
	var sync int
	if s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode) != nil || mode != "wal" {
		t.Fatal(mode)
	}
	if s.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync) != nil || sync != 2 {
		t.Fatal(sync)
	}
}
