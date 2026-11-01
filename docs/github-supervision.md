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
bindings are also acknowledged without a wake. Active bindings remain observable
when a target is disabled or removed; this does not authorize new work there.

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

The receiver shares the existing buffered wake channel with fleet observations.
A burst leaves one pending tick, and an event received during tick work leaves
a wake for the next tick. HTTP handlers never run ticks concurrently. The tick
reads all affected bindings using the existing pagination, ETags, request budgets
and rate-limit backoff. A failed GitHub read remains unknown; it cannot establish
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
the exact wake channel consumed by `Coord.run`, with no hosts. Source controls
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

To extend the event vocabulary, start with this document, then update
`githubSupervisionEvent` and its table test. Keep transport separate from state
authority. Capacity accounting is owned separately and is unchanged here.
