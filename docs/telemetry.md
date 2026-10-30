# Node telemetry

The agent collects resource observations and optional outbound probes independently of user traffic
accounting. Latest snapshots and a bounded 15-minute stream live only in RAM. PostgreSQL outages do
not interrupt collection or these API reads; Pulse owns persistent history and import progress.

## Visibility and measurements

Every metric declares `scope` (`host`, `container`, `process`), `unit`, `observedAt` and `available`.
Gauges use `value`; cumulative integer counters use decimal-string `counter`. Missing values are null
with a safe `reason`, never invented zeros. `resource` names the selected interface, disk or filesystem,
without exposing filesystem paths. Rate measurements also include `windowSeconds`.

| Source | Measurements |
| --- | --- |
| Optional host proc files | CPU utilization/core count, load 1/5/15 minutes, total/available/used RAM, swap, host uptime |
| Selected host diskstats | Device read/write bytes and bytes/second, Linux sectors multiplied by 512 |
| Explicit network interfaces | RX/TX bytes and bytes/second, individually by interface and scope |
| Named local filesystems | Total/free/available bytes and inode totals/free/used |
| Container cgroup v2 | Current/maximum memory, CPU usage microseconds and used CPU cores |
| Agent process | Go heap/system allocations, serving uptime |
| Core/dependency observations | Core uptime/readiness/version, active trackers, applied user revision, PostgreSQL reachability |

Host CPU busy is normalized to 0–100% across observed cores, excluding idle/iowait. Cgroup CPU rate
is used cores (1.0 means one full core), not a host percentage or container quota percentage. The first
sample or a reset yields an unavailable rate. Missing sources invalidate their baseline. Network
interfaces are never summed, and these counters are not assigned to VPN users. Unbounded cgroup memory
(`memory.max=max`) is explicitly unavailable with reason `unbounded` rather than a fake limit.

Without a telemetry configuration, only container/process visibility is used; host metrics report
`not_configured`. The standard Compose configures read-only host proc sources. cgroup v1
is unsupported and missing v2 files remain unavailable. On Docker Desktop, a host proc mount describes
the **Linux Docker VM**, not the macOS/Windows machine. Native measurements target Linux VPS deployment.
Core uptime is elapsed time since this agent observed the new child start, excluding later readiness
waiting. Core restart count is zero within a run: child death exits the agent rather than restarting it.

## Configuration

Optional `PULSE_NODE_TELEMETRY_FILE` points to a strict JSON object of at most 16 KiB.
See `deploy/telemetry.example.json`. Defaults: resource/runtime interval 5s, probe interval 30s,
per-target timeout 2s, four probe workers, no external probe targets.
Allowed resource interval: 1–60s; probe interval: 5–300s; timeout: 100–5,000ms; workers: 1–4.
There are at most eight entries per resource selection and 16 probe targets. Invalid settings fail
startup with sanitized errors. Intervals are delays after completion, so slow attempts extend them.

The root `compose.yaml` mounts the example by default and exposes five individual read-only Linux
proc files. To customize settings, copy the example to `.local/telemetry.json` and set
`PULSE_NODE_TELEMETRY_CONFIG=./.local/telemetry.json`. The private parent directory stays 0700;
the settings file needs to be readable by container UID 10001. It adds no capabilities, privileged mode, host PID/network mode,
host root filesystem mount or Docker socket. Host proc and cgroup settings must refer to actual local
Linux proc/cgroup sources, never remote paths, FIFOs or arbitrary files.

Host networking requires a separate explicit read-only bind of `/proc/1/net/dev` to
`/host/proc/1/net/dev` and actual interface names in `hostInterfaces`. `/host/proc/net/dev` would follow
the observer's own network namespace and is deliberately not used. Missing permissions are reported.
Select actual device names in `disks`; partitions and whole devices overlap and should not be summed.

For host filesystem capacity, bind an **empty directory on that filesystem** read-only at a dedicated
path and configure that path with `scope: host`. This avoids exposing the host root and its files.
Filesystem observation supports known local filesystems only; remote/FUSE/unknown mounts and symlink
paths are rejected as unavailable before `statfs`. No mount is created by the agent. Do not configure
remote filesystems for monitoring. Scope labels follow the declared mount contract; a wrong deployment
mapping cannot be inferred or repaired from inside a container.

## Configured outbound checks

Probe entries are `{name, type, address}` with unique names. Types:

