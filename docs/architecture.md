# Pulse VPN Node architecture baseline

Status: implemented node baseline, 2026-10-30. The executable covers the agent/core lifecycle,
authenticated API, VLESS + REALITY/Vision, live users, traffic accounting and buffered telemetry.
User requirements take precedence.

The current startup slice reads a bounded, validated JSON file through `internal/config` into the
`internal/node` model; `internal/mihomo` renders it for the pinned core. There is no raw core-config
passthrough. No file means idle; an invalid file fails startup. File edits require an explicit restart
in startup-file mode. Optional PostgreSQL management now persists users, revisions and idempotent
operations, and reconciles user changes through live reload. The file seeds users only once;
transport changes still require restart. Managed nodes collect approximate traffic totals. Resource
telemetry, configured network probes and a 15-minute RAM stream are implemented; Pulse ingestion is
separate work. See [traffic accounting](traffic.md) and [telemetry](telemetry.md) for current contracts.
See [local setup](local-reality.md) and [live users](live-users.md).

## Responsibilities and deployment

Pulse owns the operator UI, fleet management, and global policy. This node exposes an authenticated
API and applies its locally persisted users/settings to Mihomo. Client VPN traffic travels directly
through the VPS. An unavailable Pulse control panel must not terminate established node service.
The agent also collects VPS utilization and outbound network health. Pulse owns monitoring history,
graphs, alerts, and notification delivery; the node exposes current observations through its API.

Start with Go plus a separate Mihomo executable in one container and PostgreSQL in another Compose
service. The agent owns child startup, reload, failure reporting, and shutdown. Docker/Compose owns
container restart and PostgreSQL lifetime. Do not put a Docker client, host bootstrapper, Redis,
frontend, or a second public administration panel in this service.

The root Compose is the complete supported node, including user management, accounting and
telemetry. Feature-specific Compose overlays are not part of the deployment interface. Development
changes only source building, published loopback ports and restart policy. Integration tests inherit
the same runtime services, substituting disposable credentials and isolated clients/targets with no
published ports. Persistent settings belong outside the image; test fixtures stay under tests.

The management API can bind inside the container but is published on host loopback for SSH access.
The Mihomo controller stays on container loopback. PostgreSQL is reachable only on the Compose
network. Publish only the intended VPN listener publicly. Do not add TUN, NET_ADMIN, privileged
mode, or host networking merely because Mihomo supports them; select networking when real port and
client-address requirements justify it. Run with the least privileges needed for the chosen ports.

## Noninteractive initialization

`pulse-vpn-node init` prepares the four external files consumed by the main Compose:
`api-token`, `database-password`, `database-url`, and `vless.json`. Pulse chooses the
image, REALITY destination/SNI and port mapping; its provisioning worker will invoke this command.
That Pulse integration is separate work. Init only touches its configuration directory: no Docker socket, service startup, database
connections, migrations, DNS requests or TLS probes.

`internal/bootstrap` owns this filesystem boundary and cryptographic generation. It uses the
same token/VLESS validators as runtime startup. Fresh configuration has no users; Pulse adds users
through the API after the full stack is ready. Existing valid files are immutable to init, including
keys, initial users and permissions. Explicit conflicting settings or inconsistent database
credentials fail before any configuration is published. Either missing database credential file
can be reconstructed from the other; losing both is not recoverable against an existing SQL volume.

The directory is private (0700); new files are 0644 so Docker's bind-mounted secrets remain readable
by the non-root runtime UID. Init rejects symlinks/nonregular files, bounds reads, serializes runs with
a kernel lock, and publishes complete files without replacement. Publication is atomic per file,
not a four-file transaction: retries finish interrupted initialization without rotating persisted
secrets. Deployment upgrades, transport edits and secret rotation are separate operations.

Integration includes offline init/repeat/conflict checks and boots the unchanged main Compose from
those generated files, using ephemeral loopback host ports. The other integration scenarios keep
their isolated test overlay and no published ports.

## Package map

Create these paths incrementally as working code is introduced:

