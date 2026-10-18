# Cockpit action delivery to divybot

Divybot may consume an operator-provisioned private request spool. The spool is **separate** from `matrix.receipt_root`. Set `action_request_root` to the container path after the mount exists. Omit it to disable action intake. The spool and `tmp/`, `new/`, `done/` under it must be existing, nonsymlink, outside-Git directories with private mode `0700` or `0770` for a shared group/ACL. The receipt root remains mode `0700`. Cockpit has write access to the spool and read-only access to the receipt root; divybot has write access to both. There is no HTTP listener or public issue-comment transport.

Cockpit writes a bounded JSON request to `tmp/<operationId>.json` with private mode `0600`, or `0660` when the two runtimes use a shared group, syncs the file, renames it to `new/<operationId>.json`, and syncs `new/`. `operationId` is a lowercase UUID. The request shape is:

    {"schemaVersion":1,"operationId":"<uuid>","idempotencyKey":"<key>","repository":"owner/repo","issueNumber":123,"agentId":"agent_<sha256>","dispatchId":"assignment_<sha256>","expectedAgentRevision":"<sha256>","action":"stop|steer|retry|send","payload":{"text":"...","reason":"..."}}

`stop` uses only an optional reason (up to 512 bytes); `steer` and `send` require text (1 to 16,000 bytes); `retry` requires an empty payload. The entire file is limited to 20,000 bytes. Cockpit is responsible for authenticating repository and conversation grants and checking the expected agent revision before publication. Divybot independently verifies the exact persisted root job, private dispatched receipt, opaque agent and assignment identifiers, and live Herdr occupant before any seat effect. The agent revision is carried as an admission claim; divybot cannot reconstruct Harness's feed revision and does not reassert it as observed.

Divybot first syncs `matrix.receipt_root/actions/<operationId>/intent.json`, a durable no-repeat fence containing the SHA-256 of the complete request bytes. A matching replay reads the immutable `result.json`. A different digest creates an immutable `conflict-<digest>.json` rejection and does not replace the primary result. If the intent exists without a result after a crash, the result is `unknown` with reason `receipt_missing_after_fence`; the action is never repeated. After writing and syncing a result, divybot moves the original request into `done/`. The done filename includes the digest and a sequence suffix so replays do not overwrite mail.

Owner-only receipts carry identity, request digest, `accepted|rejected|unknown`, a fixed reason code, and observation time. They omit message/reason text. `accepted` means Herdr confirmed the delivery call, not that the agent executed it; execution remains unproven until the independent issue-agent feed shows a state change. A missing private receipt, missing job, ambiguous occupant, timeout, or failed Herdr call is `unknown`. Stop persists a no-respawn launch block before requesting workspace closure. Steer requires a working or blocked occupant; send accepts a live working, blocked, or idle occupant. Both submit the exact text through Herdr's prompt and Enter path. No message ID is claimed without a native observation. Retry is currently rejected with `retry_requires_terminal_successor_contract`: the existing matrix reservation is single-use for an issue brief, and divybot has no safe replacement run/agent linkage yet.

## Cockpit dispatch targets

The Cockpit also publishes which repositories agents may work in, as one snapshot `targets.json` directly in the spool root, written to a temporary file in the same directory and renamed into place:

```json
{"schema_version":1,"targets":[{"repo":"example/project","agents":["claude","codex"]}]}
```

The file must be a regular, nonsymlink file with mode `0600` or `0660`, at most 64 KiB. `repo` is `owner/name`, unique ignoring case, at most 256 entries; `agents` is a non-empty, duplicate-free subset of `claude`, `codex`, `agy`, `opencode`. An empty `targets` list turns every Cockpit target off.

Divybot reads the snapshot at the start of every tick, before the action requests, the source scan and admission; no restart is needed. A missing file before the first load means no Cockpit list yet. An unreadable or invalid snapshot keeps the last good list and logs the problem once per distinct reason, never with the file contents. A changed valid list is swapped atomically and logged as `dispatch targets from cockpit: N (<repos>)`.

The effective targets are the static `targets` followed by each Cockpit repository that is not already a static target (ignoring case); a static target wins. A Cockpit target has no inbox label, so the inbox is never polled for it; it is reached through a `/swarm` comment on that repository or an inbox issue whose `repo:` key names it. Its `agents` is an allow-list: a route whose transport is not listed is refused with `harness-conflict`. Without a `matrix.target_revisions` pin, a launch starts from the repository's default-branch head.
