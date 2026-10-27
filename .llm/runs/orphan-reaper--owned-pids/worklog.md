# Orphan reaper worklog

Design checkpoint before implementation:

1. Public surface: divybot continues dispatching while collecting orphan zombies even with long-lived owned subprocesses; no new app read behavior.
2. Vocabulary: owned PID belongs to os/exec until Wait finishes; orphan candidate is a direct child absent from the owned registry.
3. Ports: os/exec command lifecycle, Linux proc child enumeration, positive-PID Wait4(WNOHANG), existing daemon signal/ticker loop.
4. Constants: keep the existing reaper sweep; name proc child-enumeration paths/fixture exit statuses where they clarify contracts.
5. Commit slices: regression plus plan (kernel failure proves the bug); helper, migrations and selective collector (focused tests); gate/mutation/review evidence (full gates and external review). All commits update this run's durable context.
6. Deferred: deployment and live zombie counts belong to the coordinator; do not broaden into a dispatcher redesign.
7. Contributor path: cmd/divybot/reaper.go will document lifecycle and ownership rules.

Discovery: PR 1 uses wait4(-1) behind a lifetime-wide count. runHerdrForward holds through Wait (herdr_events.go:207-215); childGate.reapWith skips all collection when count != 0 (reaper.go:55-61). Current production has twelve command constructors across eight callers, all with deferred holds and existing waits. No StdoutPipe caller launches an asynchronous reader. Go documentation retrieved: https://pkg.go.dev/os/exec and https://man7.org/linux/man-pages/man2/waitpid.2.html .

Baseline regression: `go test ./cmd/divybot -run ^TestReaperCollectsOrphanWhileOwnedChildLives$ -count=1 -v` in golang:bookworm exited 1 at a real assertion: orphan remained waitable while an owned child was live. No production code changed before this measurement.

Implementation: all twelve production constructors now use one owned-command helper. The registry fences Start/registration and direct-child enumeration/reaping; Wait retains ownership through os/exec completion. Linux collects only positive unowned direct PIDs. Focused `go test ./cmd/divybot -run ^TestReaper -count=1 -v` passed, including the previously red starvation regression, preservation of owned zombie status, live-orphan safety, startup failure, cancellation, output copying, pipe ordering and concurrent starts/sweeps. Full gates and isolated compiled mutations are running with shared warm caches.

Separate read-only implementation review: PASS, no blockers. Exact operator-requested model/effort, sandbox read-only, stdin closed; complete 59 KB diff/current-source prompt supplied, tools forbidden. The reviewer checked the five golden-rule priorities.

Mutation harness correction: the removed-start-fence mutant exposed a double receive in the test itself after detecting an early start. Retain the already-received result so the mutant fails a bounded assertion rather than hanging. The unchanged production fix remains green; the full mutation suite was restarted on its isolated source tree.

Compiled mutation suite completed: exit 0, 16/16 assertion failures and 16/16 restored passing controls. This includes removal of the actual Cmd.Wait, registration/release/exclusion, start fence, positive-PID/nonblocking waits, PPID filtering, no-signalling guard, signal/backstop/stop wiring, main startup and helper bypass.

Final gates: vet/build exit 0; full test exit 0 (2,976 test/subtest passes, zero failures, six skips; 470.931s); full race exit 0 (same passes/skips, zero failures; 490.939s). All 210 Go sources matched the gate tree. Current main is unchanged. The exact owned disposable container was removed. Privacy and whitespace scans pass. No architectural debt accepted. Delivery proceeds to READY and canonical report handoff; merge/deployment remain coordinator actions.
