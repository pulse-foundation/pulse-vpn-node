# Approximate per-user traffic

Traffic accounting is available in PostgreSQL-managed mode. It uses the existing pinned Mihomo
v1.19.33, with no core patches, process reloads, or additional public controller ports.
Run the explicit `migrate` command before starting the updated agent (the managed Compose migration
service does this). Migration `002_traffic.sql` adds totals and checkpoints without modifying `001_users.sql`.
Use the same local managed setup described in [live users](live-users.md).

## API

`GET /api/v1/traffic` requires the usual node bearer token. It reads PostgreSQL, never initiates a core
poll. The response contains at most 1,024 users, including disabled users. It exposes neither VPN
credentials nor connection destinations. Byte totals and `resetCount` are decimal strings so JavaScript
consumers do not silently lose integer precision. The OpenAPI contract is in `api/openapi.yaml`.

```json
{
  "approximate": true,
  "pollIntervalSeconds": 2,
  "state": "collecting",
  "stale": false,
  "observedAt": "2026-10-30T12:00:00Z",
  "users": [{"name": "example", "enabled": true, "uploadBytes": "1024", "downloadBytes": "8192", "lastActivityAt": "2026-10-30T11:59:58Z"}],
  "resetCount": "0",
  "skippedConnections": 0
}
```

Upload means client to destination; download means destination to client. These are Mihomo-observed
TCP/UDP payload bytes, not VPS interface traffic or a billable wire-byte total. The direction comes
from the pinned [Mihomo trackers](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/tunnel/statistic/tracker.go).

`observedAt` identifies the last successfully persisted sample; `null` means no sample for the current
core generation. `state` is `starting`, `collecting` or `degraded`. A failed poll/commit reports a generic
error without refreshing the observation time. `stale` becomes true if there is no saved sample or it
is older than ten seconds. Recent saved data can remain non-stale while the collector reports degraded.
An unavailable database or unconfigured accounting returns HTTP 503, not zeros. Core readiness remains
separate on `/api/v1/status`.

Each user's `lastActivityAt` is the latest committed observation time at which an accepted
upload or download byte delta was positive. A newly observed connection with positive counters
also counts; its timestamp is when Node observed it, not when those packets were sent.
Idle/zero-byte connections, discarded samples, unknown attribution and pure counter rebaselines
do not advance it. A positive delta in either direction counts even when the other direction resets.
The value is an RFC3339 timestamp or explicit `null` when no activity has been recorded. It is
independent of the snapshot-wide `observedAt` and does not indicate online/offline status.

Migration `007_last_activity.sql` adds the nullable field without reconstructing historical activity
from old totals or checkpoints. Apply migrations through the normal managed Compose upgrade.
The date is committed atomically with traffic totals and survives core/container restart, checkpoint
pruning, credential rotation and disabling access. Like totals, a deleted identity's date remains
hidden and is retained if that stable name is recreated. Backward clock changes never lower it.
Logical backups now use schema 7; restoring a schema-6 backup is supported with unknown activity.
Older Node versions cannot restore schema-7 backups or operate on the upgraded database.

The same user field is included in the heartbeat's `trafficSnapshot` sent to Pulse. The panel must
ingest and display it; Node does not add UI. Treat it as approximate observed activity: background
traffic counts, shared credentials combine devices, short-lived connections may be missed, and
database/collection outages can delay or lose observations. `null` does not prove the user never
used VPN, especially for users that existed before this migration.

## Collection and recovery

One application-owned loop reads live connection counters, saves an ordered snapshot, then waits two
seconds. Slow/failed attempts increase the effective interval. Reads and writes have deadlines, and
shutdown cancels and joins the collector. No collector error restarts or stops the VPN core.

PostgreSQL serializes samples and atomically commits counter deltas, checkpoints and sequence. Duplicate
or older sequences do nothing; ambiguous commit retries reread committed state. There is no memory-only
byte baseline. On DB recovery, surviving connections contribute the difference from their last durable
checkpoint. Totals survive container/core restart; a confirmed new child gets a new random generation
and counts new connections from zero. The current lifecycle does not adopt an existing orphan core.
Starting a new collector against an unverified existing process is unsupported. One node process owns
each database; this is not multi-agent leader election.

The stable accounting key is `node_users.name`. Disabled identities retain their totals; deleted
identities remain hidden accounting tombstones. Credential generation runtime names map back to this
key, so rotation and late revoked sessions preserve totals. Recreating a deleted name with a fresh
UUID preserves history; retired UUIDs cannot be reused. Renaming and hard deletion are unsupported.
Unknown/empty attribution is skipped rather than
assigned to another user. A newer observation whose counter decreases adds zero in that direction and
replaces that checkpoint; a changed connection start time rebaselines both directions. `resetCount`
records these events. Integer overflow rejects the whole sample instead of wrapping totals.

## Deliberate limits

- Connections that close between polls, their final bytes, and sessions lost during a DB outage can
  be missed. No finite accuracy percentage is promised; short-lived workloads can be substantially undercounted.
- Snapshots are limited to 16,384 connections and 8 MiB. Oversized or malformed responses fail the
  whole poll and leave the last committed data intact.
- At most 131,072 checkpoints are kept. New connections beyond that cap are skipped while existing
  checkpoints continue advancing. Disappeared connections are retained for at least 24 hours.
- A monotonic cutoff, advanced only when checkpoints are actually pruned, prevents a forgotten connection from being counted from zero again.
  New-to-the-collector connections whose start predates that cutoff are conservatively skipped.
  Active tracked connections can live longer than 24 hours and continue accumulating normally.
- `skippedConnections` is the number skipped in the latest saved sample (unknown identity, cutoff
  or capacity); it is not a count of lost bytes or of connections never seen between polls.

This is operational visibility, not strict quotas or billing. VPS telemetry and its planned 15-minute
memory buffer are a separate subsystem and are not implemented by this collector.

## Local acceptance

```sh
go test -tags=integration -count=1 -timeout=30m -v ./tests/integration -run '^TestNode/Traffic$'
```

The disposable lab uses PostgreSQL and pinned Mihomo server/clients on internal Docker networks,
synthetic identities and local targets. It checks directional TCP/UDP accounting, independent users,
repeated reads/polls, database failure/recovery, disabled-user retention and container restart.
No existing VPN profile, production server, public target or host-published test port is used.
