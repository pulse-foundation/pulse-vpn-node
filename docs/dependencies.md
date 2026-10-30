# Pinned runtime dependencies

The local image combines an independent Go agent with an unchanged Mihomo executable.
No core code is embedded or copied into the agent module. Dockerfile pins all input images by digest.

| Component | Version / source |
| --- | --- |
| Go toolchain | 1.26.10, from the pinned `golang:1.26-bookworm` image |
| Mihomo | [v1.19.33 source](https://github.com/MetaCubeX/mihomo/tree/v1.19.33), binary from the pinned `metacubex/mihomo:v1.19.33` image |
| Runtime base | Pinned `alpine:3.22` image |

Mihomo's source archive for this version is available at
[v1.19.33 archive](https://github.com/MetaCubeX/mihomo/archive/refs/tags/v1.19.33.tar.gz).
Its upstream license and notices remain applicable; see its
[LICENSE](https://github.com/MetaCubeX/mihomo/blob/v1.19.33/LICENSE).
The Go toolchain and standard library retain their [Go license](https://go.dev/LICENSE).
Alpine packages retain their respective licenses. The agent is GPLv3; see `LICENSE` in this directory.

This is a local development image, not a published release. Before distribution, package the required
license notices and corresponding-source materials for the exact shipped dependencies and build.
For a core/toolchain upgrade, update pins, runtime version verification and compatibility evidence
together. A successful idle startup is not evidence of VLESS/REALITY compatibility.

The local VLESS/REALITY acceptance test uses v1.19.33 for both server and client, raw TCP + Vision,
and XUDP. It verifies HTTP/HTTPS payload integrity, UDP echo, invalid credentials, occupied-port
rejection and no direct fallback after node shutdown. See [test instructions](local-reality.md).
This evidence does not cover production routing, filtering, mobile app builds or core upgrades.

User management adds pgx v5.11.0 (MIT, github.com/jackc/pgx) with exact transitive versions/checksums
in go.mod/go.sum. Local Compose pins PostgreSQL 17 Alpine by image digest. PostgreSQL is a separate
service, not embedded in the agent image. See [live user tests](live-users.md).

## CI actions

GitHub Actions dependencies are pinned by full commit SHA in `.github/workflows/`. The version
comments identify the reviewed release; update both the SHA and comment together. The publication
job uses Docker Buildx v0.38.1. Native Ubuntu 24.04 runners cover amd64 and arm64; their managed OS
images are updated by GitHub. Local workflow syntax was checked with actionlint v1.7.12.
