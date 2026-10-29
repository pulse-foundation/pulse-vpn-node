# Pulse VPN Node

Go server agent for Pulse VPN, managing a separate pinned Mihomo process. The Pulse control panel
remains a separate project. This repository is GPLv3.

**Current status:** local agent/core lifecycle and optional startup-configured VLESS + REALITY
over raw TCP with Vision. The agent validates settings, starts the pinned Mihomo child, serves
authenticated readiness, and stops/reaps the child. Without a VPN file it starts idle and reports
`vpnConfigured: false`. Persistence, live user operations, telemetry and Pulse integration are planned.

The agent lifecycle uses shared `node.Core` / `node.Runtime` contracts. Mihomo is the only implemented
adapter; adding another core requires its adapter, wiring and packaging, without changing the HTTP
API or serving lifecycle. See the [adapter boundary](docs/architecture.md#core-adapter-boundary).

## Run locally

Requires Docker with Compose. Go need not be installed on the host. Build inputs are pinned to
Go 1.26.10 and Mihomo v1.19.33 by image digest; see [dependencies](docs/dependencies.md).

From the repository root, create a token once:

```sh
mkdir -p .local
chmod 700 .local
test -f .local/api-token || (umask 077; openssl rand -hex 32 > .local/api-token)
chmod 644 .local/api-token
docker compose -f deploy/compose.yaml up --build --wait
```

The parent directory is private on the host. The token file is readable by the container's non-root
user through its read-only Compose secret mount; do not relax the parent directory's permissions.
`.local/` is excluded from Git and the Docker build context. Do not print or commit its contents.

The API is published only at `127.0.0.1:18080`; set `PULSE_NODE_PORT` to override the host port.
The core controller stays inside the container at `127.0.0.1:19090` with a random per-launch secret.
The container runs without root, extra capabilities, a writable root filesystem, or a Docker socket.
Private runtime artifacts live on bounded tmpfs. PostgreSQL is intentionally absent from this first
lifecycle slice; it will be a separate service when durable operations are implemented.

Read authenticated status without putting credentials in command arguments:

```sh
docker compose -f deploy/compose.yaml exec node pulse-vpn-node status
```

Expected:

```json
{"status":"ready","vpnConfigured":false,"core":{"version":"v1.19.33"}}
```

`GET /api/v1/status` requires `Authorization: Bearer <token>` and returns 401 without it. Core
unavailability/version mismatch returns 503 while the agent is serving. Unexpected core exit stops
the agent with a nonzero exit code. Local Compose does not auto-restart failures, so they remain
visible. Raw core output is discarded to avoid leaking generated configuration or secrets; startup
and lifecycle failures are reported with safe messages. Start the service again after diagnosing.

```sh
docker compose -f deploy/compose.yaml logs --tail=50 node
docker compose -f deploy/compose.yaml down
```

## Verify

The isolated VPN test starts an agent-managed server and a separate Mihomo client, both v1.19.33,
with fresh credentials and local HTTP/HTTPS/UDP targets. It publishes no host ports, uses no existing
VPN profile, and removes its own containers, networks and credentials afterward:

```sh
python3 tests/integration/reality_smoke.py
```

See [local VLESS + REALITY](docs/local-reality.md) for configuration and the test's limits.

The Docker build runs formatting verification, `go vet`, unit tests, race tests, and a static build.
To run only that stage:

```sh
docker build --target check -t pulse-vpn-node-check:local .
```

With the local image built and `.local/api-token` created, the integration smoke test uses a separate
Compose project and loopback port 18081 (override with `PULSE_NODE_SMOKE_PORT`). It checks real core
readiness, API authentication, core crash propagation, restart, invalid binary startup, and SIGTERM
shutdown, then removes only its own containers/network. Python 3 is needed for this test.

```sh
python3 tests/integration/docker_smoke.py
```

Set `PULSE_NODE_IMAGE` to use a different local image tag, for example
`PULSE_NODE_IMAGE=pulse-vpn-node:reality-test python3 tests/integration/docker_smoke.py`.

With Go 1.26.10 installed: `go vet ./...`, `go test ./...`, `go test -race ./...`, and
`go build ./cmd/pulse-vpn-node`. Runtime/process tests target Unix; Docker is the reference environment.

## Guidance

- [Architecture and planned package boundaries](docs/architecture.md)
- [Go conventions](docs/go-conventions.md)
- [Agent rules and skill routing](AGENTS.md)
- [HTTP contract](api/openapi.yaml)
- [GNU GPLv3](LICENSE)
