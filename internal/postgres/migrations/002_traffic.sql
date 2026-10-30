-- One agent owns one confirmed core generation. These counters are approximate
-- payload observations; they are neither billing records nor quota enforcement.
CREATE TABLE node_traffic_state (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 generation text NOT NULL DEFAULT '' CHECK (octet_length(generation) <= 128),
 sequence bigint NOT NULL DEFAULT 0 CHECK (sequence >= 0),
 observed_at timestamptz,
 pruned_before timestamptz,
 reset_count bigint NOT NULL DEFAULT 0 CHECK (reset_count >= 0),
 skipped_connections bigint NOT NULL DEFAULT 0 CHECK (skipped_connections BETWEEN 0 AND 16384)
);
INSERT INTO node_traffic_state (singleton) VALUES (true);

CREATE TABLE node_traffic_totals (
 user_name text PRIMARY KEY REFERENCES node_users(name),
 upload_bytes bigint NOT NULL CHECK (upload_bytes >= 0),
 download_bytes bigint NOT NULL CHECK (download_bytes >= 0)
);

-- Generation is implicit in the locked singleton. Changing it atomically clears
-- all checkpoints while preserving user totals and cumulative reset diagnostics.
CREATE TABLE node_traffic_checkpoints (
 connection_id text NOT NULL CHECK (octet_length(connection_id) BETWEEN 1 AND 128),
 user_name text NOT NULL REFERENCES node_users(name),
 started_at timestamptz NOT NULL,
 upload_bytes bigint NOT NULL CHECK (upload_bytes >= 0),
 download_bytes bigint NOT NULL CHECK (download_bytes >= 0),
 last_seen timestamptz NOT NULL,
 PRIMARY KEY (connection_id, user_name)
);
CREATE INDEX node_traffic_checkpoints_last_seen ON node_traffic_checkpoints(last_seen);
