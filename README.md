# Pulse VPN Node

Go server agent for Pulse VPN, running a separate pinned Mihomo process. Pulse remains the control
panel; VPN clients connect directly to this node. This repository is GPLv3.

The complete node provides VLESS + REALITY over raw TCP with Vision, live user management,
approximate per-user traffic totals, resource metrics, configured network checks and a 15-minute
in-memory telemetry buffer. PostgreSQL stores users, operations and traffic totals. Pulse owns
monitoring history; its importer and dashboards are separate work.

## Launch layout

| File | Purpose |
| --- | --- |
| `compose.yaml` | Complete Linux node: agent + Mihomo, private PostgreSQL, one-off migrations |
| `compose.dev.yaml` | Local source build, loopback-only ports, automatic restart disabled |
| `tests/integration/compose.yaml` | Test overlay on the main node: disposable credentials, clients and local targets |

There are two long-running containers, `node` and `database`. `migrate` must finish successfully
before the node starts. Users, traffic and telemetry do not require separate Compose overlays.
The agent retains its low-level idle/file-only modes, but these are not the standard launch path.

Requires Docker with **Compose 2.24.4 or newer** (`!override` / `!reset` support).
Go is not required on the host. Go 1.26.10, Mihomo v1.19.33 and image inputs are pinned;
see [dependencies](docs/dependencies.md). Published node images and release automation are not
available yet: build locally, or supply an image you have built via `PULSE_NODE_IMAGE`.

## Try locally without preparing keys

From the repository root:

```sh
python3 tests/integration/reality_smoke.py
```

This builds the full node and a real Mihomo client, generates fresh credentials and a local TLS
target, verifies HTTP/HTTPS/UDP and rejects invalid credentials, then removes its own containers,
volumes and networks. It publishes no host ports and does not read `.local/`, use an existing VPN
profile or contact public targets. Python 3 is required for test runners.

## Configure a persistent node

Prepare a private directory, `.local/` by default (override with `PULSE_NODE_CONFIG_DIR`):

| File | Contents |
| --- | --- |
| `api-token` | Random management bearer token, at least 32 characters |
| `database-password` | PostgreSQL password |
| `database-url` | Matching URL for user/database `pulse_node` at `database:5432` |
| `vless.json` | REALITY transport and initial users; listen on `0.0.0.0:8443` |

See the [VLESS settings contract](docs/local-reality.md#startup-settings) for `vless.json`.
Use your own keys and destination for a persistent node; test fixtures are disposable.
An initializer and client-profile export are not implemented yet.

For a new local installation, generate the token and matching database files once. This command
preserves existing files and prints no secrets:

```sh
python3 - <<'PYSETUP'
from pathlib import Path
from secrets import token_hex
from urllib.parse import quote

root = Path('.local')
root.mkdir(mode=0o700, exist_ok=True)
root.chmod(0o700)
for name in ('api-token', 'database-password'):
    path = root / name
    if not path.exists():
        path.write_text(token_hex(32))
        path.chmod(0o644)
dsn = root / 'database-url'
if not dsn.exists():
    password = quote((root / 'database-password').read_text().strip(), safe='')
    dsn.write_text(f'postgres://pulse_node:{password}@database:5432/pulse_node?sslmode=disable')
    dsn.chmod(0o644)
PYSETUP
```

Keep the directory mode **0700**. Files mounted as Compose secrets must be readable by container
UID 10001; mode 0644 inside that private parent works for local bind-based secrets. Apply the same
rule to `vless.json`. `.local/` is excluded from Git and the image build context. Never print or
commit these files. Protect database volumes/backups too; VPN credentials are recoverable there.

Telemetry uses `deploy/telemetry.example.json` by default: five read-only Linux proc mounts,
container/process metrics and **no outbound probe targets**. To customize it, copy the example and
set `PULSE_NODE_TELEMETRY_CONFIG` to the new file. On Docker Desktop, host metrics describe the
Linux VM, not macOS/Windows. See [telemetry](docs/telemetry.md) for visibility limits and probes.

## Start and operate

For local development, after preparing the four files:

```sh
docker compose -f compose.yaml -f compose.dev.yaml build node
docker compose -f compose.yaml -f compose.dev.yaml up --wait
docker compose -f compose.yaml -f compose.dev.yaml exec node pulse-vpn-node status
```

Expected status includes `status: ready`, `vpnConfigured: true`, and core version `v1.19.33`.
The API is at `127.0.0.1:18080` and VPN at `127.0.0.1:18443`. Override host ports with
`PULSE_NODE_PORT` and `PULSE_NODE_VPN_PORT`. Dev keeps failures visible instead of restarting them.

On a prepared Linux VPS with the selected image available, the full launch needs just:

```sh
docker compose up --wait
docker compose exec node pulse-vpn-node status
```

The main file publishes VPN TCP **443** by default (`PULSE_NODE_VPN_BIND` / `PULSE_NODE_VPN_PORT`),
while management stays on loopback port 18080. PostgreSQL has no host port; the core controller
stays inside the container. Node and database restart unless explicitly stopped. The node runs
without root, added capabilities, a writable root filesystem or a Docker socket.

Use the same files, project name and environment for subsequent `logs`, `up` and `down` commands.
`down` preserves the database volume; **do not use `down --volumes` for a persistent node**.
After changing the image, run `up --wait` so migrations precede agent startup. Back up the database
before upgrades: an older image is not guaranteed to understand a newer schema.

**Existing local installations:** the old files used project name `pulse-vpn-node-local`.
Keep that name with `-p pulse-vpn-node-local` on every command to reuse its `node_database` volume;
the new default is `pulse-vpn-node`. Preserve your existing secrets. Initial users seed only an empty
database; changing `vless.json` does not reset managed users. Transport changes require a restart.

## Verify

All acceptance scripts use the same full node plus the single test overlay. Each owns a separate
project and cleans up only its disposable resources. They do not require persistent node settings.

```sh
python3 tests/integration/compose_check.py
python3 tests/integration/docker_smoke.py
python3 tests/integration/reality_smoke.py
python3 tests/integration/users_smoke.py
python3 tests/integration/traffic_smoke.py
python3 tests/integration/telemetry_smoke.py
```

These cover effective Compose isolation, authentication/lifecycle, real VPN traffic, live user
changes with existing sessions, database outages, persistent traffic totals and telemetry catch-up.
They establish local compatibility, not filtering resistance or mobile-app behavior.

Image builds run formatting, vet, unit tests, race tests and a static build. To run that stage alone:

```sh
docker build --target check -t pulse-vpn-node-check:local .
```

## Reference

- [Architecture and core adapter](docs/architecture.md)
- [Live users and migrations](docs/live-users.md)
- [Approximate traffic accounting](docs/traffic.md)
- [Telemetry and catch-up API](docs/telemetry.md)
- [HTTP contract](api/openapi.yaml)
- [Go conventions](docs/go-conventions.md), [agent rules](AGENTS.md), [GNU GPLv3](LICENSE)
