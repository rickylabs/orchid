# Provider limit evidence

The optional `provider_limits` block enables an owner-only normalized source for
Cockpit's `COCKPIT_PROVIDER_LIMITS_STATE_FILE`. Harness owns the public decoder
(`readProviderLimitSnapshot`) and file reader (`harness-telemetry provider-limits
--source`). Cockpit542 already accepts this version-one wire. Source merge does
not configure a running process, publish a package, or prove installed mobile UI.

Quota percentages and paid budget policy no longer reduce governor concurrency,
remove default routes, or veto exact owner choices. Physical concurrency,
provider seats, authentication, grant authority and evaluator independence keep
their checks. Fresh complete90 percent and100 percent readings are warnings.
Legacy budget config remains parseable; its refusal rows are no longer published
as availability, and legacy budget helpers cannot veto dispatch.

## Configuration

A block requires all five fields; unknown, null or duplicate JSON fields fail.
Paths below are illustrative operator paths, outside every checkout:

```json
{
  "provider_limits": {
    "private_root": "/var/lib/orchid/provider-limits",
    "snapshot_file": "/var/lib/cockpit/evidence/provider-limits.json",
    "accounts": [
      {
        "provider": "codex",
        "account_ref": "aref:v1:codex:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "source_host": "fixture-source-host",
        "launch_models": ["codex/fixture"]
      }
    ],
    "openrouter_keys": [
      {
        "name": "work-key",
        "credential_env": "OPERATOR_OPENROUTER_KEY",
        "launch_models": ["openrouter/vendor/model"]
      }
    ],
    "observed_routes": ["codex/fixture", "openrouter/vendor/model", "opencode-go/fixture"]
  }
}
```

Use an existing authorized private credential environment supplied by the secret
store, never a key literal in config. Persist and emit only the public key alias;
no credential value, prefix, hash, environment name, native session or host appears
in the public snapshot or command diagnostics. Account references are explicit
operator attestations. The existing sampler exposes one active account per vendor; the config explicitly selects its private source_host, and only that host is sampled for the bound account. Multiple accounts cannot claim the same provider route in v1. Other hosts/accounts do not supply this account's readings; missing selected-host evidence is unknown. The operator must attest that launch_models uses this account throughout dispatch, or leave it empty. Native account identity is never inferred from a token. Both windows come from the same selected reading.

`launch_models` proves exact provider-qualified credential binding. Empty means
unknown. Two accounts/keys cannot claim the same route. Key environment bindings are distinct, including transient rejection when distinct environment names resolve to the same credential. No value representation is retained. `observed_routes` reserves finite observation scopes without
claiming an account/key binding. Newly selected owner routes reserve a durable
scope before dispatch, without using their usage percentage as admission policy.
An OpenRouter route without a proven named key cannot emit a key-wide refusal.

The private ledger directory must already exist, mode0700, dispatcher UID, outside
a worktree, without symlink parents. The snapshot's parent is separately private;
the file is atomically replaced at0600 and transferred through the existing
configured receipt-owner boundary if needed. Set the environment variable on the
Cockpit API process to this snapshot file. Its UID must own the file. HTTP clients
cannot choose the file. Changing either process's live settings and restarting it
requires separate operator GO; this task does neither.

## Collection and freshness

Reuse existing native Claude statusline and Codex token-count reads. Both5h and
weekly percentages retain their source file timestamp and reset, independently.
Missing percentages/windows, malformed readings and unsupported window lengths
are unknown; repeated publication does not refresh them. Collection runs with
`provider_limits` enabled even when governor pacing is disabled. Go, AGY, Copilot
and Ollama Cloud usage is explicit unknown without a verified machine source.
No physical seat becomes usage or quota entitlement.

