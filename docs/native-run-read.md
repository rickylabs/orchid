# Native run lookup and changes

The dispatcher exposes a read-only native observation socket when `native_run_socket` is configured. This supplies the missing source primitive tracked by [#132](https://github.com/rickylabs/orchid/issues/132), for [harness#776](https://github.com/rickylabs/harness/issues/776) and [cockpit#948](https://github.com/rickylabs/atelier-cockpit/issues/948). It does not start, resume, stop, redispatch or mutate an agent.

## Configuration and access

Set `native_run_socket` in `divybot.json` to an absolute, clean socket pathname outside a checkout. The path has no default; an absent or empty setting disables this read endpoint. The existing example uses a placeholder that the operator replaces in private runtime configuration.

The operator provisions the canonical parent before startup. It must be owned by the dispatcher UID and have exactly one of these modes:

- **0700:** a private parent for a reader running as the dispatcher UID.
- **2750:** a setgid shared parent, with its group set to a group of the observer. Mount that directory into both containers at the configured pathname. Group members may traverse the directory but cannot replace the endpoint. Orchid never creates the parent or changes its owner, group or mode.

Immediately after binding, Orchid sets the socket to **0660**, using the existing held, no-follow inode permission primitive before accepting connections. The directory's setgid bit supplies the socket group; the ownership operation preserves that inherited group. No world access is granted. The owner-grant endpoint remains private mode 0600 and has a separate policy.

By default, only the dispatcher UID is allowed by the server's kernel peer-credential check. For an observer with a different numeric UID, set **`ORCHID_NATIVE_RUN_PEER_UID`** in the daemon environment to that one UID as a canonical unsigned decimal integer. This replaces the permitted reader UID; it does not add every member of the shared group. Unset or empty means the dispatcher UID. Negative values, signs, whitespace, leading zeroes and the reserved all-ones UID are refused. Configuration is captured once before bind; it is never taken from a client request. Every connection is checked using Linux `SO_PEERCRED` before reading a request, independently of filesystem access. Group access alone cannot authorize another UID.

A missing parent refuses only the read endpoint with `native-run-source unavailable reason=parent-directory-missing; supervision continues`. An invalid peer UID uses `peer-uid-invalid`. Other unsafe configuration uses `endpoint-refused`, and a live endpoint uses `endpoint-live`. These fixed reasons contain no configured paths or native identities. Supervision, run resumption and once-mode ticks continue in every case.

A leftover endpoint from a crash, kill or `--once` exit is replaced only when it is this dispatcher's 0600 or 0660 socket in an accepted parent, its connect probe returns connection refused, and its inode is unchanged after the probe. Live, foreign, non-socket, symlinked and unprobeable endpoints are preserved. `--once` removes its endpoint before exit; shutdown removes only the inode this listener created. Native identities must not enter public APIs, logs, streams or argv.

This shared-directory policy is the latest owner-directed server change for [harness#787](https://github.com/rickylabs/harness/issues/787), superseding that issue's original private-only deployment wording. **Consumer adoption is required before deployment:** Cockpit's existing `orchid-native-runs/socket.ts` still requires an own-UID 0700 parent and 0600 socket. Its explicitly configured server identity and shared-directory validation must be updated independently; relaxing the server does not change the client. On the observer only, `COCKPIT_ORCHID_RUN_SOCKET` selects the matching mount pathname and `COCKPIT_HERDR_SERVER_MAPPING` retains the exact Herdr scope mapping. The JSON-lines protocol below is unchanged. This code change does not certify deployed observer health or Home's running count.

## Protocol version 1

The transport is one UTF-8 JSON object per line. Exactly one request is accepted per connection: lookups close after the response; subscriptions become server-only event streams after acknowledgement. Requests have exactly `id`, `method`, `params`; only `run.lookup` and `run.subscribe` exist. Keys are case-sensitive, required and non-null, and unknown or duplicate fields are refused. Request frames are at most 64 KiB. Lookup batches contain at most 128 distinct keys; consumers can choose smaller batches. A non-null empty keys array is valid and proves source health/generation with an empty census. No request can select a coordinator action. The [closed wire schema](native-run-read.schema.json) describes requests and server frames.

`run.lookup` takes `params.keys`, an array of `{ key, label }`. The key is a positive canonical decimal JavaScript-safe job/source-comment coordinate; the entire label must be exactly `<agent>-<key>`. Accepted recorded vendor tokens are `codex`, `codex-run`, `claude`, `agy`, `opencode`, `opencode-run`. Orchid reads Jobs, SourceBindings and CompletedRuns directly for the requested keys. No receipt enumeration, transcript discovery, process ancestry or external call participates.

The response is `{ id, result }`. Result has `type: run_lookup`, `schemaVersion: 1`, `generation`, JS-safe integer `revision`, read-time `observedAt`, durability-health `complete`, `records` and `missing`. Complete does not mean every requested identity is known. Each record has:

- `key`, canonical source `issue: { repo, number }`, exact `dispatchKey`, private `nativeSessionId`, recorded `agent`, entire `label`;
- `scope: { host, session, machine }`, with host/session nonempty and machine possibly empty;
- stable `agentId` and `assignmentId`, using the existing `actionOpaque(kind, "orchid-" + dispatchKey)` identity shared with final-comment markers;
- `sourceCommentId`, equal to the key for comment dispatches or zero for inbox issue dispatches;
- `phase: active | completed`, and `observedAt`. Active record time is the read time. Completed record time is the persisted native completion fence time; `completedAt` is present and equal to that original fence time. It is never advanced by reads. Use result `observedAt` for source freshness, and omit read timestamps from logical-content hashes.

The native ID can come from `Job.NativeSessionID` or its RemoteControl native identity. Conflicting values are unavailable. A completion fence persists the complete safe identity projection before job removal, so job/source-binding pruning cannot change the original source issue, label, vendor, scope or opaque IDs. Legacy fences lacking this metadata return unavailable; there is no inbox fallback or receipt-verification requirement.

Missing reasons are closed: `run_not_found`, `binding_unavailable`, `identity_mismatch`, `native_identity_unavailable`, `source_unavailable`. Malformed/unavailable requests receive `{id,error:{code:"source_unavailable"}}`; no error includes native identifiers or paths. Consumers must match the full returned label, canonical vendor mapping, exact native ID and host/session/machine scope against the current Herdr integration. Duplicate or conflicting seats stay unresolved. Unknown signals never establish liveness or redispatch permission. Herdr is the current liveness authority; Orchid's completed fence establishes final-run association, not a successful quality outcome. Idle/done seats alone are stopped turns.

`run.subscribe` takes empty params. The acknowledgement is `{id,result:{type:"subscription_started",generation,revision}}`. Following frames are `{event:{type:"runs_changed",generation,revision}}`. Register/acknowledge before reading a baseline on a separate connection. Changes invalidate selected-key reads, never establish agent status. Queue length one coalesces invalidations; at most 32 connections exist. Disconnect/restart invalidates the old source. Generation changes across process restart and revision exhaustion; revision never exceeds JavaScript-safe integer precision.

Successful durable saves alone advance revision. Failed commits mark reads unavailable and wake observers at the same certified revision; consumers must accept nondecreasing revisions so health loss is delivered. A later successful save restores availability. The source serializes only the explicit safe projection, never goals, source bodies, seat handles, private operator paths or credentials.

The Linux cross-UID admission and rejection fixtures run in the existing isolated root CI lane: `NATIVE_RUN_REQUIRE_ROOT=1 go test -race ./cmd/divybot -run '^TestNativeRun(ForeignUIDKernelPeer|ConfiguredUIDKernelPeer)$'`. They prove the configured observer can subscribe and look up through actual 2750/0660 permissions with the inherited group, while another kernel UID cannot read even with pathname access. Ordinary unprivileged runs explicitly skip that proof, rather than claiming it passed.