```text
cmd/pulse-vpn-node/main.go   process entrypoint and exit code
internal/app/              dependency wiring, startup and shutdown ordering
internal/bootstrap/        offline init, secret preservation and atomic file publication
internal/config/           agent settings and validation, not VPN config generation
internal/node/             users, desired revisions, operations, reconciliation policy
internal/mihomo/           config rendering, controller adapter, child lifecycle
internal/traffic/          observations, attribution, deltas and collector loop
internal/telemetry/        VPS/container/process metrics, network probes, current snapshots
internal/postgres/         concrete SQL queries, repositories and transaction boundaries
internal/httpapi/          auth, request/response DTOs, routes and error mapping
internal/postgres/migrations/ reviewed versioned SQL migrations
api/openapi.yaml           external HTTP contract
compose.yaml               full node + PostgreSQL + one-off migration
compose.dev.yaml           local build, loopback ports and restart policy
tests/integration/compose.yaml disposable peers/fixtures over the main Compose
deploy/                    non-secret configuration examples
Dockerfile                 reproducible agent + Mihomo image
docs/                      maintained architecture and operational documentation
.agents/skills/            narrowly routed project workflows
```

Keep unit tests next to code (`*_test.go`) and small synthetic fixtures in package `testdata/`.
Put cross-component tests in `tests/integration/` only when they need a real core or PostgreSQL.
Do not create every directory now, create a public `pkg/` SDK, or split Go modules for these packages.

`app` imports and wires the packages. `httpapi` calls node commands and traffic queries through
concrete services or small consumer-owned interfaces. `node` and `traffic` do not import SQL drivers,
HTTP handlers, or the Mihomo adapter. `postgres` implements their storage contracts; `mihomo`
implements their runtime/observation contracts. Shared domain concepts belong to the package that
owns them, not a universal model package. Split `node` further only when actual code warrants it.

`telemetry` owns collection and snapshot semantics, independently of user traffic accounting.
`app` wires its OS collectors and narrow runtime-health inputs; `httpapi` reads its snapshots.
It does not own fleet history, alert delivery, SQL time-series storage, or core configuration changes.

Use named files such as `users.go`, `reconcile.go`, `render.go`, `controller.go`, `process.go`,
and `checkpoints.go` within these packages. File names do not require matching subpackages.

## Initial technical choices

### Core adapter boundary

`internal/node/core.go` defines the shared `Core` startup and `Runtime` lifecycle contracts.
`Core.Start` accepts the node's VLESS settings (nil means idle) and transfers ownership of a runtime
to the caller. Errors must be sanitized; unsuccessful startup must clean up partial resources.
The settings are read-only input, not a mutable configuration shared with a background process.

The serving lifecycle in `internal/app/app.go` uses only that contract: readiness, termination
notification and bounded stop/reap. It obtains the actual version from the adapter, rather than
assuming Mihomo's pinned version. HTTP status remains core-independent and readiness still does
not claim end-to-end connectivity. `internal/app/wiring.go` is the concrete composition point.

`internal/mihomo.Adapter` is the only implemented adapter. It keeps the controller address/protocol,
version validation, config rendering, child process and listener-ownership checks within Mihomo.
A future sing-box adapter must implement the same contracts and its own compatibility tests, then
be wired and packaged explicitly. Its binary/settings and selection belong to composition and
deployment; there is no automatic discovery, runtime switching, sing-box stub or silent fallback.
The existing `PULSE_NODE_MIHOMO_BINARY` setting remains Mihomo-specific.

The optional `node.UserRuntime` contract now provides live user replacement and targeted revoked
session sweeps; `node.UserStore` persists intent independently of the adapter. The application owns
one reconciler and repeats cleanup after late observations. Failed/ambiguous reloads keep latest
cleanup intent separate from last verified revision, including during database outages. No automatic
rollback may re-enable revoked users. The separate `traffic.Source` capability reads bounded cumulative
connection counters. An application-owned `traffic.Collector` persists them through `traffic.Store`;
database faults degrade collection without stopping the core or user reconciler.
Implementing `Runtime` alone does not imply live-user or accounting support.

### Libraries and operations

- Prefer standard `net/http`, explicit constructors, `log/slog`, and Go's test tooling.
- Use pinned `pgx`/`pgxpool` and explicit SQL for PostgreSQL. The `migrate` command applies reviewed
  embedded versioned SQL with a transaction lock and checksum validation; no automatic schema sync.
