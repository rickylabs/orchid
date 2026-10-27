# Codex checkout trust — supervisor

Baseline: `3f82fcd`. Codex implements; the operator-selected GLM reviewer independently reviews blockers using a read-only OpenCode session.

Write surface: `cmd/divybot/codex_checkout_trust.go`, `cmd/divybot/codex_checkout_trust_test.go`, `cmd/divybot/native_goal_rpc.go`, `cmd/divybot/native_goal_test.go`, `cmd/divybot/remote_control_transport.go`, `cmd/divybot/remote_control_transport_test.go`, `cmd/divybot/remote_control_guards_test.go`, `cmd/divybot/rc_resume_quota_test.go`, `cmd/divybot/registration_trust.go`, `cmd/divybot/registration_trust_test.go`, `cmd/divybot/remote_control.go`, `cmd/divybot/main.go`, `cmd/divybot/checkout_storage.go`, `cmd/divybot/checkout_storage_test.go`, `cmd/divybot/README.md`, and this run directory. No deployments, merges, service restarts, unrelated checkouts, or app-side work.

Routing: fresh feature matrix queried. Explicit assignment overrides select this Codex implementation and `opencode-go/glm-5.3-flash`, variant `max`, for independent review; these differ from the current fleet defaults. Requested reasoning effort is high; application of that setting is dispatcher-owned and not independently verified here.

Specific operator instructions supersede the generic inbox PR template: this is an Orchid PR, its body starts with Refs and the tracking URL, and the tracker remains open. Final report uses the dispatcher file handoff.
