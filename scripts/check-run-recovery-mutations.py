#!/usr/bin/env python3
"""Require assertion failures for checkout and native recovery regressions."""
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
CHECKOUT = "cmd/divybot/run_checkout.go"
RECOVERY = "cmd/divybot/goal_recovery.go"
MAIN = "cmd/divybot/main.go"
COMPLETION = "cmd/divybot/completion.go"
TRANSPORT = "cmd/divybot/remote_control_transport.go"
COMMAND = ["go", "test", "./cmd/divybot", "-run", "^(TestRunCheckout|TestCapacity)", "-count=1", "-json"]
MUTATIONS = [
    ("default base uses admission-era stale ref", CHECKOUT, 'return "HEAD", nil', 'return "refs/heads/release", nil'),
    ("default fetch uses cached local HEAD", CHECKOUT, 'git fetch --depth=1 --no-tags origin %s >/dev/null 2>&1', 'git rev-parse %s >/dev/null'),
    ("explicit brief base ignored", CHECKOUT, 'return o.Base, nil', 'return "HEAD", nil'),
    ("start commit not recorded", CHECKOUT, 'binding["StartSHA"] = goalJSONBytes(sha)', 'delete(binding, "StartSHA")'),
    ("start commit can be replaced", CHECKOUT, 'previousSHA != sha', 'false'),
    ("native capacity recognition removed", RECOVERY, 'code == "serverOverloaded"', 'code == "unrecognizedCapacity"'),
    ("all native failures treated as capacity", RECOVERY, 'code == "serverOverloaded"', '(code == "serverOverloaded" || true)'),
    ("paused goal resumed", RECOVERY, 'goal.Status != "blocked" && !(owned && goal.Status == "active")', 'goal.Status != "paused" && goal.Status != "blocked" && !(owned && goal.Status == "active")'),
    ("backoff removed", RECOVERY, 'if now.Before(next.NextAttemptAt) {', 'if now.Before(next.NextAttemptAt) && false {'),
    ("retry bound removed", RECOVERY, 'if next.Attempts >= capacityRetryLimit {', 'if next.Attempts >= capacityRetryLimit && false {'),
    ("reservation moved after native effect", RECOVERY, 'if err := c.saveGoalRecovery(j, next); err != nil {\n\t\treturn true, err\n\t}\n\treceipt, native, err :=', 'if err := error(nil); err != nil {\n\t\treturn true, err\n\t}\n\treceipt, native, err :='),
    ("private native binding not checked", RECOVERY, 'native != p.thread', 'false'),
    ("post-checkpoint goal change ignored", RECOVERY, '!reflect.DeepEqual(goal, current)', 'false'),
    ("resume writes goal intent again", RECOVERY, '"status": "active", "origin": "automatic"', '"status": "active", "origin": "automatic", "objective": intent.Objective'),
    ("native resume missing", RECOVERY, 'resumed, err := resumeCapacityGoal(p, current, j.NativeGoal.Intent)', 'resumed, err := current, error(nil)'),
    ("native accounting verification removed", RECOVERY, 'after.TokensUsed < before.TokensUsed', 'false'),
    ("running slot released during recovery", MAIN, 'if capacityRecoveryOwns(j) {\n\t\treturn true\n\t}', 'if capacityRecoveryOwns(j) {\n\t\treturn false\n\t}'),
    ("ordinary blocked supervision wins", MAIN, 'if matched && !recovering && ref.Status == "blocked" {', 'if matched && ref.Status == "blocked" {'),
    ("stranded input allowed during recovery", MAIN, 'suppressInput := recovering || !matched', 'suppressInput := !matched'),
    ("daemon timer ignores pending recovery", RECOVERY, 'if delay == 0 || next < delay {', 'if delay == 0 || next < delay { next = fleetRecoveryEvery;'),
    ("noncanonical resume allowed", RECOVERY, 'if !p.canonical {', 'if !p.canonical && false {'),
    ("canonical capability lost", TRANSPORT, 'p.canonical = true', 'p.canonical = false'),
    ("failed state saves create a hot timer", RECOVERY, 'next = capacityRetryInitial // durability failures must not busy-loop the daemon', 'next = time.Millisecond // durability failures must not busy-loop the daemon'),
    ("watch deadline ignored", RECOVERY, 'if next.WatchUntil.IsZero() || !now.Before(next.WatchUntil) {', 'if next.WatchUntil.IsZero() || !now.Before(next.WatchUntil) && false {'),
    ("unchanged idle probes repeated", RECOVERY, '} else if !changed && now.Sub(j.GoalRecoveryObservation.At) < fleetRecoveryEvery {', '} else if !changed && now.Sub(j.GoalRecoveryObservation.At) < fleetRecoveryEvery && false {'),
    ("recovery timer polls the entire board", MAIN, 'case <-recoveryWake:\n\t\t\tc.capacityRecoveryPass(ctx)', 'case <-recoveryWake:\n\t\t\tc.tick(ctx)'),
    ("completion capacity hook removed", COMPLETION, 'c.recoverCapacityWithPort(ctx, j, p, time.Now())', 'false, error(nil)'),
    ("manual native resume ignored", RECOVERY, 'if owned && j.GoalRecovery.State == recoveryExhausted && goal.Status == "active" {', 'if owned && j.GoalRecovery.State == recoveryExhausted && goal.Status == "active" && false {'),
    ("exhausted supervision never observes resumes", RECOVERY, 'changed := capacityObservationChanged(j, ref)\n\tif owned && j.GoalRecovery.State != recoveryExhausted {', 'changed := capacityObservationChanged(j, ref)\n\tif owned && j.GoalRecovery.State == recoveryExhausted { return true }; if owned {'),
    ("exhausted observations bypass debounce", RECOVERY, 'changed := capacityObservationChanged(j, ref)\n\tif owned && j.GoalRecovery.State != recoveryExhausted {', 'changed := capacityObservationChanged(j, ref)\n\tif owned {'),
    ("native release keeps suppression", RECOVERY, 'next.State = ""', 'next.State = recoveryExhausted'),
    ("unchanged automatic exhaustion releases ownership", RECOVERY, 'if turn == next.TurnID {', 'if turn == next.TurnID && false {'),
    ("native update timestamp ignored", RECOVERY, 'goal.UpdatedAt != prior.ObservedGoalUpdatedAt', 'false'),
    ("automatic readback snapshot lost", RECOVERY, 'next.ObservedGoalStatus = resumed.Status', 'next.ObservedGoalStatus = "blocked"'),
    ("exhaustion snapshot stale after lost acknowledgement", RECOVERY, 'next.ObservedGoalStatus = goal.Status', 'next.ObservedGoalStatus = "blocked"'),
    ("same failed turn notice repeated", RECOVERY, 'if notified {', 'if notified && false {'),
    ("legacy exhaustion notice history lost", RECOVERY, 'next.ExhaustedTurnID = next.TurnID // migrate exhausted records written before notice tracking', 'next.ExhaustedTurnID = "" // migrate exhausted records written before notice tracking'),
]


