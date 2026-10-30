# Telemetry implementation plan

**Goal:** Implement the agreed VPS telemetry contract with 15 minutes of bounded RAM retention,
cached API reads and explicit catch-up gaps. Production is outside this task.

**Spec:** `docs/architecture.md`, VPS telemetry and network checks.

**Architecture:** `internal/telemetry` owns resource/probe samplers and an ordered memory buffer.
`internal/app` owns cancellable loops and supplies runtime/dependency observations. HTTP reads only
the buffer. No SQL history or changes to Pulse are needed. Go standard library, pinned existing images.

## Decisions

- Default resource interval 5s; connectivity 30s; configurable bounded intervals and deadlines.
- Resource visibility is explicit: optional read-only host proc mount, selected host/container
  interfaces and disks, named filesystem mounts. No privileged container or Docker socket.
- Default connectivity target list is empty; only configured DNS/TCP/HTTPS/IPv4 ICMP probes run.
- RAM retention: 15 minutes elapsed time, 2,048 records and 4 MiB; 64 KiB maximum record.
- A random agent-run ID plus increasing sequence identifies every published sample. Pages are bounded
  to 128 records/512 KiB, report eviction/run change, reject future cursors, and never consume data.
- API: `/api/v1/system/stats`, `/api/v1/connectivity`, `/api/v1/telemetry/samples` with existing auth.

## Deliverables and verification

- [x] Resource samplers: bounded Linux proc/cgroup reads, CPU/RAM/load/swap/filesystems/disk/network
  counters and rates; fixture tests for permission/format/reset/first-sample/scope behavior.
- [x] Memory stream: coherent latest/publication, immutable bounded pages, elapsed-time/count/byte
  eviction, restart/gap/invalid cursor tests and concurrent race checks.
- [x] Connectivity: explicit direct transports, deadlines, concurrency bound and cancellation;
  local DNS/TCP/TLS/ICMP tests, proxy/redirect rejection and permission-unavailable behavior.
- [x] App/config/API: strict deployment config, runtime/dependency sampling isolated from resource
  sampling, cached authenticated reads and OpenAPI; lifecycle/auth/cursor tests.
- [x] Local Docker acceptance: Linux metrics, catch-up retry, restarted run, database outage, VPN
  remains usable; full Go formatting/vet/tests/race/build and independent review.

## Review focus

Wall-clock changes must not control retention; missing visibility must not become host zero values;
failed probes must not refresh old successes; slow SQL/core reads must not delay resource collection;
cursor retries and concurrent eviction must remain coherent. Tests above cover each boundary.

Completed locally: Docker telemetry acceptance, Go formatting/vet/tests/race/build, source/cursor failure tests and independent review. Production was not contacted.
