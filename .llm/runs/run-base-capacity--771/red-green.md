# Red and green regression evidence

Both required defects fail assertions on original production and pass with the changes.

Baseline: `22f14e0eca84ba0b600a83eb782db653d66d88b9`. An isolated source archive carries only the synthetic regression fixtures; the checkout fixture uses the baseline receipt field name. No fixed production helper is present in that archive.

Command: `go test ./cmd/divybot -run '^(TestRunCheckoutFreshHeadDespiteAdmissionPin|TestCapacityStallEntersRecoveryFromNativeSignal)$' -count=1 -v` in `golang:bookworm` with warm modules.

- `TestRunCheckoutFreshHeadDespiteAdmissionPin`: RED, assertion that the run branch started from the stale admission pin instead of freshly fetched default HEAD. The test drives production spawn against a real synthetic Git remote. Fixed controls additionally cover cached local default refs, reused branches, explicit SHA/ref bases, a changed remote default, missing refs and foreign origins.
- `TestCapacityStallEntersRecoveryFromNativeSignal`: RED, assertion that the native capacity stall was left idle. This test drives the canonical socket transport with native thread/turn shapes. Fixed controls additionally cover persisted bounds/backoff, ambiguous acknowledgements, native-start deadline, accounting, pauses, binding changes, canonical capability and observation debounce.

Focused fixed controls are GREEN. Raw output and original source archive remain private; the final broader gate verdict is recorded separately in gate-evidence.md.
