# Round 2 compatibility and debt

The existing native goal API conditional-update limitation is unchanged; see debt.md. No new service, transport, polling loop or application-read effect is introduced.

Existing retry-exhausted records can infer that the native goal was blocked. Existing unproven automatic-resume records without a native status/update snapshot conservatively require native turn progress before release. This avoids treating the daemon's own stale active write as a later external resume. New records carry the native snapshot; lifetime attempt and failed-turn notice history remain durable after release.

A failed state write keeps recovery ownership. If its observation checkpoint succeeded before release failed, the next unchanged observation may wait for the existing safety backstop. The independent reviewer accepted this fail-safe tradeoff.