- Select the supported Go version at implementation time and keep module, CI, and image versions
  consistent. Record exact Mihomo version, download checksums, and compatibility evidence.
- Use OpenAPI-ready request/response types. Do not expose internal database structs or raw core
  configuration as the public API. Version routes under `/api/v1`.
- Keep an operation/revision response for changes requiring reconciliation; return a durable
  acknowledgement separately from eventual runtime success. Avoid a broker while one local worker
  and PostgreSQL provide the needed serialization and recovery.

These are defaults for this repository, not universal Go rules. A dependency or new package needs
a specific problem to solve; hypothetical future cores do not justify a plugin framework now.

## Desired state and runtime state

In one transaction, save desired data, its revision, and a recoverable operation. A worker owns
application to Mihomo; it works from an immutable revision snapshot, never a mix of concurrent edits.
Keep desired revision, last verified applied revision, and current runtime health distinct. Guard
operation completion by the revision it actually applied. A successful older operation cannot mark
a newer revision applied. Keep pending intent recoverable after agent/container restart.

Render a candidate from the snapshot, validate using the pinned core, then apply via its controller.
Serialise runtime changes and verify relevant postconditions before marking applied. An HTTP success
alone is insufficient: listener creation can fail. Preserve the last verified working configuration;
attempt bounded recovery on failure and verify the recovery itself. Never silently roll back newer
desired database state or claim that an old applied revision means the current runtime is healthy.

SQL transactions do not extend across a controller call. If the controller succeeds but saving the
result fails, reconciliation must recover without duplicating operations or reporting false success.
Idempotency, retries, and applied-state recovery belong to this explicit boundary.

For revocation, first remove credentials from the active configuration, then close matching tracked
connections. Late-visible sessions accepted before application require subsequent sweeps. Keep the
revoked identity available for attribution and sweeping; distinguish credential removal from session
cleanup. A single empty sample is not proof of a drain barrier. Report incomplete cleanup honestly
and keep retrying within the operation policy; do not automatically restart all clients to satisfy
a status flag. Strict immediate revocation would need an explicitly chosen stronger mechanism.

Recovery must not blindly restore an artifact that re-enables known-revoked credentials. Reconcile
a validated candidate honoring current revocations; report failed recovery if no safe candidate is
available. Give attempts deadlines and capped backoff. Exhausting an attempt leaves cleanup durably
pending/degraded and eligible for later reconciliation; elapsed time alone cannot mark it complete.

## Storage and approximate traffic

PostgreSQL holds local desired users/settings, operation state, applied revisions, traffic totals,
and collection checkpoints. The materialized core config is a runtime artifact with restricted
permissions. Persistence and ownership of each secret must be specified when its schema is added;
hash verification-only API tokens, and keep recoverable VPN credentials protected and out of normal
read responses. Do not invent reversible encryption without specifying external key management.

Per-user traffic is approximate: closed connections can vanish between samples. Persist observed
bytes without fabricating missing traffic or promising strict quotas. Node-wide totals are separate
and must not be redistributed among users. Keep up/down direction explicit in the API.

A checkpoint identifies core process generation, connection ID, stable user ID, and the last
committed counters. Commit increments and checkpoint advancement together. A failed commit must not
advance the in-memory baseline. Retried snapshots and overlapping polls must not double count.
Discard stale/out-of-order samples rather than lowering a checkpoint; an older snapshot is not a
core reset. Preserve observation ordering when collection and persistence run concurrently.

On first observation of an attributed connection in a confirmed generation, count its cumulative
values from zero. Reuse durable checkpoints across an agent-only restart if the core is provably the
same instance. A new core process is a new generation; PID alone is insufficient. Handle unexpected
counter decreases and uncertain continuity explicitly, favouring a documented conservative
rebaseline over invented precision. Never turn a reset into a negative increment or huge overflow.
If a connection disappears, keep the last committed total without estimating its final bytes;
prune checkpoints only under a documented retention policy that accounts for temporary reappearance.

The intended default is one agent owning one child. If the agent dies, the container lifecycle should
not leave an orphan child or launch a duplicate on restart. Adoption/recovery tests must nevertheless
not equate agent restart with confirmed core restart. A transient DB outage should leave the last
working core serving; reject mutations that cannot be persisted and expose degraded management health.
Approximate accounting may lose observations during outages; do not trade that allowance for double
counting already committed traffic.

