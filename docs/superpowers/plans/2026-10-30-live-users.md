# Live users without restarting the core

Goal: durable authenticated user management, one serialized reconciler, local-only acceptance.
Baseline committed: f1e1600. PostgreSQL owns desired users and idempotent operations. The mounted
VLESS file owns transport/REALITY settings and seeds users only on first initialization.

1. Add optional node.UserRuntime and UserStore contracts; implement bounded Mihomo validation,
   live reload, listener ownership verification and repeated targeted revoked-session cleanup.
2. Add pinned pgx and private PostgreSQL service; explicit versioned migration command. Commit
   desired revision, identity uniqueness and idempotency record atomically with optimistic concurrency.
3. Add authenticated bounded GET/PUT user and GET operation APIs. Ordinary reads omit UUIDs. A
   request commits only intent and returns 202; the application-owned reconciler performs core I/O.
4. Recover latest desired state before core start. Persist operation results only for that revision;
   coalesced older operations are superseded. Retry safely after core/DB errors with capped backoff,
   keep serving through DB outages, keep disabled names for late-session sweeps.
5. Verify unit/race/vet/build, real PostgreSQL conflicts/idempotency/restart, and isolated real Mihomo
   TCP/UDP sessions across add/disable. Assert unchanged core PID, new-user access, revoked denial,
   unaffected established sessions, zero-active-user rejection and recovery after agent restart.

No production, public targets, existing client profiles or secrets. No sing-box implementation,
credential rotation, hard deletion, traffic accounting or UI. Readiness and credential application
are distinct from observed session cleanup; no strict instantaneous drain claim.

## Result

Implemented and locally verified on 2026-10-30: Go vet/unit/race/build; actual disposable PostgreSQL
migration, concurrent writers, idempotency, rollback and restart tests; real Mihomo live-user lab
including unchanged PID/start ticks, TCP/UDP continuity, targeted revoke, all-disabled denial,
database outage and persisted restart. Review corrections cover UUID seed case and ambiguous
application cleanup intent. Real lab exposed XUDP NAT regeneration; exact active IN-USER egress
allow rules and terminal REJECT prevent revoked identities regenerating outbound flows. No claim
of hidden mux-parent transport drain. Original no-DB REALITY acceptance also passes.
