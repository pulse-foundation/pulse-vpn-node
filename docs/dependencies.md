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

## Automated security checks

Dependabot proposes weekly Go module, Docker and GitHub Actions updates. The CI workflow also
runs every Monday and can be started manually; scheduled runs use the default branch. Updates
are reviewed, never automatically merged or deployed. Keep the server/client Mihomo pins aligned
and update the corresponding source/license references after compatibility tests.

The shared verification workflow runs pinned govulncheck against the agent source and Trivy against
the exact tested node image (including the Mihomo binary) and the PostgreSQL image from Compose.
The unfiltered JSON inventories and readable High/Critical reports are saved as
`security-reports-amd64` and `security-reports-arm64` artifacts for 30 days, including failed runs.
High/Critical counts appear in the job summary and workflow warnings.

For the Node image we build, the gate blocks High/Critical **OS package** findings with a vendor fix available, using
Trivy's native `--pkg-types os --ignore-unfixed` filters only for the gate. The full inventory is
not filtered. Scanner, registry, database and report failures remain fatal before image upload.
The agent's source and transitive dependencies remain protected by mandatory govulncheck.

Findings inside ready-made upstream binaries (Mihomo) and unfixed OS advisories are review
items, not automatic release blockers. The ready-made PostgreSQL service image is fully scanned
in advisory mode, including OS findings: a fixed distro package does not mean a compatible
updated vendor image has been published. The workflow explicitly selects `built-image` for
Node and `upstream-image` for PostgreSQL; unknown modes fail. Do not classify our Node build
as upstream to bypass the OS gate. A fixed Go library version does
not mean a compatible fixed Mihomo binary has been released. Do not fork the core or override
its dependencies just to satisfy the inventory scanner. There are no per-CVE baselines, expiry
extensions, blanket ignore files or continue-on-error bypasses.

Review weekly reports even when CI is green: new Critical findings, applicable remotely
exploitable issues and available vendor fixes need prompt triage and may require holding a
release. Passing CI means the automated gates passed, not that the complete image is free of
known vulnerabilities. GitHub Actions failure notifications cover blocking findings and scanner
errors; advisory warnings alone do not generate failure notifications. No VPS daemon is added.
Run `node --test scripts/scan-image.test.mjs` to check gate failure and report preservation.

On 2026-11-01, refreshed Alpine 3.22 and the agent dependencies scan clean, but current stable
Mihomo 1.19.33 and the PostgreSQL image's bundled `gosu` executable still have dependency findings. These findings remain visible in the advisory reports and no longer fail every release.
They are not declared safe or suppressed. Functional tests and the blocking checks above still
run before the exact verified image can be published.

The PostgreSQL service is a separate image: changing the Node release alone does not update it.
Consumers that generate their own Compose must adopt the reviewed database digest separately.
