# Resume PR 130 round 2

Existing Orchid PR 130, baseline 94d0809204f9a861557faf4d21c2536c0a7a3695. Preserve its branch/history; no new PR, merge, deployment or service restart. Harness issue 771 stays open.

The implementation fixes exhausted ownership after native manual resume independently of the open-PR retirement guard. Native status/update snapshots retain unchanged automatic exhaustion; notice identity and lifetime retry attempts survive release/restart. Unchanged observations/saves do not add per-tick writes. The stale-base regression compiles on original production, and the test-only admissionRevision accessor is removed.

Plan and independent same-session Claude Opus 5.5 implementation evaluation PASS. Manual/open-PR regressions assertion-red on prior head and green after; checkout regression assertion-red on original production. Final recovery mutation gate: 37 compiled assertion-red mutants and 37 restored passing controls. Vet/build/full ordinary/full race PASS in golang:bookworm, each full suite with 3,039 passes and six explicit skips. Separate two-UID kernel controls and 17 endpoint mutants PASS. The inherited source-trigger and checkout-trust scans are still running on isolated copies. The committed/pushed implementation checkpoint is not final gate sign-off; keep the PR draft until those scans finish.

Next: collect the two remaining scans, verify source/privacy/formatting, commit final run evidence and reconciliation, push the same branch, mark existing PR ready, update its body/comment, publish the excluded assignment final-report handoff and verify GitHub readback, remove the owned disposable gate container, then mark the native goal complete. Private logs, gate scripts and reusable memory remain in project run storage.
