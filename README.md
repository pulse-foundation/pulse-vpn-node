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
Running the node needs no host Go installation; integration tests require Go 1.27.3.
Go 1.27.3, Mihomo v1.19.33 and image inputs are pinned;
see [dependencies](docs/dependencies.md). GitHub Actions publishes versioned multi-platform images
to `ghcr.io/pulse-foundation/pulse-vpn-node` when a release is published. Select an explicit release
tag via `PULSE_NODE_IMAGE`; use `pulse-vpn-node:local` for a local source build.

## Try locally without preparing keys

From the repository root:

```sh
go test -tags=integration -count=1 -timeout=30m -v ./tests/integration -run '^TestNode/Reality$'
```

This builds the full node and a real Mihomo client, generates fresh credentials and a local TLS
target, verifies HTTP/HTTPS/UDP and rejects invalid credentials, then removes its own containers,
volumes and networks. It publishes no host ports and does not read `.local/`, use an existing VPN
profile or contact public targets. The coordinator uses only standard Go `testing` and `os/exec`.

## Configure a persistent node

Prepare a private directory, `.local/` by default (override with `PULSE_NODE_CONFIG_DIR`):

| File | Contents |
| --- | --- |
| `api-token` | Random management bearer token, at least 32 characters |
| `database-password` | PostgreSQL password |
| `database-url` | Matching URL for user/database `pulse_node` at `database:5432` |
| `vless.json` | JSON `null` for managed idle startup, or legacy REALITY transport and initial users |

The image includes a noninteractive initializer. Build the local image from this checkout first;
a published image must include managed idle support before using this workflow:

```sh
docker compose -f compose.yaml -f compose.dev.yaml build node
export PULSE_NODE_IMAGE=pulse-vpn-node:local
install -d -m 700 .local
docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true --user "$(id -u):$(id -g)" \
  --mount type=bind,src="$PWD/.local",dst=/config \
  "$PULSE_NODE_IMAGE" init --config-dir /config --idle
```

The managed node starts healthy with `vpnConfigured: false` and no public VPN listener. Create its
single raw VLESS/REALITY connection through `PUT /api/v1/connection`, then add users through the
[managed API](docs/live-users.md). The default Compose mapping expects internal port 8443. Keys and
short IDs are generated once and persisted in PostgreSQL when the connection is created. Init does
not contact a REALITY target or validate its TLS compatibility.

