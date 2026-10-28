# Resume run base and capacity repair

Implementation and local validation are complete for Orchid PR https://github.com/rickylabs/orchid/pull/130. Stop at READY; the coordinator owns merge and deployment. Tracking issue https://github.com/rickylabs/harness/issues/771 stays open.

Baseline: 22f14e0eca84ba0b600a83eb782db653d66d88b9. Causes: matrix.go:790 admitted an old routing pin as checkout base; main.go:3482 fetched/reset to it. Completion.go:112 only refused a blocked goal, while main.go:3817 suppressed idle/done input and had no native capacity recovery owner.

run_checkout.go now fetches remote HEAD or explicit brief base and persists the actual StartSHA before a seat exists. Admission revisions remain separate metadata. goal_recovery.go uses a matching blocked owned goal plus typed native serverOverloaded, five durable attempts, backoff, a bounded native-start deadline, a canonical shared-daemon capability, observation debounce and a daemon-owned timer. Managed running capacity is retained and ordinary blocked/idle input defers.

Final gates: vet/build/full test/full race PASS in golang:bookworm; each full suite has 3,020 passes and six explicit skips. All 27 new compiled mutants fail assertions with restored green controls; inherited 50/17/36 guard scans pass. Independent read-only Claude Opus 5.5 evaluation PASS after one repair pass. See gate-evidence.md, mutation-evidence.md, red-green.md and implementation-review.md. Accepted native conditional-update limitation is recorded in debt.md.

All 216 Go files plus module/mutation source match the tested snapshot. Private raw logs, routing metadata and seven reusable memory notes stay in project run storage. Final publication uses the excluded assignment handoff, then a GitHub readback. No merge, deployment, service restart or foreign-resource mutation is authorized here.
