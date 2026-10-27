# Starvation regression and mutation evidence

The coordinator hypothesis is confirmed by an isolated Linux child-subreaper test, with no production changes before the first run.

Baseline command in golang:bookworm: `go test ./cmd/divybot -run ^TestReaperCollectsOrphanWhileOwnedChildLives$ -count=1 -v` — exit 1.

    reaper_linux_test.go:52: orphan remained waitable while an owned child was live
    reaper_linux_test.go:54: isolated owned-child starvation regression failed

After replacing the lifetime-wide hold with owned PID registration, `go test ./cmd/divybot -run ^TestReaper -count=1 -v` — exit 0. This includes the previously failing regression and the existing kernel adoption/live-child/owned-zombie assertions.

`python3 scripts/check-reaper-mutations.py` — exit 0, in an isolated source copy using the same warm module/build cache. All 16 mutants compiled and failed assertions; all 16 restored controls passed. Mutation names and output are in mutation-evidence.txt.

The required Wait mutation replaces the actual `err := c.Cmd.Wait()` with a declaration that performs no wait. It fails at behavioral assertions that the process was waited and its exit status preserved, followed by a restored green control. This is a runtime assertion failure, not a compile failure.

One initial run was inconclusive because the removed-start-fence mutant exposed a double receive in the test after it detected an early start. The test was corrected to retain the received result. The complete restarted suite then passed all 16 mutation/restoration pairs.
