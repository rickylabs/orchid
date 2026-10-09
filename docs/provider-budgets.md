# Legacy provider budget evidence

The optional `provider_budgets` block remains accepted for compatibility with
existing operator configuration. Its historical billing/source helpers still
validate their closed private schema, but their decisions no longer authorize
or refuse launches and are no longer published as availability rows. In particular,
zero caps, missing billing, reported budget exhaustion, included entitlement and
estimates cannot force a quota fallback or reduce physical concurrency.

[Provider limits](provider-limits.md) owns current advisory meters and actual
provider-refusal evidence. Independent physical seats, authentication, owner grants
and evaluator independence retain their checks. A successful metadata read is
neither a successful inference nor a paid-eligibility claim. No live settings or
service restart follows from this source change.
