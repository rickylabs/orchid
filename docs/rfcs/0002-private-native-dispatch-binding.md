# RFC 0002: Private native dispatch join

- Status: Proposed; implementation included for review
- Date: 2026-09-15
- Scope: One private native session join key on an Orchid dispatch receipt

## Evidence snapshot

- MEASURED: Fresh Orchid clone initially based on main `e2f31e64699ce970039d4b4e9b4b90cc90ce7617`; rebased onto main `4cc9c2c72bc2bf68cddaff26fdbc8ad3716002d8` before publication.
- MEASURED: Installed herdr 0.9.0, bundled protocol 22/schema 1; binary SHA-256 `4fa1a01158dd8043da92d31b270780b0dcc10603038d9b61cac4d81ab63fb71f`.
- MEASURED: Installed Codex CLI 0.154.0; start parameters generated offline by `codex app-server generate-json-schema`.
- MEASURED: `scripts/check-native-identity-contract.py` finds no usable native hook reference on the explicitly selected current agent. Verdict: INCONCLUSIVE, `native-session-unavailable`. No live launch-to-native binding is claimed.

## Delivery tracking

- CURRENT: The coordinator's native-identity brief authorizes this slice. [Harness 370](https://github.com/rickylabs/harness/issues/370) records that native ancestry already exists and only the dispatch join is missing; this PR does not close that broader evidence issue.
- CURRENT: [Orchid PR 9](https://github.com/rickylabs/orchid/pull/9) recovered the effort change onto main. This PR targets main directly and includes no separate effort implementation.

## Claim labels

Every material statement is marked CURRENT (repository behavior), CITED (external contract), PLANNED (proposed behavior), or MEASURED (executed evidence). Synthetic tests prove implementation behavior, not live enrollment or independent runtime model observation.

## Summary

PLANNED: Add one field, `NativeSessionID`, to the existing private `binding.json` after successful registration. Accept only the native Codex hook reference returned by herdr. Leave it absent on inconclusive or failed launches. Harness owns the existing tree and parent reader; no ancestry collector, parent lookup or nesting format is added.

## Context and current state

CURRENT: `Host.spawnAgent` in `cmd/divybot/main.go` discards the successful `herdr agent start` result. `dispatchLocation` identifies terminal layout only. A layout identifier is not a native session identity.

MEASURED: Herdr's bundled schema describes `agent_started.agent` and `agent_info.agent` as `AgentInfo`, with optional `agent_session: {source, agent, kind, value}`. Kind distinguishes an ID from a path. The bundled Codex integration v8 takes the native SessionStart ID from stdin, rejects an inherited-thread disagreement and reports it over the socket as source `herdr:codex`. This change neither installs nor enables that integration.

CITED: [Codex hooks](https://learn.chatgpt.com/docs/hooks) receive JSON on stdin. Generic subagent hook session IDs can refer to the parent. The herdr integration accepts SessionStart and guards the inherited thread; arbitrary hook reports are not accepted as dispatch identity.

MEASURED: Neither installed interactive CLI help nor generated `ThreadStartParams` advertises a caller-selected new session ID. An invented configuration key would not establish identity. No ID is placed in CLI arguments.

## Proposed join and publication

PLANNED: `nativeSessionFromStart` checks the exact ready Codex occupant, response type and source-tagged ID. One `agent get` of that same occupant may obtain a hook report absent from the start response. No fleet-list search, file selection, pane scraping or timestamp matching is performed.

PLANNED: `binding.json.NativeSessionID` is the unmodified native session reference. It is not an Orchid run ID or public agent ID. The fixed provenance is the verified `herdr:codex` ID variant. All existing binding fields remain intact. No new file or receipt envelope is introduced.

PLANNED: Publication follows the successful `dispatched` snapshot. It uses the existing reservation directory, temporary-file/sync/rename pattern and configured receipt owner. File mode remains 0600; directories remain 0700. The native join is removed on uncertain launch transitions. If invalidation cannot rewrite the binding, it removes that private binding rather than retaining a usable native join; the durable reservation fence remains. Filesystem failure is reported, never certified as successful publication.

PLANNED: A consumer joins this private value to an existing native telemetry run only for the same successful dispatch reservation. It exports its existing opaque public identities, never the native value. `dispatch.json.parentRunId` stays null. Native telemetry remains the only ancestry source.

CURRENT: Harness's existing `packages/telemetry/src/orchid-dispatch.ts` reads `dispatch.json`, not `binding.json`. This slice adds the missing source field; it does not claim the Harness consumer has been updated or that the alpha view is enrolled.

## Closed inconclusive reasons

PLANNED:

| Reason | Meaning |
| --- | --- |
| `native-session-unavailable` | The exact agent has no native hook reference. |
| `native-session-contract-unsupported` | This transport has no supported native binding adapter. |
| `native-session-evidence-invalid` | Wrong occupant, source, kind, identity or ambiguous response. |

PLANNED: Inconclusive identity does not make an otherwise successful registration fail. Failed registration never enters identity publication. Logs contain only the issue number and closed reason, not the native response.

## Consequences

PLANNED: A working native hook can provide the one missing dispatch-to-telemetry join. Missing hooks remain explicit. Other transports remain inconclusive until their own identity contracts are supported. No model request, native API observer, ancestry read or service restart is added.

CURRENT: No footer observation is added. The coordinator supplied an unpaired footer for a different issue; no requested-versus-observed mismatch can be established from it. The matrix receipt's observed fields remain unchanged.

## Revisit triggers

PLANNED: Revisit when herdr changes its source-tagged session contract, Codex provides a safe caller-selected identity, or another transport gains an authoritative adapter. Weak footer evidence is a separate question: disagreement may support a weak mismatch; agreement never earns known status.
