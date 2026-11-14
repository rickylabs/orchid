# Durable assignment ends

Assignment ends are dispatcher facts independent of workspace cleanup. The
dispatcher persists the first terminal native projection in `native_terminals`,
keyed by dispatch identity, with an exact-key index in `native_terminal_keys`.
The existing atomic state-file write, file sync, rename and directory sync are
the durability fence. No database or observer write endpoint is introduced.

The first identity, scope, source association, clock and reason are immutable.
A failed write prevents release; a directory-sync error after rename retains
the first bytes and keeps native readback unavailable until a successful save.
Retries cannot erase or re-clock that possibly committed fact. Cleanup pruning,
binding removal and process restart cannot remove the retained business fact.
Lookups remain bounded point reads, without receipt or history enumeration.

## Source defect and lifecycle

Previously the only retained `completedAt` came from `retireCompleted`'s
temporary `CompletedRuns` cleanup fence. That path required the current native
occupant, completion sequence, delivery, PR and native-goal checks to pass.
Cancellation, retirement, timeout and disappearance could remove jobs without
retaining that projection; confirmed closed-issue cleanup could then prune the
only terminal record. Meanwhile `pollPR` erased a merged PR pointer and reused
the warm session, while native goal finalization was tied to source closure.
An open source with a final report could therefore remain an active job forever.

The new source facts are written before authoritative cancellation/retirement,
timeout or proved native disappearance releases tracking. Verified completion
also retains its fact before cleanup. Valid legacy terminal fences are promoted
before pruning, preserving their original clock. Terminal assignments receive
no continuation input; uncertain native cleanup remains separate and tracked.
A completed vendor turn alone never establishes assignment completion.

Missing screen detection only selects an ordinary-seat candidate. The existing
typed native workspace inventory must prove both captured workspace and label
absent. Shared-daemon jobs keep the stronger existing native owned-tree reaper.
Neither path infers success or failure. No close-by-label effect follows ordinary
native absence, so a later replacement workspace cannot be closed accidentally.

Private reasons are `assignment_completed`, `cancelled`, `retired`, `timed_out`
and `ended_unobserved`. They describe the dispatcher's end decision, not a quality
verdict. In particular, `ended_unobserved` does not fabricate success or failure.
Its clock is the first dispatcher observation of the end, not an invented historic
finish time.

## Bounded historical reconciliation

Run the installed binary as the dispatcher service account, with its existing
private configuration and credentials. Supply an explicit JSON selection:

```json
{"keys":[{"key":"701","label":"codex-701"}]}
```

The example is synthetic. Provision the real selection privately. Input is at
most 64 KiB and 128 distinct exact native keys. There is no automatic history
scan. These commands use explicit operator-provisioned non-secret path variables;
they never discover settings from a process environment:

```sh
"$ORCHID_BIN" reconcile-completions \
  --config "$ORCHID_CONFIG" --keys-file "$ORCHID_COMPLETION_KEYS"

"$ORCHID_BIN" reconcile-completions \
  --config "$ORCHID_CONFIG" --keys-file "$ORCHID_COMPLETION_KEYS" --apply
```

The first command is read-only and is the default. It prints `eligible`,
`already_recorded` or `unknown` per key. Exit 2 means invalid/unavailable input
or configuration. Exit 0 means the bounded assessment completed, not that every
key was provable. No command runs a coordinator tick or saves its offline state.

Apply only enqueues requests through the existing private action maildir and
returns `enqueued`; that is not completion. The running dispatcher consumes at
most one reconciliation request per action pass, revalidates its exact native
identity and work revision, and writes under its service identity. Retries and
duplicate requests converge on the first immutable fact. Other actions retain
their existing processing. The new private action is not a public phone action;
existing receipt readers select the operation IDs they admitted.

For an active historical job, proof requires its exact authenticated final-report
marker, author and dispatch-time fence; positive terminal source/PR evidence;
and native inactivity. A lost PR pointer can be recovered from at most 16 links
in that exact report, restricted to configured repositories. Every linked PR
must be closed or merged. Native owned work, queues, tools and descendants must
be inactive; a newer root turn or contradictory notice during the last native
read rejects the proof. Vendor turn time is only an exclusion fence. Unsupported,
unavailable, active, changed or ambiguous work remains `unknown`. A pre-existing
valid terminal cleanup fence can be retained without fabricating a new clock.

This command does not close issues, publish comments, send input, redispatch,
stop native work, close workspaces, change labels or delete run directories.
Keep `unknown` results for source investigation; do not turn them into forced ends.

## Publication and rollout

The native wire remains schema version 2 with the identical closed record shape.
Source-private reasons add no field or enum to that record. Existing version 2
readers consume `phase=completed` and its frozen `completedAt`; version 1-only
clients still require the already documented version 2 adoption. There is no new
client/producer version ordering for this change.

A successful state save advances the existing native subscription revision. The
observer re-reads selected keys, then existing workers publish through durable
streams and prepared KV projections. Phone GETs never call GitHub or a vendor.
Cockpit already treats a completed native record as ended with unknown outcome.
No mobile change or additional Cockpit protocol change is required.

Build and install this head through the existing dispatcher supervisor's normal
rollout, preserving its configuration, UID, state volume, socket access and
action maildir. Back up the private state before an upgrade. Do not roll an older
writer over newly extended state: old Go decoders can read the additive state
keys, but an old writer does not retain them. Reconcile only after the new
dispatcher is running. Deployment, shared-service restart and live reconciliation
are operator work and are not performed by this source PR.

## Audit scope and remaining work

This addresses native lifecycle/completion gaps F1/F2 and the ordinary missing
seat heuristic O03 in [Cockpit's bottleneck audit](https://github.com/rickylabs/atelier-cockpit/pull/956),
`docs/audits/2026-11-bottlenecks.md`, tracked by
[Cockpit #955](https://github.com/rickylabs/atelier-cockpit/issues/955). It reuses
the existing native workspace, owned-tree and state publication primitives.
It does not tune screen patterns or build another receipt-history poller.

The rest of the audit remains follow-up work under its existing owners: F3
GitHub wake delivery (Harness #783), F4 attachment ownership (Harness #784), F5
native evidence watch wakeups (Harness #714), F6 device/stage correlation
(Cockpit #957), F7 operation-scoped phone receipt updates (mobile #521), and F8
native startup/auth evidence (Harness #785). Existing native GitHub wake support
is retained; these audit entries are not all claims of still-missing code.
In particular native negative-identity indexing/replay in [#135](https://github.com/rickylabs/orchid/issues/135)
is still required for identities that never had a canonical retained record.
An absent job or a failed read alone cannot prove such an assignment ended.
This change does not certify deployed freshness, phone acceptance, admission
capacity, or a historical success/failure outcome.

Validation uses `go test -race ./...`, `go vet ./...`, a dispatcher build and the
existing compiled guards. `scripts/check-native-run-terminal-mutations.py`
requires assertion-red then restored-green for every new regression, including
wire compatibility, immutable replay, durability, identity races, final native
read conflicts, bounded action consumption and default dry-run behavior.
