# Codex checkout trust — plan

Plan & Design — READY FOR REVIEW. One scoped implementation slice.

1. Use Codex 0.164 `config/value/write` on the canonical daemon connection already opened by preparation. Grant only `projects.<quoted exact cwd>` with `trust_level=trusted`; remove that same entry with null. No whole-projects replacement, config-file editing, screen reading, consent input or new polling.
2. A typed private request constrains the RPC write capability to this trust shape. Fail launch on refused, malformed or overridden writes. Keep native thread verification and daemon-version pinning.
3. Keep per-thread trust because thread creation precedes persistent trust. Drop the ineffective remote CLI trust override; plain local Codex retains its process-only flag.
4. Cleanup revokes before deleting the recorded checkout; a failed revoke or deletion retains retry responsibility. Failed launches revoke their grant without consulting the app.

Dependency order: protocol evidence → plan gate → implementation/tests → draft PR → live checks and broad gates → independent cross-family review → ready.

Risks: quoted path parsing (special-path test and live API check), sibling deletion (narrow key assertions), ignored writes (strict native status checks), launch/cleanup sequencing (fixture tests), unavailable daemon (refusal tests).

Owner forks: none. Explicit operator instructions choose the protocol, scope, evaluation route, privacy constraints and READY stopping point. No architectural amendment.

Sources: installed 0.164 generated `ConfigValueWriteParams` and `ConfigWriteResponse`; official [config manager](https://github.com/openai/codex/blob/rust-v0.164.0/codex-rs/app-server/src/config_manager_service.rs), especially `parse_key_path` and null `clear_path`; Orchid `cmd/divybot/main.go` launch and teardown, `remote_control_transport.go` preparation, `registration_trust.go` process override.
