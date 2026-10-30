"""Run the same native-platform verification locally and in GitHub Actions.

No publication or deployment. Requires Docker Compose and Python 3. Only disposable
projects are managed; runtime image identity must survive all acceptance suites.
"""

import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
PROJECT = "pulse-vpn-node-storage-test"
COMPOSE = ["docker", "compose", "--env-file", "/dev/null", "-p", PROJECT,
           "-f", "compose.yaml", "-f", "tests/integration/compose.yaml"]
IMAGE = "pulse-vpn-node:integration-test"


def run(args, *, capture=False, timeout=600, env=None):
    return subprocess.run(args, cwd=ROOT, check=True, text=True,
                          capture_output=capture, timeout=timeout, env=env)


def image_id():
    return json.loads(run(["docker", "image", "inspect", IMAGE], capture=True).stdout)[0]["Id"]


def assert_clean(project):
    for args in (["ps", "-aq"], ["network", "ls", "-q"], ["volume", "ls", "-q"]):
        result = run(["docker", *args, "--filter", "label=com.docker.compose.project=" + project], capture=True)
        if result.stdout.strip():
            raise RuntimeError("test project has remaining resources: " + project)


def main():
    run([sys.executable, "tests/integration/compose_check.py"])
    # All cases own disposable resources; refuse stale projects before any mutation.
    cases = [("docker", "pulse-vpn-node-smoke"), ("reality", "pulse-vpn-node-reality-test"),
             ("users", "pulse-vpn-node-users-test"), ("traffic", "pulse-vpn-node-traffic-test"),
             ("telemetry", "pulse-vpn-node-telemetry-test")]
    for project in [PROJECT, *(project for _, project in cases)]:
        assert_clean(project)
    # Runtime build includes formatting, vet, unit/race tests and static compilation.
    run(COMPOSE + ["--profile", "test", "build", "node", "generate", "postgres-check"], timeout=1200)
    verified = image_id()
    try:
        run(COMPOSE + ["up", "-d", "--wait", "--wait-timeout", "120", "database"])
        run(COMPOSE + ["run", "--rm", "--no-deps", "postgres-check"], timeout=300)
    finally:
        run(COMPOSE + ["--profile", "test", "down", "--volumes", "--remove-orphans"])
    assert_clean(PROJECT)
    for case, project in cases:
        run([sys.executable, f"tests/integration/{case}_smoke.py"],
            env={**os.environ, "PULSE_NODE_TEST_NO_BUILD": "1"})
        if image_id() != verified:
            raise RuntimeError("runtime image changed during acceptance tests")
        assert_clean(project)
        print(f"PASS: {case}, unchanged image and clean resources", flush=True)
    print("PASS: native image verified, including PostgreSQL integration tests", flush=True)


if __name__ == "__main__":
    main()
