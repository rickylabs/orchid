# Codex checkout trust — worklog

## Design checkpoint

1. Public surface: managed remote Codex starts ready in its prepared checkout, and cleanup removes its trust entry.
2. Vocabulary: checkout trust is one daemon user-config project entry; thread trust is the existing session override. They have different lifetimes.
3. Ports: canonical Codex RPC, Herdr native startup, existing remote shell checkout deletion.
4. Constants: one config write method and trust level, private typed write parameters.
5. Commit slices: plan plus one implementation slice for the trust helper, RPC capability, preparation, teardown, remote args and regression tests. Gate: gofmt, go vet, Go suite in the requested image, live trusted/untrusted native readiness comparison, independent GLM review. Run artifacts accompany each commit.
6. Deferred: deployment and merge belong to the coordinator; no app read triggers work; no screen automation.
7. Contributor path: `cmd/divybot/codex_checkout_trust.go` owns grant and removal.

Discovery complete. No existing PR covers daemon configuration trust; previous startup-trust PR used the process-only CLI override. Consulted prior Orchid environment memory. Explicit operator evaluation route takes precedence over the current feature matrix defaults.

Implementation slice: native grant/removal helper and narrow RPC capability added; preparation writes once after exact thread readback, remote argv drops process trust, and recorded-checkout cleanup revokes before deletion. Focused tests passed, including canonical framing. Broader gates, live proof and independent review are pending; this commit is implementation progress, not certification.

Broader local suite found one stale client-selection fixture: preparation correctly refused its empty config-write response. Fixture now acknowledges native writes; its targeted regression passes. Startup rollback is installed before preparation so even an ambiguous grant failure triggers a bounded removal attempt. Live proof requires a real Git checkout with a project layer; an empty folder was an ineffective negative control.

Live native control passed with production remote arguments: disabled project layer and no resume without trust, enabled project layer and ready/proven resume after API write, disabled/no resume after API removal. Proof-owned resources were cleaned up, including the orphaned thread from an early failed probe. GLM review was attempted read-only with stdin closed, but its provider credential expired before review; independent evaluation is unproven and explicitly disclosed. Image gates continue with Go, Deno, a warm module cache and an unprivileged test user.

2026-10-27 final gates: local Go 1.25 vet and full tests PASS; prepared golang:bookworm vet, build, full tests and full race tests PASS with explicit package verdicts and zero container exit. Root-only endpoint subset PASS. Formatting, diff checks and the controlled privacy scan PASS. Reconciled against freshly fetched main at the unchanged baseline. Final evidence commit precedes marking PR 128 ready for external review; GLM evaluation remains unproven due to the credential refusal. No merge or deployment.

Coordinator review repair checkpoint: public surface remains unattended exact-checkout startup and cleanup. Orphan trust is now a first-class reconciliation concern; the ports remain the canonical Codex API and existing State jobs. Constants name config/read and the typed revoke refusal. One repair slice adds reconciliation, symmetric validation and teardown compatibility, then runs focused tests, guard mutations and the full image gates. Deferred: no deployment, merge, service restart or app-triggered cleanup. Contributor path remains codex_checkout_trust.go.

Repair implementation: startup and pre-admission tick reconciliation reads native config once per configured host and removes only orphan trusted direct issue checkouts under the persistent/legacy roots. Retained jobs and foreign/untrusted entries are protected; bounded release attempts immediate removal, and failed removals remain available for retry. Invalid checkout cleanup skips; ordinary rm errors no longer block teardown. Grants and revokes share canonical positive issue validation. Focused tests and their race run pass, and all 16 isolated compiled guard mutations go assertion-red with a green control. The production reconciler also passed a live owned-root check: remove an unsaved grant, preserve a tracked checkout, then remove its entry after release. Full repaired-head gates are running.

The final repair preserves comment dispatch identity: a `/swarm` comment's checkout job key differs from its source issue number. The extra receipt-number restriction and its mutation were removed; a real native client-selection fixture now covers that distinction. All requested lifecycle guards remain in the final 16-mutation gate. The TUI red/green/removal proof passed again using a canonical issue checkout. Normal final-head and race image gates continue.

Final repair gates PASS at functional head 34c65aa: local Go 1.25 vet/full suite; bookworm vet/build/full normal/full race suites with explicit passing package verdicts and zero container exit; 16/16 compiled assertion-red guard mutations with a green control; isolated root endpoint subset; live production recovery; and repeated canonical-checkout TUI red/green/removal proof. Native readback confirms no proof trust entries remain. The final evidence commit changes only artifacts, then the PR body and coordinator-requested READY follow-up will be updated with its head. No merge or deployment.

Round 3 design checkpoint:

1. Public surface: active fleet checkouts keep daemon trust through adoption; recovery performs bounded background work.
2. Vocabulary: raw fleet working folders protect all reported occupants, even those excluded from issue adoption. A pending recovery is requested by a failed launch/revoke or bounded release.
3. Ports: existing Herdr fleet snapshot, State jobs, canonical daemon config API; no new transport or timer.
4. Constants: reuse fleetRecoveryEvery for the ordinary recovery interval.
5. One repair slice: update lifecycle/recovery and adoption, behavioral tests and compiled mutations; prove focused guards, then bookworm vet/build/full normal/race tests. All listed files are in supervisor.md.
6. Deferred: external review is coordinator-owned, and live trust/TUI proofs from the prior round need no repeat because this repair changes scheduling/retention only. No merge or deploy.
7. Contributor path: cmd/divybot/codex_checkout_trust.go owns trust and recovery.

