CREATE TABLE cluster_config_templates (
 cluster_id TEXT PRIMARY KEY REFERENCES clusters(id) ON DELETE CASCADE,
 source_node_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 settings TEXT NOT NULL
) STRICT;
-- statement
CREATE TABLE cluster_config_jobs (
 node_id TEXT PRIMARY KEY REFERENCES cluster_members(node_id) ON DELETE CASCADE,
 desired_revision INTEGER NOT NULL CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','applied','conflict','unavailable')),
 last_code TEXT NOT NULL DEFAULT '',
 next_attempt INTEGER NOT NULL,
 lease_until INTEGER NOT NULL DEFAULT 0,
 lease_token TEXT NOT NULL DEFAULT ''
) STRICT;
-- statement
CREATE INDEX cluster_config_jobs_due ON cluster_config_jobs(next_attempt,lease_until);
