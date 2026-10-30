# Live user management

Managed connection and user state require the node's own PostgreSQL database. A fresh managed node
may start idle with no VPN listener. Mihomo v1.19.33 remains the only adapter. The API commits desired state; an
application-owned reconciler validates and reloads the running core, without restarting its process.
Changing users recreates the VLESS listener internally: established unrelated TCP/UDP sessions are
tested for continuity, but a simultaneous new handshake may need to retry during listener replacement.

## Local acceptance

```sh
go test -tags=integration -count=1 -timeout=30m -v ./tests/integration -run '^TestNode/(Users|ManagedLifecycle|InitIdle)$'
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

The root `compose.yaml` includes the private PostgreSQL service, persistent `node_database` volume,
and explicit `migrate` service. See the [README](../README.md#configure-a-persistent-node) for the
single setup path and secret files. Local development adds only `compose.dev.yaml`; no user-management
overlay is needed. PostgreSQL has no host port.

Migration applies reviewed embedded SQL (`001_users.sql`, `002_traffic.sql`, `003_node_lifecycle.sql`) transactionally and
verifies recorded checksums. Normal agent startup never creates schema. Future changes need new
versioned migrations. Do not remove the database volume to restart a node. Initial users are imported
exactly once; the connection is imported once without regenerating REALITY material. Later file edits
do not reset SQL state, rotate keys or re-enable disabled identities. In managed mode SQL owns the
singleton connection; changing its transport after creation is unsupported.

## Authenticated API

All routes require the existing bearer token; responses use `Cache-Control: no-store`.

| Route | Behavior |
| --- | --- |
| `GET /api/v1/users` | Global desired revision, visible user names/enabled state/credentialRevision and runtime status; no UUIDs |
| `PUT /api/v1/connection` | Create the single raw connection; returns 202 with a shared revision operation |
| `PUT /api/v1/users/{name}` | Create a user or change enabled state; returns 202 with an operation |
| `DELETE /api/v1/users/{name}` | Remove visible access, retain accounting and revoked identity history |
| `POST /api/v1/users/{name}/rotate` | Replace credential explicitly, preserving enabled state and totals |
| `GET /api/v1/operations/{revision}` | Read queued/applied/failed/superseded outcome |

PUT requires `Idempotency-Key` (1–128 printable ASCII characters) and a bounded JSON body containing
`enabled` and `expectedRevision` from the users response. Creation also requires a nonzero lowercase
canonical `uuid`. For an existing visible user omit `uuid`; ordinary updates cannot rotate credentials.
A maximum of 1,024 visible identities includes disabled users. Disable/re-enable with `enabled`.
DELETE requires `{ "expectedRevision": N }`. Rotation requires
`{ "expectedRevision": N, "uuid": "new-lowercase-uuid" }`. Both require `Idempotency-Key`.
Deleted users disappear from users/traffic reads, but remain as accounting tombstones. Recreating
the same name requires a fresh UUID and retains its historical totals. Previously retired UUIDs
cannot be reused by any user. Rotating to the current UUID is a safe no-op with a new revision.
`credentialRevision` identifies the global revision that created the current credential; imported
credentials use baseline 1. It does not change on ordinary updates or same-credential rotation retries. Names remain stable accounting
keys; display-name changes belong to the caller. Client-profile export is outside this API.

Credential generations use separate internal runtime names. Retirement sweeps close the old
credential's sessions while leaving the replacement credential and unrelated users alone. All
runtime names remain mapped to the same stable accounting name, including late-observed sessions.
History is bounded at 65,536 credentials: exhaustion rejects new credentials without forgetting
revocations. Back up the SQL volume to preserve this history.

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
or retired credential generations. `cleanupState` is `pending`, `retrying`, or `observed_empty`; none promises an instantaneous
drain barrier. Bounds are 8 MiB / 16,384 connections per snapshot and 128 closes per sweep. Oversized,
malformed or incomplete snapshots report retrying rather than success. Sweeps continue during DB
outages using the latest attempted cleanup intent. Ambiguous application remains retrying; an older
verified snapshot must not keep disconnecting a re-enabled identity.

Mihomo closes tracked outbound UDP state but may retain an authenticated inbound XUDP/mux parent.
The generated configuration therefore also authorizes egress by exact active `IN-USER` rules with a
terminal `REJECT`; revoked identities cannot recreate working outbound flows after cleanup. The
configured REALITY handshake target alone uses explicit `proxy: DIRECT`. Hidden parent connections
may remain physically open; this is access denial, not proof of transport drain. Transport conversion and quota APIs are absent. Approximate traffic totals are available through
`GET /api/v1/traffic`; see [traffic accounting](traffic.md).

## Public connection descriptor

`GET /api/v1/connection` uses the same bearer authentication and `Cache-Control: no-store`
as the other management routes. It returns `{"schemaVersion":1,"connection":null}` when no
verified applied connection exists. Otherwise `connection` contains `protocol: "vless"`,
`transport: {"type":"raw","flow":"xtls-rprx-vision"}`, the internal `listenPort`, and
`reality: {"publicKey": "…", "shortIds": ["…"], "serverNames": ["…"]}`.
The public key is derived with Go X25519 from validated applied material, encoded as unpadded
base64url. Private keys, user identities/UUIDs and REALITY destination are never included.
The descriptor is published only after the common reconciler verifies the applied connection, and
stays stable across live user reconciliation. It does not prove external reachability. Pulse must supply the public node address and any port
mapping. Invalid embedded startup settings fail closed with a generic 503; normal startup
configuration loading already rejects invalid settings. See `api/openapi.yaml` for the contract.

## Idle initialization and first connection

Use `pulse-vpn-node init --config-dir /config --idle` for managed installation. It writes the usual
secret files and a `vless.json` containing JSON `null`. Start the standard Compose normally: the
Mihomo controller is healthy, `GET /status` reports `ready` and `vpnConfigured: false`, and users
remain readable. `capabilities` advertises `connectionCreate`, `userDelete`, and `userRotate` for
managed instances. File-only instances report false.

User mutations while the managed connection is absent return 409; users/revision reads stay
available. Create the connection using the latest users revision and an idempotency key:

```json
{
  "expectedRevision": 1,
  "transport": { "type": "raw" },
  "listenPort": 8443,
  "reality": { "destination": "target.example:443", "serverNames": ["target.example"] }
}
```

The API generates and stores REALITY secrets once. Poll the returned operation and confirm the
runtime revision before using the descriptor. An identical create with a fresh expected revision
retains all connection material; different settings return 409. Conversion, multiple listeners and
XHTTP are unsupported. The container port must match the deployment's published port mapping.
The agent's actual management port and the adapter's controller port are rejected with HTTP 400
before storing any intent or advancing the revision. An unrelated occupied port can still fail
asynchronously when the reconciler attempts to bind it.
Failed application keeps durable intent for bounded retries; it never restores revoked users or
restarts the shared core as a normal user operation. Status reports observed core readiness; an uncertain controller application degrades it, while a
rejected candidate or database control outage does not falsify an otherwise healthy serving core.
Operation and users.runtime fields report desired-state/application/cleanup failures separately.
Managed startup brings up an idle core first, then reconciles SQL intent. Even when a first
creation failed and its port remains occupied after restart, management stays available and
`GET /connection` stays null. Releasing the port allows the same revision to apply without a
new creation request or core restart. The startup file does not override SQL state.
Database loss is not recovered by regenerating keys from the idle file.