The current implementation polls every two seconds after each attempt and stores totals plus
checkpoints in migration `002_traffic.sql`. User `name` is currently an immutable, non-reusable key;
renaming and hard deletion are unsupported. A future display name must not replace this attribution
key. A fresh cryptographically random generation is allocated only after starting a new owned child.
No orphan adoption or agent-only continuation is implemented. Snapshot sequence orders observations
within that generation. See [traffic accounting](traffic.md) for pruning, reset and capacity semantics.

## VPS telemetry and network checks

Collect measurements in application-owned background loops with bounded work, configurable intervals,
timeouts, and concurrency limits. API reads return the latest snapshot rather than triggering shell
commands or probes. Keep the latest snapshots, rate baselines, and a bounded 15-minute ring buffer in
memory. Pulse polls the authenticated API, catches up from the buffer, and stores history centrally.
Do not add a local monitoring history database or an unbounded backlog while Pulse is unavailable.
Gaps remain gaps, not fabricated healthy samples.
Local durable VPN traffic totals and accounting checkpoints retain their separate storage contract.

The initial measurement scope is:

| Group | Observations |
| --- | --- |
| VPS resources | CPU utilization and core count, load average, RAM available/used/total, swap |
| Storage | Capacity/free space and inode availability for explicitly identified filesystems; disk I/O where available |
| Network interfaces | Receive/transmit counters and rates for explicitly selected interfaces, with clear direction |
| Runtime | Separate VPS/agent/core uptime, core state/version/restarts, active connection count, applied revision |
| Dependencies | PostgreSQL reachability and collector/application errors, without secrets |
| Outbound connectivity | ICMP RTT/loss, DNS resolution, TCP/HTTPS reachability and timing to configured targets |

Each measurement/result needs its observation time, units, scope (`host`, `container`, or `process`),
and availability/freshness. Identify the interface, filesystem, process, or probe target where relevant.
Report missing permissions, unsupported sources, failed collection, and stale last-success values
explicitly; do not substitute zero or refresh a successful sample's timestamp on failure. Document
CPU normalization and sampling windows. Counter resets or insufficient samples produce an unavailable
rate until a valid baseline exists, not a negative rate or a spike. Do not sum overlapping virtual
interfaces or confuse host network counters with Mihomo's per-user VPN traffic totals.

Container visibility is a deployment concern. Do not label container filesystem, network, cgroup, or
process data as whole-VPS utilization. Select and document minimal host metric sources and read-only
mounts where needed; a mount alone does not necessarily expose host network namespace statistics.
Do not grant privileged mode, mount the Docker socket, or expose host control just for monitoring.
If a host metric cannot be obtained with the chosen access, expose it as unavailable. ICMP may need
specific socket permissions/capabilities; distinguish inability to run it from measured packet loss.

Use configured probe targets with bounded responses, frequency, and deadlines. Do not expose an API
for arbitrary URL fetching or shell execution. Checks must use the intended direct node egress and
record their vantage point; they must not silently use a configured VPN proxy. Keep ICMP, DNS, TCP,
and HTTPS outcomes separate: blocked ICMP does not alone prove an internet outage. Probe failures
report degraded observations and never trigger a core restart or block unrelated user operations.

Three vantage points remain distinct:

- Node to internet: the agent performs outbound checks.
- External observer to node: Pulse or another probe location checks management/public reachability;
  a stopped or unreachable agent cannot report its own outage.
- VPN client through node to internet: an external real-client probe checks the complete VPN path.
  A successful ping, TCP listener check, or local core process check cannot substitute for this.

### Latest snapshots and bounded catch-up

Latest-snapshot reads and buffered imports serve different needs. `GET /api/v1/system/stats` and
`GET /api/v1/connectivity` return ready in-memory snapshots without waiting for new measurements or
database access. `GET /api/v1/telemetry/samples` returns retained records after a cursor in bounded
pages. These authenticated routes are implemented in `api/openapi.yaml`; see [telemetry](telemetry.md)
for the precise fields, visibility limits and safe deployment configuration.

