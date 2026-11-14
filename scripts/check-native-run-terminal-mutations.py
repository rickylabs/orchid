#!/usr/bin/env python3
"""Prove assignment-end regression tests fail behavioral mutants, then pass restored."""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
TERMINAL = "cmd/divybot/native_run_terminal.go"
RECONCILE = "cmd/divybot/native_run_reconcile.go"
CLI = "cmd/divybot/native_run_reconcile_cli.go"
MUTATIONS = [
    ("new assignment pins older same-key history", "cmd/divybot/native_run_retention.go",
     'j != nil && j.DispatchKey == key { // guard:terminal-retention-tracked',
     'j != nil { // guard:terminal-retention-tracked',
     "^TestNativeTerminalRetentionBoundsOldestFactsAndBlocks$"),
    ("source-close failure keeps ended capacity", "cmd/divybot/remote_control_cleanup.go",
     'log.Printf("issue #%d: timeout source notification unconfirmed; releasing ended cleanup", j.Issue)',
     'log.Printf("fixture notification unavailable"); return // mutant couples cleanup release to source notification',
     "^TestNativeTerminalRemoteTimeoutReleasedAtStopBound$"),
    ("terminal fence skips timeout cleanup", "cmd/divybot/main.go",
     'ended := c.st.nativeEndedLocked(j)',
     'ended := c.st.nativeEndedLocked(j); fenced = fenced || ended',
     "^TestNativeTerminal(RemoteTimeout|OrdinaryTimeout)"),
    ("end no longer fences continuation", "cmd/divybot/main.go",
     'if ended { // guard:terminal-input-only',
     'if ended && false { // guard:terminal-input-only',
     "^TestNativeTerminalRetirementPrecedesUncertainCleanup$"),
    ("recorded end rejects stop", "cmd/divybot/action_delivery.go",
     '(req.Action != "stop" && c.st.nativeEndedLocked(j))',
     'c.st.nativeEndedLocked(j)',
     "^TestNativeTerminalStopAcceptsRecordedEnd$"),
    ("pending cleanup rejects stop", "cmd/divybot/action_delivery.go",
     'if j.RemoteCleanup != "" && (req.Action != "stop" || j.RemoteStopOperation != "") {',
     'if j.RemoteCleanup != "" {',
     "^TestNativeTerminalStopAcceptsRecordedEnd$"),
    ("retention disconnected from durable save", "cmd/divybot/main.go",
     'pruned := s.pruneNativeTerminalsLocked() // guard:terminal-retention-save',
     'var pruned *nativeTerminalPrune // mutant omits retention',
     "^TestNativeTerminalRetentionBoundsOldestFactsAndBlocks$"),
    ("newest facts pruned first", "cmd/divybot/native_run_retention.go",
     'return candidates[i].at.Before(candidates[j].at)',
     'return candidates[i].at.After(candidates[j].at)',
     "^TestNativeTerminalRetentionBoundsOldestFactsAndBlocks$"),
    ("tracked facts pruned to force the bound", "cmd/divybot/native_run_retention.go",
     'if j := s.Jobs[n]; j != nil && j.DispatchKey == key { // guard:terminal-retention-tracked',
     'if j := s.Jobs[n]; j != nil && j.DispatchKey == key && false { // guard:terminal-retention-tracked',
     "^TestNativeTerminalRetentionPinsTrackedJobsAboveLimit$"),
    ("synthetic launch block retained forever", "cmd/divybot/native_run_retention.go",
     'delete(s.LaunchBlocks, n)', '_ = n // mutant retains synthetic block',
     "^TestNativeTerminalRetentionBoundsOldestFactsAndBlocks$"),
    ("failed save loses pruned facts", "cmd/divybot/main.go",
     's.restoreNativeTerminalPruneLocked(pruned)', '_ = pruned // mutant skips rollback',
     "^TestNativeTerminalRetentionSaveFence$"),
    ("uncertain rename resurrects expired facts", "cmd/divybot/main.go",
     'if saveErr != nil && !(errors.As(saveErr, &saved) && saved.afterRename) {',
     'if saveErr != nil { _ = saved',
     "^TestNativeTerminalRetentionSaveFence$"),
    ("invalid retention configuration accepted", "cmd/divybot/main.go",
     'present && (string(value) == "null" || c.NativeTerminalLimit <= 0)',
     'present && false && (string(value) == "null" || c.NativeTerminalLimit <= 0)',
     "^TestNativeTerminalRetentionConfig$"),
    ("configured limit not applied to dispatcher", "cmd/divybot/main.go",
     'state.nativeTerminalLimit = cfg.nativeTerminalRetentionLimit()',
     'state.nativeTerminalLimit = defaultNativeTerminalLimit',
     "^TestNativeTerminalRetentionBoundsOldestFactsAndBlocks$"),
    ("ordinary stop loses terminal identity", "cmd/divybot/action_delivery.go",
     'if c.recordNativeEndLocked(j.Issue, j, "cancelled") != nil { // guard:ordinary-stop-terminal',
     'if false { // mutant omits terminal write',
     "^TestNativeTerminalOrdinaryCancellationPrecedesClose$"),
    ("legacy terminal pruned without retention", "cmd/divybot/completion.go",
     'if c.st.publishNativeTerminalLocked(*prior.NativeRun, "ended_unobserved") != nil { // guard:retain-legacy-terminal',
     'if false { // mutant omits legacy terminal retention',
     "^TestNativeTerminalLegacyFencePromotedBeforePruning$"),
    ("uncertain rename erases the first fact", TERMINAL,
     'if errors.As(err, &saved) && saved.afterRename {',
     'if errors.As(err, &saved) && saved.afterRename && false {',
     "^TestNativeTerminalUncertainDirectorySyncNeverRewritesFact$"),
    ("replay skips uncertain durability", TERMINAL,
     'return s.saveLocked() // retry any uncertain directory sync without rewriting the fact',
     'return nil // mutant skips durability',
     "^TestNativeTerminalUncertainDirectorySyncNeverRewritesFact$"),
    ("terminal read depends on pruned cleanup", "cmd/divybot/native_run_read.go",
     'if terminal != nil || terminalReason != "" {', 'if terminalReason != "" {',
     "^TestNativeTerminalImmutableAcrossCleanupAndRestart$"),
    ("first end rewritten", TERMINAL,
     'if prior, ok := s.NativeTerminals[record.DispatchKey]; ok {',
     'if prior, ok := s.NativeTerminals[record.DispatchKey]; ok && prior.Reason == "never" {',
     "^TestNativeTerminalImmutableAcrossCleanupAndRestart$"),
    ("failed durability certifies terminal", TERMINAL,
     'if err := s.saveLocked(); err != nil {', 'if err := s.saveLocked(); err != nil && false {',
     "^TestNativeTerminalCommitFailureRetainsUnknown$"),
    ("idle alone closes assignment", RECONCILE,
     'return !e.ReportAt.IsZero() && !e.ClosedAt.IsZero() && !e.ReportAt.After(time.Now()) && !e.ClosedAt.After(time.Now()) && e.Idle',
     'return e.Idle',
     "^TestNativeTerminalReconciliationRequiresProofAndFencesRaces$"),
    ("proof crosses changed work", RECONCILE,
     '(current != nil && nativeEndRevision(*current, c.st.Jobs[n]) != expected)',
     '(current != nil && nativeEndRevision(*current, c.st.Jobs[n]) != expected && false)',
     "^TestNativeTerminalReconciliationRequiresProofAndFencesRaces$"),
    ("apply is the default", CLI,
     'fs.Bool("apply", false,', 'fs.Bool("apply", true,',
     "^TestNativeTerminalCLIDefaultIsDryAndApplyUsesDispatcher$"),
    ("reconciliation monopolizes action pass", "cmd/divybot/action_delivery.go",
     'if reconciled {', 'if reconciled && false {',
     "^TestNativeTerminalCLIDefaultIsDryAndApplyUsesDispatcher$"),
    ("retirement waits for cleanup", "cmd/divybot/main.go",
     'err := c.recordNativeEndLocked(n, j, reason)',
     'var err error; _ = reason',
     "^TestNativeTerminalRetirementPrecedesUncertainCleanup$"),
    ("merge erases completion opportunity", "cmd/divybot/main.go",
     'end := c.reconcileNativeEnd(ctx, nativeRunQuery{strconv.Itoa(n), j.Label}, "", true)',
     'end := nativeEndResult{}',
     "^TestNativeTerminalMergedPRIsNotErasedBeforeFinalization$"),
    ("vendor turn promoted to assignment end", "cmd/divybot/completion.go",
     'if err == nil && complete && j.NativeGoal != nil && goalAllowed {',
     'if err == nil && complete {',
     "^TestNativeTerminalTurnCompletionAloneNeverEndsAssignment$"),
    ("historical final-report PR evidence lost", RECONCILE,
     'links := terminalPRLink.FindAllStringSubmatch(reportBody, 17)',
     'links := terminalPRLink.FindAllStringSubmatch(reportBody, 0)',
     "^TestNativeTerminalHistoricalReportLinksAreBoundedAndScoped$"),
    ("screen miss replaces native absence", "cmd/divybot/native_run_abandon.go",
     'if !h.forJob(&copy).remoteSeatAbsent(check, &copy) {',
     'if !h.forJob(&copy).remoteSeatAbsent(check, &copy) && false {',
     "^TestNativeTerminalAbandonmentRequiresNativeAbsence$"),
    ("last native read contradiction ignored", RECONCILE,
     'if err := p.reconcileNativeLifecycle(); err != nil {',
     'if err := p.reconcileNativeLifecycle(); err != nil && false {',
     "^TestNativeTerminalReconciliationRejectsNewerNativeWork$"),
    ("newer work accepted under old report", RECONCILE,
     '!time.Unix(*t.CompletedAt, 0).After(cutoff)',
     '(!time.Unix(*t.CompletedAt, 0).After(cutoff) || true)',
     "^TestNativeTerminalReconciliationRejectsNewerNativeWork$"),
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
    elif p.returncode != 1 or not failures or "panic:" in p.stdout:
        print(p.stdout, file=sys.stderr)
        raise RuntimeError("mutant survived or failed before assertions")


def main():
    # Never mutate the checkout or a source tree another gate might be reading.
    with tempfile.TemporaryDirectory(prefix="terminal-") as directory:
        root = Path(directory) / "source"
        shutil.copytree(ROOT, root, ignore=shutil.ignore_patterns(".git", ".llm"))
        gate(root, "green control", "^TestNativeTerminal", True)
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
