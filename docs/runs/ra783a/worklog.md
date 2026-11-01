# GitHub supervision wake delivery

Design checkpoint: Plan & Design — READY FOR REVIEW, reviewed PASS_PLAN.

1. Public surface: opt-in authenticated native GitHub webhooks invalidate the
   existing supervision owner through its buffered wake channel.
2. Vocabulary: delivery is a signed HTTP request; wake is an invalidation;
   reconciliation is an authoritative dispatcher tick; neither delivery nor
   wake is permission to launch.
3. Ports: HTTP handler, repository scope predicate, clock and wake callback.
   Existing private-file/socket helpers enforce endpoint ownership. Existing
   source, grant, matrix and PR readers retain authority.
4. Constants: body/header/connection bounds, delivery-cache bound and lifetime,
   HTTP deadlines and event vocabulary. The existing repair interval stays.
5. Commit slices: first ship the receiver, production wiring, operator guide
   and focused native-event controls; then record complete gate evidence and
   independent supervisor sign-off. Each slice updates these run records.
6. Deferred scope: live ingress/proxy/webhook setup and deployed native timing
   proof. Capacity accounting belongs to a separate change and is untouched.
7. Contributor path: [GitHub supervision events](../../github-supervision.md).

Independent design supervision returned PASS_PLAN before source edits. The
quiet-fleet production wiring control compiled and failed at the missing
receiver on the original dispatcher, then passed with the implementation.
Replaying the same signed body under another delivery header queues no extra
wake. The source control uses a real durable grant and launch receipt, with a
counted fixture launch callback, to prove one effect across reconciliation and
saved-state reload. That callback is not a native runtime start measurement.

Further controls cover authenticated event vocabulary, scope and HTTP bounds;
concurrent replay; cache expiry and eviction; burst coalescing and invalidation
during work; unsafe endpoint/secret configuration; connection cancellation;
reordered closure, unknown reads and missed-event repair; PR reviews, checks and
READY-triggered reads of the authoritative current head.

Private run evidence retains raw timestamps and test output. Public evidence
contains no operator endpoint, native identity or credential. No deployment,
capacity change, protected run rewrite or generated compatibility copy is part
of this change.

Implementation review: the same independent supervisor returned FAIL_FIX for a
fixture permission assumption, then PASS after the explicit mode correction.
Final test and measurement review also returned PASS. Both delivery commits
are supervisor-owned, with no source edit by the sign-off step.

Final local verification:

| Gate | Exit and evidence |
| --- | --- |
| `go vet ./...` | 0, no diagnostics |
| `go build ./cmd/divybot` | 0, no diagnostics |
| `go test -race -timeout 20m ./... -count=1` | 0, `ok orch/cmd/divybot 309.484s` |
| Final focused GitHub race controls | 0 |
| Twenty timing-control repetitions | 0 |
| New compiled ingress mutations | 0, all 16 assertion-RED; restored controls GREEN |
| Configured source mutations | 0, all 50 assertion-RED; exact source restoration |
| Configured owner endpoint mutations | 0, all 17 assertion-RED with privileged checks |
| Configured checkout-trust mutations | 0, all 36 assertion-RED; restored control GREEN |
| Configured recovery mutations | 0, all 37 assertion-RED and 37 restored controls GREEN |
| Privileged endpoint and foreign-peer kernel checks | 0 |
| Added-line leak scan, whitespace and file cap | 0 |

All 233 Go files in the final isolated verification snapshot match the working
source bytes. Setup failures and abandoned test copies are excluded from these
verdicts. The original dispatcher full plain-test baseline also exited zero;
the quiet-fleet production wake control compiled RED there and GREEN here.

The [measurement guide](../../github-supervision.md) separates controlled
ingress/pickup from source/PR work and from deployed native task-start evidence.
Accepted debt: webhook/proxy setup and native before/after correlation remain
deployment work. No production latency improvement is claimed by fixture times.
