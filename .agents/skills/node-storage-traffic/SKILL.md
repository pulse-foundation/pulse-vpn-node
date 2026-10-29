---
name: node-storage-traffic
description: Use when changing Pulse VPN Node's PostgreSQL schema, migrations, durable desired state, traffic collection, counter checkpoints, or persistence recovery.
---

# Storage and traffic workflow

Read `docs/architecture.md` and `docs/go-conventions.md` from the repository root for ownership,
transaction, and accounting semantics. PostgreSQL is local node persistence; it is not the private
Pulse database. Traffic totals are approximate operational data, not a billing ledger.

## Persist intent and observations

- Save a desired-state change, its revision, and its recoverable operation in one transaction.
  Complete only the revision actually applied. Never hold a SQL transaction across a core call.
- For traffic, identify observations by core generation, connection ID, and stable user ID.
  Display names and an agent PID are not durable identity. Keep upload/download direction explicit.
- Read the last committed checkpoint, calculate nonnegative increments, and commit totals plus
  checkpoint advancement atomically. Serialize collectors or enforce concurrency with conditional
  writes/locking; overlapping samples must not both increment from the same baseline.
- On a failed commit, do not advance a memory baseline. When a commit outcome is uncertain, reread
  durable state before retrying. Retries must not blindly repeat an increment that may have committed.
- For a newly observed, attributed connection in a confirmed core generation, count cumulative bytes
  from zero. Preserve checkpoints over an agent-only restart when core continuity is proven.
  A confirmed core restart starts a new generation; a PID alone does not establish continuity.
- Treat decreases or uncertain continuity as an explicit reset/rebaseline case, never as negative
  traffic or unsigned overflow. Discard stale samples: 1,500 received after committed 2,000 must not
  lower that checkpoint or make the next 2,000 sample add 500. Establish ordering before diagnosing
  a genuine reset. Do not guess attribution for unknown users or spread global totals
  across users. Keep checkpoints long enough to handle temporary disappearance without recounting.

Closed connections may disappear before a sample. Keep their last committed totals and accept missing
final bytes. Do not add core patches, exact quotas, or an event-storage platform to hide this known
limitation. A DB outage should degrade collection/control without stopping a healthy serving core.

## Schema changes

Inspect existing schema and released migrations first. Add a reviewed, versioned migration; never
rewrite a released one. Use explicit parameterized SQL and database constraints for invariants.
Keep SQL and transaction implementation in `internal/postgres`, with contracts owned by consumers.
Document secret ownership and recovery when adding credential columns; use synthetic migration data.

## Verify recovery

Use disposable PostgreSQL for commit, rollback, constraint, migration, and concurrency tests. Examples
of meaningful cases are a failed write at 1,000 observed bytes followed by a successful 1,500-byte
sample (total 1,500), then an agent restart and a 2,000-byte sample on the same core (total 2,000).
Exercise an uncertain commit result, duplicate snapshots, a confirmed core restart, and user deletion
while old sessions remain attributable. Do not replace these checks with SQL-string assertions.

When core application or process identity changes, also load `node-mihomo-runtime`. Keep tests local
and independent of production databases, real profiles, and the operator's VPN client.
