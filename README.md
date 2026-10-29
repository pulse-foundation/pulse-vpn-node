# Pulse VPN Node

Server agent for Pulse VPN: Go manages a separate Mihomo process serving VLESS + REALITY.
Clients connect directly to the VPN node. The Pulse VPN control panel remains a separate project.

**Status:** architecture and agent guidance only. There is no runnable application or image yet.

The initial deployment target is an agent + Mihomo container with a separate PostgreSQL service.
User traffic reporting is approximate and is not intended for billing or strict traffic quotas.

- [Architecture and package boundaries](docs/architecture.md)
- [Go conventions and planned quality checks](docs/go-conventions.md)
- [Contributor/agent instructions](AGENTS.md)

Repository-local skills live under `.agents/skills` and are routed from `AGENTS.md`.
Application directories will be created alongside working implementations, not as empty scaffolding.

Licensed under [GNU GPLv3](LICENSE). Third-party components retain their own licenses.
