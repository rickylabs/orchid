# Native run lookup and changes

The dispatcher exposes a read-only native observation socket when `native_run_socket` is configured. This supplies the missing source primitive tracked by [#132](https://github.com/rickylabs/orchid/issues/132), for [harness#776](https://github.com/rickylabs/harness/issues/776). It does not start, resume, stop, redispatch or mutate an agent.

Choose an absolute socket path under a canonical private directory owned by the dispatcher account (directory mode 0700). The socket is mode 0600. A pre-existing endpoint is never replaced; shutdown removes only the socket inode this instance created. Mount or forward that configured private endpoint to the observation worker through the operator's existing deployment wiring. Native identities must never cross a public API, log, stream or argv.

## Protocol version 1

The transport is one UTF-8 JSON object per line. Requests have `id`, `method` and `params`; only `run.lookup` and `run.subscribe` exist. No request can select a coordinator action. Request frames are at most 64 KiB; lookup batches are at most 128 distinct keys. Large native censuses use multiple batches, never a larger limit.

`run.lookup` takes `params.keys`, an array of `{ key, label }`. The key is the decimal source comment/job coordinate in the restored Herdr workspace label, such as the placeholder `codex-<commentId>`; the label must carry that exact coordinate. Orchid reads its native Jobs, SourceBindings and CompletedRuns maps directly for those keys. It does not enumerate receipt directories, load vendor transcripts, walk process ancestry or call GitHub. Retained history cannot exclude an active key.

The response is `{ id, result }`. Result has `type: run_lookup`, `schemaVersion: 1`, a process `generation`, native `revision`, source-health `complete`, `records` and `missing`. Each record has `key`, canonical source `issue: { repo, number }`, exact `dispatchKey`, private `nativeSessionId`, native job `agent` and `label`, `phase: active | completed`, and `observedAt`. A removed completed job can have empty agent/label fields; the receipt owner must verify its vendor and label before binding it. Missing entries carry a closed reason (`run_not_found`, `binding_unavailable`, `identity_mismatch`, `native_identity_unavailable`, `source_unavailable`). Missing identity is never a running or ended claim.

A completion fence is native final-run evidence; a Herdr idle/done seat alone is only a stopped turn, not a final assignment outcome. Consumers must match current native session/vendor and the exact receipt identity, and treat conflicting or duplicated seats as unresolved.

`run.subscribe` takes empty params. The acknowledgement is `{ id, result: { type: subscription_started, generation, revision } }`. Following frames are `{ event: { type: runs_changed, generation, revision } }`. Subscribe before taking the baseline. Changes are invalidations: re-read the current selected keys; never infer state or identity from an event counter. One dirty bit per observer coalesces notifications; slow readers cannot accumulate an unbounded history queue. At most 32 connections are served concurrently. Disconnects, malformed/oversized frames and restart generations invalidate the old read.

State notifications follow durable commits. A failed state commit makes reads unavailable until a later successful commit; uncommitted in-memory changes cannot certify a run. The source carries only an explicit projection: no goal, prompt, source-comment body, route credential or private operator configuration is serialized.
