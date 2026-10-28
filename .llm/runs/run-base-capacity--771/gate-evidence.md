# Final local gate evidence

PASS. Required and configured local gates completed in the isolated `golang:bookworm` image, using warm modules/build caches and Go 1.27.3.

- `go vet ./...` — exit 0.
- `go build -o <artifact> ./cmd/divybot` — exit 0.
- `go test -count=1 -json ./...` — exit 0; 3,020 test/subtest passes, zero failures, six explicit skips; 543.769 seconds.
- `go test -count=1 -race -json ./...` — exit 0; 3,020 test/subtest passes, zero failures, six explicit skips; 495.068 seconds.
- `python3 scripts/check-run-recovery-mutations.py` — exit 0; all 27 mutants compiled and failed assertions; all 27 restored controls executed tests and passed.
- Existing source-trigger guards — 50 compiled assertion-red mutants, exact sources restored.
- Existing owner endpoint guards — 17 compiled assertion-red mutants; actual root/operator UID kernel controls passed, exact sources restored.
- Existing checkout-trust guards — 36 compiled assertion-red mutants; positive control passed and isolated sources restored.
- Independent same-session read-only Claude Opus 5.5 plan and implementation review — final PASS after one implementation repair pass. Native model metadata confirms the requested model; command effort high, stdin closed.
- `git diff --check`, formatting and public-content/commit-identity scans — PASS.

All 216 Go files, go.mod and the mutation runner match the final gate snapshot byte-for-byte. No product code changed after these gates.

Explicit ordinary/race skips, never assumed green:

- TestRealHarnessReadOnlyProbe
- TestOwnerNativeEndpointTwoUIDs
- TestOwnerNativeEndpointOperatorChild
- TestOwnerNativeSocketOwnershipNoFollow/foreign-owner
- TestOwnerNativeSocketRegistrationGuards/wrong-owner
- TestReceiptOwnerCrossUID

The endpoint cases run separately under actual root/operator UIDs. The real Harness probe and cross-UID receipt case remain environment-gated. No deployment, provider-backed capacity experiment or merge is claimed.

An initial broad run exposed outdated synthetic checkout responses; those were corrected. A later run under competing mutation compilation hit a 100-ms readiness fixture before its first read. Twenty isolated repetitions passed on both candidate and original baseline. The unchanged final source then passed the complete serialized ordinary and race suites above. Raw failures remain retained privately rather than being discarded.

Leak check: the owned disposable image gate container was removed and its absence was verified. No foreign resource was mutated; no live service was restarted.
