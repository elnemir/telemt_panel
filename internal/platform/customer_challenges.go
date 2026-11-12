package platform

import (
	"context"
	"database/sql"
	"errors"
	"net/mail"
	"strings"
)

// NormalizeCustomerEmail deliberately supports ASCII mailboxes with a
// case-insensitive account key. It does not accept display names or comments.
func NormalizeCustomerEmail(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 3 || len(raw) > 254 || strings.ContainsAny(raw, " \t\r\n()<>\"\\") {
		return "", ErrInvalid
	}
	for _, r := range raw {
		if r < 33 || r > 126 {
			return "", ErrInvalid
		}
	}
	address, e := mail.ParseAddress(raw)
	if e != nil || address.Address != raw {
		return "", ErrInvalid
	}
	return strings.ToLower(raw), nil
}
func validCustomerPasswordHash(hash string) bool {
	return len(hash) == 60 && (strings.HasPrefix(hash, "$2a$10$") || strings.HasPrefix(hash, "$2b$10$"))
}

// PrepareEmailChallenge stores only the token hash. The caller sends the raw
// token over SMTP after commit; no network operation holds the DB transaction.
func (s *Store) PrepareEmailChallenge(ctx context.Context, purpose, email, tokenHash, passwordHash, sessionHash string) error {
	email, e := NormalizeCustomerEmail(email)
	if e != nil {
		return e
	}
	if !authHashPattern.MatchString(tokenHash) {
		return ErrInvalid
	}
	if purpose == "reset" {
		if passwordHash != "" {
			return ErrInvalid
		}
	} else if (purpose != "register" && purpose != "link") || !validCustomerPasswordHash(passwordHash) {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		now := s.now().Unix()
		ttl := int64(1800)
		var customerID any
		revision := int64(0)
		existing, e := scanCustomerCredential(tx.QueryRowContext(ctx, "SELECT "+customerColumns+" FROM customers WHERE email=?", email))
		if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
		switch purpose {
		case "register":
			if e == nil {
				return ErrConflict
			}
		case "reset":
			ttl = 900
			if e != nil || !existing.Customer.Enabled || !existing.Customer.EmailVerified {
				return ErrNotFound
			}
			customerID = existing.Customer.ID
			revision = existing.AuthRevision
		case "link":
			if e == nil {
				return ErrConflict
			}
			session, e := resolveCustomerSession(ctx, tx, sessionHash, now)
			if e != nil {
				return e
			}
			if now-session.CreatedAt > CustomerFreshAuth || session.Customer.Email != "" {
				return ErrCustomerAuth
			}
			customerID = session.Customer.ID
			revision = session.AuthRevision
		}
		if now < 0 || now > maxBillingEpoch-ttl {
			return ErrInvalid
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM customer_email_challenges WHERE expires_at<=? OR (email=? AND purpose=?)", now, email, purpose); e != nil {
			return e
		}
		var count int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM customer_email_challenges").Scan(&count); e != nil {
			return e
		}
		if count >= 20000 {
			return ErrConflict
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO customer_email_challenges(id_hash,email,purpose,password_hash,customer_id,auth_revision,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?)", tokenHash, email, purpose, passwordHash, customerID, revision, now, now+ttl)
		return e
	})
}
func (s *Store) CompleteEmailChallenge(ctx context.Context, tokenHash, newPasswordHash string) (CustomerCredential, error) {
	if !authHashPattern.MatchString(tokenHash) {
		return CustomerCredential{}, ErrCustomerAuth
	}
	var result CustomerCredential
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		var email, purpose, passwordHash string
		var customerID sql.NullString
		var revision, expires int64
		e := tx.QueryRowContext(ctx, "SELECT email,purpose,password_hash,customer_id,auth_revision,expires_at FROM customer_email_challenges WHERE id_hash=?", tokenHash).Scan(&email, &purpose, &passwordHash, &customerID, &revision, &expires)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrCustomerAuth
		}
		if e != nil {
			return e
		}
		now := s.now().Unix()
		if now >= expires {
			return ErrCustomerAuth
		}
		if purpose == "reset" {
			if !validCustomerPasswordHash(newPasswordHash) {
				return ErrInvalid
			}
			passwordHash = newPasswordHash
		} else if newPasswordHash != "" {
			return ErrInvalid
		}
		var id string
		if purpose == "register" {
			var exists bool
			if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM customers WHERE email=?)", email).Scan(&exists); e != nil {
				return e
			}
			if exists {
				return ErrConflict
			}
			if e = checkCustomerCapacity(ctx, tx); e != nil {
				return e
			}
			id = newID("customer_")
			if _, e = tx.ExecContext(ctx, "INSERT INTO customers(id,email,email_verified,password_hash,created_at,updated_at) VALUES(?,?,1,?,?,?)", id, email, passwordHash, now, now); e != nil {
				return e
			}
		} else {
			c, e := scanCustomerCredential(tx.QueryRowContext(ctx, "SELECT "+customerColumns+" FROM customers WHERE id=?", customerID.String))
			if e != nil {
				return e
			}
			if !c.Customer.Enabled || c.AuthRevision != revision {
				return ErrCustomerAuth
			}
			id = c.Customer.ID
			if purpose == "reset" {
				if !c.Customer.EmailVerified || c.Customer.Email != email {
					return ErrCustomerAuth
				}
				if _, e = tx.ExecContext(ctx, "UPDATE customers SET password_hash=?,auth_revision=auth_revision+1,updated_at=? WHERE id=?", passwordHash, now, id); e != nil {
					return e
				}
			} else {
				if c.Customer.Email != "" {
					return ErrConflict
				}
				var exists bool
				if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM customers WHERE email=?)", email).Scan(&exists); e != nil {
					return e
				}
				if exists {
					return ErrConflict
				}
				if _, e = tx.ExecContext(ctx, "UPDATE customers SET email=?,email_verified=1,password_hash=?,auth_revision=auth_revision+1,updated_at=? WHERE id=?", email, passwordHash, now, id); e != nil {
					return e
				}
			}
			if _, e = tx.ExecContext(ctx, "DELETE FROM customer_sessions WHERE customer_id=?", id); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM customer_email_challenges WHERE id_hash=?", tokenHash); e != nil {
			return e
		}
		result, e = scanCustomerCredential(tx.QueryRowContext(ctx, "SELECT "+customerColumns+" FROM customers WHERE id=?", id))
		return e
	})
	return result, e
}

