# Round 2 findings

Both coordinator findings are reproduced at the supplied PR head; the repair keeps release in the native recovery owner and leaves PR retirement separate.

- Prior supervision returned true solely because state was exhausted, before any native goal read: cmd/divybot/goal_recovery.go at prior head 94d0809, lines 333-336. That value suppresses PR relays/re-pokes and blocked alerts in cmd/divybot/main.go:3826 and :3935.
- Completion's native evidence path follows completionPRPending, so an open run PR prevents that path from observing any resume: cmd/divybot/completion.go:345-348. The PR-open regression uses actual supervise -> retireCompleted -> superviseActive and observes a resumed PR review relay without retiring the worker.
- Automatic unproven exhaustion may already have active native status: cmd/divybot/goal_recovery.go at prior head 94d0809, lines 181-186 and :298-300. Plan review identified that this needs native status/UpdatedAt history; unchanged automatic exhaustion cannot act as a manual resume.
- The prior stale-checkout test used renamed admission fields and newly introduced checkout helpers: cmd/divybot/run_recovery_regression_test.go at prior head 94d0809, lines 57-60 and :77-99. Its replacement uses real matrix admission to populate compatible metadata, and reaches its stale-base assertion unmodified on original production 22f14e0.
- The only caller of admissionRevision was a test (cmd/divybot/cockpit_targets_test.go:283); the accessor is removed and the fixture reads its known nonnil receipt metadata directly.

Assignment source: https://github.com/rickylabs/harness/issues/771; existing PR: https://github.com/rickylabs/orchid/pull/130. Both retrieved during this run. No external vendor claims are introduced.
