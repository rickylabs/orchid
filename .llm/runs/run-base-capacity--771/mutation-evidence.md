# Compiled mutation evidence

PASS: all 27 final-source mutants compiled and failed behavioral assertions; all 27 restored controls executed tests and passed. The baseline control also passed. Command: `python3 scripts/check-run-recovery-mutations.py` in an isolated `golang:bookworm` source copy with warm module/build caches.

The guards cover default/explicit checkout bases, stale cached refs, start-SHA persistence and immutability, exact native capacity classification, pauses, backoff and retry limit, durable pre-write reservation, private binding and goal rechecks, status-only mutation, native accounting, retained running capacity, conflicting supervision, timer ownership, canonical transport capability, bounded native-start wait, observation debounce and persistence-failure wake pacing.

Compilation errors and empty test runs are inconclusive, never successful mutation kills. The runner checks unique mutation anchors and restores source bytes even after failure. New CI wiring runs this same command.

The two regressions also fail on the original production baseline without any production fix; see red-green.md. Raw logs and the baseline source archive remain private. Final gate source equivalence covers all 216 Go files plus go.mod and this mutation runner.
