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
