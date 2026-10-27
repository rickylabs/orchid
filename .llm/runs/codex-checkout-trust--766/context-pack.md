# Codex checkout trust — round 4 READY for coordinator review

Existing PR: https://github.com/rickylabs/orchid/pull/128. Tracking issue: https://github.com/rickylabs/harness/issues/766. Functional head: `5381d68af04bf2000269f5fa2978e1547be84728`. The final evidence commit changes run artifacts only.

The coordinator's adoption blocker is fixed at cmd/divybot/main.go:2288. Adoption records Workdir only for a known host, a canonical positive issue checkout matching the internal job key, and an exact direct parent from checkoutTrustRoots. Other observed paths leave Workdir empty. Raw fleet trust retention and the legacy cleanup fallback are unchanged.

PASS: official golang:bookworm vet/build/full normal/full race; all eight new boundary cases in both suites; a green checkout-trust control and 36/36 compiled assertion-red mutations; configured source-trigger 50/50 and two-UID endpoint 17/17 mutations, plus the isolated endpoint race subset. All mutation sources restored. See round4-gate-summary.json and validation.md. The parent-guard mutation must independently fail both outside-roots and nested-checkout regressions. Fixtures reload saved state and exercise real teardown while recording deletion arguments and checking surviving directories.

Contributor path: codex_checkout_trust.go defines canonical paths and owned roots; main.go adoption consumes them. No new interface, I/O, timer or architectural debt. See the dated round-4 lesson.

The existing branch was reconciled with its remote at the tested functional head before the evidence commit. No rebase or unrelated code change. Coordinator review is pending; no external review or independent implementation PASS is claimed. No merge, deployment, service restart or live trust mutation this round. Prior live API/TUI evidence remains historical in live-check.md.

Canonical publication uses the assignment checkout's excluded final-report file. An accepted matching comment on issue 766 proves publication; writing the file alone does not. Leave the tracking issue open and stop at READY.
