# GitHub supervision events

GitHub webhooks wake the existing serialized dispatcher tick independently of
Herdr activity. The HTTP receiver only invalidates observations: it never treats
a payload as an issue state, grant, review verdict, head commit or launch request.
The tick still reads GitHub and applies its existing admission and closure fences.

## Operator setup

Set both `ORCHID_GITHUB_WEBHOOK_SOCKET` and
`ORCHID_GITHUB_WEBHOOK_SECRET_FILE` in the coordinator's private service
environment. The first names an unused Unix socket in a canonical, owner-only
directory. The second names a regular, owner-only secret file in a canonical,
owner-only directory. Directory permissions must be `0700`, the secret file
`0600`. The secret must contain between 16 and 4096 bytes. Those exact bytes,
including any newline, are the signing secret.

The receiver binds an HTTP Unix socket with `0600` permissions. Configure an
operator-managed TLS reverse proxy running with access to that socket, and use
its HTTPS URL as the repository or organization webhook destination. Select
JSON payloads and configure the identical secret on GitHub. Do not put endpoint
addresses or secret material in repository files. No public TCP listener or
additional scheduler is started by Orchid.

Subscribe to these native events:

- `issue_comment` and `issues`: source commands, READY comments, assignment,
  labels, issue closure and reopening.
- `pull_request`, `pull_request_review`, `pull_request_review_comment`, and
  `pull_request_review_thread`: new heads, readiness, reviews and discussion.
- `check_run`, `check_suite`, and `status`: check and commit-status changes,
  including events without an associated pull request in their payload.

GitHub's signed `ping` is acknowledged without scheduling work. Unsupported
events and repositories outside the inbox, effective target list and active job
bindings are acknowledged without a wake. Within those repositories, the filter
accepts tracked inbox/source issues, pending source grants, newly launch-labelled
inbox issues, configured bot assignments, and new `/swarm` comments by the owner
or an exact named grant. Ordinary comments on untracked issues do not wake it.

PR events must name the configured bot as PR author, or identify an active job's
exact branch in the same repository. PR conversation comments must identify the
bot as the PR author or match a tracked issue/source binding. Checks/statuses
must identify an owned branch or an owned linked PR. Empty PR-link arrays work
when a check's suite identifies the owned branch; otherwise ownership is unknown
and repair handles it. No GitHub ownership lookup runs per webhook. HTTP threads
read immutable launch scope, not the mutable PR pointer or relay tracker. Active
bindings remain observable when a target is removed; this does not authorize new
work there. Grants installed after an otherwise ignored member comment are still
discovered by repair.

The receiver verifies `X-Hub-Signature-256` against the raw request bytes using
HMAC-SHA256 and constant-time comparison before parsing or enqueueing. It follows
GitHub's [delivery validation](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries),
[event contract](https://docs.github.com/en/webhooks/webhook-events-and-payloads),
and [webhook best practices](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks).

Both variables absent disables the receiver. Partial or invalid configuration,
unsafe permissions, endpoint collisions, or a listener failure produce a fixed
unavailable reason; supervision and repair continue. Existing endpoints are
never replaced, including a stale socket. An operator must verify ownership and
remove a stale endpoint before restarting. Shutdown removes only the socket
inode created by this receiver. Secret rotation requires a coordinator restart.

## Queue, replay and repair

The receiver and fleet observations feed a producer-side broker; the existing
serialized loop remains the only tick owner. Each input has one pending bit.
GitHub events collect for a bounded 250 ms window, anchored at the first pending
event so a sustained stream cannot postpone it indefinitely. GitHub-induced
receive handoffs are spaced by at least 30 seconds after the previous handoff.
The output is unbuffered: a busy owner's cooldown starts when it actually takes
the wake, not when a sender previously queued it. Events during work leave one
pending invalidation for the next eligible handoff. Native fleet invalidations
retain their immediate path and can reconcile an already-pending GitHub change.
These limits bound GitHub-induced ticks, not all native fleet activity.

The receiver issues no GitHub reads. Existing source-comment discovery and
binding checks retain their pagination, conditional ETags, per-tick read budgets,
and core-remaining/reset pause. Those controls belong to the source reader.
PR, merge-sweep and inbox reads do not share a global `Retry-After`/403/429 backoff
policy. This change adds no such policy, and pacing is not an API quota guarantee.
A failed authoritative read remains unknown; a webhook payload cannot establish
that a run is closed.

The in-memory cache retains at most 2048 deliveries for 24 hours. It suppresses
both repeated delivery IDs and identical signed bodies with changed IDs. The
cache stores digests, not payloads. Cache expiry, eviction or process restart may
cause another read. The existing durable repository/issue/comment identity,
grant claim and launch receipt still prevent a second launch effect. Reordered
events only trigger fresh reads, so a delayed close or old-head check cannot
overwrite current authority.

There are at most 32 accepted connections, a 2 MiB request body bound, an 8 KiB
header bound, and bounded read/write/idle deadlines. Oversized requests return
413 and are recovered through reconciliation. The five-minute
`fleetRecoveryEvery` tick remains the bounded repair for missed events, receiver
outages and cache loss. `poll_interval` does not set supervision cadence.
Webhook delivery is opt-in: source installation alone does not configure GitHub
or the reverse proxy, and a successful HTTP acknowledgment is not a launch receipt.

## Measurement and extension

The quiet-fleet regression exercises the production producer constructor and
the exact wake channel consumed by `Coord.run`, with no hosts. A production-default
burst control allows at most one pickup within the minimum-spacing window.
Accelerated constructor policies test repeated bursts with a continuously ready
consumer and an explicitly unpaced negative control. A delayed-owner control
proves spacing is measured from handoff, and a sustained-input control proves the
debounce cannot starve delivery. Source controls
then run the existing source reader, grant claim and matrix admission with a
counted launch callback. Replay and a saved-state reload must leave that count
at one. Closure, failed reads and a missed-event reader pass use the same
production readers. PR controls wake the production PR reader and require the
current head even when the delivered event names an older head.

Controlled measurements keep event-to-pickup and reconciliation work separate.
These fixture times are not production native task-start measurements. For a
deployment comparison, privately correlate the GitHub comment timestamp, receipt
time, wake consumption, authoritative read, admission, and the matching native
task-start event. For READY, record the comment's exact SHA and require that same
head at supervision pickup. Report queue wait and tick work independently; do
not substitute a merge timestamp for pickup or file modification time for a
native event. Keep cross-clock uncertainty explicit and native identifiers out
of public reports.

Earlier sub-millisecond fixture pickup samples described the unpaced receiver
before this review repair; they are not current timing claims. The default
constructor now includes the debounce and minimum handoff spacing above. Reader
controls may use shorter injected timing policies and are labelled as controls.
Deployed native-start and exact-head pickup measurements remain separate work.
The audit's approximately 170-second native start sample and 923-second
READY-to-merge sample have different endpoints and cannot establish a before/after
improvement from these controls.

To extend the event vocabulary, start with this document, then update
`githubSupervisionEvent` and its table test. Keep transport separate from state
authority. Capacity accounting is owned separately and is unchanged here.
