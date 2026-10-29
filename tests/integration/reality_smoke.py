"""Run disposable VLESS/REALITY tests on internal Docker networks. No published ports."""

import json
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
COMPOSE = ["docker", "compose", "-p", "pulse-vpn-node-reality-test",
           "-f", "deploy/compose.reality-test.yaml"]


def command(args, *, check=True, timeout=180):
    result = subprocess.run(args, cwd=ROOT, text=True, capture_output=True,
                            check=False, timeout=timeout)
    if check and result.returncode:
        # Test programs emit only sanitized errors, never fixture contents.
        raise RuntimeError(result.stderr.strip() or "Docker test command failed")
    return result


def probe(name):
    target_id = command(COMPOSE + ["ps", "-q", "target"]).stdout.strip()
    details = json.loads(command(["docker", "inspect", target_id]).stdout)[0]
    networks = details["NetworkSettings"]["Networks"]
    target_ip = networks["pulse-vpn-node-reality-test_back"]["IPAddress"]
    result = command(COMPOSE + ["run", "--rm", "--no-deps", "-e",
                               "REALITY_TEST_TARGET_IP=" + target_ip, "probe", name])
    print(result.stdout.strip(), flush=True)


def main():
    if command(COMPOSE + ["ps", "-a", "-q"]).stdout.strip():
        raise RuntimeError("test project exists; inspect before rerunning")
    try:
        # Build logs contain no fixture credentials; fixtures are generated at runtime only.
        subprocess.run(COMPOSE + ["build", "generate", "node"], cwd=ROOT, check=True, timeout=240)
        command(COMPOSE + ["up", "-d", "--wait", "--wait-timeout", "90",
                           "target", "node", "client", "bad-uuid", "bad-key"])
        probe("probe")
        probe("reject-bad-uuid")
        probe("reject-bad-key")

        # Bind the VPN port inside a disposable node container before starting its agent.
        failed = command(COMPOSE + ["run", "--rm", "--no-deps", "--entrypoint", "/bin/sh",
                                   "node", "-c", 'nc -l -p 8443 >/dev/null 2>&1 & '
                                   'while ! netstat -ltn | grep -q ":8443 "; do sleep 0.05; done; '
                                   'exec /usr/local/bin/pulse-vpn-node'], check=False, timeout=30)
        assert failed.returncode != 0
        assert "VLESS listen address is unavailable" in failed.stderr
        print("PASS: occupied VLESS port fails startup", flush=True)

        command(COMPOSE + ["stop", "node"])
        probe("reject-stopped-node")
        print("PASS: stopping the node prevents transfer; no direct fallback", flush=True)
    finally:
        command(COMPOSE + ["down", "--volumes", "--remove-orphans"])


if __name__ == "__main__":
    main()
