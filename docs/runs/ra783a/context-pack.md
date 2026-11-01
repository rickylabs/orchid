# Resume GitHub supervision wake work

Refs rickylabs/harness#783.

Baseline: `8074c8204293257a9c751ef6c29f8e12e0dc4937`. The assignment's explicit
operator scope selects Orchid as the implementation repository and requires a
reference to the Harness issue without a closing keyword.

The receiver is started with the observation producers and sends only a dirty
bit to the channel already consumed by the serialized dispatcher loop. Main
supervision, capacity and source/PR state authority remain unchanged. The
operator guide describes private endpoint setup, subscribed GitHub events,
bounded replay suppression, repair and stage measurement.

The independent cross-vendor supervisor passed the design, returned FAIL_FIX
for an umask-sensitive fixture, and then passed the corrected implementation
and final READY/measurement update in the same session. Both commits are owned
by that supervisor. Transport and durable-source controls run without live
GitHub, Herdr or inference effects.

Vet, build, final full race, focused race, repeated timing controls, all 16 new
mutations, configured source/owner/trust/recovery mutations, privileged kernel
checks, leak scan, whitespace and file-cap checks exit zero. The 233 Go files
in the final verification snapshot match the source bytes. See the worklog for
actual output and counts. Main and capacity accounting are untouched, and the
upstream baseline stayed unchanged during reconciliation.

Controlled twenty-run source pickup is p50 0.224 ms, with subsequent fixture
launch-callback work p50 9.345 ms. Exact-head READY pickup is p50 0.260 ms, with
PR reader work p50 1.618 ms. Deployed native start and READY pickup remain
unproven until operator ingress setup and rollout; the fixture callback and
the audit's merge endpoint do not substitute for those measurements.

For the next contributor, read [the operator and extension guide](../../github-supervision.md)
and [the checkpoint](worklog.md), then extend the existing invalidation/reader
seam. A webhook payload cannot establish completion, current head, source grant
or admission, even when authenticated.
