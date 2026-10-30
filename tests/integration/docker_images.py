"""Build local lab images, or require the exact prebuilt pair in CI."""

import os
import subprocess


def prepare_images(compose, root):
    mode = os.environ.get("PULSE_NODE_TEST_NO_BUILD", "0")
    if mode not in ("0", "1"):
        raise ValueError("PULSE_NODE_TEST_NO_BUILD must be 0 or 1")
    if mode == "1":
        # Fail if either artifact is missing; never silently build a substitute.
        subprocess.run(["docker", "image", "inspect", "pulse-vpn-node:integration-test",
                        "pulse-vpn-node-reality-lab:local"], cwd=root,
                       capture_output=True, check=True, timeout=30)
        return
    subprocess.run(compose + ["build", "generate", "node"], cwd=root,
                   check=True, timeout=600)
