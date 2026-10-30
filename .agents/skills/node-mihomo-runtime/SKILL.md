---
name: node-mihomo-runtime
description: Use when changing Pulse VPN Node's Mihomo configuration, controller integration, child lifecycle, user access application, Docker packaging, or core compatibility tests.
---

# Mihomo runtime workflow

Read `docs/architecture.md` in the repository for package ownership and runtime-state semantics.
This workflow targets a separate core executable and VLESS + REALITY over raw TCP.

## Identify the affected boundary

| Change | Evidence needed |
| --- | --- |
| Render/validate | deterministic candidate, pinned-core validation, no live mutation during validation |
| Apply/revoke | immutable revision, serialized application, listener/access postconditions |
| Supervision | child identity, cancellation ownership, exit/reap behavior, bounded recovery |
| Container/core release | pinned inputs, private management ports, persistent state, compatibility test |

Start with the actual module, API client, and core version. Do not infer capabilities from another
core, panel, or release. On an empty checkout, document proposed interfaces instead of claiming they
exist. Keep render, controller, and process responsibilities separate inside `internal/mihomo`.

## Apply a revision

1. Take a coherent desired-state snapshot and identify its revision and core generation.
2. Render and validate a restricted-permission candidate; retain the last verified working artifact.
3. Apply through the controller under the single runtime writer. A successful HTTP response is only
   one observation: verify the expected listener and the access behavior affected by the change.
4. Persist completion for that specific revision. On failure, preserve desired intent, expose runtime
   health separately, and perform only bounded, verified recovery. Recover from a DB failure after a
   successful core change without falsely marking a newer revision applied.

For disabling users, remove credentials before closing their tracked connections. Keep revoked
identities in subsequent sweeps for sessions that become visible late. Report credential application
and incomplete session cleanup separately; one empty snapshot is not a strict drain guarantee.
Do not solve that race by automatically restarting the core and disconnecting unrelated users.
Recovery artifacts must not blindly re-enable known-revoked credentials. Bound retry attempts and
backoff, retaining unfinished cleanup durably for later reconciliation instead of declaring success.

## Verify and package

Use real local core/client tests for acceptance, rejection, and already-open sessions. Include an
occupied listener port, concurrent desired revisions, and failure after the controller call. Test
TCP and UDP separately when their behavior is affected. Synthetic fixtures and a local TLS target
keep the test independent of production credentials, public targets, and an operator's running VPN.

Record exact core/client versions and observed behavior. A successful test on one version does not
establish seamless new handshakes on every version. During packaging, preserve source/license
information, restrict controller visibility, and test child shutdown and volume-based recovery.

Common mistakes are equating PID survival with working service, equating HTTP 204 with successful
listener binding, validating only a generated file, and tying the shared child to an HTTP request's
context. For database and counter changes, load `node-storage-traffic` only when that boundary is touched.
