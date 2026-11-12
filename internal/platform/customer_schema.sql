ALTER TABLE customers ADD COLUMN auth_revision INTEGER NOT NULL DEFAULT 1 CHECK(auth_revision>0);
-- statement
CREATE TABLE customer_sessions (
 id_hash TEXT PRIMARY KEY,
 customer_id TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
 auth_revision INTEGER NOT NULL,
 created_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL CHECK(expires_at>created_at)
) STRICT;
-- statement
CREATE INDEX customer_sessions_owner ON customer_sessions(customer_id,created_at);
-- statement
CREATE INDEX customer_sessions_expiry ON customer_sessions(expires_at);
-- statement
CREATE TABLE customer_email_challenges (
 id_hash TEXT PRIMARY KEY,
 email TEXT NOT NULL,
 purpose TEXT NOT NULL CHECK(purpose IN ('register','reset','link')),
 password_hash TEXT NOT NULL,
 customer_id TEXT REFERENCES customers(id) ON DELETE CASCADE,
 auth_revision INTEGER NOT NULL,
 created_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL CHECK(expires_at>created_at),
 UNIQUE(email,purpose)
) STRICT;
-- statement
CREATE INDEX customer_email_challenges_expiry ON customer_email_challenges(expires_at);
-- statement
CREATE TABLE customer_oidc_states (
 id_hash TEXT PRIMARY KEY,
 browser_hash TEXT NOT NULL,
 verifier_cipher BLOB NOT NULL,
 nonce_hash TEXT NOT NULL,
 session_hash TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 expires_at INTEGER NOT NULL CHECK(expires_at>created_at)
) STRICT;
-- statement
CREATE INDEX customer_oidc_states_expiry ON customer_oidc_states(expires_at);
