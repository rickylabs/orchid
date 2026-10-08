# Concurrency and advisory usage

The governor preserves a physical per-account max_active cap, alongside each host's capacity and the independent explicit AGY/OpenCode provider seats. Subscription usage percentages do not reduce concurrency, pause admission, select a fallback, or alter an exact owner choice. Unknown or stale usage is not failed entitlement.

The existing sampler reads Claude statusline and Codex token_count once per configured source. Both five-hour and weekly windows retain source times and resets. Fresh complete readings at 90 percent, including 100 percent, warn. The legacy ceiling, slack, minimum and burn-window config remains parseable for compatibility but has no quota admission effect. enabled controls periodic sampling; provider_limits enables that same sampling loop even when enabled is false.

```json
"governor": {
  "enabled": true,
  "max_active": 16,
  "sample_interval": "90s"
}
```

max_active controls physical concurrency. sample_interval controls the existing native sampling loop. Target priority still orders admission independently of usage.

Only an actual verified provider quota/payment/rate refusal supplies quota blocking evidence. Reset time or refreshed metadata cannot clear it; a strictly later matching verified inference must do so. See [provider limits](provider-limits.md) for private source configuration, named OpenRouter own-key caps, native proof and durable ledger recovery. Authentication, grant authority, physical capacity and evaluator independence retain their checks. Source merge does not authorize live configuration or restart.
