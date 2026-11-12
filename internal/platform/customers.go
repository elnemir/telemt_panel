package platform

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
)

var ErrCustomerAuth = errors.New("customer authentication failed")
var authHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var telegramSubjectPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

const CustomerSessionTTL int64 = 30 * 24 * 60 * 60
const CustomerFreshAuth int64 = 5 * 60
const maxCustomers = 100000

// Credential is an internal authentication snapshot, never an HTTP DTO. A
// session can only be created if its revision still matches after password work.
type CustomerCredential struct {
	Customer     Customer
	PasswordHash string
	AuthRevision int64
}

const customerColumns = "id,COALESCE(email,''),email_verified,telegram_subject IS NOT NULL,enabled,created_at,password_hash,auth_revision"

func scanCustomerCredential(row scanner) (CustomerCredential, error) {
	var c CustomerCredential
	e := row.Scan(&c.Customer.ID, &c.Customer.Email, &c.Customer.EmailVerified, &c.Customer.TelegramLinked, &c.Customer.Enabled, &c.Customer.CreatedAt, &c.PasswordHash, &c.AuthRevision)
	if errors.Is(e, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, e
}
func (s *Store) CustomerCredentials(ctx context.Context, email string) (CustomerCredential, error) {
	return scanCustomerCredential(s.db.QueryRowContext(ctx, "SELECT "+customerColumns+" FROM customers WHERE email=?", email))
}
func (s *Store) GetCustomer(ctx context.Context, id string) (Customer, error) {
	c, e := scanCustomerCredential(s.db.QueryRowContext(ctx, "SELECT "+customerColumns+" FROM customers WHERE id=?", id))
	return c.Customer, e
}

type CustomerSession struct {
	Customer     Customer
	IDHash       string
	AuthRevision int64
	CreatedAt    int64
	ExpiresAt    int64
}

func resolveCustomerSession(ctx context.Context, q queryRower, hash string, now int64) (CustomerSession, error) {
	if !authHashPattern.MatchString(hash) {
		return CustomerSession{}, ErrCustomerAuth
	}
	var session CustomerSession
	var id string
	e := q.QueryRowContext(ctx, "SELECT customer_id,auth_revision,created_at,expires_at FROM customer_sessions WHERE id_hash=?", hash).Scan(&id, &session.AuthRevision, &session.CreatedAt, &session.ExpiresAt)
	if errors.Is(e, sql.ErrNoRows) {
		return session, ErrCustomerAuth
	}
	if e != nil {
		return session, e
	}
	c, e := scanCustomerCredential(q.QueryRowContext(ctx, "SELECT "+customerColumns+" FROM customers WHERE id=?", id))
	if e != nil {
		return session, e
	}
	if !c.Customer.Enabled || c.AuthRevision != session.AuthRevision || now >= session.ExpiresAt || now < session.CreatedAt || (!c.Customer.EmailVerified && !c.Customer.TelegramLinked) {
		return CustomerSession{}, ErrCustomerAuth
	}
	session.Customer = c.Customer
	session.IDHash = hash
	return session, nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) ResolveCustomerSession(ctx context.Context, hash string) (CustomerSession, error) {
	return resolveCustomerSession(ctx, s.db, hash, s.now().Unix())
}
func (s *Store) CreateCustomerSession(ctx context.Context, id string, revision int64, hash string) (CustomerSession, error) {
	if !authHashPattern.MatchString(hash) {
		return CustomerSession{}, ErrInvalid
	}
	var session CustomerSession
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := scanCustomerCredential(tx.QueryRowContext(ctx, "SELECT "+customerColumns+" FROM customers WHERE id=?", id))
		if e != nil {
			return e
		}
		if !c.Customer.Enabled || c.AuthRevision != revision || (!c.Customer.EmailVerified && !c.Customer.TelegramLinked) {
			return ErrCustomerAuth
		}
		now := s.now().Unix()
		if now < 0 || now > maxBillingEpoch-CustomerSessionTTL {
			return ErrInvalid
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM customer_sessions WHERE expires_at<=?", now); e != nil {
			return e
		}
		// Retain the nine newest sessions before adding the new one.
		if _, e = tx.ExecContext(ctx, "DELETE FROM customer_sessions WHERE customer_id=? AND id_hash NOT IN (SELECT id_hash FROM customer_sessions WHERE customer_id=? ORDER BY created_at DESC,id_hash DESC LIMIT 9)", id, id); e != nil {
			return e
		}
		session = CustomerSession{Customer: c.Customer, IDHash: hash, AuthRevision: revision, CreatedAt: now, ExpiresAt: now + CustomerSessionTTL}
		if _, e = tx.ExecContext(ctx, "INSERT INTO customer_sessions(id_hash,customer_id,auth_revision,created_at,expires_at) VALUES(?,?,?,?,?)", hash, id, revision, now, session.ExpiresAt); e != nil {
			return e
		}
		return nil
	})
	return session, e
}
func (s *Store) RevokeCustomerSession(ctx context.Context, hash string) error {
	if !authHashPattern.MatchString(hash) {
		return ErrCustomerAuth
	}
	_, e := s.db.ExecContext(ctx, "DELETE FROM customer_sessions WHERE id_hash=?", hash)
	return e
}
func checkCustomerCapacity(ctx context.Context, tx *sql.Tx) error {
	var count int
	if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM customers").Scan(&count); e != nil {
		return e
	}
	if count >= maxCustomers {
		return ErrConflict
	}
	return nil
}
func (s *Store) TelegramCustomer(ctx context.Context, subject string) (CustomerCredential, error) {
	if !telegramSubjectPattern.MatchString(subject) {
		return CustomerCredential{}, ErrInvalid
	}
	var result CustomerCredential
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		c, e := scanCustomerCredential(tx.QueryRowContext(ctx, "SELECT "+customerColumns+" FROM customers WHERE telegram_subject=?", subject))
		if e == nil {
			if !c.Customer.Enabled {
				return ErrCustomerAuth
			}
			result = c
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		if e = checkCustomerCapacity(ctx, tx); e != nil {
			return e
		}
		id := newID("customer_")
		now := s.now().Unix()
		if _, e = tx.ExecContext(ctx, "INSERT INTO customers(id,telegram_subject,created_at,updated_at) VALUES(?,?,?,?)", id, subject, now, now); e != nil {
			return e
		}
		result, e = scanCustomerCredential(tx.QueryRowContext(ctx, "SELECT "+customerColumns+" FROM customers WHERE id=?", id))
		return e
	})
	return result, e
}
func (s *Store) LinkCustomerTelegram(ctx context.Context, sessionHash, subject string) (CustomerCredential, error) {
	if !telegramSubjectPattern.MatchString(subject) {
		return CustomerCredential{}, ErrInvalid
	}
	var result CustomerCredential
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		session, e := resolveCustomerSession(ctx, tx, sessionHash, s.now().Unix())
		if e != nil {
			return e
		}
		if s.now().Unix()-session.CreatedAt > CustomerFreshAuth {
			return ErrCustomerAuth
		}
		var owner string
		e = tx.QueryRowContext(ctx, "SELECT id FROM customers WHERE telegram_subject=?", subject).Scan(&owner)
		if e == nil {
			if owner != session.Customer.ID {
				return ErrConflict
			}
			result, e = scanCustomerCredential(tx.QueryRowContext(ctx, "SELECT "+customerColumns+" FROM customers WHERE id=?", owner))
			return e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if session.Customer.TelegramLinked {
			return ErrConflict
		}
		if _, e = tx.ExecContext(ctx, "UPDATE customers SET telegram_subject=?,auth_revision=auth_revision+1,updated_at=? WHERE id=?", subject, s.now().Unix(), session.Customer.ID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM customer_sessions WHERE customer_id=?", session.Customer.ID); e != nil {
			return e
		}
		result, e = scanCustomerCredential(tx.QueryRowContext(ctx, "SELECT "+customerColumns+" FROM customers WHERE id=?", session.Customer.ID))
		return e
	})
	return result, e
}
