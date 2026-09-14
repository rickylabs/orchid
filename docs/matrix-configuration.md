# Build and validate matrix configuration

`divybot matrix build` prepares a new private configuration file and runs issue policy
preflight. `divybot matrix validate` checks an existing file before dispatch. Both exit
before coordinator construction: no queue polling, host access, credential sync, launch,
receipt reservation or GitHub write. They reuse the Go types and `briefDigest` in
`cmd/divybot/matrix.go` and the same pinned NetScript bridge used by dispatch.

## Template

The `matrix` block in [`divybot.example.json`](../cmd/divybot/divybot.example.json)
is deliberately placeholder-only. Replace placeholders in private operator configuration;
never commit the resolved file. Copying the example unchanged is not a working deployment.

```json
{
  "matrix": {
    "source": "<ABSOLUTE_CLEAN_NETSCRIPT_CHECKOUT>",
    "revision": "<FULL_NETSCRIPT_COMMIT>",
    "receipt_root": "<ABSOLUTE_PRIVATE_RECEIPT_DIRECTORY>",
    "target_revisions": {
      "<TARGET_OWNER>/<TARGET_REPOSITORY>": "<FULL_TARGET_COMMIT>"
    },
    "pins": {},
    "grants": []
  }
}
```

`source` must be a clean checkout, including no untracked files, with the first-party
runtime files and Deno available. `revision` is its exact lowercase 40-character HEAD.
Each configured target repository needs its own exact target commit; profiles and owner
worklog entries are read from that revision. `receipt_root` must already exist, mode 0700,
outside any Git worktree, with no symlink aliases. Config and output directories are private.

`pins` and `grants` may be empty. There is **no unconditional grant requirement** for an
ordinary matrix-selected route. The issue/profile must still supply valid tier and role.
Privileged tiers require matching authority, and a pin that deviates from the selected route
requires the existing trusted owner override. The builder creates neither authority nor
pin defaults. Existing pins, grants and source/target revision pins are preserved; mismatched
source pins are refused rather than silently advanced.

## Build the binary and a candidate

In the Orchid checkout containing these commands:

```sh
go build -trimpath -o "$PRIVATE/divybot" ./cmd/divybot
"$PRIVATE/divybot" matrix build \
  -config "$CONFIG" \
  -source "$NETSCRIPT_CHECKOUT" \
  -receipt-root "$RECEIPT_ROOT" \
  -issue "$INBOX_ISSUE_NUMBER" \
  -target "$TARGET_REPOSITORY" \
  -out "$PRIVATE/candidate.json"
```

All variables are operator-supplied placeholders. `$PRIVATE` and `$RECEIPT_ROOT` are existing
absolute private directories (0700) outside Git; `$CONFIG` is a private copy of the dispatcher
configuration. `$TARGET_REPOSITORY` must match a configured target. Build works with the legacy
configuration that has no matrix block; do not first insert literal template placeholders.
If preparing an existing matrix block, keep valid pins or deliberately remove only the pins
you intend to resolve again from the private input copy.

The command canonicalizes the checkout location, reads `git rev-parse HEAD`, verifies clean
source, and fills missing target revisions with the result of
`gh api repos/<TARGET_REPOSITORY>/commits/HEAD --jq .sha`. Existing target pins are retained.
It fetches the entire inbox issue using `gh issue view --json id,number,title,body`, reads its
profile at the target pin, then invokes the real bridge. GitHub access is read-only. Native
transport eligibility is hypothetical during this check; it is not a quota observation.

The candidate is created exclusively with mode 0600; an existing output file is never
overwritten. The complete configuration is preserved, including unrelated extension keys;
only its matrix block changes. Resolved paths, identities, pins and digests go to that private
file, never stdout. The original configuration, dispatcher state and running process stay
untouched. The operator reviews and applies the candidate separately.

## Build a brief-bound grant, when required

Add `-grant-input "$PRIVATE/grant-input.json"` to the build command. This input is a
`MatrixGrant` with `issue_id`, `repo` and `brief_digest` **omitted**; the builder fills those
three from the fetched issue and chosen configured target. A minimal privileged grant input is:

```json
{
  "tier": "<PRIVILEGED_WORKLOAD_TIER>",
  "role": "<DELEGATION_ROLE>",
  "authorization": {
    "authorizer": "<ALLOWED_AUTHORIZER>",
    "rationale": "<RECORDED_AUTHORIZATION_RATIONALE>"
  }
}
```

The actual allowed authorizers are `owner` and `milestone_coordinator`. Supply authority
already granted by that person; this command does not grant permission. For a deviating named
pin, configure `pins` privately and include `ownerMatrixOverride` in the grant input:

```json
{
  "pin": "<CONFIGURED_PIN_NAME>",
  "authorizer": "<OWNER_AUTHORIZER>",
  "rationale": "<RECORDED_OVERRIDE_RATIONALE>",
  "worklogPath": "<REPOSITORY_RELATIVE_WORKLOG_FILE>",
  "route": { "model": "<AUTHORIZED_LOGICAL_MODEL>", "effort": "<AUTHORIZED_EFFORT>" }
}
```

That object is the value of `ownerMatrixOverride`, not a standalone grant. Its authorizer must
be `owner`; `pin` names the configured pin, whose values must match the authorized route.
The exact first-party owner-override worklog entry must already exist at the target revision.
Issue prose cannot create it. Privileged-tier authorization is a separate requirement.

The builder calls production `briefDigest`: SHA-256 of `json.Marshal` of the Go struct with
`Title` followed by `Body`. Go escaping, complete body, trailing newlines and text after any
fence are included. Do not calculate this with shell concatenation, `jq`, Python JSON defaults,
or a shortened prompt. Old grants are retained; a matching current grant is not duplicated.
An edited issue needs a new matching grant if its route requires authority.

## Validate before applying

```sh
"$PRIVATE/divybot" matrix validate \
  -config "$PRIVATE/candidate.json" \
  -issue "$INBOX_ISSUE_NUMBER" \
  -target "$TARGET_REPOSITORY"
```

Exit 0 means configuration and selected-issue policy preflight passed. Exit 2 names a fixed
field and reason, such as `matrix.revision: full-lowercase-commit-required`; no input values
or subprocess diagnostics are echoed. Duplicate/unknown matrix JSON fields, missing source,
dirty or mismatched revisions, unsafe receipt roots, missing target pins, malformed grants,
and selected-issue authorization/profile errors refuse. Arbitrary model/effort policy remains
owned by the pinned NetScript source. Unused pins/grants receive structural checks; selected
issue policy is resolved by the bridge.

For structural/source checks alone, omit both `-issue` and `-target`. The output explicitly
marks issue authorization and profile policy as unchecked. With or without an issue, **live
quota, actual host placement and launch are unchecked**. No native session evidence is
manufactured. A valid config cannot promise live admission, and an evaluator still refuses
with `observer-unavailable`.

## Negative controls

`TestMatrixConfigCLI` uses the real bridge against a synthetic committed source and a
read-only GitHub fixture. It builds a private candidate, checks the complete grant digest,
validates it, refuses an existing output file, then refuses the same legacy input without
its matrix block and names each required field. Additional tests reject ambiguous JSON,
invalid authority and pins, and prove body changes cannot reuse a grant. Fixtures contain
invented data only; no live source output or operator configuration is published.
