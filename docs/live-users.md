# Live user management

User management is optional and requires the node's own PostgreSQL database plus startup VLESS
transport settings. Mihomo v1.19.33 remains the only adapter. The API commits desired state; an
application-owned reconciler validates and reloads the running core, without restarting its process.
Changing users recreates the VLESS listener internally: established unrelated TCP/UDP sessions are
tested for continuity, but a simultaneous new handshake may need to retry during listener replacement.

## Local acceptance

```sh
python3 tests/integration/users_smoke.py
```

This builds fresh local containers, generates disposable credentials, and publishes no host ports.
It verifies adding a second user, rejecting unknown credentials, closing only revoked sessions,
preserving another user's existing TCP and UDP traffic, denying every user when all are disabled,
and re-enabling access. It checks Mihomo PID and Linux start ticks stay unchanged. It also stops
PostgreSQL to verify mutations fail while VPN sessions survive, then restarts the agent to verify
persisted users override the initial seed file. Its database, networks and credential volume are
removed afterward. This does not prove mobile app or filtered public-network behavior.

PostgreSQL transaction/concurrency tests run with
`TEST_DATABASE_URL_FILE=/path/to/disposable-dsn go test -race ./internal/postgres`. They create and
remove their own random schemas; never supply a production DSN. Without this environment variable,
those database tests skip explicitly. The full local lab above provisions its own database.

## Configuration and migrations

`PULSE_NODE_DATABASE_URL_FILE` names a protected file containing a PostgreSQL connection URL. It is
never printed. Without this variable, startup-file mode works as before and management routes return
503. When configured, unavailable/unmigrated storage fails startup before launching the core. Once
serving, a database outage must not stop the core; management returns 503 and reconciliation retries.

Use one node process per database. Running multiple agents against the same database is unsupported:
SQL transactions serialize desired state, but no distributed runtime ownership lease is implemented.

The local `deploy/compose.managed.yaml` overlay provides a separate private PostgreSQL service,
persistent database volume and explicit `migrate` service. The database has no host port. Prepare
these files under a host-private `.local/` directory (mode 0700):

- `api-token`: the existing token from the README.
- `database-password`: a generated database password.
- `database-url`: matching URL for `pulse_node` database/user at `database:5432`, with the password
  URL-encoded and `sslmode=disable` only for this private local Compose network.
- `vless.json`: validated transport settings described in [local REALITY](local-reality.md), listening
  on `0.0.0.0:8443`. Its users are initial seed data only.

Container UID 10001 must be able to read its secret files. For local bind-based Compose secrets, files
may be mode 0644 inside the private 0700 parent directory; never loosen the parent or commit files.
Protect database volumes/backups: UUIDs are recoverable credentials stored in the private database,
without application-layer encryption. The REALITY private key remains in the mounted VLESS file.

```sh
docker compose -f deploy/compose.yaml -f deploy/compose.managed.yaml build node
docker compose -f deploy/compose.yaml -f deploy/compose.managed.yaml up --wait
```

The API and VPN ports are loopback-only, 18080 and 18443. Migration executes reviewed embedded
`internal/postgres/migrations/001_users.sql` transactionally and verifies recorded checksums. Normal
agent startup never creates schema. Future changes need new versioned migrations. Do not remove the
database volume to restart a node. Initial users are imported atomically exactly once; later file edits
do not reset users or re-enable disabled identities. Transport changes still require an agent restart.

## Authenticated API

All routes require the existing bearer token; responses use `Cache-Control: no-store`.

| Route | Behavior |
| --- | --- |
| `GET /api/v1/users` | Desired revision, user names/enabled state, current runtime status; no UUIDs |
| `PUT /api/v1/users/{name}` | Create a user or change enabled state; returns 202 with an operation |
| `GET /api/v1/operations/{revision}` | Read queued/applied/failed/superseded outcome |

PUT requires `Idempotency-Key` (1–128 printable ASCII characters) and a bounded JSON body containing
`enabled` and `expectedRevision` from the users response. Creation also requires a nonzero lowercase
canonical `uuid`. For an existing user omit `uuid`; identities cannot be renamed, rotated or reused.
A maximum of 1,024 identities includes disabled users. Disable instead of deleting; re-enable by
setting `enabled: true`. Credential generation/export and client profiles are outside this API.

A matching retry with the same idempotency key returns the original operation even if its expected
revision is now old. Reusing a key with different input, stale revisions and duplicate credentials
return 409. Invalid input returns 400, missing operation 404, unavailable management 503. An uncertain
HTTP/DB commit result should be retried with the same key and identical body, never a new key.

An operation is `applied` only after core reload and local version/listener checks. Failed attempts
remain retryable; they never roll back desired data or restart the shared core. Concurrent changes
may coalesce: skipped older operations become `superseded` after the latest revision applies. The
runtime's `appliedRevision` and `applyingRevision` distinguish this process's observations from durable
operation history. After restart the runtime observation is re-established, not inferred from SQL.

If the core applies a revision but saving its result fails, the reconciler retries that result before
applying a newer revision. Until persistence recovers, the current core keeps serving and existing
revocation sweeps continue; new desired changes remain pending rather than losing the verified result.

Revocation first removes credentials, then repeatedly closes only connections attributed to disabled
user names. `cleanupState` is `pending`, `retrying`, or `observed_empty`; none promises an instantaneous
drain barrier. Bounds are 8 MiB / 16,384 connections per snapshot and 128 closes per sweep. Oversized,
malformed or incomplete snapshots report retrying rather than success. Sweeps continue during DB
outages using the latest attempted cleanup intent. Ambiguous application remains retrying; an older
verified snapshot must not keep disconnecting a re-enabled identity.

Mihomo closes tracked outbound UDP state but may retain an authenticated inbound XUDP/mux parent.
The generated configuration therefore also authorizes egress by exact active `IN-USER` rules with a
terminal `REJECT`; revoked identities cannot recreate working outbound flows after cleanup. The
configured REALITY handshake target alone uses explicit `proxy: DIRECT`. Hidden parent connections
may remain physically open; this is access denial, not proof of transport drain. Transport, quota
and traffic accounting APIs are absent.
