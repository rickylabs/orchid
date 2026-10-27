# Coordinator review repair

The independent Opus 5.5 CHANGES review of de9b9f9 has been addressed on the same PR branch. All final execution gates pass; no independent PASS is claimed for the repaired head.

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

The extra receipt-number check was discarded because source-comment dispatches use the comment ID as their internal job key (`source_binding.go:1233`, `main.go:3448` checkoutDirectory call). The source issue number remains a separate identity. The prepared path is validated without conflating these identifiers.

## Round 3 — coordinator re-review of 1bbc5bb

The two supplied blockers are addressed; coordinator review of the resulting head remains pending. No external review was attempted this round, as explicitly instructed.

- **Live job trust:** main.go:2958 captures raw Herdr working folders before issue/identity/role filtering; main.go:2966 carries cwd into agentRef and main.go:2282 records it in adopted Job.Workdir. main.go:2772 invokes ordinary recovery after observation and adoption. The canonical daemon fixture asserts the job exists with its real cwd when the first config read occurs. Adoption persistence and later observation omission have separate regressions.
- **Sweep cost:** codex_checkout_trust.go:200 skips down hosts; :203 checks enabled/eligible Codex launches or recorded/observed Codex jobs; :206 paces ordinary attempts by fleetRecoveryEvery, including failed reads. Failure hooks request immediate recovery; a pre-observation request remains pending. All work stays in the serialized dispatcher loop, without another timer or app read.
- **Global retention:** codex_checkout_trust.go:178 builds one path set from raw fleet folders and every retained Job, including a job whose host is no longer configured. The canonical fixture serves two host aliases from one daemon and checks both retain the same paths.
- **Evidence correction:** the prior round's worklog now consistently reports 16 final mutations. Round 3 expands that gate to 32. Its first run found a masked released-job assertion; a separate orphan-recovery test now proves that guard independently of direct bounded-release revocation.

Protocol choice and write scope are unchanged: exact-checkout grant and null removal remain native API operations in the single trust module. Per-thread trust remains for thread creation. Existing live API/TUI proof is historical; this scheduling/retention repair introduces no new live configuration mutation.