Use one ordered telemetry stream for completed resource samples and probe results. Give every agent
run a new unique ID and every appended record a strictly increasing sequence within that run.
Records carry their measurement time/window separately; timestamps and PIDs are not deduplication
keys. A failed collection/probe is an observation too, with its own outcome; do not turn a cached
old success into a new successful measurement. Publishing a snapshot and appending its observation
must preserve a consistent identity even when collectors finish concurrently.

Retain records for 15 minutes in RAM, with hard record-count and byte limits. Evict oldest records
when a limit is reached; limits may shorten the available window under load. Base retention on elapsed
time rather than wall-clock ordering. Reads never consume or acknowledge records. Return the run ID,
available sequence bounds, continuation cursor, and whether more data is available. Capture a coherent
page under concurrent append/eviction, and bound serialization work without holding collection locks
through network writes. Latest snapshots remain separately available with their actual age.

If a cursor predates retained records, explicitly report a gap and the earliest available position.
If it belongs to a previous run, report a run change, not a continuous stream. Pulse records that
discontinuity and resumes from the available data; never silently skip to the newest sample. A cursor
ahead of the current stream is invalid, not permission to advance an import position. An agent restart
loses the RAM buffer. No disk spool, WAL, SQL persistence, or lossless-delivery guarantee is planned.

Pulse owns durable import progress. Deduplicate records by stable node identity, agent-run ID, and
sequence, and commit imported records, any reported gap, and the next cursor in one transaction.
Retries may repeat pages but must not duplicate measurements or downstream aggregates. Serialize
imports per node or conditionally advance the cursor so overlapping/late responses cannot move it
backward or replace a newer run's progress. On failed or uncertain commits, recover from Pulse's
durable cursor and retry safely. This is an integration contract; its implementation stays in Pulse.

For example, sampling every 5 seconds and polling every 30 seconds transfers six measurements per
successful poll, rather than only the latest. A short Pulse outage can be filled from retained data;
an outage beyond retention, early capacity eviction, or an agent restart leaves an explicit gap.
These intervals illustrate the protocol, not mandatory collection settings. Collection and snapshot
reads continue during Pulse or local PostgreSQL outages and must not affect the serving VPN core.

Verify scope labeling, missing permissions, stale/partial samples, counter resets,
probe cancellation/timeouts, and database/Pulse outages using local fixtures and disposable targets.
Also verify catch-up pagination, retry deduplication, atomic import/cursor recovery, concurrent polls,
time/count/byte eviction, agent-run changes, and explicit gaps. Check that snapshot reads never wait
for collectors and that buffer use remains bounded while Pulse is offline.
Do not probe live VPSs or public targets as part of documentation work or routine tests.

## Alternatives and scope

Embedding Mihomo as a library would couple this project to internal core APIs. A separate executable
fits the currently accepted approximate accounting and independently pinned core releases. Keep this
boundary unless a demonstrated requirement changes it. PostgreSQL was chosen for local persistence;
do not silently substitute SQLite or depend on the central Pulse database for each connection.

The first supported protocol is VLESS + REALITY over raw TCP. sing-box integration, other protocols,
exact billing, and a custom Mihomo fork are outside the initial scope. This node does not replace
client applications or generate a second management UI.

## Evidence and acceptance

Local checks on Mihomo 1.19.33 demonstrated user changes through config reload while established
TCP and UDP sessions survived. They also demonstrated missing per-user data for short connections
between snapshots. These observations are version-scoped, not guarantees for arbitrary releases,
transports, simultaneous new handshakes, or production networks.

Maintain reproducible local tests for config rejection/bind failure, concurrent revisions, adding
users with another user's connection open, revocation with late-visible sessions, core failure,
restart persistence, and accounting transaction/retry behavior. Use disposable credentials, ports,
PostgreSQL, a local TLS target, and real client/core binaries. No production endpoints or captured
customer configurations are test fixtures. A new core version needs this compatibility check before
being accepted into the image.

## Sources

- [Go module layout](https://go.dev/doc/modules/layout): basis for `cmd` and `internal`.
- [Go conventions](go-conventions.md): language and verification guidance.
- [Mihomo config API, v1.19.33](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/hub/route/configs.go).
- [Mihomo listener replacement, v1.19.33](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/listener/listener.go).
- [Mihomo connection snapshots, v1.19.33](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/hub/route/connections.go).
