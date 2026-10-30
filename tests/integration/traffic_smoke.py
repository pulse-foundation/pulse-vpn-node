"""Disposable per-user traffic acceptance with pinned Mihomo and PostgreSQL.

Run from the repository root:
    python3 tests/integration/traffic_smoke.py

No published ports or public targets; the coordinator only stops/restarts services
inside its own Compose project. Runtime fixture credentials never enter output.
"""

import json
import subprocess

import users_smoke as lab

lab.PROJECT = "pulse-vpn-node-traffic-test"
lab.COMPOSE = ["docker", "compose", "--env-file", "/dev/null", "-p", lab.PROJECT,
               "-f", "compose.yaml", "-f", "tests/integration/compose.yaml"]


def main():
    if lab.command(lab.COMPOSE + ["ps", "-a", "-q"]):
        raise RuntimeError("traffic test project exists; inspect before rerunning")
    try:
        subprocess.run(lab.COMPOSE + ["build", "generate", "node"], cwd=lab.ROOT,
                       check=True, timeout=600)
        # Fresh named volumes are root-owned; the probe gets only this test volume.
        lab.command(lab.COMPOSE + ["run", "--rm", "--no-deps", "-T", "--user", "0:0",
                                  "--cap-add", "CHOWN", "--entrypoint", "/bin/chown", "probe",
                                  "10001:10001", "/traffic-baseline"])
        lab.command(lab.COMPOSE + ["up", "-d", "--wait", "--wait-timeout", "120",
                                  "target", "node", "client", "second"])
        target_id = lab.command(lab.COMPOSE + ["ps", "-q", "target"])
        details = json.loads(lab.command(["docker", "inspect", target_id]))[0]
        target_ip = details["NetworkSettings"]["Networks"][lab.PROJECT + "_back"]["IPAddress"]
        identity = lab.core_identity()
        lab.run_live(target_ip, "traffic-live")
        if identity != lab.core_identity():
            raise RuntimeError("Mihomo process changed during traffic/recovery checks")
        print("PASS: same Mihomo process serves traffic during database outage/recovery", flush=True)
        lab.command(lab.COMPOSE + ["restart", "node"])
        lab.command(lab.COMPOSE + ["up", "-d", "--no-deps", "--wait", "node"])
        output = lab.command(lab.COMPOSE + ["run", "--rm", "--no-deps", "-T",
                                           "probe", "traffic-persist"])
        for line in output.splitlines():
            if line.startswith("PASS: "):
                print(line, flush=True)
    finally:
        lab.command(lab.COMPOSE + ["--profile", "test", "down", "--volumes", "--remove-orphans"])


if __name__ == "__main__":
    main()
