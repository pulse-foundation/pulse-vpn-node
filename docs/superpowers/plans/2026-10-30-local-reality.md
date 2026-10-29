# Local VLESS + REALITY implementation plan

**Goal:** Connect a real pinned Mihomo client through the agent-managed VLESS/REALITY server entirely in local Docker.
**Spec:** [Architecture](../../architecture.md), first startup-configured VPN slice.
**Execution:** Inline, using node-mihomo-runtime, test-driven-development and a fresh final review.

## Design and boundaries

Load an optional strict JSON VLESS configuration from a mounted secret file. Own the validated model
in `internal/node`, file loading in `config`, and pure core rendering in `mihomo`. Serve raw TCP with
REALITY and Vision, with no alternate transports, bypass TLS flags or database-dependent CRUD.
Existing no-file startup remains idle. Status reports whether VPN is configured; startup checks both
controller readiness and listener reachability, while an external test proves actual authentication.
File edits require explicit restart for now; dynamic user management remains a later durable operation.

Use new synthetic credentials and a disposable TLS target. Separate internal Docker networks place
the target behind the node and prevent the probe/client from reaching it directly. Publish no ports.
No production, public TLS targets, host proxy, or existing desktop client participates.

## Tasks

- [x] Add tests and implementation for validated node VLESS settings, strict bounded file loading,
  render correctness, idle compatibility and occupied-listener rejection.
- [x] Wire configured runtime readiness and non-secret status; verify unit/race/vet/build gates.
- [x] Add a test-only Go fixture tool to generate credentials, serve local TLS/HTTP/UDP targets,
  and probe via a real Mihomo client's explicit SOCKS/HTTP proxy. It is not part of the agent binary.
- [x] Run an isolated Docker lab: HTTP/HTTPS payload transfer and UDP echo succeed; wrong UUID and
  REALITY public key fail; stopping the node prevents transfer; an occupied VPN port fails startup.
- [x] Update operational docs and OpenAPI; review and retain only local reproducible fixtures.

## Verification focus

Reject invalid/missing secrets without echoing them; do not silently fall back to plaintext or idle
on bad configuration. The client has no DIRECT fallback. Publish no test proxy or VPN port. Do not
claim a successful local test proves operation on a VPS, in a mobile client, or through filtering.

Minimal runtime baseline committed as `6895fc1`. Dependencies remain Go 1.26.10 and Mihomo v1.19.33.

Verified locally on 2026-10-30: formatting, vet, unit/race tests, static image build; real HTTP/HTTPS
1 MiB echo and UDP/XUDP transfer; wrong UUID/key rejection; occupied port; stopped-node rejection.
Independent review found a bind-after-preflight race. Fixed by verifying listening socket ownership
through container Linux procfs, with a deterministic regression test and a passing real Docker rerun.
