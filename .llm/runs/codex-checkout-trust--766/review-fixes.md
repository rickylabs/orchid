# Coordinator review repair

The independent Opus 5.5 CHANGES review of de9b9f9 has been addressed on the same PR branch. Execution gates for the final repair are still running; no independent PASS is claimed for the repaired head.

| Review blocker | Repair | Regression evidence |
| --- | --- | --- |
| Cleanup failures affect every agent | Invalid checkout paths skip again; ordinary rm errors are logged and remain nonfatal; only the dedicated Codex revoke refusal blocks confirmation | Production teardown tests for Claude, AGY, OpenCode and plain Codex; invalid-path and fatal-filter mutations |
| Bounded stop release retains trust | Attempt exact checkout revoke outside the State lock before returning released; reconciliation also treats bounded jobs as released | Bounded-release native fixture and removal mutation |
| Failed launch rollback is never retried | Read native config on the next existing tick and remove any orphan trusted managed checkout; an unconfirmed removal remains visible for later retry | Failed-removal retry, live native recovery and tick-wiring mutation |
| Crash after grant but before Job save | Reconcile native config during startup, independently of any in-memory grant registry | Startup recovery fixture reloads saved jobs, preserves the retained checkout and removes orphan entries; startup and unsaved-job mutations |
| Grant/revoke validation differs | Typed native writes and preparation both use validJobCheckout for a canonical positive checkout key | Direct payload, preparation and positive-job-key mutations; comment-job fixture uses a key different from its source issue |

The sweep is bounded to direct issue checkouts under the configured persistent and compatibility roots. It preserves retained jobs per host, other roots, nested paths and explicit untrusted entries. Disabling Codex launches does not strand old grants. Missing State or an unconfirmed native config read licenses no mutation. No new timer or application-triggered work is added.

The final mutation gate contains 16 compiled guard removals, each required to cause a behavioral assertion failure, preceded by a green control. It runs on an isolated source copy and is part of CI. Functional coverage includes the startup/tick wiring and the production teardown caller, rather than only testing parameter rendering.

A live owned-root check using the production Go helper removed an unsaved grant, preserved a retained job's grant, then removed it after release. The TUI check was repeated with a valid issue checkout: no trust timed out with no native resume; API trust became ready and resumed; removal made startup fail again. No screen was read or answered, and only proof-owned resources were cleaned up.

The extra receipt-number check was discarded because source-comment dispatches use the comment ID as their internal job key (`source_binding.go:1233`, `main.go` checkoutDirectory call). The source issue number remains a separate identity. The prepared path is validated without conflating these identifiers.
