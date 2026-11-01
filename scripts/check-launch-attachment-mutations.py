#!/usr/bin/env python3
"""Break the Codex launch attachment and acceptance guards in an isolated copy.

Every mutant must compile and fail a focused behavioural assertion; every
exact restoration must pass the same selector again.
"""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
BOUNDARY = "cmd/divybot/codex_launch_boundary.go"
IDENTITY = "cmd/divybot/remote_control_identity.go"
SIGNALS = "cmd/divybot/codex_acceptance_signals.go"
ACCEPT = "cmd/divybot/native_codex_acceptance.go"
MUTATIONS = [
    ("reused PID reported as another process", BOUNDARY,
     "case stored.PID == current.PID: // guard:process-reused\n\t\treturn attachmentProcessReused",
     "case stored.PID == current.PID: // guard:process-reused\n\t\treturn attachmentConsumerChanged",
     "^TestAttachmentProcessContinuityTyped$"),
    ("ambiguous resume record reported as a changed consumer", IDENTITY,
     "out('refused',reason='ambiguous')", "out('refused',reason='consumer-changed')",
     "^TestAttachmentResumeRecordTypedOutcomes$"),
    ("refused resume record waited on as pending", IDENTITY,
     "\t\t\tcase attachmentAmbiguous, attachmentConsumerChanged:\n\t\t\t\treturn attachmentRefused(outcome)",
     "\t\t\tcase \"never\":\n\t\t\t\treturn attachmentRefused(outcome)",
     "^TestAttachmentResumeRecordTypedOutcomes$"),
    ("private log proof labelled vendor-native", BOUNDARY,
     "a.Source = attachmentSourcePrivateLog", 'a.Source = "vendor-native"',
     "^TestAttachmentAuthorityIsNeverVendorNative$"),
    ("stale generation admitted", SIGNALS,
     "if sig.Generation != s.generation { // guard:signal-generation", "if false { // guard:signal-generation",
     "^TestAcceptanceSignalReducerTable$/^stale-generation$"),
    ("duplicate notification treated as new", SIGNALS,
     "if s.seen[signalKey(sig)] && sig.Kind != signalApproval { // guard:signal-duplicate", "if false {",
     "^TestAcceptanceDuplicateNotifications$"),
    ("turn before the effect admitted", SIGNALS,
     'if sig.Kind != signalReady || sig.Status != "idle" { // guard:signal-pre-submit', "if false {",
     "^TestAcceptancePreSubmitTurnSignal$"),
    ("native exit ignored", SIGNALS,
     "if sig.Kind == signalExit { // guard:signal-exit", "if false {",
     "^TestAcceptanceNativeExitAfterEffect$"),
    ("acceptance ahead of the submit reply unclassified", SIGNALS,
     "if s.inFlight(sig.Seq) {\n\t\treturn classAheadOfAck, nil", "if false {\n\t\treturn classAheadOfAck, nil",
     "^TestAcceptanceNativeAheadOfSubmitAck$"),
    ("approval request answered", SIGNALS,
     'return p.onServerRequest(method, m["params"]) // guard:never-answer',
     '_ = p.send(map[string]any{"id": m["id"], "result": map[string]any{}})\n\treturn p.onServerRequest(method, m["params"])',
     "^TestAcceptanceApprovalNeverAnswered$"),
    ("signal wake ignored", SIGNALS,
     "if !p.wake || !honor {", "if true {",
     "^TestAcceptanceItemSignalWakesMaterializingTurn$"),
    ("official subscription dropped", ACCEPT,
     'raw, err := p.request("thread/resume", map[string]any{"threadId": p.thread, "excludeTurns": true}) // guard:official-subscription',
     'raw, err := json.RawMessage(nil), error(goalError("goal-daemon-refused"))',
     "^TestAcceptanceOfficialSubscriptionOrder$"),
    ("lost acknowledgment upgraded by early native evidence", ACCEPT,
     "if opErr != nil { // guard:lost-ack", "if opErr != nil && r.signals.aheadOfAck == 0 {",
     "^TestAcceptanceAheadOfAckThenLostAck$"),
    ("pre-effect binding check removed", ACCEPT,
     "if err := a.binding(ctx); err != nil { // guard:pre-effect-binding",
     "if err := error(nil); err != nil {",
     "^TestAcceptancePreEffectBindingTyped$"),
    ("reused process collapsed into binding-changed", ACCEPT,
     "if o == attachmentProcessReused { // guard:binding-typed", "if false {",
     "^TestAcceptancePreEffectBindingTyped$/^process-reused$"),
    ("signal refusal swallowed as a source gap", ACCEPT,
     'if g, ok := err.(goalError); ok && strings.HasPrefix(string(g), "delivery-unconfirmed-") { // guard:keep-signal-refusal',
     "if false {",
     "^TestAcceptancePreSubmitTurnSignal$"),
]


def gate(root, label, selector, green):
    p = subprocess.run(["go", "test", "./cmd/divybot", "-run", selector, "-count=1", "-json"], cwd=root, text=True,
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=300)
    events = []
    for line in p.stdout.splitlines():
        try:
            events.append(json.loads(line))
        except ValueError:
            pass
    failures = [e["Test"] for e in events if e.get("Test") and e.get("Action") == "fail"]
    passes = [e for e in events if e.get("Test") and e.get("Action") == "pass"]
    build_failed = any(e.get("Action") == "build-fail" for e in events) or "build failed" in p.stdout
    print(f"{label}: exit {p.returncode}; test failures {len(failures)}", flush=True)
    if green:
        if p.returncode != 0 or not passes:
            print(p.stdout, file=sys.stderr)
            raise RuntimeError("green control failed or ran no tests")
    elif p.returncode != 1 or not failures or build_failed:
        print(p.stdout, file=sys.stderr)
        raise RuntimeError("mutant survived or failed before assertions")


def main():
    # Never mutate the checkout or a source tree another gate might be reading.
    with tempfile.TemporaryDirectory(prefix="attach-") as directory:
        root = Path(directory) / "source"
        shutil.copytree(ROOT, root, ignore=shutil.ignore_patterns(".git", ".llm"))
        gate(root, "green control", "^(TestAttachment|TestAcceptanceSignal|TestAcceptance(Official|Duplicate|PreSubmit|NativeExit|NativeAhead|AheadOfAck|Approval|ItemSignal|PreEffect))", True)
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
            if path.read_text() != original:
                raise RuntimeError("restoration failed: " + label)
            gate(root, "restored: " + label, selector, True)
        print(f"PASS: {len(MUTATIONS)} compiled mutants failed assertions; all restored controls passed", flush=True)


if __name__ == "__main__":
    try:
        main()
    except RuntimeError as e:
        print("FAIL:", e, file=sys.stderr)
        sys.exit(1)
