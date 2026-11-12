CREATE TABLE customers (
 id TEXT PRIMARY KEY,
 email TEXT UNIQUE,
 email_verified INTEGER NOT NULL DEFAULT 0 CHECK(email_verified IN (0,1)),
 password_hash TEXT NOT NULL DEFAULT '',
 telegram_subject TEXT UNIQUE,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
-- statement
CREATE TABLE plans (
 id TEXT PRIMARY KEY,
 cluster_id TEXT NOT NULL REFERENCES clusters(id) ON DELETE RESTRICT,
 name TEXT NOT NULL,
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 revision INTEGER NOT NULL CHECK(revision>0),
 current_version INTEGER NOT NULL CHECK(current_version>0),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
-- statement
CREATE TABLE plan_versions (
 plan_id TEXT NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
 version INTEGER NOT NULL CHECK(version>0),
 terms TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(plan_id,version)
) STRICT;
-- statement
CREATE TABLE subscriptions (
 id TEXT PRIMARY KEY,
 customer_id TEXT NOT NULL REFERENCES customers(id) ON DELETE RESTRICT,
 cluster_id TEXT NOT NULL REFERENCES clusters(id) ON DELETE RESTRICT,
 identity_id TEXT UNIQUE REFERENCES identities(id) ON DELETE RESTRICT,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
-- statement
CREATE INDEX subscriptions_customer ON subscriptions(customer_id,id);
-- statement
CREATE TABLE payment_accounts (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 provider TEXT NOT NULL CHECK(provider IN ('robokassa','yoomoney')),
 is_test INTEGER NOT NULL CHECK(is_test IN (0,1)),
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 config TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
-- statement
CREATE TABLE invoices (
 id TEXT PRIMARY KEY,
 number INTEGER NOT NULL UNIQUE CHECK(number>0),
 customer_id TEXT NOT NULL REFERENCES customers(id) ON DELETE RESTRICT,
 subscription_id TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE RESTRICT,
 account_id TEXT NOT NULL REFERENCES payment_accounts(id) ON DELETE RESTRICT,
 plan_id TEXT NOT NULL,
 plan_version INTEGER NOT NULL,
 currency TEXT NOT NULL,
 currency_scale INTEGER NOT NULL,
 amount_minor INTEGER NOT NULL CHECK(amount_minor>0),
 is_test INTEGER NOT NULL CHECK(is_test IN (0,1)),
 status TEXT NOT NULL CHECK(status IN ('pending','paid','test_paid')),
 terms TEXT NOT NULL,
 request_key TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 paid_at INTEGER,
 UNIQUE(customer_id,request_key),
 FOREIGN KEY(plan_id,plan_version) REFERENCES plan_versions(plan_id,version) ON DELETE RESTRICT
) STRICT;
-- statement
CREATE TABLE billing_sequences (name TEXT PRIMARY KEY,value INTEGER NOT NULL) STRICT;
-- statement
INSERT INTO billing_sequences(name,value) VALUES('invoice',0);
-- statement
CREATE INDEX invoices_customer ON invoices(customer_id,created_at,id);
-- statement
CREATE TABLE payment_events (
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES payment_accounts(id) ON DELETE RESTRICT,
 external_id TEXT NOT NULL,
 invoice_id TEXT NOT NULL REFERENCES invoices(id) ON DELETE RESTRICT,
 fingerprint TEXT NOT NULL,
 gross_minor INTEGER NOT NULL,
 net_minor INTEGER,
 currency TEXT NOT NULL,
 is_test INTEGER NOT NULL CHECK(is_test IN (0,1)),
 payment_at INTEGER NOT NULL,
 time_source TEXT NOT NULL CHECK(time_source IN ('provider','verified_notification')),
 received_at INTEGER NOT NULL,
 UNIQUE(account_id,external_id)
) STRICT;
-- statement
CREATE TABLE paid_periods (
 id TEXT PRIMARY KEY,
 subscription_id TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE RESTRICT,
 invoice_id TEXT NOT NULL UNIQUE REFERENCES invoices(id) ON DELETE RESTRICT,
 plan_id TEXT NOT NULL,
 plan_version INTEGER NOT NULL,
 starts_at INTEGER NOT NULL,
 ends_at INTEGER NOT NULL CHECK(ends_at>starts_at),
 terms TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 UNIQUE(subscription_id,starts_at),
 FOREIGN KEY(plan_id,plan_version) REFERENCES plan_versions(plan_id,version) ON DELETE RESTRICT
) STRICT;
-- statement
CREATE INDEX paid_periods_current ON paid_periods(subscription_id,starts_at,ends_at);
-- statement
CREATE TABLE billing_ledger (
 id TEXT PRIMARY KEY,
 customer_id TEXT NOT NULL REFERENCES customers(id) ON DELETE RESTRICT,
 subscription_id TEXT NOT NULL REFERENCES subscriptions(id) ON DELETE RESTRICT,
 invoice_id TEXT NOT NULL REFERENCES invoices(id) ON DELETE RESTRICT,
 kind TEXT NOT NULL CHECK(kind IN ('service_payment','unallocated_payment')),
 event_id TEXT NOT NULL UNIQUE REFERENCES payment_events(id) ON DELETE RESTRICT,
 period_id TEXT UNIQUE REFERENCES paid_periods(id) ON DELETE RESTRICT,
 amount_minor INTEGER NOT NULL CHECK(amount_minor>0),
 currency TEXT NOT NULL,
 created_at INTEGER NOT NULL
) STRICT;
-- statement
CREATE TRIGGER plan_versions_immutable_update BEFORE UPDATE ON plan_versions BEGIN SELECT RAISE(ABORT,'immutable plan version'); END;
-- statement
CREATE TRIGGER plan_versions_immutable_delete BEFORE DELETE ON plan_versions BEGIN SELECT RAISE(ABORT,'immutable plan version'); END;
-- statement
CREATE TRIGGER payment_events_immutable_update BEFORE UPDATE ON payment_events BEGIN SELECT RAISE(ABORT,'immutable payment event'); END;
-- statement
CREATE TRIGGER payment_events_immutable_delete BEFORE DELETE ON payment_events BEGIN SELECT RAISE(ABORT,'immutable payment event'); END;
-- statement
CREATE TRIGGER paid_periods_immutable_update BEFORE UPDATE ON paid_periods BEGIN SELECT RAISE(ABORT,'immutable paid period'); END;
-- statement
CREATE TRIGGER paid_periods_immutable_delete BEFORE DELETE ON paid_periods BEGIN SELECT RAISE(ABORT,'immutable paid period'); END;
-- statement
CREATE TRIGGER billing_ledger_immutable_update BEFORE UPDATE ON billing_ledger BEGIN SELECT RAISE(ABORT,'immutable ledger'); END;
-- statement
CREATE TRIGGER billing_ledger_immutable_delete BEFORE DELETE ON billing_ledger BEGIN SELECT RAISE(ABORT,'immutable ledger'); END;

-- statement
CREATE TRIGGER invoices_immutable_terms BEFORE UPDATE ON invoices WHEN
 NEW.id IS NOT OLD.id OR NEW.number IS NOT OLD.number OR NEW.customer_id IS NOT OLD.customer_id OR
 NEW.subscription_id IS NOT OLD.subscription_id OR NEW.account_id IS NOT OLD.account_id OR
 NEW.plan_id IS NOT OLD.plan_id OR NEW.plan_version IS NOT OLD.plan_version OR
 NEW.currency IS NOT OLD.currency OR NEW.currency_scale IS NOT OLD.currency_scale OR
 NEW.amount_minor IS NOT OLD.amount_minor OR NEW.is_test IS NOT OLD.is_test OR NEW.terms IS NOT OLD.terms OR
 NEW.request_key IS NOT OLD.request_key OR NEW.request_hash IS NOT OLD.request_hash OR NEW.created_at IS NOT OLD.created_at OR
 (OLD.status<>'pending' AND (NEW.status IS NOT OLD.status OR NEW.paid_at IS NOT OLD.paid_at))
 BEGIN SELECT RAISE(ABORT,'immutable invoice terms'); END;
-- statement
CREATE TRIGGER payment_accounts_immutable_identity BEFORE UPDATE ON payment_accounts WHEN
 NEW.id IS NOT OLD.id OR NEW.provider IS NOT OLD.provider OR NEW.is_test IS NOT OLD.is_test OR
 NEW.config IS NOT OLD.config OR NEW.created_at IS NOT OLD.created_at
 BEGIN SELECT RAISE(ABORT,'immutable payment account identity'); END;
-- statement
CREATE TRIGGER invoices_immutable_delete BEFORE DELETE ON invoices BEGIN SELECT RAISE(ABORT,'immutable invoice'); END;
