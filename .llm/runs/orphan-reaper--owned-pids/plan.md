# Orphan reaper plan

Prove starvation, then replace the lifetime-wide global gate with PID ownership and an atomic start/registration fence.

1. Add an isolated Linux subreaper regression: keep a directly owned child live, adopt and exit another child, and require the orphan to be collected before the owned child ends. Run unchanged production first; expect a failing assertion.
2. Centralize command lifecycle in one owned-command helper: Start registers atomically against sweeps; Wait releases only after os/exec has waited, including copy goroutines. Synchronous wrappers call Start/Wait; streaming users finish their pipe readers before Wait.
3. Enumerate direct child candidates on Linux and wait by positive PID only, excluding tracked owned PIDs under the same mutex. SIGCHLD and the backstop remain daemon-owned. Non-Linux builds keep an inactive collector.
4. Migrate every production exec site; protect ownership, startup failure, cancellation, exit status, output draining, and concurrent starts in tests. Replace obsolete global-gate mutations with ownership/lifecycle mutations, including removing Wait.
5. Run go vet, go build, go test ./..., go test -race ./... in the warm golang:bookworm container. Run required read-only Codex blocker review, fix blockers once, then mark the PR ready.

Decisions: operator explicitly requests a positive-PID ownership design if starvation holds and a particular same-vendor separate review. No owner fork remains.

Risks: PID reuse/start races (fence starts and candidate validation); inherited output pipes (preserve os/exec copying and caller pipe sequencing); accidental live tool execution (isolated container plus repository test shims); noisy public evidence (fixed assertions and synthetic examples).

Dependency order: red regression -> lifecycle/collector -> green and mutation -> independent review -> READY.
