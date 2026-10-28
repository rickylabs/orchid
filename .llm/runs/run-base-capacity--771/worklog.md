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
