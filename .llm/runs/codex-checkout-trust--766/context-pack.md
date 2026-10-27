# Codex checkout trust — coordinator review repair delivered

The Opus 5.5 CHANGES review of de9b9f9 is addressed on the same PR branch. PR: https://github.com/rickylabs/orchid/pull/128. Tracker: https://github.com/rickylabs/harness/issues/766. Functional repair 34c65aa88e968cd8aa3548cfaaa1c49045f1f745 passes all final gates. The final evidence commit changes only these run artifacts; update the PR and post the authorized READY follow-up with its head, then stop for coordinator review.

Cleanup again skips invalid paths and treats ordinary deletion failures as best effort. Only a failed Codex revoke can block teardown confirmation. Bounded release attempts immediate revoke outside the State lock. Startup and each existing serialized tick reconcile orphan trusted direct issue checkouts under configured persistent/compatibility roots before admission. Failed rollback and a crash before Job save recover from native config without a grant registry. Live jobs are retained per host; foreign/nested paths and explicit untrusted entries are protected. Disabling new Codex launches still permits recovery.

Grant and removal share validJobCheckout validation. Comment job keys are comment IDs, distinct from source issue numbers; the native launch fixture covers that distinction. Per-thread trust remains for creation; the ineffective remote trust argument stays removed.

PASS: local Go 1.25 vet/full tests; bookworm vet/build/full normal/full race suites; root endpoint subset; green mutation control plus 16/16 compiled assertion-red guards; native live recovery and repeated TUI red/green/removal proof; formatting/diff/privacy checks. All proof resources are confirmed cleaned up. See validation.md and review-fixes.md.

The coordinator supplied the independent Opus review and authorized this repair, superseding the unavailable GLM route. No independent PASS is claimed for the new head. The coordinator owns subsequent review, merge and deployment; this run performs neither merge nor deployment. The latest user explicitly authorizes posting READY on issue 766; leave that issue open. No service restart or remote CI wait.