Sources: main.go:2278 drops cwd on adoption; main.go:2307 and main.go:2550 recover before observation; main.go:2939 filters issue observations; codex_checkout_trust.go:156 uses per-host retention and RemoteControl pointer eligibility. Read the prior project memory before implementation. No unresolved owner forks: the assignment explicitly approves the scope and review handoff.

Round 3 implementation: raw fleet cwd protection is collected before issue/identity filtering; agentRef carries cwd and adoption persists Workdir. Recovery uses one retained-path set across all hosts, waits for observation and adoption, and paces attempts per host with fleetRecoveryEvery. Failed launches/revokes and bounded releases request an immediate serialized pass. Down and non-Codex hosts are skipped, while recorded/observed Codex jobs remain eligible with launches disabled. Focused behavioral tests PASS. The expanded compiled mutation gate and full bookworm execution gates are running. No external certification is claimed.

Reconcile: fetched upstream main and the existing PR branch; main remains 3f82fcd and the PR baseline remains 1bbc5bb. No concurrent branch changes or conflicts.

The compiled guard run caught a coverage gap in the repair tests: direct bounded-release revocation masked a missing released-job guard in reconciliation. Added an independent released-job orphan test; the production implementation is unchanged. The first expanded mutation set contains 32 isolated guard mutations, including sweep ordering, raw cwd capture/propagation, global retention, host eligibility, interval pacing and all three failure triggers. Final gates are still running.

Final ordering correction: a retry action can observe the fleet before the main tick adopts live jobs. A failure request therefore remains pending until the first scheduled post-adoption recovery has attempted a host; subsequent failures still recover immediately. Added an independent regression and a 33rd compiled guard mutation. Also synchronized fixture control writes and removed an unused failure knob. Focused tests pass; full final-image gates are rerunning because this changes production ordering.

Gate correction: the superseded implementation's full normal image suite passed, but its race suite failed in the unchanged GoalConfirmationOnRealCaller/dir-sync registration fixture while multiple optional mutation builders competed. The tested fault had not fired. Resource pressure is suspected, not established as a baseline defect. Retired only the verified owned superseded test container; the final code's full normal/race/mutation gates run without those competing jobs. Root endpoint checks and 17 endpoint guard mutations completed successfully; incomplete optional source-trigger mutation runs are not a PASS.

Final round 3 execution gates PASS on functional head 9bdeeea71246a1cde98fb2c81d5b0a2cea73b245: local focused normal/race; bookworm vet/build/full normal/full race with two explicit passing package verdicts and zero image exit; green trust control plus 33/33 compiled assertion-red guards; 50/50 source-trigger guards in a separate sequential image run; additional unchanged endpoint controls and 17 endpoint guard mutations. Both mutation wrappers restore source bytes. The earlier registration fixture passes in both final full suites, with no timeout or production workaround. Complete final results supersede the earlier partial/contended runs. Sanitized execution evidence is committed in round3-gate-summary.json. The final commit changes docs/evidence only; coordinator review remains pending, without merge or deployment.

Final reconcile: freshly fetched origin/main remains 3f82fcd612fc20ea5513747ba7bf0ab063eb5787; local and remote repair branch both point to tested functional head 9bdeeea71246a1cde98fb2c81d5b0a2cea73b245 before the evidence commit. No merge/rebase or code adjustment is needed. The final evidence commit contains run artifacts and a README clarification only.

Round 4 design checkpoint (Plan & Design — READY FOR REVIEW):

1. Public surface: adopted jobs may record only their matching direct managed checkout as Workdir; arbitrary observed fleet folders retain trust without acquiring deletion authority.
2. Vocabulary: observed cwd is fleet evidence, while recorded Workdir is checkout cleanup authority.
3. Ports: existing agentRef observation, host checkoutTrustRoots, State jobs and checkout cleanup; no new interfaces.
4. Constants: reuse validTrustCheckout and existing root defaults; match the issue suffix to the internal job key.
5. One repair slice: constrain main.go adoption, add real-filesystem regression controls in checkout_storage_test.go, extend the existing compiled mutation wrapper, and update the contributor explanation. Gate: focused controls, guard-removal reds, bookworm vet/build/full normal/full race and configured mutation wrappers.
6. Deferred: coordinator evaluation, merge and deployment; no external review or live trust manipulation this round. The supplied coordinator review is the design checkpoint and the owner explicitly approves this exact correction.
7. Contributor path: cmd/divybot/codex_checkout_trust.go defines canonical checkout and root ownership; adoption in main.go consumes those constraints.

Discovery: at baseline main.go:2282 unconditionally assigns ref.Cwd; checkout_storage.go:54 chooses it and checkout_storage.go:82 deletes it. validJobCheckout validates the basename without root ownership. checkoutTrustRoots in codex_checkout_trust.go:92 defines the approved direct-parent set. Prior run memory consulted before implementation. No new owner forks.

Round 4 implementation: adoption validates the observed cwd and its job key before checking the host's exact trusted roots; only a direct managed checkout becomes Job.Workdir. The existing raw-fleet retained set and cleanup fallback are unchanged. New real-filesystem adoption/state-reload/teardown tests cover both approved roots and rejected foreign, nested, mismatched, unclean and invalid observations. The compiled wrapper adds three guard mutations and requires both requested boundary cases to fail on direct-parent guard removal. Initial focused adoption tests pass; official bookworm broad gates are running. This commit records implementation progress, not independent certification.
