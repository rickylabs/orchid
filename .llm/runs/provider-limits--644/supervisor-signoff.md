# Supervisor source sign-off — Orchid producer/governor dependency for Harness644, final Owner549 source (2026-10-08)

Verdict: PASS. Supersedes the earlier sign-off recorded at commit 497fa8d. The Owner549 delta (rate_limited/429 informational) was reviewed substantively by this supervisor as a separate read of cmd/divybot/provider_limits.go, provider_limits_test.go and docs/provider-limits.md against 497fa8d; all other product paths are byte-identical to the earlier reviewed set. The same independent Meta MuseSpark1.3/xhigh conversation returned PASS on call3 for these frozen bytes (call1 output-exhausted UNPROVEN, call2 certified prior bytes only, no terminal failures). No product edit followed call3.

Verified before commit: all 21 product paths in reviewed-product-source.json and verification-source.json match current SHA256; the final producer, source-trigger and endpoint mutation runners report exact restoration with hashes equal to the shipping bytes; `git diff --check` clean; run records screened for credentials, native session IDs, private operational paths and real addresses (fixtures and source hashes only).

## Owner549 delta invariants reviewed

- Rate observations use a separate private entry key (scope plus rate suffix); hard refusals and successes share the scope key. Reservation, retirement and capacity stay keyed by scope, so one scope holds at most two rows.
- A later 429 reads and writes only its own key; the older-than-existing check returns without fencing for rate; the success-clearance loop excludes rate rows. A later 429 can neither erase nor shadow quota_exhausted/payment_required, and a stale rate row cannot block a success record. Admission skips rate rows outright.
- Verified-success clearance is unchanged for hard refusals: strictly later, same clock, matching provider/key/account and exact or provider-wide model.
- Legacy rate rows keyed by bare scope migrate to the suffixed key before validation and fail closed on duplicate.
- Compaction archives only inactive model-scoped successes, so rate rows are never retired; rate keys never collide with retired scope keys; the reservation sweep keeps a scope while either row exists.
- Reserved scopes are bounded at 512 on load, record and launch reservation; every entry's scope is reserved, so entries are at most 1024, the unchanged public row bound accepted by the Harness decoder without outcome dedupe.
- Same-clock highwater, equal-instant refusal dominance and cross-clock non-clearance are untouched. An older rate observation from any clock is dropped silently and never sets the persistence failure; an older hard refusal on a foreign clock still fences.
- Native observedAt and retry-after reset are stored verbatim for rate rows and asserted after publication. No wire field, diagnostic, credential, host or activation path was added.

## Earlier reviewed invariants, unchanged bytes

Native mtime and message-time preservation with ledger NativeAt equality; one attested account per provider polled only on its source host; exact-binding refusal matching with advisory meters and physical-only concurrency; occupant recheck after export with typed native refusal/success evidence; lease-protected 0600 no-follow ledger with reserved capacity, refusal-preserving compaction and permanent retired fences; explicit unbound identity rows for binding retirement; fixed own-key endpoint with alias-only publication.

## Final gates on these exact bytes (private receipts under the project run directory)

| Gate | Exit |
| --- | --- |
| go test -race ./... (561.451 s) | 0 |
| go vet ./... | 0 |
| go build ./cmd/divybot | 0 |
| Source-trigger mutations, 41 compiled assertion-red, restored | 0 |
| Endpoint non-root mutations, 7 compiled assertion-red, restored | 0 |
| Producer mutations, 10 compiled assertion-red, restored | 0 |
| Final affected producer tests | 0 |
| Configured privileged two-UID kernel | 127, UNPROVEN, sudo absent |
| Configured privileged root endpoint mutations | 1, UNPROVEN, sudo absent |

Independent paired proof: four actual-producer snapshots (rate-only and hard-plus-later-429 for quota_exhausted and payment_required) against the separately owned renewed Cockpit550 checkout fd059e62944e9cf7d890df3bc87d270337b3ca59 exit 0. Hard refusal stays blocking through every picker configuration and admission despite a later 429; rate-only and verified-success histories launch. Fixture only; no foreign source was changed.

## Limitations preserved

- Root two-UID gates are UNPROVEN and are not called green; non-root subsets do not prove two-UID behavior.
- Live configuration, process restart, secret stores, package publication, real-provider inference, mobile645 and the canonical parent report are out of scope and unproven.
- Automatic native outcomes exist only for the bound OpenCode route adapter; unsupported adapters stay unknown. Automatic provider-wide native emission is unproven; provider-wide blocking is fixture-proven.
- Non-blocking debt: a success whose only matching refusal is a rate row is retained by the clearing-tombstone check; rate rows persist bounded by scope count; collection holds the limits mutex up to 30 s; a foreign-clock older hard refusal fences until restart.
