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

Repair implementation: startup and pre-admission tick reconciliation reads native config once per configured host and removes only orphan trusted direct issue checkouts under the persistent/legacy roots. Retained jobs and foreign/untrusted entries are protected; bounded release attempts immediate removal, and failed removals remain available for retry. Invalid checkout cleanup skips; ordinary rm errors no longer block teardown. Grants and revokes share canonical positive issue validation. Focused tests and their race run pass, and all 17 isolated compiled guard mutations go assertion-red with a green control. The production reconciler also passed a live owned-root check: remove an unsaved grant, preserve a tracked checkout, then remove its entry after release. Full repaired-head gates are running.

The final repair preserves comment dispatch identity: a `/swarm` comment's checkout job key differs from its source issue number. The extra receipt-number restriction and its mutation were removed; a real native client-selection fixture now covers that distinction. All requested lifecycle guards remain in the final 16-mutation gate. The TUI red/green/removal proof passed again using a canonical issue checkout. Normal final-head and race image gates continue.

Final repair gates PASS at functional head 34c65aa: local Go 1.25 vet/full suite; bookworm vet/build/full normal/full race suites with explicit passing package verdicts and zero container exit; 16/16 compiled assertion-red guard mutations with a green control; isolated root endpoint subset; live production recovery; and repeated canonical-checkout TUI red/green/removal proof. Native readback confirms no proof trust entries remain. The final evidence commit changes only artifacts, then the PR body and coordinator-requested READY follow-up will be updated with its head. No merge or deployment.
