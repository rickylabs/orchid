# GitHub supervision wake delivery

Plan & Design — READY FOR REVIEW.

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

Implementation review: same independent supervisor returned FAIL_FIX for a
fixture permission assumption, then PASS after the explicit mode correction.
Vet, build and focused race checks exit zero. The original dispatcher full
Go test baseline also exits zero. Full changed-suite and mutation results are
still being collected; this checkpoint does not claim those pending gates.
