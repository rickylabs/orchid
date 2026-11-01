# Resume GitHub supervision wake work

Refs rickylabs/harness#783.

Baseline: `8074c8204293257a9c751ef6c29f8e12e0dc4937`. Operator scope selects
Orchid for implementation and requires a reference without a closing keyword.
PR 138 carries the change and the coordinator-requested repair of Opus findings
on `891cb20`; capacity work in PR 137 remains separate.

The signed receiver filters to tracked issue/source subjects, eligible source
commands, bot-authored PRs and exact owned branches. It reads immutable scope
without per-event GitHub queries or mutable PR/tracker reads. Unknown check
ownership stays on repair. A producer broker feeds the existing tick owner
through an unbuffered channel: first-pending GitHub debounce is 250 ms, minimum
handoff spacing is 30 seconds, fleet observations remain immediate, and the
five-minute repair tick remains. Main and capacity source are unchanged.

Docs distinguish source-reader ETags/read budgets/core-reset pause from absent
global PR/inbox/sweep HTTP backoff. Previous sub-millisecond fixture samples
are superseded; the pacing defaults define current behavior.

The same independent cross-vendor supervisor passed the design, implementation,
exact-head controls and substantive review repair. Round five also passed the final factory/debounce test delta.
Sign-off commits belong to that supervisor. Fresh baseline default-burst and
unowned-CI controls compile RED on the reviewed head and GREEN on the repair.
The accelerated signed burst records two pickups with pacing and 60 when
explicitly unpaced. Debounce, delayed-owner, no-starvation and immediate-fleet
controls use the production factory. Durable replay still admits one fixture
launch; authoritative PR reads still require the current exact head.

Local vet/build, full plain/race, affected race, mutation controls, configured
guards and privileged kernel checks: all exited zero. The exact isolated
snapshot contains 239 Go files. See [the worklog](worklog.md) for actual output
and counts; private logs stay external. No deployment or protected run changes.

Deployed native command-to-task-start and exact-head READY-to-pickup remain
UNPROVEN without verified operator ingress/runtime access. Fixture pickup and
launch callbacks do not substitute for native start, and merge is not pickup.
Read [the operator and extension guide](../../github-supervision.md) before
extending the native-event predicate or broker. Webhooks never establish
completion, current head, grants or admission.


Latest coordinator correction: the broker's private `run` method collided with
`Coord.run` in the name-keyed AST inventory. Its method and single caller are
renamed to `forwardWakes`, with identical body and unchanged inventory/test.
The previous head fails 12/20 repetitions; the fix passes 20/20 normally and
20/20 with race instrumentation. Current vet/build pass; final full suites:
plain and race both exit 0; the same inventory test also passes 20/20 on an isolated source merge of current main. See the worklog for exact outputs. This is a
same-supervisor mechanical slice, without reopening the completed feature
evaluation loop. Existing capacity ownership and native timing limits remain.
