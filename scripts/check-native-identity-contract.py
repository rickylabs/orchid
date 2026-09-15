#!/usr/bin/env python3
"""Read-only contract probe. Never print a response value or a native identifier."""
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import tempfile


def run(argv):
    result = subprocess.run(argv, capture_output=True, text=True, timeout=15)
    if result.returncode:
        raise RuntimeError("contract command unavailable")
    return result.stdout


def check(condition, label):
    print(("PASS " if condition else "FAIL ") + label)
    if not condition:
        raise RuntimeError("contract check failed")


def main():
    schema = json.loads(run(["herdr", "api", "schema", "--json"]))
    check(schema["protocol"] == 22, "herdr protocol 22")
    definitions = schema["schemas"]["success_response"]["$defs"]
    check("agent_session" in definitions["AgentInfo"]["properties"], "AgentInfo has optional native session")
    check(set(definitions["AgentSessionInfo"]["required"]) == {"source", "agent", "kind", "value"}, "native session has provenance and kind")
    check(definitions["AgentSessionRefKind"]["enum"] == ["id", "path"], "id is distinct from path")
    binary = pathlib.Path(shutil.which("herdr")).read_bytes()
    check(b"# HERDR_INTEGRATION_ID=codex\n# HERDR_INTEGRATION_VERSION=8" in binary, "bundled Codex integration v8")
    start = binary.index(b"# HERDR_INTEGRATION_ID=codex")
    end = binary.index(b"\nPY", start)
    hook = binary[start:end]
    for needle, label in [
        (b'source = "herdr:codex"', "Codex native hook source"),
        (b'session_id = hook_input.get("session_id")', "identity comes from hook input"),
        (b'inherited_session_id != agent_session_id', "inherited thread mismatch is guarded"),
        (b'"method": "pane.report_agent_session"', "hook reports identity over IPC"),
    ]:
        check(needle in hook, label)
    print("herdr binary sha256=" + hashlib.sha256(binary).hexdigest())
    with tempfile.TemporaryDirectory() as directory:
        run(["codex", "app-server", "generate-json-schema", "--out", directory])
        root = pathlib.Path(directory)
        def read(name):
            return json.loads(next(root.rglob(name + ".json")).read_text())
        start = read("ThreadStartParams")["properties"]
        check("threadId" not in start and "sessionId" not in start, "thread/start does not accept caller-selected identity")
    help_text = run(["codex", "--help"])
    check("--session-id" not in help_text and "--thread-id" not in help_text, "interactive CLI has no advertised caller-identity flag")
    if os.environ.get("HERDR_ENV") != "1" or not os.environ.get("HERDR_PANE_ID"):
        print("INCONCLUSIVE native-session-unavailable (no explicit current pane)")
        return 2
    result = json.loads(run(["herdr", "agent", "get", os.environ["HERDR_PANE_ID"]]))["result"]["agent"]
    native = result.get("agent_session")
    if not isinstance(native, dict) or native.get("source") != "herdr:codex" or native.get("kind") != "id" or not native.get("value"):
        print("INCONCLUSIVE native-session-unavailable (current agent has no usable native hook reference)")
        return 2
    print("Native hook reference present; launch-to-thread verification requires a new dispatch receipt.")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (KeyError, ValueError, OSError, RuntimeError, subprocess.TimeoutExpired, StopIteration):
        print("INCONCLUSIVE native-session-contract-unsupported")
        raise SystemExit(2)
