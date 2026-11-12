CREATE TABLE platform_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
-- statement
CREATE TABLE clusters (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 name_key TEXT NOT NULL UNIQUE,
 revision INTEGER NOT NULL CHECK(revision>0),
 routing TEXT NOT NULL DEFAULT '{}',
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
-- statement
CREATE TABLE cluster_members (
 node_id TEXT PRIMARY KEY,
 cluster_id TEXT NOT NULL REFERENCES clusters(id) ON DELETE RESTRICT,
 state TEXT NOT NULL CHECK(state IN ('active','removing')),
 weight INTEGER NOT NULL DEFAULT 1 CHECK(weight BETWEEN 1 AND 1000),
 drain INTEGER NOT NULL DEFAULT 0 CHECK(drain IN (0,1)),
 created_at INTEGER NOT NULL
) STRICT;
-- statement
CREATE INDEX cluster_members_cluster ON cluster_members(cluster_id,node_id);
-- statement
CREATE TABLE identities (
 id TEXT PRIMARY KEY,
 cluster_id TEXT NOT NULL REFERENCES clusters(id) ON DELETE RESTRICT,
 username TEXT NOT NULL,
 secret_cipher BLOB NOT NULL,
 secret_hash TEXT NOT NULL,
 policy TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0,1)),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(cluster_id,username),
 UNIQUE(cluster_id,secret_hash)
) STRICT;
-- statement
CREATE TABLE identity_jobs (
 identity_id TEXT NOT NULL REFERENCES identities(id) ON DELETE RESTRICT,
 node_id TEXT NOT NULL REFERENCES cluster_members(node_id) ON DELETE RESTRICT,
 desired_revision INTEGER NOT NULL CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0,
 applied_secret_hash TEXT NOT NULL DEFAULT '',
 retirement_cipher BLOB,
 retirement_hash TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('pending','running','applied','conflict','unavailable')),
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt INTEGER NOT NULL,
 lease_until INTEGER NOT NULL DEFAULT 0,
 lease_token TEXT NOT NULL DEFAULT '',
 last_code TEXT NOT NULL DEFAULT '',
 in_runtime INTEGER NOT NULL DEFAULT 0 CHECK(in_runtime IN (0,1)),
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(identity_id,node_id)
) STRICT;
-- statement
CREATE TABLE identity_credentials (
 identity_id TEXT NOT NULL REFERENCES identities(id) ON DELETE RESTRICT,
 secret_hash TEXT NOT NULL,
 revision INTEGER NOT NULL,
 PRIMARY KEY(identity_id,secret_hash)
) STRICT;
-- statement
CREATE INDEX identity_jobs_due ON identity_jobs(status,next_attempt,lease_until);
-- statement
CREATE TABLE platform_audit (
 id INTEGER PRIMARY KEY,
 created_at INTEGER NOT NULL,
 action TEXT NOT NULL,
 subject TEXT NOT NULL,
 revision INTEGER NOT NULL,
 detail TEXT NOT NULL DEFAULT ''
) STRICT;
