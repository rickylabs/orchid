# Codex checkout trust — round 3 execution validation

All final execution gates PASS on functional head `9bdeeea71246a1cde98fb2c81d5b0a2cea73b245`. Coordinator review remains pending; the generator claims no independent implementation PASS. [Machine-readable execution evidence](round3-gate-summary.json) records explicit package verdicts, exit codes, guard counts and source restoration.

| Gate | Result |
| --- | --- |
| Final focused normal and race tests, local Go 1.25 | PASS |
| Official golang:bookworm vet and build | PASS |
| Bookworm full normal suite | PASS; explicit package verdict, no failed tests |
| Bookworm full race suite | PASS; explicit package verdict, no race reports |
| Checkout-trust compiled mutation wrapper | Green control, 33/33 compiled guard removals fail behavioral assertions; isolated sources restored |
| Source-trigger compiled mutation wrapper | 50/50 compiled guard removals fail behavioral assertions; sources restored; zero image exit |
| Additional unchanged endpoint ownership controls | PASS, including 17/17 compiled endpoint guard mutations; completed before the final ordering guard |
| Formatting, diff and added-line privacy checks | PASS |

The bookworm image uses an unprivileged runner, a seeded module cache and Deno. Go has no external module dependencies here. Ordinary tests, then race tests, then trust mutations run sequentially. The source wrapper runs separately in an owned test container. Both final gate containers exit zero. No remote CI was polled.

Coverage includes raw fleet folders excluded by issue/identity/role matching, global retention across aliases and unconfigured job hosts, adopted Workdir persistence, first-snapshot and post-adoption ordering, retry-action failure before adoption, down/non-Codex host skips, retained/observed Codex jobs with launches disabled, pacing of successful and failed reads, and immediate failure/release recovery. Existing checkout validation, ownership boundaries, cleanup compatibility and native write guarantees remain covered.

An earlier superseded race run failed in the unchanged GoalConfirmationOnRealCaller/dir-sync registration fixture while optional mutation builds competed. The requested fault never fired. Resource pressure was suspected, not established as a baseline defect. The final run passes that fixture in both normal and race modes without changing its timeout or production behavior.

Live API/TUI proof in live-check.md belongs to the preceding repair. This round changes recovery scheduling/retention and performs no live trust mutation. No service restart, merge or deployment occurred. The tracking issue remains open.
