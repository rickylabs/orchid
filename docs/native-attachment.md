# Codex launch attachment and turn acceptance

This page describes the boundary between Orchid's Remote Control Codex launch and the goal turn. It
covers how Orchid knows the prepared thread is attached to the intended TUI consumer, and how it
accepts the one goal turn. The code entry point is `cmd/divybot/codex_launch_boundary.go`.

## Authorities, kept separate

Four sources are involved, and each proves a different thing:

| Source | Proves | Does not prove |
| --- | --- | --- |
| Private daemon-log resume record (`remote_control_identity.go`) | One pinned `codex-tui` connection resumed the thread Orchid created, on the canonical daemon, and then proceeded. | That the record is vendor-native. Its authority is labelled `private-daemon-log`. |
| Herdr structured occupant | The exact pane, workspace, cwd and session occupant. Its `interactive_ready` and idle/done gate typed input. | Thread attachment. Readiness is screen-derived. |
| Kernel TUI process start and argv fingerprint | Continuity of the launched process, and safe cleanup. | Issue ancestry or task success. |
| Official app-server signals on the acceptance connection | Ready, turn, item, approval and exit events for the bound thread in this generation. | Attachment, or acceptance on their own. The readback decides. |

These sources are not a single vendor-native lifecycle proof.

## Upstream capability gaps

The inventory was taken from the Codex CLI 0.166.0 app-server v2 schema and rechecked at 0.167.0
with `codex app-server generate-json-schema`.

- **`codex-app-server-v2-no-thread-subscriber-acknowledgment`.** No request lists the connections or
  clients subscribed to a thread, and no notification announces another client's `thread/resume` or its
  `clientInfo`. `thread/attachment/*` handles content attachments (identity keys), not clients, and
  `thread/loaded/list` returns thread ids only. Until upstream supplies such an acknowledgment, the
  private record stays and every attachment outcome is either `private-log-proven` or unknown.
- **`codex-tui-no-composer-ready-acknowledgment`.** The TUI exposes no programmatic composer-ready
  signal, so the Herdr readiness gate stays.
- **`codex-app-server-v2-no-running-only-resume`.** `thread/resume` cannot be made conditional on the
  thread already running.

To check a new Codex release, regenerate the schema and search `ClientRequest.json` and
`ServerNotification.json` for a subscriber or client acknowledgment. Replace the private record only
when one exists and is bound to the thread and the client connection.

## Typed attachment outcomes

Every refusal keeps the outward reason `remote-control-identity-unconfirmed`. The coordinator log
records the typed outcome as `attachment <outcome>`:

| Outcome | Cause |
| --- | --- |
| `private-log-proven` | The record is complete for the pinned client. |
| `pending` | No complete record yet; read once per second until the launch deadline. |
| `ambiguous` | More than one creator, more than one TUI connection, or a reused connection id. |
| `consumer-changed` | A foreign producer or client version, an occupant change, or another process. |
| `process-reused` | The same PID with a different kernel start or argv. |
| `unreadable` | The read failed or the client is unpinned. |
| `deadline` | The launch budget ended while pending. |

## Acceptance generation

Each acceptance owns one canonical connection, and that connection is the generation. In order:

1. The connection initializes with the official `optOutNotificationMethods`, which suppresses streaming
   deltas.
2. It verifies the binding and requires `thread/read` to report `idle`.
3. It rejoins the running thread with `thread/resume {threadId, excludeTurns: true}`. The response must
   still carry the exact prepared id, cwd, model, effort, `approvalPolicy: never` and full-access sandbox.
4. It requires `idle` again, and then reads the baseline page.
5. A durable `attempted` receipt and a fresh occupant and TUI-process check follow. A mismatch here
   means zero submits. The receive barrier then drains to observed quiescence, including frames
   received while earlier frames are reduced. Bound pre-effect activity refuses with zero submits.
6. The single submit runs once. Frames keep being consumed while it is in flight.
7. The exact-input readback, the fence read and the binding re-checks decide, as before.

A refused rejoin (`wake-capability-missing`) falls back to bounded polling. Approval server requests
are journalled and never answered, because a reply from Orchid could resolve a request shared with the
TUI. Closing the connection ends the subscription. The strict joined drain still owns every frame that
arrives after the fence.

Signals are classified, journalled with their class, and then applied:

- `ahead-of-ack`: received while the submit was in flight.
- `duplicate`: the same event seen again; no turn is counted.
- `foreign`: another thread on the shared daemon; ignored and not journalled.

Refusals are:

- `stale-generation`.
- `baseline-active`: a turn, active status or approval before the effect.
- `barrier-busy`: the pre-effect barrier still has received frames after reducing 1,024 frames.
  The private receipt and journal retain this reason; the outward refusal stays `pre-effect`.
- `contradiction`: a second new turn.
- `native-exit`: `thread/closed`, `notLoaded` or `systemError`.

Pending states are journalled as `pending-no-turn`, `pending-readback` and `pending-materializing`. A
user-item, turn, status or exit signal wakes one readback, after at most 32 queued frames are
coalesced. There are at most 64 wakes; after that, polling continues at the interval. A lost submit
acknowledgment is `submit-uncertain` even when native evidence arrived first. No path submits twice.

Quiescence means the consumed sequence caught up to the freshly observed receive counter. It is not
composer readiness or an atomic server-side idle/submit guarantee: a scheduling window remains after
the final counter observation and before submit begins. Duplicate and foreign traffic count toward
the barrier's exact frame budget; a busy barrier refuses rather than draining without bound.

### Accepted debt

Codex has no running-only rejoin. A thread unloaded between the idle read and the rejoin is therefore
cold-loaded by Orchid's connection. The cost is bounded: TUI absence is still caught by the pre-effect
and later binding checks and by the deadline, and nothing is ever re-sent.

The strict post-fence drain journals approval requests but omits resolution-notification history.
The cost is missing resolution evidence after the fence; acceptance is unchanged and no approval
reply is sent. This history gap does not supply readiness, attachment or completion proof.

## Stage evidence

Each launch logs one line: `issue #N: launch stages <stage>=<ms> ... total=<ms> <counter>=<n>`. Stages
run from `thread-prepared` through `committed`. Counters are `attachment-reads`, `page-reads`, `wakes`,
`signals` and `duplicate-signals`. The line carries no identifiers, paths, native text or usage.

`ORCHID_STAGE_BENCH=<n> go test ./cmd/divybot -run TestCodexAcceptanceStageBenchmark -v` records the
synthetic staged benchmark, comparing subscribed wakes with interval polling. It reports results and
asserts no timing.

Controls: `python3 scripts/check-launch-attachment-mutations.py`.
