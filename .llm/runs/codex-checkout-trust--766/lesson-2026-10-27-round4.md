# Lesson — 2026-10-27 — adoption and cleanup authority

A reported working folder is enough to retain a live agent's trust; recording it as Job.Workdir also authorizes deletion. Validate that stronger authority at adoption, before it enters durable state (cmd/divybot/main.go:2288; cmd/divybot/checkout_storage.go:54).

Require a canonical matching issue checkout and an exact direct parent from the adopting host's validated roots. A prefix or nested path is insufficient. Keep raw fleet trust retention independent of the recorded cleanup path.

Test the stored state and the deletion effect together. Here both foreign and nested paths must go assertion-red when the parent guard is removed, while valid disk/legacy root controls stay green (cmd/divybot/checkout_storage_test.go:56; .github/scripts/check-codex-checkout-trust-mutants.py:36).

Serialize long image suites and mutation builders to avoid unrelated fixture contention. Require explicit package passes, expected mutation counts and source-restoration markers. Execution evidence does not replace coordinator review. No architectural debt was accepted in this repair.
