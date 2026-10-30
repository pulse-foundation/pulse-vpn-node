-- VPN UUIDs are recoverable credentials. This private node database and its
-- backups need restricted access; API read DTOs must never expose this column.
CREATE TABLE node_user_state (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 initialized boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
 applied_revision bigint NOT NULL DEFAULT 0 CHECK (applied_revision >= 0 AND applied_revision <= revision)
);
INSERT INTO node_user_state (singleton) VALUES (true);

CREATE TABLE node_users (
 name text PRIMARY KEY CHECK (name ~ '^[a-zA-Z0-9_-]{1,64}$'),
 uuid uuid NOT NULL UNIQUE CHECK (uuid <> '00000000-0000-0000-0000-000000000000'),
 enabled boolean NOT NULL
);

CREATE TABLE node_user_operations (
 revision bigint PRIMARY KEY CHECK (revision > 0),
 idempotency_key text UNIQUE CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 request_hash bytea CHECK (octet_length(request_hash) = 32),
 state text NOT NULL CHECK (state IN ('queued', 'applied', 'failed', 'superseded')),
 error text NOT NULL DEFAULT '',
 CHECK ((idempotency_key IS NULL) = (request_hash IS NULL))
);
