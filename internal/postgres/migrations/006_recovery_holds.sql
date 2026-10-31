-- A restored snapshot cannot prove continuity of offline expiry latches. Holds
-- survive restarts and clock correction; only an explicit post-hold revision may
-- release them. Restore itself creates a fresh accounting/installation epoch.
ALTER TABLE node_users ADD COLUMN recovery_hold_id uuid;
ALTER TABLE node_users ADD COLUMN recovery_hold_revision bigint NOT NULL DEFAULT 0 CHECK (recovery_hold_revision >= 0);
ALTER TABLE node_user_state ADD COLUMN recovery_generation uuid;
ALTER TABLE node_user_state ADD COLUMN recovery_pending boolean NOT NULL DEFAULT false;
ALTER TABLE node_user_state ADD COLUMN recovery_request_id uuid;
ALTER TABLE node_user_state ADD COLUMN recovery_request_hash bytea;
ALTER TABLE node_user_state ADD COLUMN recovery_restore_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE node_users ADD COLUMN released_recovery_hold_id uuid;
ALTER TABLE node_users ADD COLUMN recovery_release_policy_revision bigint NOT NULL DEFAULT 0;
