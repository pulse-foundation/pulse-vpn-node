"""Exercise this project's real local image, without production services or VPN fixtures."""

import json
import os
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parents[2]
PORT = int(os.environ.get("PULSE_NODE_SMOKE_PORT", "18081"))
ENV = {**os.environ, "PULSE_NODE_PORT": str(PORT)}
COMPOSE = ["docker", "compose", "-p", "pulse-vpn-node-smoke", "-f", "deploy/compose.yaml"]


def command(args, *, check=True):
    return subprocess.run(args, cwd=ROOT, env=ENV, text=True, capture_output=True,
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
    token = (ROOT / ".local/api-token").read_text().strip()
    if command(COMPOSE + ["ps", "-a", "-q"]).stdout.strip():
        raise RuntimeError("smoke project already exists; inspect it before running this test")
    try:
        command(COMPOSE + ["up", "-d", "--wait", "--wait-timeout", "45", "--no-build"])
        cid = command(COMPOSE + ["ps", "-q", "node"]).stdout.strip()
        assert inspect(cid)["State"]["Health"]["Status"] == "healthy"
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        url = f"http://127.0.0.1:{PORT}/api/v1/status"
        for headers in [{}, {"Authorization": "Bearer wrong"}]:
            try:
                opener.open(urllib.request.Request(url, headers=headers), timeout=5)
            except urllib.error.HTTPError as exc:
                assert exc.code == 401
            else:
                raise AssertionError("unauthorized request accepted")
        req = urllib.request.Request(url, headers={"Authorization": "Bearer " + token})
        with opener.open(req, timeout=5) as response:
            status = json.load(response)
        assert status == {"status": "ready", "vpnConfigured": False,
                          "core": {"version": "v1.19.33"}}
        print("PASS: real pinned core, healthcheck and API authentication")

        command(COMPOSE + ["exec", "-T", "node", "sh", "-c", 'kill -KILL "$(pidof mihomo)"'])
        assert wait_exited(cid)["ExitCode"] != 0
        command(COMPOSE + ["up", "-d", "--wait", "--wait-timeout", "45", "--no-build"])
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
        command(COMPOSE + ["down", "--remove-orphans"])


if __name__ == "__main__":
    main()
