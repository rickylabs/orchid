# Round 2 final gate evidence

PASS. All requested and configured local gates completed on the final source in golang:bookworm, Go 1.27.3, with warm modules and an isolated nonroot fixture account.

- go vet ./... — exit 0.
- go build -o <artifact> ./cmd/divybot — exit 0.
- go test -count=1 -json ./... — exit 0; 3,039 test/subtest passes, zero failures, six explicit skips; 455.835 seconds.
- go test -count=1 -race -json ./... — exit 0; 3,039 test/subtest passes, zero failures, six explicit skips; 484.956 seconds.
- python3 scripts/check-run-recovery-mutations.py — exit 0; all 37 compiled mutants fail assertions and all 37 restored controls execute tests and pass.
- Configured source-trigger guard scan — exit 0; 50 compiled assertion-red mutants, exact sources restored.
- Configured checkout-trust guard scan — exit 0; 36 compiled assertion-red mutants, positive control passes, isolated sources restored.
- Separate root/operator kernel controls and configured endpoint guard scan — exit 0; 17 compiled assertion-red mutants using actual distinct fixture UIDs, exact sources restored.
- Retained independent read-only Claude Opus 5.5 evaluation — PASS, high effort, stdin closed. See round2-implementation-review.md.
- Formatting, public-patch privacy and diff checks — PASS.

All 216 Go files, go.mod and the recovery mutation runner match the tested snapshot byte for byte. The sorted source-manifest SHA256 is 7f2ed2aba4674400a7593b2787172920e2bad8832d63a05c2709d5a1a9783e5c. Production source did not change after independent implementation review; the inherited capability fixture's startup allowance was separately reviewed and verified.

Explicit ordinary/race skips:

- TestRealHarnessReadOnlyProbe
- TestOwnerNativeEndpointTwoUIDs
- TestOwnerNativeEndpointOperatorChild
- TestOwnerNativeSocketOwnershipNoFollow/foreign-owner
- TestOwnerNativeSocketRegistrationGuards/wrong-owner
- TestReceiptOwnerCrossUID

The endpoint cases have separate root/operator kernel evidence. The real Harness probe and cross-UID receipt integration remain environment-gated and unproven, never assumed green.

Both required manual-resume regressions compile and fail their intended assertions on prior head 94d0809, then pass after repair. The checkout regression compiles unmodified against original production 22f14e0 and fails first at the stale-base assertion. See round2-red-green.md.

Earlier evidence remains retained: the obsolete runner before the anchor repair was canceled; an inherited one-second capability fixture failed once in 20 isolated repeats on both prior and candidate code. Its test-only startup allowance became five seconds, all 20 repeats then passed, and the full final sequence above passed without further product changes.

Leak check: the owned disposable gate container was removed and its absence verified. No foreign resource was mutated. No merge, deployment or live service restart was performed by this run. Remote CI was not polled or treated as local evidence.