Each configured OpenRouter key makes a bounded request to the fixed HTTPS
[own-key endpoint](https://openrouter.ai/docs/api/api-reference/api-keys/get-current-api-key).
No redirects, account-credit requests or alternate URL are allowed. Requests have
a15-second deadline and64KiB response cap, within a30-second collection budget.
A capped key emits `used = limit - limit_remaining`; account balance and all-time
usage cannot supply its percentage. An uncapped key retains its actual reported
amount but no invented cap or percentage. Reset policy selects the window; absent
absolute reset time stays null. HTTP metadata402/429, missing secrets and failed
reads emit unknown and never record success or refusal.

## Actual inference outcomes

Bound native OpenCode export establishes the whole prompt digest, exact route,
selected session, parent, source clocks and unchanged pane occupant before an
outcome can be recorded. Structured API402,429 and GoUsageLimitError are normalized
to payment_required, rate_limited and quota_exhausted. Raw provider text, metadata,
response bodies and retry headers are never public. Reported retry/reset times
remain separate from clearance. A completed nonempty native assistant response
from that same bound observation is a success; exit status, PR state, metadata
success, Herdr idle/done or unverified assistant text cannot supply it.

The public outcome `observedAt` is the verified native event timestamp, preserved unchanged even when collection is delayed. Receipt time does not become fresh native proof. Native clock domain, event identity and ingestion sequence remain private. A repeated or older native event cannot overwrite a newer outcome in the same clock. A delayed earlier refusal cannot supersede a verified later successful inference: public and dispatcher ordering both follow source chronology. Cross-clock success cannot clear a refusal without proven ordering. Strictly later matching success may clear a global refusal; another account, key or model cannot. Equal native instants retain refusal. Reset, missing source, aging, lower usage and public source deletion do not clear. Unsupported native outcome adapters stay unknown; there is no operator-import success path.

Binding retirement is explicit: the private ledger retains a bounded registry of previously emitted meter identities. A removed or replaced identity is republished as an unknown row with empty launchModels, so Cockpit drops its old route binding through restart rather than mistaking omission for a source gap. Its actual scoped refusal remains visible in Costs. This is neither an inference success nor quota clearance. Registry overflow refuses storage; identities and refusals are never silently evicted.

Every publication includes the full bounded outcome ledger, including verified
success tombstones. A new outcome republishes cached meters immediately without
polling keys again or refreshing their source times. Restart reloads history from
the separate private ledger; deleting the public source cannot remove it.

## Bounds, recovery and validation

The public snapshot has schemaVersion1, generatedAt, meters and outcomes only.
Both collections are bounded1024 rows and the document is at most4MiB. No private
wrapper, extra fields, raw errors or secret-like labels cross the seam. Private
files use no-follow regular-file same-UID checks and atomic synced temporary
replacement. One exclusive writer lease prevents another dispatcher or compactor
from overwriting current history. Ledger failure refuses new dispatch with the
existing persistence diagnostic; it never truncates active refusals into availability.

Scopes reserve bounded ledger storage before admission. Existing refusals never
evict. Offline `divybot provider-limits compact --config <private config>` archives
only inactive verified successes, preserving bounded permanent replay fences. A success that clears a retained global refusal also remains active evidence and cannot be archived. Configured launch bindings remain active even when absent from observed_routes. Inactive reservations with no outcome may be released because they contain no inference history.
It refuses a live writer lease. Retired scopes cannot be silently rebound; inspect
and deliberately retain their success watermark in an operator-managed migration.
No timer automatically clears or compacts a refusal. The private retired archive
has its own4096-scope bound and refuses overflow rather than discard evidence.

Run the full configured repository gates. Producer tests include native-shaped
proof, ownership races, metadata errors/redirects, key caps, independent windows,
source time, durable restart/deletion, scope rebinding, replay, clock domains,
compaction and storage failures. Isolated compatibility proof uses the actual
produced snapshot against Harness and Cockpit decoders, Costs/catalog and final
admission. Mutation tests must make the percentage veto and premature-clearance
regressions fail. Live deployment, live subscriptions and installed APK rendering
remain unproven until their separate activation evidence exists.
