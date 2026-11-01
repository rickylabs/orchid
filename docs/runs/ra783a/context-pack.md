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

The independent cross-vendor supervisor passed the design. Focused transport
and durable-source controls run without live GitHub, Herdr or inference effects.
Implementation supervision returned FAIL_FIX for an umask-sensitive fixture,
then PASS after an explicit permission correction and stronger replay controls.
Vet, build and focused race tests exited zero. Full configured gate results
follow in the delivery update; deployed native latency remains unproven.

For the next contributor, read [the operator and extension guide](../../github-supervision.md)
and [the checkpoint](worklog.md), then extend the existing invalidation/reader
seam. A webhook payload cannot establish completion, current head, source grant
or admission, even when authenticated.
