# Provider/model budget producer

The optional `provider_budgets` config block adds public per-model decisions and
launch refusals to the existing governor transport snapshot. Policies and source
bindings are private operator data. No model list, multiplier, price, limit or
expensive-model default is embedded in the dispatcher.

This first producer has **no paid admission path**. A configured policy returns
`budget-reached` when its zero cap or fresh exact billing row proves exhaustion;
otherwise it returns `budget-unavailable`. A below-limit measurement, an included
billing discount, a cost estimate or `expensive: false` cannot grant admission.
Paid admission requires a separately verified provider-enforced full-run maximum,
a durable atomic reservation and a settlement adapter. None is implemented or
claimed by this PR. Running workers are not stopped by this check.

## Private configuration

Omitting `provider_budgets` retains existing behavior. An explicit block must be
an object with exactly these fields; null, duplicate JSON keys, unknown fields
and missing fields are rejected by runtime loading and config diagnostics.

| Field | Meaning |
| --- | --- |
| `source_file` | Clean absolute pathname of the existing collector checkpoint; root, aliases and control characters are invalid. |
| `source_hash` | Exact collector source-scope digest, written as lowercase SHA-256 hex. This binds the configured source identity; it is not the file content digest. |
| `source_owner_uid` | Required expected owner of the private checkpoint. |
| `max_age` | Positive Go duration bounding the authority of an observation. |
| `models` | Nonempty bounded array of exact provider/model policies. |

Each policy contains exactly these fields, including explicitly null quantities
when unavailable:

| Field | Meaning |
| --- | --- |
| `provider` | Exact provider ID, separate from the model. |
| `model` | Exact provider-native model ID, including any native vendor/model suffix. Its own provider prefix is rejected, never stripped. Case and a contract-valid leading tilde are preserved. |
| `account_ref` | Collector's opaque account reference; never published by Orchid. |
| `unit` | One of `premium_requests`, `ai_credits`, `usd`, `unknown`. Units are never blended or converted. |
| `period` | Exact UTC `day` or `month`. |
| `limit` | Canonical nonnegative decimal string for a known unit; null for `unknown`. |
| `included_entitlement` | Operator entitlement data, nullable. Consumed included billing quantity is not an entitlement. |
| `max_overage` | Operator overage cap, nullable. Already-reported net quantity above this cap proves exhaustion. |
| `expensive` | Required boolean supplied by the operator; false does not exempt the policy from its admission proof. |

Quantities use exact fixed-point arithmetic with at most nine integral and nine
fractional digits. Scientific notation, negative values, leading zeros and
trailing fractional zeros are invalid. An unknown-unit policy has null limit,
entitlement and overage quantities. Duplicate exact provider/model pairs fail
validation, even if other policy fields differ.

A provider represented in `models` is covered: its unlisted model or an unresolved
OpenCode identity refuses as `budget-unavailable`. A different provider does not
inherit that policy. Native Codex/Claude/AGY paths keep their existing quota and
capacity rules. Both `opencode` and the legacy `opencode-run` entry use the check;
`codex-run` remains a native Codex entry.

## Source and authority

The producer reads the existing collector's private `sourceHash` plus schema-2
`snapshot` checkpoint. It does not read credentials or call a provider API. It
opens without following symlinks or blocking on a FIFO, checks regular-file
ownership and exact owner-only permissions, bounds the read, and rejects changes
to the opened file or its pathname identity during the read. Missing, malformed,
partial, oversized, replaced or incorrectly scoped data cannot open admission.

The billing subset requires the expected source hash, envelope/provider schema
versions and payload inventories. Native account, pricing, history and coverage
payloads remain owned by their existing readers and cannot authorize this
producer. The producer validates every meter's closed field inventory and only
uses a unique exact provider, native model, account, unit and current UTC period
match. Provider aggregates, aliases, family matches and local cost history are
not substitutes.

Reported exhaustion requires a fresh canonical observation in a known
`github-billing` row for `github-copilot` premium requests or AI credits, with
consistent gross/included/net quantities and USD splits. Future clocks, expired
observations, ambiguous duplicates, invalid optional price or settlement clocks,
and mismatched current-period spans yield `budget-unavailable`. A local explicit
zero cap yields `budget-reached` even when measurement is unavailable. Price
rows and USD/history estimates never grant admission or prove a request budget.

GitHub measurement remains UNKNOWN with `no-plan-read-credential` until the
operator supplies `GITHUB_COPILOT_PLAN_READ_TOKEN` to the collector's configured
private credential file. The classic GitHub CLI token and OpenCode OAuth token
are not substitutes for the required Plan-read user credential. Even a valid
billing credential supplies measurement, not a full-run bound or settlement
proof. The disabled Copilot pool remains separately disabled.

## Reader and launch integration

The existing private transport-availability publisher adds optional
`providerBudgets`. Every row has exactly the six fields published in
`@rickylabs/harness-contracts` 0.33.1:

`provider`, `model`, `observedAt`, `validUntil`, `available`, `reason`.

Rows are sorted by the exact provider/model pair. Both clocks match the parent
snapshot; all decisions in this first producer have `available: false` and a
closed `budget-reached` or `budget-unavailable` reason. No policy quantities,
unit, account reference, source pathname, expensive flag or raw errors cross
this boundary. An invalid policy removes the old snapshot instead of leaving a
stale decision file. Provider seat pools and native quota rows keep their shape.

The same evaluator runs in matrix admission before host selection, durable
launching publication and dispatch, and in direct spawn before authentication
or workspace effects. Retry and preflight share the matrix check. Existing
closed launch-refusal readers already support the two budget reason codes;
this change adds no teardown cause.

## Required later gate

An available paid decision must wait for real proof that covers retries, children,
continuation and credential scope. Its reservation must serialize concurrent
admission, persist before effects and retain liability through ambiguous launches,
restarts, delayed billing and period resets. Polling, done state and missing usage
rows cannot free liability; settlement requires authoritative exact charge proof.
The current general dispatcher state loader is not a money ledger. Synthetic
controls do not establish any of these live enforcement guarantees.

Deployment, runtime policy edits and a real dispatch remain coordinator-owned.
