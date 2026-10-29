# Minimal local runtime implementation plan

> Execute inline with superpowers:executing-plans and test-driven-development.

**Goal:** Run a Go agent owning a pinned Mihomo child in local Docker, with authenticated status and bounded shutdown.

**Architecture:** `app` wires configuration, the Mihomo process/controller adapter and HTTP status API.
Mihomo starts with an agent-generated idle configuration: no VPN listeners or users yet.
PostgreSQL, reconciliation, telemetry and client provisioning remain future work.

**Tech stack:** Go 1.26.10 standard library; Mihomo v1.19.33; digest-pinned Docker stages.
**Spec:** [Architecture](../../architecture.md), limited to the first executable lifecycle slice.

## Constraints and review focus

- Bind the published API to host loopback; authenticate all `/api/v1` requests from a mounted token file.
- Keep the core controller private, with a fresh secret per launch. Never log tokens or raw core output.
- Validate before starting, confirm the pinned controller version, and fail startup on timeout.
- Reap the child on shutdown and partial startup failure; unexpected child exit fails the agent.
- Container hardening: non-root, read-only root, dropped capabilities, bounded writable tmpfs.
- Status readiness means agent/core ready, not working VPN. Return `vpnConfigured: false` explicitly.
- Do not access production or publish images. Commit the architecture first; keep implementation separately reviewable.

## Tasks

### 1. Configuration and authenticated status

Files: `go.mod`, `internal/config/{config,config_test}.go`, `internal/httpapi/{server,server_test}.go`.
Interfaces: `config.Load() (Config, error)`, `httpapi.New(token string, check func(context.Context) (string, error)) http.Handler`.
- [x] Write tests for missing/invalid token, valid file, unknown route, unauthorized access, ready/degraded status and sanitized errors; observe failures.
- [x] Implement environment configuration and bounded status/auth handling; run focused tests.

### 2. Child lifecycle and composition

Files: `internal/mihomo/{render,process,controller}.go` with tests, `internal/app/app.go`, `cmd/pulse-vpn-node/main.go`.
Interfaces: `mihomo.Start(ctx, Options) (*Process, error)`, `Process.Done/Err/Stop`, `Controller.Version(ctx)`, `app.Run(ctx, Config)`.
- [x] Write tests for invalid candidate, protected runtime artifact, child reap, forced shutdown and controller auth/version failure; observe failures.
- [x] Implement startup validation, controller readiness, graceful termination, HTTP shutdown and a healthcheck subcommand.
- [x] Run vet, tests, race detector and build in the pinned Go container.

### 3. Local Docker and operational proof

Files: `Dockerfile`, `.dockerignore`, `.gitignore`, `deploy/compose.yaml`, `api/openapi.yaml`, `README.md`, `docs/dependencies.md`; update docs-only status in guidance.
- [x] Build with pinned images and source/license references; generate a local token outside Git.
- [x] Start the local Compose service; confirm health, authentication and exact core version.
- [x] Exercise unexpected core exit, restart and clean SIGTERM shutdown using only this task's container.
- [x] Review the complete diff, document exact commands and limitations, and leave a healthy local service for the user.

## Execution notes

Architecture committed as `0a380e6`. Work continues on `codex/minimal-node-runtime` in the requested repository.
Go is unavailable on the host; use the existing pinned Go container for every check. No additional worktree is needed for this empty dedicated project.

Verification: formatting, vet, unit tests, race detector and Docker build passed. The real Docker
smoke test passed authentication, pinned version, child crash, restart, invalid binary and graceful
shutdown. Fresh review found a healthcheck bind-host bug; IPv4/IPv6 address mapping now has coverage.
The complete Docker smoke is repeated after that correction. No production access or image publishing.
