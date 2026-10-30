"""Validate resolved Compose contracts without starting containers or reading secrets."""

import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]


def config(*overlays, extra_env=None):
    # Ignore developer overrides and dotenv files so the default contract is tested.
    env = {k: v for k, v in os.environ.items()
           if not k.startswith(("PULSE_NODE_", "COMPOSE_"))}
    env.update(extra_env or {})
    args = ["docker", "compose", "--env-file", "/dev/null", "--profile", "test", "-f", "compose.yaml"]
    for overlay in overlays:
        args += ["-f", overlay]
    result = subprocess.run(args + ["config", "--format", "json"], cwd=ROOT, env=env,
                            capture_output=True, text=True, check=True, timeout=30)
    return json.loads(result.stdout)


def check_node(model):
    services = model["services"]
    node, migrate, database = (services[name] for name in ("node", "migrate", "database"))
    assert node["image"] == migrate["image"], "migrations must use the node image"
    assert not database.get("ports"), "database must stay private"
    assert node["depends_on"]["migrate"]["condition"] == "service_completed_successfully"
    assert migrate["depends_on"]["database"]["condition"] == "service_healthy"
    for service in (node, migrate):
        assert service["read_only"] and "ALL" in service["cap_drop"]
        assert not service.get("privileged") and not service.get("cap_add")
        assert "no-new-privileges:true" in service["security_opt"]
    for key in ("TOKEN", "VLESS", "DATABASE_URL", "TELEMETRY"):
        assert node["environment"][f"PULSE_NODE_{key}_FILE"]
    assert any(v["type"] == "volume" and v["target"] == "/var/lib/postgresql/data"
               for v in database["volumes"])
    assert not any(p["target"] == 19090 for p in node.get("ports", []))


def main():
    base = config()
    check_node(base)
    assert set(base["services"]) == {"node", "migrate", "database"}
    assert base["services"]["node"]["restart"] == "unless-stopped"
    assert base["services"]["migrate"]["restart"] == "no"
    assert "build" not in base["services"]["node"]
    dev = config("compose.dev.yaml")
    check_node(dev)
    ports = dev["services"]["node"]["ports"]
    assert len(ports) == 2 and all(p["host_ip"] == "127.0.0.1" for p in ports)
    assert {p["published"] for p in ports} == {"18080", "18443"}
    assert dev["services"]["node"]["restart"] == "no"
    # Even operator port/bind/image overrides must not leak into the test lab.
    lab = config("tests/integration/compose.yaml", extra_env={
        "PULSE_NODE_VPN_BIND": "0.0.0.0", "PULSE_NODE_VPN_PORT": "443",
        "PULSE_NODE_IMAGE": "unrelated:do-not-use",
        "PULSE_NODE_CONFIG_DIR": "/does-not-exist/private",
    })
    check_node(lab)
    assert not lab.get("secrets"), "lab must not use developer secrets"
    assert all(n["internal"] for n in lab["networks"].values())
    for service in lab["services"].values():
        assert not service.get("ports"), "lab must not publish ports"
        assert not service.get("secrets"), "lab must use disposable fixtures"
        assert service.get("network_mode") in (None, "none")
    assert set(lab["services"]["target"]["networks"]) == {"back"}
    for name in ("client", "second", "probe", "bad-key", "bad-uuid"):
        assert set(lab["services"][name]["networks"]) == {"default"}
    assert lab["services"]["node"]["image"] == "pulse-vpn-node:integration-test"
    print("PASS: full node, dev port replacement, shared runtime and isolated test lab")


if __name__ == "__main__":
    main()
