"""Exercise lifecycle/auth using the complete disposable node lab; no host ports."""

import json
from pathlib import Path
import subprocess
import time


ROOT = Path(__file__).resolve().parents[2]
COMPOSE = ["docker", "compose", "--env-file", "/dev/null", "-p", "pulse-vpn-node-smoke",
           "-f", "compose.yaml", "-f", "tests/integration/compose.yaml"]


def command(args, *, check=True):
    return subprocess.run(args, cwd=ROOT, text=True, capture_output=True,
                          check=check, timeout=120)


def inspect(container):
    return json.loads(command(["docker", "inspect", container]).stdout)[0]


def wait_exited(container):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        state = inspect(container)["State"]
        if state["Status"] == "exited":
            return state
        time.sleep(0.1)
    raise AssertionError("agent did not exit")


def main():
    if command(COMPOSE + ["ps", "-a", "-q"]).stdout.strip():
        raise RuntimeError("smoke project already exists; inspect it before running this test")
    try:
        subprocess.run(COMPOSE + ["build", "generate", "node"], cwd=ROOT,
                       check=True, timeout=600)
        command(COMPOSE + ["up", "-d", "--wait", "--wait-timeout", "90", "node"])
        cid = command(COMPOSE + ["ps", "-q", "node"]).stdout.strip()
        assert inspect(cid)["State"]["Health"]["Status"] == "healthy"
        token = command(COMPOSE + ["exec", "-T", "node", "cat", "/fixtures/api-token"]).stdout.strip()
        for headers in [[], ["--header", "Authorization: Bearer wrong"]]:
            response = command(COMPOSE + ["exec", "-T", "node", "wget", "-S", "-O", "/dev/null"]
                               + headers + ["http://127.0.0.1:8080/api/v1/status"], check=False)
            assert response.returncode != 0 and "401 Unauthorized" in response.stderr
        status = json.loads(command(COMPOSE + ["exec", "-T", "node", "pulse-vpn-node", "status"]).stdout)
        assert status == {"status": "ready", "vpnConfigured": True,
                          "core": {"version": "v1.19.33"}}
        print("PASS: real pinned core, healthcheck and API authentication")

        command(COMPOSE + ["exec", "-T", "node", "sh", "-c", 'kill -KILL "$(pidof mihomo)"'])
        assert wait_exited(cid)["ExitCode"] != 0
        command(COMPOSE + ["up", "-d", "--no-deps", "--wait", "--wait-timeout", "45", "node"])
        print("PASS: unexpected core exit fails agent; explicit restart recovers")

        failed = command(COMPOSE + ["run", "--rm", "--no-deps", "-e",
                                   "PULSE_NODE_MIHOMO_BINARY=/missing", "node"], check=False)
        assert failed.returncode != 0
        assert "core configuration validation failed" in failed.stderr
        print("PASS: invalid core binary fails startup")

        command(COMPOSE + ["stop", "-t", "12", "node"])
        state = wait_exited(cid)
        assert state["ExitCode"] == 0 and not state["OOMKilled"]
        logs = command(COMPOSE + ["logs", "--no-color", "node"]).stdout
        assert token not in logs
        print("PASS: graceful SIGTERM shutdown; API token absent from logs")
    finally:
        command(COMPOSE + ["--profile", "test", "down", "--volumes", "--remove-orphans"])


if __name__ == "__main__":
    main()