- `dns`: hostname resolution through the node's system resolver; IP literals are rejected. Local
  resolver/hosts caching may answer without an upstream query; this is resolution, not DNS-server health.
- `tcp`: direct `host:port` connection.
- `https`: verified TLS GET to a configured HTTPS URL. No userinfo, query, fragment or redirects.
  Success requires 2xx; response headers/body and total time are bounded. Proxy environment variables
  are ignored. No insecure TLS switch is provided.
- `icmp`: three IPv4 echo requests to a configured literal, using unprivileged Linux ping sockets.
  Results report average RTT of replies and loss percentage. Lack of permission is `unavailable`
  with null RTT/loss, not 100% loss. Linux `net.ipv4.ping_group_range` must permit GID 10001 if these
  checks are wanted; no raw socket capability is needed and the agent does not change sysctls.

Targets come only from deployment configuration. HTTP callers cannot trigger arbitrary probes or
supply URLs. Results expose target names/types, safe outcomes and timing, not raw URLs or errors.
All checks declare `scope: container` and `vantage: node_direct`: they follow the node namespace's
normal egress, with no application proxy. They do not prove inbound reachability or a working VPN path.
An empty target list publishes connectivity observations without checks; nothing is probed by default.

## APIs and catch-up

All routes require the existing bearer token and send `Cache-Control: no-store`:

- `GET /api/v1/system/stats`: `{system, runtime}` cached latest envelopes. Separate loops keep a slow
  core/DB health read from delaying resource observations.
- `GET /api/v1/connectivity`: cached latest connectivity envelope.
- `GET /api/v1/telemetry/samples?runId=<id>&after=<sequence>&limit=64`: retained ordered observations.
  Omit both `runId` and `after` for the first page. Subsequent requests pass `nextCursor` unchanged.

A latest envelope contains `runId`, `sample`, `ageSeconds`, `stale`. Before any observation its sample
and age are null. Samples preserve their measurement timestamps separately. Freshness is elapsed time
since publication; it becomes stale after three configured intervals. Failed measurements are new
failed observations; old successes are not republished with fresh timestamps. A slow collector can
therefore have fresh failure data, while a stopped collector's last snapshot becomes stale.

The stream uses a random 32-hex run ID and increasing sequence shared across `system`, `runtime` and
`connectivity`. Sequence/cursor fields are decimal strings, not JSON floating-point numbers. Publication
assigns the same identity to the latest snapshot and retained record. Reads never consume data.
Each page reports `firstSequence`, `lastSequence` (available bounds; zero if empty), `headSequence`
(last published), `nextCursor`, `hasMore`, `gap`, and `runChanged`.

Retention uses monotonic elapsed time, not measurement wall timestamps. Limits are 2,048 records,
4 MiB of serialized sample payload plus bounded object overhead, and 64 KiB per record. Capacity can
shorten retention; oldest records are evicted. Pages are capped at 128 records and 512 KiB. Latest
snapshots remain separately available with their age even after buffer eviction. Expiration is pruned
on publication or read; a stopped collector cannot return expired history.

An old cursor yields `gap: true` and starts at the earliest available record. A previous run yields
`runChanged: true` and starts at this run's earliest retained data, including a gap if it has already
evicted data. An ahead-of-stream cursor is HTTP 400. An empty retained buffer can still report a gap
and move `nextCursor.after` to `headSequence`. A restart discards the RAM buffer; there is no SQL/disk
spool or lossless-delivery guarantee.

Pulse must deduplicate by node/run/sequence and transactionally save records, discontinuities and the
cursor. Retry the same cursor after an uncertain import. Serialize imports or conditionally advance
the cursor so late replies cannot regress it or replace a newer run. **Pulse importer changes are not
part of this node implementation.** The local test simulates retry/deduplication, not a durable Pulse DB.

## Local verification

`python3 tests/integration/telemetry_smoke.py` builds an isolated Docker lab with real Linux metrics,
pinned Mihomo, PostgreSQL and local DNS/TCP/verified-TLS targets. It tests authenticated cache reads,
pagination/retry/dedup, explicit unavailable sources, continued collection during DB outage, unchanged
VPN sessions/core identity and restarted-run cursors. No host test ports or public/VPS targets are used.
Deterministic unit tests cover 15-minute elapsed eviction, count/byte caps, clock changes, stale data,
rate resets, concurrent publication/reads, cancellation and unavailable ICMP permissions.
