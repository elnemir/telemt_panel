CREATE TABLE quota_allocations (
 period_id TEXT NOT NULL REFERENCES paid_periods(id) ON DELETE RESTRICT,
 node_id TEXT NOT NULL,
 boot_id TEXT NOT NULL,
 identity_id TEXT NOT NULL REFERENCES identities(id) ON DELETE RESTRICT,
 granted_bytes INTEGER NOT NULL CHECK(granted_bytes>=0),
 observed_bytes INTEGER NOT NULL DEFAULT 0 CHECK(observed_bytes>=0 AND observed_bytes<=granted_bytes),
 sequence INTEGER NOT NULL CHECK(sequence>0),
 valid_until INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 reported_at INTEGER,
 PRIMARY KEY(period_id,node_id,boot_id)
) STRICT;
-- statement
CREATE TABLE quota_sequences (
 identity_id TEXT NOT NULL REFERENCES identities(id) ON DELETE RESTRICT,
 node_id TEXT NOT NULL,
 sequence INTEGER NOT NULL CHECK(sequence>0),
 PRIMARY KEY(identity_id,node_id)
) STRICT;
-- statement
CREATE TRIGGER quota_allocations_no_reclaim BEFORE UPDATE ON quota_allocations WHEN
 NEW.period_id IS NOT OLD.period_id OR NEW.node_id IS NOT OLD.node_id OR
 NEW.boot_id IS NOT OLD.boot_id OR NEW.identity_id IS NOT OLD.identity_id OR
 NEW.granted_bytes<OLD.granted_bytes OR NEW.observed_bytes<OLD.observed_bytes OR NEW.sequence<OLD.sequence
 BEGIN SELECT RAISE(ABORT,'quota allocation cannot be reclaimed'); END;
-- statement
CREATE TRIGGER quota_allocations_no_delete BEFORE DELETE ON quota_allocations BEGIN SELECT RAISE(ABORT,'quota allocation cannot be reclaimed'); END;
-- statement
ALTER TABLE subscriptions ADD COLUMN entitlement_next_attempt INTEGER NOT NULL DEFAULT 0;
-- statement
CREATE INDEX subscriptions_entitlement_due ON subscriptions(entitlement_next_attempt,id);
-- statement
ALTER TABLE identity_jobs ADD COLUMN links_cipher BLOB;
-- statement
ALTER TABLE identity_jobs ADD COLUMN budget_period_id TEXT NOT NULL DEFAULT '';
-- statement
ALTER TABLE identity_jobs ADD COLUMN budget_until INTEGER NOT NULL DEFAULT 0;
