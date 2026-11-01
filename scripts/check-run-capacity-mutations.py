#!/usr/bin/env python3
"""Break native capacity controls in an isolated copy; require real test failures."""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
REAPER = "cmd/divybot/remote_run_reaper.go"
CAPACITY = "cmd/divybot/run_capacity.go"
MAIN = "cmd/divybot/main.go"
MUTATIONS = [
    ("reaper disabled", REAPER, "if c.dry {", "if true {", "^TestRemoteRunReaperReleasesOnlyNativeInactive$"),
    ("ACTIVE native session mistaken for idle", "cmd/divybot/remote_control_stop.go", 'head.Thread.Status.Type != "idle"', "false", "^TestRemoteRunReaperKeepsActiveAndUnknown$/^active$"),
    ("workspace label ignored", REAPER, " || ws.Label == j.Label", "", "^TestRemoteRunReaperKeepsActiveAndUnknown$/^same-label$"),
    ("done account slot retained", MAIN, "return !nativeCapacityDoneForRef(j, ref, known)", "return true", "^TestRemoteRunDoneReleasesAccountRetainsRecord$"),
    ("governor counts done records", "cmd/divybot/launch_attempt.go", "active[a] = count.active()", "active[a] = count.active() + count.Done", "^TestRemoteRunGovernorCategories$"),
    ("closed inbox fallback removed", MAIN, 'if evidence != "" {', 'if evidence != "" && false {', "^TestRemoteRunClosedInboxTeardownWithoutTUIAnchor$"),
    ("closed inbox skips checkout cleanup", MAIN, "remoteReleaseEvidence = evidence", "remoteReleaseEvidence = evidence; return true", "^TestRemoteRunClosedInbox"),
    ("governor categories hidden", MAIN, "active %d live %d done %d unconfirmed %d", "active %d seated %d finished %d uncertain %d", "^TestRemoteRunGovernorCategories$"),
    ("reaper disconnected from tick", MAIN, "c.reapRemoteRuns(ctx, status, up, allOpen)", "// reaper disconnected", "^TestRemoteRunProductionWiring$"),
    ("input capacity reservation removed", CAPACITY, "func (c *Coord) acquireRunCapacity(h Host, pane string) error {", "func (c *Coord) acquireRunCapacity(h Host, pane string) error { return nil;", "^TestRemoteRunDoneInvalidationAndInputReservation$/^relay$"),
    ("same-tick admission clamp disconnected", "cmd/divybot/matrix.go", "c.reconcileRunCapacityBudget(budget)", "// stale budget retained", "^TestRemoteRunProductionWiring$"),
    ("launch clamp consumes a legacy done seat", CAPACITY, "running := c.admissionRunningLocked(status)", "running := c.activeByAccount()", "^TestRemoteRunAdmissionClampPreservesLegacyDone$"),
    ("pending PR capacity probe publishes final report", CAPACITY, "_, scope, err := c.finalScope(j)", "_, _ = c.publishFinalReport(ctx, j)\n\t\t_, scope, err := c.finalScope(j)", "^TestRemoteRunClaudeDoneProofDoesNotPublish$"),
    ("Claude clear treated as session exit", "cmd/divybot/claude_session_end.go", 'case "logout", "prompt_input_exit", "other":', 'case "clear", "logout", "prompt_input_exit", "other":', "^TestRemoteRunClaudeEndRequiresExitedNativeProcess$"),
    ("native archive precedes durable fence", REAPER, 'c.st.LaunchBlocks[n] = launchBlock{Reason: "agent_disappeared"}\n\t\t\tif c.st.saveLocked() != nil {', 'c.st.LaunchBlocks[n] = launchBlock{Reason: "agent_disappeared"}\n\t\t\tif false {', "^TestRemoteRunReaperReleasesOnlyNativeInactive$/^valid$"),
]


def gate(root, label, selector, green):
    p = subprocess.run(["go", "test", "./cmd/divybot", "-run", selector,
                        "-count=1", "-json"], cwd=root, text=True,
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=120)
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
    with tempfile.TemporaryDirectory(prefix="capacity-") as directory:
        root = Path(directory) / "source"
        shutil.copytree(ROOT, root, ignore=shutil.ignore_patterns(".git", ".llm"))
        gate(root, "green control", "^TestRemoteRun", True)
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
