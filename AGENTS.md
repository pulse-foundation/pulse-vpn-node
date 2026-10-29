# Pulse VPN Node Agent Instructions

This is the public GPLv3 server component for Pulse VPN. The repository currently contains
documentation only; a documented directory or command is not evidence of an implementation.
Follow the checkout and current user request. Keep documentation-only work free of application code.

## Product boundaries

- Use Go for the agent and an unmodified, version-pinned Mihomo child process for VLESS + REALITY.
  Protocol changes, alternative cores, and embedding Mihomo as a library are separate decisions.
- The deployment baseline is one agent + Mihomo container and a separate PostgreSQL Compose service.
  The agent manages its own child, not the Docker daemon or the VPS operating system.
- Pulse VPN remains the separate private control panel. Clients connect directly to this node.
  Do not copy private Pulse code, credentials, fixtures, or infrastructure details into this repository.
- User traffic is approximate operational information, not billing or strict quota enforcement.
  Do not fork Mihomo to improve accounting unless explicitly requested.
- The agent collects VPS utilization and outbound network checks and exposes current snapshots through
  its authenticated API. Pulse owns monitoring history, graphs, alert policy, and notifications.
  Keep a bounded 15-minute in-memory telemetry buffer for catch-up; do not persist it to disk or SQL.
  Durable VPN traffic totals/checkpoints remain local; they are distinct from monitoring history.
- A change to one user's access must not routinely restart the core or disconnect other users.
- A local change does not authorize production deployment, probing live VPSs, or publishing releases.

## Read only the relevant guidance

- Read [architecture](docs/architecture.md) when choosing packages, dependencies, or boundaries.
- Read its [telemetry section](docs/architecture.md#vps-telemetry-and-network-checks) for resource
  metrics, network probes, snapshot APIs, or container access to host measurements.
- Read [Go conventions](docs/go-conventions.md) before Go, test, dependency, or CI changes.
- Use [node-mihomo-runtime](.agents/skills/node-mihomo-runtime/SKILL.md) for config application,
  child supervision, access revocation, Docker packaging, or core compatibility testing.
- Use [node-storage-traffic](.agents/skills/node-storage-traffic/SKILL.md) for PostgreSQL,
  migrations, desired-state transactions, traffic collection, or restart recovery.

Do not load both skills for an unrelated task. These are repository-local workflows;
standard Go guidance lives in the conventions document rather than a duplicate generic skill.

## Always-on implementation rules

- Keep one Go module. Put the executable under `cmd/pulse-vpn-node` and implementation under
  `internal`. Create packages when real code needs them; do not pre-create empty scaffolding.
- Keep HTTP, SQL, and Mihomo transport details out of node policy. Wire concrete dependencies
  in `internal/app`; define small interfaces at their consumers when an actual boundary needs one.
- Avoid catch-all `utils`, `common`, `models`, and `interfaces` packages, generic repositories,
  service locators, and a controller/service/repository directory hierarchy for every resource.
- Keep config rendering deterministic and free of filesystem, network, and database effects.
- Distinguish desired, applying, verified applied, and failed state. Neither a DB commit nor a
  controller HTTP 204 proves that a user can connect or that a listener successfully opened.
- Give every background loop an owner, cancellation path, and bounded shutdown. Reconcile durable
  intent from PostgreSQL; do not spawn untracked goroutines from HTTP handlers.
- Keep telemetry in `internal/telemetry`. Serve bounded cached snapshots with measurement times,
  units, scope, and freshness; unavailable data is not zero. Do not run probes on every API read.
- Expose buffered measurements in bounded, non-destructive pages. Identify each record by agent-run
  ID and increasing sequence; report retention gaps and run changes explicitly. Pulse deduplicates
  by node/run/sequence and commits imported records and its cursor together without cursor regression.
- Keep telemetry collection and snapshot reads independent of Pulse/SQL availability. Buffer eviction
  and agent restart may lose measurements; never hide gaps or promise lossless delivery.
- Distinguish VPS, container, and process metrics. Do not add privileged mode or a Docker socket
  for monitoring. Document minimal host visibility and ICMP permissions in deployment configuration.
- Outbound probes do not prove inbound reachability or end-to-end VPN health. Keep external checks
  in Pulse or a separate probe location; telemetry failures must not stop the serving VPN core.
- Never log complete configurations, bearer tokens, VLESS credentials, private keys, DB passwords,
  or unfiltered child output. Use synthetic credentials in tests and redact diagnostic errors.
- Protect `/api/v1` with node authentication. Default management access to host loopback through
  SSH forwarding; keep the Mihomo controller and PostgreSQL private. Avoid Docker socket mounts.
- Add reviewed, versioned SQL migrations. Do not rewrite released migrations or use automatic
  schema synchronization in production.
- Pin build/release inputs. Preserve dependency licenses and the corresponding-source information
  for distributed Mihomo binaries; public agent code does not relicense its dependencies.
- Test changed behavior and failure recovery. Do not claim Go tests, builds, or runtime checks
  passed when only documentation was validated. Do not add application tests for wording edits.

If implementation requires changing these boundaries, explain the concrete tradeoff and update
the architecture note with the decision. Do not silently import assumptions from the Pulse monorepo.
