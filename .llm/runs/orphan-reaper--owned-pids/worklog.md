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