def gate(label, green, selector=None):
    command = COMMAND.copy()
    if selector is not None:
        command[command.index("-run") + 1] = selector
    p = subprocess.run(command, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=120)
    failures, assertions = [], []
    passed = 0
    for line in p.stdout.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if event.get("Action") == "pass" and event.get("Test"):
            passed += 1
        if event.get("Action") == "fail" and event.get("Test"):
            failures.append(event["Test"])
        output = event.get("Output", "").strip()
        if output.startswith(("goal_recovery", "run_checkout", "run_recovery")) and ".go:" in output:
            assertions.append(output)
    print(f"{label}: exit {p.returncode}; assertion failures {len(failures)}", flush=True)
    for line in assertions:
        print(line, flush=True)
    if green:
        if p.returncode != 0 or passed == 0:
            raise RuntimeError("green control failed or executed no tests")
    elif p.returncode != 1 or not failures:
        raise RuntimeError("mutation survived or did not compile and reach assertions")


def main():
    originals = {name: (ROOT / name).read_text() for name in {m[1] for m in MUTATIONS}}
    try:
        gate("control", True)
        for label, filename, before, after in MUTATIONS:
            original = originals[filename]
            if original.count(before) != 1:
                raise RuntimeError("mutation anchor is ambiguous: " + label)
            (ROOT / filename).write_text(original.replace(before, after, 1))
            try:
                selector = "^TestRunCheckout" if filename == CHECKOUT else "^TestCapacity"
                gate("mutant: " + label, False, selector)
            finally:
                (ROOT / filename).write_text(original)
            gate("restored: " + label, True, selector)
        print(f"PASS: {len(MUTATIONS)} compiled mutants failed assertions; {len(MUTATIONS)} restored controls passed", flush=True)
    finally:
        for filename, content in originals.items():
            (ROOT / filename).write_text(content)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("FAIL/INCONCLUSIVE: " + str(error), file=sys.stderr)
        sys.exit(2)
