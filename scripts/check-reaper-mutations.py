#!/usr/bin/env python3
"""Mutate only synthetic reaper checks, restore every source, and retain no private diagnostics."""
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
GATE = "cmd/divybot/reaper.go"
UNIX = "cmd/divybot/reaper_unix.go"
MAIN = "cmd/divybot/main.go"
MATRIX = "cmd/divybot/matrix.go"
COMMAND = ["go", "test", "./cmd/divybot", "-run", "Test(Reap|Reaper|NoSpawn|HoldCount)", "-count=1", "-timeout=45s", "-json"]
MUTATIONS = [
    ("hold registration", GATE, "g.count++", "g.count += 0"),
    ("hold release", GATE, "g.count--", "g.count -= 0"),
    ("owned child exclusion", GATE, "if g.count != 0 {", "if false {"),
    ("spawn exclusion during collection", GATE, "return collect()", "g.mu.Unlock(); n := collect(); g.mu.Lock(); return n"),
    ("PID 1 activation", GATE, "return pid == 1", "return true"),
    ("nonblocking live-child fence", UNIX, "syscall.WNOHANG, nil", "0, nil"),
    ("wait interrupted retry", UNIX, "if err == syscall.EINTR {", "if false {"),
    ("drain all exited children", UNIX, "n++", "n++; return n"),
    ("stop on live children and wait errors", UNIX, "pid <= 0 || err != nil", "pid <= 0 && err != nil"),
    ("collect any adopted child", UNIX, "wait(-1, &ws", "wait(0, &ws"),
    # This code is unreachable. The source guard must reject a signalling API without ever invoking it.
    ("no signalling API", UNIX, "n := 0", "if false { _ = syscall.Kill(0, 0) }; n := 0"),
    ("signal wake", UNIX, "case <-sigs:", "case <-make(chan os.Signal):"),
    ("backstop wake", UNIX, "case <-ticks:", "case <-make(chan time.Time):"),
    ("stop lifecycle", UNIX, "case <-stop:\n\t\t\treturn", "case <-stop:\n\t\t\tcontinue"),
    ("main startup hook", MAIN, "startReaper(ctx.Done())", ""),
]
for function, filename in [("run", MAIN), ("resolveGHToken", MAIN), ("refreshLocal", MAIN), ("ghJSON", MAIN), ("matrixCommand", MATRIX)]:
    MUTATIONS.append(("exec gate: " + function, filename, function, None))


def check(label, should_pass):
    result = subprocess.run(COMMAND, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=90)
    failures = []
    assertions = []
    package_result = []
    for line in result.stdout.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if event.get("Action") == "fail" and event.get("Test"):
            failures.append(event["Test"])
        output = event.get("Output", "").rstrip()
        if output.startswith("    reaper") and ".go:" in output:
            assertions.append(output)
        if event.get("Action") == "output" and not event.get("Test") and output.startswith(("ok  ", "FAIL\t")):
            package_result.append(output)
    print(label + ": " + " ".join(COMMAND), flush=True)
    print("exit " + str(result.returncode), flush=True)
    # These are actual assertion lines and test outcomes, excluding helper dumps and absolute paths.
    for line in assertions + package_result:
        print(line, flush=True)
    if should_pass:
        if result.returncode != 0:
            raise RuntimeError("control failed or was inconclusive")
    elif result.returncode != 1 or not failures:
        raise RuntimeError("mutation survived or did not reach a test assertion")


def main():
    originals = {name: (ROOT / name).read_text() for name in {m[1] for m in MUTATIONS}}
    try:
        check("baseline", True)
        for name, filename, before, after in MUTATIONS:
            original = originals[filename]
            if after is None:
                # Remove precisely this function's hold, not a different launch site's hold.
                import re
                declaration = re.search(r"func (?:\([^\n]*\) )?" + re.escape(before) + r"\([^\n]*\{", original)
                if declaration is None:
                    raise RuntimeError("mutation site missing")
                start = original.index("defer children.hold()()", declaration.end())
                changed = original[:start] + original[start:].replace("defer children.hold()()", "", 1)
            else:
                if original.count(before) != 1:
                    raise RuntimeError("mutation site ambiguous")
                changed = original.replace(before, after, 1)
            (ROOT / filename).write_text(changed)
            try:
                check("mutated: " + name, False)
            finally:
                (ROOT / filename).write_text(original)
            check("restored: " + name, True)
        print(f"PASS: {len(MUTATIONS)} mutations failed assertions; {len(MUTATIONS)} restored controls passed", flush=True)
    finally:
        for name, original in originals.items():
            (ROOT / name).write_text(original)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("FAIL/INCONCLUSIVE: reaper mutation verification did not finish", file=sys.stderr)
        sys.exit(2)
