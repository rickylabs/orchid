# GitHub supervision wake delivery

Design checkpoint: Plan & Design — READY FOR REVIEW, reviewed PASS_PLAN.

1. Public surface: opt-in signed GitHub webhooks invalidate the existing
   supervision owner, with owned-subject filtering and bounded GitHub pacing.
2. Vocabulary: delivery is a signed request; wake is an invalidation; handoff
   is the owner's receive; reconciliation is an authoritative dispatcher tick.
   A delivery or wake never authorizes a launch.
3. Ports: HTTP handler, typed native-event predicate, clock and wake broker.
   Existing endpoint ownership, source, grant, matrix and PR readers retain
   authority. Only the existing serialized owner runs ticks.
4. Constants: 250 ms leading debounce, 30-second minimum GitHub handoff spacing,
   bounded connections/body/headers/deadlines/replay cache, unchanged repair.
5. Commit slices: receiver and wiring; exact-head reader controls; review
   repair for subject scope, pacing, burst/negative controls and truthful rate
   documentation. Each slice updates these run records and is supervised.
6. Deferred scope: live ingress/proxy setup and deployed native timing proof.
   A global API backoff policy is not implemented or claimed. Capacity
   accounting belongs to a separate change and is untouched.
7. Contributor path: [GitHub supervision events](../../github-supervision.md).

The first quiet-fleet production-constructor control compiled RED before the
receiver existed and GREEN after integration. Durable source-grant/receipt
controls admit one counted fixture launch across replay and saved-state reload;
that callback is not a native runtime start measurement. PR controls require the
authoritative current exact head when delayed events name an older one. Failed
reads retain unknown, and closure/missed-event controls use existing readers.

Opus review of `891cb20` identified repository-wide wake scope and the lack of
minimum tick spacing, plus an overbroad backoff claim. The repair filters native
subjects locally without external ownership reads or mutable PR/tracker access.
It coalesces two bounded producer bits through an unbuffered handoff: GitHub
has leading debounce and minimum spacing; native fleet observations stay
immediate. A sustained burst cannot reset the first debounce window forever.
Only the source reader has the existing ETag/budget/core-reset pause; PR,
merge-sweep and inbox reads have no global `Retry-After`/403/429 policy.

Fresh baseline controls on the reviewed head both compiled and failed at
assertions: an owned burst produced another tick inside the spacing window,
and unrelated CI in an allowed repository woke supervision. Both controls pass
with the repair. The accelerated signed-HTTP burst yields two pickups for 60
unique owned checks; explicitly disabling pacing yields 60, exceeding the same
bound. Separate controls cover production defaults, unrelated checks, debounce,
a busy receiver, sustained input, immediate fleet observations and cancellation.

The same independent cross-vendor session passed design, corrected one fixture
permission assumption, and passed implementation and exact-head controls. It
also passed the substantive Opus repair in round four. Round five also passed the final factory/debounce test delta.
The sign-off commits are supervisor-owned; the final step edits no source.

Final local verification of the review repair:

| Gate | Exit and evidence |
| --- | --- |
| `go vet ./...` | 0, no diagnostics |
| `go build ./cmd/divybot` | 0, no diagnostics |
| `go test -timeout 20m ./... -count=1` | Exit 0, `ok orch/cmd/divybot 339.323s` |
| `go test -race -timeout 20m ./... -count=1` | 0, `ok orch/cmd/divybot 245.173s` |
| Final affected race controls | 0, `ok orch/cmd/divybot 8.523s` |
| Review repair mutations | Exit 0, all nine compiled assertion-RED; restored controls GREEN |
| Configured source/owner/trust/recovery mutations | Exit 0, all 50 source / 17 owner / 36 trust / 37 recovery assertion-RED; restored controls GREEN |
| Privileged endpoint and foreign-peer kernel checks | 0 |
| Added-line leak scan, whitespace and file cap | Exit 0 |

One additional full-race attempt under concurrent compiler load failed before
an unchanged 100 ms readiness fixture created its call log (exit 1). The exact
unchanged test then passed 20 isolated race repetitions (exit 0); the final full
race rerun is recorded above. This failed attempt is retained in private evidence.

All 239 Go files in the isolated verification snapshot match the working
source bytes. Mutation gates use separate matching copies and restore exact
bytes. Setup failures are excluded from test verdicts. Main, capacity,
protected runs and generated files are unchanged.

Private evidence retains raw output; public records contain no operator
endpoint, native identity or credential. Earlier sub-millisecond pickup samples
predate pacing and are superseded. Deployed source-comment to native task-start
and exact-head READY to supervision pickup remain UNPROVEN. Accepted debt is
operator ingress setup and native before/after correlation, not a claim of
measured production improvement. See the [measurement guide](../../github-supervision.md).

Reconciliation: main advanced with the separate capacity change in PR 137.
Its changed files do not overlap this repair; capacity remains outside the PR.