type CustomerOIDCState struct {
	Verifier    string
	NonceHash   string
	SessionHash string
}

func (s *Store) PutCustomerOIDCState(ctx context.Context, stateHash, browserHash, verifier, nonceHash, sessionHash string) error {
	if !authHashPattern.MatchString(stateHash) || !authHashPattern.MatchString(browserHash) || !authHashPattern.MatchString(nonceHash) || len(verifier) < 43 || len(verifier) > 128 {
		return ErrInvalid
	}
	if sessionHash != "" && !authHashPattern.MatchString(sessionHash) {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		now := s.now().Unix()
		if sessionHash != "" {
			session, e := resolveCustomerSession(ctx, tx, sessionHash, now)
			if e != nil {
				return e
			}
			if now-session.CreatedAt > CustomerFreshAuth {
				return ErrCustomerAuth
			}
		}
		if _, e := tx.ExecContext(ctx, "DELETE FROM customer_oidc_states WHERE expires_at<=?", now); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM customer_oidc_states").Scan(&count); e != nil {
			return e
		}
		if count >= 5000 {
			return ErrConflict
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO customer_oidc_states(id_hash,browser_hash,verifier_cipher,nonce_hash,session_hash,created_at,expires_at) VALUES(?,?,?,?,?,?,?)", stateHash, browserHash, s.encrypt("oidc:"+stateHash, verifier), nonceHash, sessionHash, now, now+600)
		return e
	})
}
func (s *Store) TakeCustomerOIDCState(ctx context.Context, stateHash, browserHash string) (CustomerOIDCState, error) {
	if !authHashPattern.MatchString(stateHash) || !authHashPattern.MatchString(browserHash) {
		return CustomerOIDCState{}, ErrCustomerAuth
	}
	var state CustomerOIDCState
	e := s.transaction(ctx, func(tx *sql.Tx) error {
		var cipher []byte
		var expires int64
		e := tx.QueryRowContext(ctx, "SELECT verifier_cipher,nonce_hash,session_hash,expires_at FROM customer_oidc_states WHERE id_hash=? AND browser_hash=?", stateHash, browserHash).Scan(&cipher, &state.NonceHash, &state.SessionHash, &expires)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrCustomerAuth
		}
		if e != nil {
			return e
		}
		if s.now().Unix() >= expires {
			return ErrCustomerAuth
		}
		if state.SessionHash != "" {
			session, e := resolveCustomerSession(ctx, tx, state.SessionHash, s.now().Unix())
			if e != nil {
				return e
			}
			if s.now().Unix()-session.CreatedAt > CustomerFreshAuth {
				return ErrCustomerAuth
			}
		}
		state.Verifier, e = s.decrypt("oidc:"+stateHash, cipher)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "DELETE FROM customer_oidc_states WHERE id_hash=?", stateHash)
		return e
	})
	return state, e
}
