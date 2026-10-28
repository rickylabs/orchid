# Run worklog

Design checkpoint:

1. Public surface: new runs use fresh default-branch commits or explicit base; private run records expose the verified start commit; native capacity stalls recover without an app read and remain running during retries.
2. Vocabulary: admission revision is routing authorization metadata; start SHA is the actual fetched checkout commit; recovery is a bounded same-goal native status transition.
3. Ports: existing Host remote execution, private receipt/state storage, Codex goal RPC and daemon supervision; no new service.
4. Constants: named retry count, initial delay and cap; fixed closed diagnostic codes; validated Git references.
5. Commit slices: regression and first checkout fix; native recovery and accounting; full evidence and review repairs. Gates: focused Git/native regressions, compiled mutations, image vet/build/full test.
6. Deferred scope: deployment, UI workflows, provider/model fallback, arbitrary native failures, manual pauses and exhausted budgets.
7. Contributor path: docs/native-lifecycle.md links the checkout and recovery owners and their contracts.

2026-10-28: Read assignment, charter, doctrine and existing memory. No duplicate PR found. Fresh Orchid baseline checked out. Operator approval covers all implementation decisions; public repository privacy is mandatory.

2026-10-28: Plan review round 1 FAIL_FIX; all three blockers addressed and same-session round 2 PASS. Both new regressions failed assertions on baseline production. Checkout owner now fetches remote HEAD independently of authorization pins, verifies the commit, and writes StartSHA/BaseRef before registration. Focused checkout gate passed; capacity regression is reserved for the next slice.

2026-10-28: Draft Orchid PR 130 opened after the checkout slice. Native recovery now recognizes only the typed capacity failure, persists five lifetime attempts and exponential backoff, and owns blocked/idle continuation until native progress. Managed admission remains occupied. Focused controls cover restart, exact status-only writes, missing acknowledgements, pause/binding changes after the checkpoint, native accounting, timer scheduling and conflicting supervision. Both regressions were re-run on an isolated original baseline and failed their intended assertions; both pass with the fixes.

2026-10-28: Broad gate exposed synthetic launch fixtures that returned no commit SHA. Updated those fixtures to model verified checkout output and use non-null receipt metadata; actual Git tests continue exercising remote fetch/origin/branch behavior. Their focused integration gate passes. Added the compiled regression mutation gate to CI.

2026-10-28: Initial mutation verification passed 21 compiled mutants and 21 restored controls. Independent implementation review returned three concrete blockers; the single repair pass adds a bounded native-start wait, canonical-only resume authority and observation debounce. Focused repaired controls and the delivery-authority architecture inventory pass. Final image gates and an expanded 26-mutant run are executing on isolated copies.

2026-10-28: Retained independent evaluator verified the one repair pass and returned PASS. Its nonblocking persistence-failure note was addressed with a bounded overdue-wake floor and an injected-save-failure control. An unrelated 100-ms readiness fixture expired before its first read under concurrent gate load; 20 isolated repetitions pass on both candidate and original baseline. The final full suite will be rerun on the final source without competing mutation builds.

2026-10-28: Final 26-guard mutation run passed with 26 restored green controls. The final-source run adds the persistence-failure wake guard, with scoped mutation controls and a required nonempty test execution. Broader suite rerun remains pending until mutation compilation finishes.

2026-10-28: Serialized vet/build/full test PASS (3,020 test/subtest passes, six explicit skips). Race run in progress. Workflow reconciliation includes the inherited checkout-trust mutation scan; new recovery mutations use a separate CI job to avoid extending the existing serial gate.

2026-10-28: Final local gates PASS: vet/build, full ordinary and race suites (3,020 passes each, six explicit skips), 27 new plus 50 source-trigger, 17 endpoint and 36 checkout-trust compiled guard mutants. Independent implementation review PASS. Final source identity and privacy checks are recorded; final delivery stops at READY.

2026-10-28, review round 2: Resume baseline 94d0809 on existing PR 130. Full assignment, charter, doctrine and prior run memory read. Seven-item design checkpoint and exact mutation surface are recorded in round2-plan.md and round2-supervisor.md. The two coordinator findings are accepted; no owner fork remains. Independent plan checkpoint precedes product edits.

2026-10-28, round 2 implementation: Independent plan round 1 FAIL_FIX identified automatic-active exhaustion; amended native snapshot design and same-session round 2 PASS preceded product edits. Both manual-resume regression tests compiled and failed their intended assertions on 94d0809. The fixed focused suite has 64 test/subtest passes. The checkout regression now drives actual matrix admission, compiles unmodified against 22f14e0, and fails first at the stale-base assertion. Helper-only checkout controls moved into run_checkout_test.go; test-only admissionRevision removed. Expanded mutation controls and full image gates are in progress; independent implementation review uses the same reviewer session.

2026-10-28, round 2 review: Independent retained implementation reviewer accepted production behavior and found two ambiguous mutation anchors. The single repair narrows them to the observation/debounce branch; all 37 anchors are unique. Same reviewer recheck PASS. No production code changed after independent implementation review. Serialized image gate execution continues; its first attempt retains the pre-repair mutation-runner failure as evidence.

2026-10-28, round 2 gates: Final 37 compiled recovery mutants fail assertions, and all 37 restored controls execute tests and pass. Vet/build pass. Full ordinary suite passes with 3,039 test/subtest passes, zero failures and six explicit environment-gated skips. Separate root/operator kernel controls and all 17 compiled endpoint guard mutants pass. Full race and remaining configured scans continue on isolated final-source copies.

2026-10-28, round 2 implementation checkpoint: Full race suite also passes with 3,039 test/subtest passes, zero failures and the same six explicit skips. The independent-reviewed implementation is committed and pushed as a review checkpoint while the inherited source-trigger and checkout-trust scans finish. This checkpoint is not final gate sign-off; the PR remains draft until all configured scans and final evidence are complete.

2026-10-28, round 2 final sign-off: All local gates PASS on unchanged final Go source: vet/build/full ordinary/full race (3,039 passes each; six explicit skips), 37 recovery mutants plus 37 restored controls, 50 source-trigger, 17 endpoint and 36 checkout-trust mutants. Independent retained review PASS. Source identity, formatting and privacy verified. Owned gate container removed and absence confirmed. Final delivery continues through the existing PR and the excluded canonical-report handoff; no merge/deploy/restart.
