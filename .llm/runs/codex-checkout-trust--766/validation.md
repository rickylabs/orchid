# Codex checkout trust — round 4 execution validation

All requested and configured execution gates PASS on functional head `5381d68af04bf2000269f5fa2978e1547be84728`. The final evidence commit changes run artifacts only. Coordinator review is pending; these are execution results, not independent implementation certification. [Machine-readable results](round4-gate-summary.json).

| Gate | Result |
| --- | --- |
| Official golang:bookworm vet and build | PASS |
| Full normal suite | PASS; explicit package verdict, zero failed tests |
| Full race suite | PASS; explicit package verdict, zero failed tests/race reports |
| Adoption boundary cases | Eight pass in each full suite |
| Checkout-trust mutation wrapper | Green control and 36/36 compiled assertion-red mutations; isolated sources restored |
| Source-trigger mutation wrapper | 50/50 compiled assertion-red mutations; source bytes restored |
| Isolated two-UID endpoint race subset | PASS with required root/operator identities |
| Endpoint mutation wrapper | 17/17 compiled assertion-red mutations; source bytes restored |
| Both final owned gate containers | Zero exit; owned containers removed after completion |
| Formatting, diff and added-line privacy checks | PASS |

The parent-guard mutation independently fails both requested outside-root and nested-path cases. Separate mutations remove canonical-path validation and matching-job validation. Positive controls retain both supported roots. Further cases reject unclean paths, wrong issue keys, invalid roots and unknown hosts. The fixtures reload saved state, invoke production teardown with isolated fleet discovery, record the real rm target and verify only the root issue checkout disappears while unrelated directories survive (cmd/divybot/checkout_storage_test.go:56).

Execution uses unprivileged normal/race tests with a seeded module cache and Deno in the requested image. Mutation builders run sequentially after the full suites; the kernel endpoint subset runs separately in an owned disposable image. The initial endpoint-only container had a root-owned temporary directory that the dropped operator UID could not write. Its fixture setup failed before the protocol assertions. A fresh container with a shared sticky temporary directory runs the same unchanged checks successfully; that complete run supersedes the setup failure. No product or test source was changed for this correction. No remote CI was polled. No live trust mutation or external review was performed this round, as instructed.

---

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
