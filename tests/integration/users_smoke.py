"""Disposable live-user acceptance lab. Internal networks; no published ports.

Run from the repository root with Docker Compose available:
    python3 tests/integration/users_smoke.py
"""

import json
import queue
import subprocess
import threading
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PROJECT = "pulse-vpn-node-users-test"
COMPOSE = ["docker", "compose", "-p", PROJECT,
           "-f", "deploy/compose.reality-test.yaml",
           "-f", "deploy/compose.users-test.yaml"]


def command(args, *, timeout=180):
    try:
        result = subprocess.run(args, cwd=ROOT, text=True, capture_output=True,
                                check=False, timeout=timeout)
    except subprocess.TimeoutExpired:
        raise RuntimeError("local users lab command timed out") from None
    if result.returncode:
        # Docker/DB logs may contain credentials; do not surface raw diagnostics.
        raise RuntimeError("local users lab command failed (output withheld)")
    return result.stdout.strip()


def core_identity():
    # PID plus Linux process start ticks identifies the actual core across reloads.
    value = command(COMPOSE + ["exec", "-T", "node", "/bin/sh", "-c",
                              'for p in /proc/[0-9]*; do '
                              'if [ "$(cat "$p/comm" 2>/dev/null)" = mihomo ]; then '
                              'awk \'{print $1, $22}\' "$p/stat"; fi; done'])
    if len(value.splitlines()) != 1 or len(value.split()) != 2:
        raise RuntimeError("expected exactly one managed Mihomo process")
    return value


def run_live(target_ip):
    proc = subprocess.Popen(COMPOSE + ["run", "--rm", "--no-deps", "-T", "-e",
                            "REALITY_TEST_TARGET_IP=" + target_ip, "probe", "users-live"],
                            cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, text=True, bufsize=1)
    lines = queue.Queue()

    def read_lines():
        for line in proc.stdout:
            lines.put(line.strip())
        lines.put(None)

    reader = threading.Thread(target=read_lines, daemon=True)
    reader.start()
    failure = None
    try:
        while True:
            try:
                line = lines.get(timeout=100)
            except queue.Empty:
                raise RuntimeError("live user probe progress timed out") from None
            if line is None:
                break
            if line == "READY: database outage":
                command(COMPOSE + ["stop", "database"])
                proc.stdin.write("continue\n")
                proc.stdin.flush()
            elif line == "READY: database recovery":
                command(COMPOSE + ["up", "-d", "--no-deps", "--wait", "database"])
                proc.stdin.write("continue\n")
                proc.stdin.flush()
            elif line.startswith("FAIL: "):
                failure = line[:512]
            elif line.startswith("PASS: "):
                print(line, flush=True)
        if proc.wait(timeout=10):
            raise RuntimeError(failure or "live user probe failed (diagnostics withheld)")
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=10)
        proc.stdin.close()
        proc.stdout.close()
        reader.join(timeout=2)


def main():
    if command(COMPOSE + ["ps", "-a", "-q"]):
        raise RuntimeError("users test project exists; inspect before rerunning")
    try:
        # Credentials are generated at runtime; build diagnostics contain no fixtures.
        subprocess.run(COMPOSE + ["build", "generate", "node"], cwd=ROOT,
                       check=True, timeout=600)
        command(COMPOSE + ["up", "-d", "--wait", "--wait-timeout", "120",
                           "target", "node", "client", "second"])
        target_id = command(COMPOSE + ["ps", "-q", "target"])
        details = json.loads(command(["docker", "inspect", target_id]))[0]
        target_ip = details["NetworkSettings"]["Networks"][PROJECT + "_back"]["IPAddress"]
        identity = core_identity()
        run_live(target_ip)
        if identity != core_identity():
            raise RuntimeError("Mihomo process changed during live user operations")
        print("PASS: Mihomo PID and process start time unchanged across live user operations", flush=True)
        command(COMPOSE + ["restart", "node"])
        command(COMPOSE + ["up", "-d", "--no-deps", "--wait", "node"])
        output = command(COMPOSE + ["run", "--rm", "--no-deps", "-T", "-e",
                                    "REALITY_TEST_TARGET_IP=" + target_ip,
                                    "probe", "users-persist"])
        for line in output.splitlines():
            if line.startswith("PASS: "):
                print(line, flush=True)
    finally:
        command(COMPOSE + ["down", "--volumes", "--remove-orphans"])


if __name__ == "__main__":
    main()
