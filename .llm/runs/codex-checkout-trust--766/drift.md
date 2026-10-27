# Codex checkout trust — drift

The assignment checkout contains Harness rather than the instructed implementation repository. Created an isolated Orchid worktree at current upstream main and retained the assigned branch name. No Harness product files are changed.

The named `use harness` skill is not installed; the supplied leaf profile plus the Harness doctrine provide its workflow. Run artifacts are maintained directly. The assigned cross-family review command targets OpenCode 1.x syntax, but the installed client is 2.1.2; verify its supported invocation before review and preserve the exact model/variant.

Broader regression coverage required updating the native client-selection fixture in `cmd/divybot/codex_client_test.go` to acknowledge the newly required trust write. This expands only the declared test surface.

## Coordinator review revision

The coordinator supplied an independent Opus 5.5 CHANGES review of de9b9f9 and authorized one repair on the same branch. This replaces the unavailable GLM evaluation route for the current repair. Blocking findings: unrelated cleanup failures changed teardown semantics; orphan trust survives bounded release, failed rollback and grant-before-Job-save crashes; grant/revoke validation is asymmetric.

Approved plan expansion: add a native config read and a bounded reconciliation pass during startup and the existing serialized tick, removing only trusted direct issue checkouts under configured persistent/legacy roots when no retained job owns them. This is the coordinator's explicit exception to the original no-added-polling scope: use the existing lifecycle tick, no new timer or app read. Preserve ordinary cleanup's best-effort behavior and attempt revoke at bounded release. Expand tests to exercise lifecycle wiring and each guard with assertion-red mutations. Additional test surface: cmd/divybot/codex_checkout_trust_reconcile_test.go and the existing native-client fixtures.

The repair adds `.github/scripts/check-codex-checkout-trust-mutants.py` and its CI step so the requested assertion-red guard checks are repeatable. The script mutates an isolated source copy, preserving the working checkout. Existing fixture changes also include `launch_attempt_test.go` so its cancellation control uses a valid issue checkout.

The existing closed diagnostic-site inventory requires one distinct registered site per refusal. The temporary checkout refusal was added to `cmd/divybot/matrix_diagnostics.go`, then removed with the unnecessary receipt-number restriction described below; no refusal reason or app schema is widened. Existing owner-grant startup tests construct a coordinator without State, so reconciliation now fails closed without mutation when State is unavailable.

The added receipt-number check was removed before final validation: comment dispatches use the comment ID as their internal job key and checkout suffix, while the receipt's home names the source issue (`source_binding.go:1233`, `main.go:3451`). Grant and revoke share checkout validation without conflating these identities. A native client-selection regression now uses a comment job key different from source issue 7. The temporary diagnostic-site addition is consequently removed. The final mutation set contains 16 guard removals.

## Round 3 approved correction

The coordinator's re-review of 1bbc5bb found premature recovery before fleet adoption and unnecessary daemon reads on every tick. Replace those with raw fleet cwd retention, recorded adopted Workdir, one global retained-path set, a post-adoption first sweep, interval-bounded subsequent sweeps and failure-triggered recovery. Down and non-Codex hosts are skipped. No external review is requested this round because the review credential expired; coordinator review remains pending. The existing plan is preserved; these instructions authorize this repair and its regression/mutation gates.
