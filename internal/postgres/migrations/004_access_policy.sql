-- Desired enablement is independent of the effective expiry latch. No TTL: the
-- control plane must be able to reconcile after any offline interval or deletion.
ALTER TABLE node_users ADD COLUMN policy_revision bigint NOT NULL DEFAULT 0 CHECK (policy_revision >= 0);
ALTER TABLE node_users ADD COLUMN expires_at timestamptz;
ALTER TABLE node_users ADD COLUMN expiry_enforced_revision bigint NOT NULL DEFAULT 0 CHECK (expiry_enforced_revision >= 0);
ALTER TABLE node_users ADD COLUMN renewal_revision bigint NOT NULL DEFAULT 0 CHECK (renewal_revision >= 0 AND renewal_revision <= policy_revision);
CREATE TABLE node_access_policy_events (
 revision bigint NOT NULL,
 name text NOT NULL,
 policy_revision bigint NOT NULL,
 credential_revision bigint NOT NULL,
 enforced_at timestamptz NOT NULL,
 PRIMARY KEY (revision, name)
);
