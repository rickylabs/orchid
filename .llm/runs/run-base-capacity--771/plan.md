# Run base and capacity plan

Plan & Design — READY FOR REVIEW. Repair launch checkout ownership and daemon-owned Codex capacity recovery.

1. Prove the stale-pin launch defect with a real local Git remote and existing branch; prove native capacity stalls have no recovery before code changes.
2. Centralize checkout preparation in run_checkout.go: use freshly fetched remote HEAD unless the brief explicitly names base; validate references; reset only the dedicated run branch; verify its commit and durably record StartSHA before creating a seat. Routing/profile configuration is admission metadata, never an implicit checkout override.
3. Centralize recovery in goal_recovery.go: read an owned, matching goal plus the latest failed turn's structured capacity signal; retain active run accounting through bounded exponential backoff; durably reserve each attempt before a status-only native resume, verify its notification/readback. Preserve pauses, blockers without capacity evidence, usage/budget limits, ownership and completion fences. Exhaustion is visible and never triggers a fresh launch.
4. Add boundary, restart, accounting, guard and protocol tests. Run regression tests red then green and compile-and-assert mutation checks.
5. Run go vet, build and full go test ./... in isolated golang:bookworm with warm module cache. Run the requested independent read-only blocker review; fix blockers once; reconcile, publish evidence and mark READY.

Decisions: operator authorizes implementation choices and exact review route. No unanswered owner fork. Native protocol sources and repository paths are captured in research.md. The native API lacks conditional goal updates; recheck immediately before status-only resume and document that residual external-write race.

Risks: stale remote HEAD aliases (fetch remote HEAD directly); branch reuse (force the dedicated run branch from fetched commit); uncertain native effects (durable attempt reservation); false stall classification (typed native error only); capacity churn (bounded backoff); privacy (public diff and commit scan).

Dependency order: independent plan evaluation -> red controls -> fixes -> green/mutations -> full gates -> implementation review -> READY.

Plan amendment after review:

- Retire admission metadata from the checkout base path: rename targetBase to targetAdmissionRevision, receipt BaseRevision to AdmissionRevision and its cached field accordingly. readTargetHead remains a bounded admission/worklog read, never a checkout authority. Owner grant TargetRevision remains a revision of the authorization evidence, retained across grant-install retries; its comments/docs will explicitly distinguish it from the launch checkout. The owner-authorized default-head policy in this issue takes precedence over the former implicit checkout pin. Explicit base comes only from the bound brief. A new retry operation fetches again; restoration of an existing run never prepares its checkout again. Each launch writes BaseRef and verified StartSHA before spawning.
- Capacity recovery owns idle/blocked continuation input and status while pending or exhausted. Completion returns a distinct recovering result; ordinary blocked transitions and stranded pokes defer. Existing operator timeout and explicit stop still win. Missing native identity never authorizes a write.
- Native sources and exact protocol evidence are recorded in research.md before implementation. Status-only resume uses automatic origin when supported; the observed protocol permits active status. The capacity allowlist initially covers serverOverloaded, the typed native variant behind the reported message.
