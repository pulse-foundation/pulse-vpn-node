# Local VLESS + REALITY

The agent supports one VLESS listener over raw TCP, with REALITY and `xtls-rprx-vision` for every
configured user. Server and test client use unmodified Mihomo v1.19.33. This is a local startup
configuration mode. Optional [live users](live-users.md) adds PostgreSQL-backed management.

## Reproducible connection test

From the repository root, with Docker Compose and Python 3 available:

```sh
python3 tests/integration/reality_smoke.py
```

The script builds the agent image and a separate test helper, creates fresh X25519 keys, UUIDs,
short IDs and a local TLS certificate, and runs this path:

```text
probe → Mihomo client → agent-managed Mihomo server → local target
        front network  [front + back networks]       back network
```

Both networks are Docker `internal` networks. No port is published to the host; no public TLS target,
production server, desktop proxy or existing VPN credentials participate. The TLS target is reachable
only from the backend network. The client has a single `MATCH,vpn` rule and no DIRECT fallback.
UDP uses the target's inspected backend IPv4 because Mihomo resolves UDP destinations on the client,
while Docker intentionally hides backend DNS from the frontend network.

Acceptance checks cover authenticated agent readiness, HTTP and certificate-verified HTTPS with a
1 MiB random payload echoed intact, and UDP/XUDP echo. Separate clients with a wrong UUID or REALITY
public key must fail. An occupied VPN port must fail agent startup; stopping the node must prevent
further transfers. The test helper and generated credentials are not shipped in the agent image.

The script removes its own project and credential volume even on failure. If interrupted forcibly,
inspect and remove only this project before rerunning:

```sh
docker compose -p pulse-vpn-node-reality-test -f deploy/compose.reality-test.yaml down --volumes --remove-orphans
```

These checks establish local core compatibility, not VPS reachability, resistance to filtering,
YouTube/QUIC behavior, or compatibility with a particular mobile app build. The client fixture uses
`support-x25519mlkem768: false`; it does not test the previously discussed flag difference.

## Startup settings

Set `PULSE_NODE_VLESS_FILE` to a read-only mounted JSON file readable by container UID 10001. Protect
the parent directory and file; never commit or print it. The default `deploy/compose.yaml` remains
idle and does not mount this optional file or expose a VPN port. The test Compose shows the wiring.

The file is at most 64 KiB and contains one JSON object. Unknown fields and trailing documents fail
startup. Required fields are:

| Field | Contract |
| --- | --- |
| `listen` | Literal IP to bind inside the container, such as `0.0.0.0` |
| `port` | TCP port 1–65535; avoid management ports; the lab uses 8443 |
| `users` | Array of `{ "name": ..., "uuid": ... }`; unique identities, empty denies all users |
| `reality.destination` | Reachable TLS destination `host:port` compatible with REALITY |
| `reality.privateKey` | 32-byte X25519 private key in unpadded base64url |
| `reality.shortIds` | Nonempty array of nonempty even-length hexadecimal IDs, at most 8 bytes each |
| `reality.serverNames` | Nonempty array of accepted DNS server names |

The matching client needs the user's UUID, derived public key, one short ID, an accepted server name,
the node address/port, TLS, Vision, and raw TCP. The local fixture generator demonstrates that mapping
without storing reusable credentials in Git. The agent does not issue/export client profiles yet.

Editing the file has no effect until the agent is explicitly restarted. Invalid settings fail startup
instead of enabling plaintext VLESS or silently starting idle. `vpnConfigured` reports supplied VPN
settings; readiness checks controller version, child ownership of the listening socket, and TCP
reachability, not REALITY authentication or
end-to-end traffic. The external local test provides that stronger evidence.

Configured-VPN readiness requires Linux procfs in the container. It reads only the child process
socket descriptors and network tables; no host mounts, Docker socket or additional capabilities
are needed. Missing visibility fails readiness instead of trusting an unrelated listening process.
