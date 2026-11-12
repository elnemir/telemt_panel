// Package platform owns durable cluster and commercial state independently of
// optional observability history. Network calls never run inside its SQL transactions.
package platform

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"
)

var (
	ErrUnavailable = errors.New("transactional platform requires the full build")
	ErrKey         = errors.New("platform encryption key missing or incompatible")
	ErrSchema      = errors.New("platform database schema is incompatible")
	ErrInvalid     = errors.New("invalid platform input")
	ErrConflict    = errors.New("platform revision or ownership conflict")
	ErrNotFound    = errors.New("platform record not found")
)

//go:embed schema.sql
var schema string

//go:embed billing_schema.sql
var billingSchema string

//go:embed customer_schema.sql
var customerSchema string

//go:embed quota_schema.sql
var quotaSchema string

type Store struct {
	db    *sql.DB
	seal  cipher.AEAD
	keyID string
	now   func() time.Time
}

func KeyFromEnvironment(name string) ([]byte, error) {
	if name == "" {
		return nil, ErrKey
	}
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil, ErrKey
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, ErrKey
	}
	return key, nil
}
func newStore(db *sql.DB, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, ErrKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrKey
	}
	id := sha256.Sum256(key)
	return &Store{db: db, seal: aead, keyID: hex.EncodeToString(id[:]), now: time.Now}, nil
}
func newID(prefix string) string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return prefix + hex.EncodeToString(id[:])
}
func (s *Store) encrypt(owner, plain string) []byte {
	nonce := make([]byte, s.seal.NonceSize())
	_, _ = rand.Read(nonce)
	return s.seal.Seal(nonce, nonce, []byte(plain), []byte("telemt-platform:v1:"+owner))
}
func (s *Store) decrypt(owner string, sealed []byte) (string, error) {
	n := s.seal.NonceSize()
	if len(sealed) < n+s.seal.Overhead() {
		return "", ErrKey
	}
	plain, err := s.seal.Open(nil, sealed[:n], sealed[n:], []byte("telemt-platform:v1:"+owner))
	if err != nil {
		return "", ErrKey
	}
	return string(plain), nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) initialize(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var version int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if version < 0 || version > 4 {
			return ErrSchema
		}
		if version == 0 {
			var tables int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
				return err
			}
			if tables != 0 {
				return ErrSchema
			}
			for _, statement := range strings.Split(schema, "-- statement") {
				if strings.TrimSpace(statement) != "" {
					if _, err := tx.ExecContext(ctx, statement); err != nil {
						return err
					}
				}
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO platform_meta(key,value) VALUES('encryption_key_id',?)", s.keyID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "PRAGMA user_version=1"); err != nil {
				return err
			}
		}
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT value FROM platform_meta WHERE key='encryption_key_id'").Scan(&id); err != nil {
			return ErrSchema
		}
		if id != s.keyID {
			return ErrKey
		}

		// Check the key before any migration, so a wrong-key startup cannot
		// alter a database that it is unable to use.
		if version < 2 {
			for _, statement := range strings.Split(billingSchema, "-- statement") {
				if strings.TrimSpace(statement) != "" {
					if _, err := tx.ExecContext(ctx, statement); err != nil {
						return err
					}
				}
			}
			if _, err := tx.ExecContext(ctx, "PRAGMA user_version=2"); err != nil {
				return err
			}
		}
		if version < 3 {
			for _, statement := range strings.Split(customerSchema, "-- statement") {
				if strings.TrimSpace(statement) != "" {
					if _, err := tx.ExecContext(ctx, statement); err != nil {
						return err
					}
				}
			}
			if _, err := tx.ExecContext(ctx, "PRAGMA user_version=3"); err != nil {
				return err
			}
		}
		if version < 4 {
			for _, statement := range strings.Split(quotaSchema, "-- statement") {
				if strings.TrimSpace(statement) != "" {
					if _, err := tx.ExecContext(ctx, statement); err != nil {
						return err
					}
				}
			}
			if _, err := tx.ExecContext(ctx, "PRAGMA user_version=4"); err != nil {
				return err
			}
		}

		return nil
	})
}
