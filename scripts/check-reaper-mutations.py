#!/usr/bin/env python3
"""Mutate only synthetic reaper checks, restore every source, and retain no private diagnostics."""
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
GATE = "cmd/divybot/reaper.go"
UNIX = "cmd/divybot/reaper_unix.go"
MAIN = "cmd/divybot/main.go"
LINUX = "cmd/divybot/reaper_linux.go"
COMMAND = ["go", "test", "./cmd/divybot", "-run", "^TestReaper", "-count=1", "-timeout=45s", "-json"]
MUTATIONS = [
    ("owned Wait removed", GATE, "err := c.Cmd.Wait() // guard:owned-wait", "var err error // guard:owned-wait"),
    ("owned PID registration", GATE, "c.owner.owned[c.Process.Pid] = c", ""),
    ("owned PID release", GATE, "delete(c.owner.owned, c.Process.Pid)", ""),
    ("owned PID exclusion", GATE, "pid <= 0 || g.owned[pid] != nil", "pid <= 0"),
    ("start fence during collection", GATE, "defer g.mu.Unlock()\n\tn := 0", "defer g.mu.Unlock()\n\tg.mu.Unlock()\n\tdefer g.mu.Lock()\n\tn := 0"),
    ("PID 1 activation", GATE, "return pid == 1", "return true"),
    ("nonblocking live-child wait", UNIX, "syscall.WNOHANG, nil", "0, nil"),
    ("wait interrupted retry", UNIX, "if err == syscall.EINTR {", "if false {"),
    ("positive-PID wait", UNIX, "wait(pid, &ws", "wait(-1, &ws"),
    ("direct-child enumeration", LINUX, "parent == os.Getpid()", "parent != os.Getpid()"),
    # Unreachable: prove the source guard rejects signalling without sending one.
    ("no signalling API", UNIX, "if pid <= 0 {", "if false { _ = syscall.Kill(0, 0) }; if pid <= 0 {"),
    ("signal wake", UNIX, "case <-sigs:", "case <-make(chan os.Signal):"),
    ("backstop wake", UNIX, "case <-ticks:", "case <-make(chan time.Time):"),
    ("stop lifecycle", UNIX, "case <-stop:\n\t\t\treturn", "case <-stop:\n\t\t\tcontinue"),
    ("main startup hook", MAIN, "startReaper(ctx.Done())", ""),
    ("helper bypass", MAIN, "out, err := cmd.CombinedOutput()", "out, err := cmd.Cmd.CombinedOutput()"),
]


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
