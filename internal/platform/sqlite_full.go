//go:build !lite

package platform

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"time"

	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

const Available = true

func Open(path string, key []byte) (*Store, error) {
	if !filepath.IsAbs(path) || len(key) != 32 {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	// Create with private permissions before SQLite can publish any secret data.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_txlock", "immediate")
	for _, p := range []string{"busy_timeout(5000)", "synchronous(FULL)", "foreign_keys(ON)"} {
		q.Add("_pragma", p)
	}
	u.RawQuery = q.Encode()
	db, err := sqlitedriver.Open(u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s, err := newStore(db, key)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = s.initialize(ctx); err == nil {
		var mode string
		err = db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode)
		if err == nil && mode != "wal" {
			err = ErrSchema
		}
	}
	if err == nil {
		var check string
		err = db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check)
		if err == nil && check != "ok" {
			err = ErrSchema
		}
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}
