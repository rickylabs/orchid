#!/usr/bin/env python3
"""Exercise herdr's real busy-pane refusal without launching a model or retaining native IDs."""
import json
import os
import subprocess
import sys
import time


def call(*args):
    result = subprocess.run(["herdr", *args], capture_output=True, text=True, timeout=15)
    try:
        payload = json.loads(result.stdout or result.stderr)
    except ValueError:
        payload = {}
    return result.returncode, payload


def main():
    if os.environ.get("HERDR_ENV") != "1":
        print("registration-negative: INCONCLUSIVE not inside a herdr-managed pane")
        return 2
    code, created = call("pane", "split", "--current", "--direction", "down", "--cwd", os.getcwd(), "--no-focus")
    pane = created.get("result", {}).get("pane", {}).get("pane_id")
    if code or not pane:
        print("registration-negative: INCONCLUSIVE cannot create owned test pane")
        return 2
    outcome = 1
    try:
        code, _ = call("pane", "run", pane, "sleep 30")
        if code:
            print("registration-negative: INCONCLUSIVE foreground command unavailable")
            return 2
        time.sleep(0.5)
        code, reply = call("agent", "start", "registration-negative-control", "--kind", "codex", "--pane", pane, "--timeout", "2000")
        expected = reply.get("error", {}).get("code") == "agent_pane_busy"
        print("busy-pane agent start exit:", code)
        print("busy-pane agent start error:", "agent_pane_busy" if expected else "unexpected")
        outcome = 0 if code == 1 and expected else 1
    finally:
        code, _ = call("pane", "close", pane)
        print("owned-pane cleanup exit:", code)
        if code:
            outcome = 1
    print("registration-negative:", "PASS" if outcome == 0 else "FAIL")
    return outcome


if __name__ == "__main__":
    sys.exit(main())
