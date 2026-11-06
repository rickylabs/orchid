#!/usr/bin/env python3
"""Break native-run launch and task-start clock guards in an isolated copy; require real test failures."""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
CLOCK = "cmd/divybot/native_run_clock.go"
READ = "cmd/divybot/native_run_read.go"
MAIN = "cmd/divybot/main.go"
COMMIT = "cmd/divybot/native_codex_commit.go"
ACCEPT = "cmd/divybot/native_codex_acceptance.go"
MUTATIONS = [
    ("launch clock disconnected from registration", MAIN,
     "c.recordLaunchClockLocked(j) // guard:launch-clock", "// launch clock disconnected",
     "^TestNativeRunSpawnClocksLaunchAtRegistration$"),
    ("restart re-clocks the launch", CLOCK,
     'if j.LaunchGeneration == "" && !j.SpawnedAt.IsZero() {', "if !j.SpawnedAt.IsZero() {",
     "^TestNativeRunLaunchInstantNeverAdvances$"),
    ("read time served as the launch", READ,
     "Launch: nativeRunInstantOf(j.SpawnedAt, j.LaunchGeneration),", "Launch: nativeRunInstantOf(time.Now(), j.LaunchGeneration),",
     "^TestNativeRunLaunchInstantNeverAdvances$"),
    ("adopted job clocked as a launch", MAIN,
     "SpawnedAt: time.Now(),\n\t\tHome: is.home(),\n\t}", "SpawnedAt: time.Now(),\n\t\tHome: is.home(), LaunchGeneration: c.instance,\n\t}",
     "^TestNativeRunLaunchInstantOmittedForLegacyAndAdoptedJobs$"),
    ("unclocked instant exposed", CLOCK,
     "if at.IsZero() || !nativeRunGeneration.MatchString(generation) {", "if at.IsZero() {",
     "^TestNativeRunLaunchInstantOmittedForLegacyAndAdoptedJobs$"),
    ("sub-millisecond or zoned wire instant", CLOCK,
     "at.UTC().Truncate(time.Millisecond).Format(nativeRunInstantLayout)", "at.Format(time.RFC3339Nano)",
     "^TestNativeRunLaunchInstantIsMillisecondUTCAndClocked$"),
    ("non-canonical fenced instant served", READ,
     " || !validNativeRunInstant(r.Launch) || !validNativeRunInstant(r.TaskStart)", "",
     "^TestNativeRunLaunchInstantFrozenIntoCompletionFence$"),
    ("version 1 advertised with version 2 fields", CLOCK,
     "const nativeRunSchemaVersion = 2", "const nativeRunSchemaVersion = 1",
     "^TestNativeRunLaunchInstantIsMillisecondUTCAndClocked$"),
    ("task start renewed by a later acceptance", CLOCK,
     "if j.TaskStart == nil && !accepted.IsZero() {", "if !accepted.IsZero() {",
     "^TestNativeRunTaskStartPersistedOnceThroughTheDeliveryCommit$"),
    ("refused commit keeps its task start", COMMIT,
     "\t\tj.TaskStart = priorTaskStart\n", "\t\t_ = priorTaskStart\n",
     "^TestNativeRunTaskStartPersistedOnceThroughTheDeliveryCommit$"),
    ("certified acceptance instant dropped", ACCEPT,
     "a.acceptedAt = r.acceptedAt // guard:task-start-certified", "// certified instant dropped",
     "^TestNativeRunTaskStartOnlyFromCertifiedAcceptance$"),
    ("task start invented without acceptance", ACCEPT,
     "\treturn a.acceptedAt\n}", "\treturn time.Now()\n}",
     "^TestNativeRunTaskStartOnlyFromCertifiedAcceptance$"),
]


def gate(root, label, selector, green):
    p = subprocess.run(["go", "test", "./cmd/divybot", "-run", selector,
                        "-count=1", "-json"], cwd=root, text=True,
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=300)
    events = []
    for line in p.stdout.splitlines():
        try:
            events.append(json.loads(line))
        except ValueError:
            pass
    failures = [e["Test"] for e in events if e.get("Test") and e.get("Action") == "fail"]
    passes = [e for e in events if e.get("Test") and e.get("Action") == "pass"]
    print(f"{label}: exit {p.returncode}; test failures {len(failures)}", flush=True)
    if green:
        if p.returncode != 0 or not passes:
            print(p.stdout, file=sys.stderr)
            raise RuntimeError("green control failed or ran no tests")
    elif p.returncode != 1 or not failures:
        print(p.stdout, file=sys.stderr)
        raise RuntimeError("mutant survived or failed before assertions")


def main():
    # Never mutate the checkout or a source tree another gate might be reading.
    with tempfile.TemporaryDirectory(prefix="clock-") as directory:
        root = Path(directory) / "source"
        shutil.copytree(ROOT, root, ignore=shutil.ignore_patterns(".git", ".llm"))
        gate(root, "green control", "^TestNativeRun(LaunchInstant|TaskStart|SpawnClocks)|^TestNativeRunSchemaMatchesSocketOutput$", True)
        for label, name, before, after, selector in MUTATIONS:
            path = root / name
            original = path.read_text()
            if original.count(before) != 1:
                raise RuntimeError("ambiguous mutation anchor: " + label)
            try:
                path.write_text(original.replace(before, after, 1))
                gate(root, "red: " + label, selector, False)
            finally:
                path.write_text(original)
            gate(root, "restored: " + label, selector, True)
        print(f"PASS: {len(MUTATIONS)} compiled mutants failed assertions; all restored controls passed", flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("FAIL/INCONCLUSIVE: " + str(error), file=sys.stderr)
        sys.exit(2)