Standalone preconfigured initialization remains available with `--reality-destination HOST:PORT`
and `--reality-server-names NAME,...` instead of `--idle`. It generates the transport in `vless.json`;
managed startup imports this material once without changing keys, users or disabled state. SQL is
subsequently authoritative. `--listen` and `--port` select its initial listener; Compose's container
port mapping must match. See the [VLESS settings contract](docs/local-reality.md#startup-settings).

Repeat the same command without transport flags to check and fill missing files. Existing valid
files, keys, users and permissions stay byte-for-byte unchanged. Explicit transport flags must match
existing settings. An invalid file or mismatched database password/URL stops init before it writes
any configuration files. A missing database-password or database-url is recovered from its existing
counterpart. Init cannot recover secrets if both copies have been lost: preserve the config directory
alongside database backups, and do not use fresh credentials against an existing database volume.

Output is JSON containing only `created` and `preserved` filename lists. No prompts, secrets,
service startup, migrations or Docker control occur inside init. Use `init --help` for options.
Updating an installation is a separate operation; init never rotates secrets or edits existing files.
An exclusive `.init.lock` prevents simultaneous init runs. Each missing file is published atomically
without replacement; after an interrupted run, retry to complete the remaining files.

Keep the directory mode **0700**. New files have mode **0644** inside that private parent so
bind-mounted Compose secrets are readable by container UID 10001. Existing file modes are preserved;
operators must ensure their existing secrets are readable by the runtime UID. Symlinks and
nonregular managed files are rejected. `.local/` is excluded from Git and the image build context.
Never print or commit these files. Protect database volumes/backups too; VPN credentials are
recoverable there.

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

The Go integration package uses the full node plus the single test overlay. It builds the images
once, then runs sequential subtests: `Compose`, `Init`, `Postgres`, `Lifecycle`, `Reality`, `Users`, `Traffic`
and `Telemetry`. A failed image preparation stops the suite without retrying in later scenarios. Each Docker scenario
refuses stale resources and cleans up its own project via
`t.Cleanup`, including failures. No persistent node settings are needed.

```sh
go test -tags=integration -count=1 -timeout=30m -v ./tests/integration
```

For a single scenario, add `-run '^TestNode/Reality$'` (or another subtest name). `Compose` only
resolves configuration and does not start containers. Ordinary `go test ./...` excludes these
Docker tests; `-count=1` prevents cached acceptance results. To deliberately test previously built
images without rebuilding, append `-args -images-prebuilt`; all three lab images must already exist.
The default, including CI, builds from the current checkout and checks the resulting image ID
throughout the test run. A regression test also verifies that unsafe Compose configuration stops
both full and individually selected scenarios before runtime preparation.

These cover effective Compose isolation, authentication/lifecycle, real VPN traffic, live user
changes with existing sessions, database outages, persistent traffic totals and telemetry catch-up.
They establish local compatibility, not filtering resistance or mobile-app behavior.

Image builds run formatting, vet, unit tests, race tests and a static build. To run that stage alone:

```sh
docker build --target check -t pulse-vpn-node-check:local .
```

## GitHub Actions and releases

- PRs and pushes to `main` run the shared verification workflow on native Linux **amd64 and arm64** runners.
- Checks include the image build gates, Compose validation, real PostgreSQL transaction tests and
  all five VPN/lifecycle acceptance scenarios. Test projects must be clean before and after each run.
- A **published GitHub Release** triggers the same checks, then publishes the verified image pair to
  `ghcr.io/pulse-foundation/pulse-vpn-node:<release-tag>`. Release tags must look like `v0.1.0` or
  `v0.1.0-rc.1`; prereleases are supported. There is no automatic `latest` tag.
- Publication loads the saved images and checks their IDs and architectures; it never rebuilds them.
  The version manifest includes source/revision/version annotations. Existing version tags are
  rejected. Intermediate `build-<run>-<attempt>-<arch>` tags retain each uploaded architecture.
- Only the publish job has `packages: write`, using GitHub's built-in `GITHUB_TOKEN`. PR/push checks
  have read-only repository access. Actions are pinned to reviewed commit SHAs.
- Publication does not update any VPS. Select the image tag/digest through `PULSE_NODE_IMAGE` when
  separately performing an authorized update.

The organization must allow Actions and package creation for this repository. After the first
publication, make the GHCR package **public** if nodes should pull without authentication: public
repository visibility does not automatically make a new container package public. No personal access
token or production/SSH credentials are needed by these workflows. See [GitHub's registry guide](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).

To run the same verification locally on the Docker daemon's native architecture:

```sh
go test -tags=integration -count=1 -timeout=30m -v ./tests/integration
```

This command never publishes an image. It checks that the runtime image ID remains unchanged across
acceptance tests and leaves the verified image in the local Docker cache. The reusable workflow then
saves it only for release runs. A successful local run does not prove GitHub permissions or registry
publication; the first authorized release will exercise those external steps.

## Reference

- [Architecture and core adapter](docs/architecture.md)
- [Live users and migrations](docs/live-users.md)
- [Approximate traffic accounting](docs/traffic.md)
- [Telemetry and catch-up API](docs/telemetry.md)
- [HTTP contract](api/openapi.yaml)
- [Go conventions](docs/go-conventions.md), [agent rules](AGENTS.md), [GNU GPLv3](LICENSE)

### Optional telemetry delivery

Collection stays in bounded RAM even without enrollment. Set
`PULSE_NODE_TELEMETRY_EXPORT_FILE` to an absolute path in a dedicated read-only
**directory** mount (the standard Compose uses `/run/pulse-telemetry/enrollment.json`).
Provision that host directory with mode 0700 and UID/GID 10001; write the file
atomically, mode 0600 and UID/GID 10001. Do not bind an individual file: atomic
replacement must become visible without restarting the agent or VPN core.

The private JSON contains `schemaVersion: 1`, `endpoint` (HTTPS origin on port 443
plus `/api/v1/node-ingest`), `enrollmentGeneration` (UUID), and `credential`
(a separate scoped bearer credential, never the Node management token). Enrollment
is explicit; `init` does not invent a receiver or sender identity. Missing file
disables delivery. Invalid files stop only delivery and expose a safe status code.
The watcher reloads within five seconds, cancels and joins the previous sender,
and resumes the same telemetry run and acknowledged sequences after rotation.

The receiver contract uses `/runs`, `/heartbeat`, `/samples` with schema version 1,
decimal uint64 sequences and committed ACKs. Heartbeat runs every 30 seconds
with ±3 seconds jitter independently of history retries. Pages are limited to 64
records/512 KiB; retry backoff is 1–60 seconds and retained history stays bounded
by the existing 15-minute RAM buffer. Redirects, proxies, loopback/link-local
receiver destinations and invalid TLS certificates are rejected. DNS addresses
are checked at each dial. Receiver failures do not stop sampling or VPN service.
Configure explicit permitted direct probe targets in the telemetry file; the
generic example intentionally does not contact third-party targets by default.
`/api/v1/status` reports `capabilities.telemetryPushVersion` and cached
`telemetryDelivery`; it never returns the private enrollment or receiver address.
