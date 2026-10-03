# Private live owner grants

An operator can install a grant for an owner-approved inert issue without
rewriting the dispatcher configuration or restarting it for each grant. The
port binds the complete GitHub title and body using the same Go builder as
`divybot matrix build`. It returns `LIVE` only after the exact immutable record
is durable, active, and selectable by the running coordinator.

The feature is optional. Without `owner_native_grant_port`, startup grants and
ordinary routing retain their existing behavior. With the port configured,
normal admission and action retry both consult its independent grant snapshot.
Capacity, subscription readings, provider budgets, reservation receipts, native
prompt confirmation and completion checks still apply after owner authorization.
A LIVE grant proves authority; it does not prove a CLI run succeeded.

## Bootstrap and trust

Configure the optional `owner_native_grant_port` object once at deployment:

| Key | Requirement |
| --- | --- |
| `socket` | Absolute clean Unix socket path in an existing mode-0700 private directory owned by `operator_uid`. The listener creates a mode-0600 socket owned by that operator. |
| `store_root` | Existing absolute private directory owned by the daemon UID; grants and intent indexes live here. |
| `approval_root` | Existing absolute private directory owned by `operator_uid`; independently issued approvals live here. |
| `operator_uid` | Required numeric UID authenticated with Linux `SO_PEERCRED` for both port methods. |

All directories must have mode 0700, have no symlink components, and be outside
Git. The endpoint directory and approval root belong to `operator_uid`; the grant
store and immutable records remain owned by the daemon UID. The store and approval roots must be separate and not nested. Files and
the socket have mode 0600. The operator owns the endpoint and approvals, so a root daemon can serve a
different unprivileged operator without widening permissions. An unprivileged
daemon must be able to assign the socket to the configured operator; failure
keeps the port unavailable. Same-UID deployments retain the existing behavior. Other platforms
fail closed until their peer authentication and no-follow implementation exists.
An existing endpoint is never overwritten, including an apparently stale socket;
the operator must resolve it before bootstrap. The process removes its own socket
on graceful shutdown after checking its identity.

Only an operator-issued private approval record is authority. An input authorizer,
issue comment, GitHub label, public picker choice or model catalogue entry cannot
approve a route. The Unix peer must be authenticated even with a valid approval.
Approval records are private operator inputs, never emitted by the issue builder
or created by a public API. They must refer to the exact approved inert issue.

## Private request and approval

The install request has these required, non-null, case-sensitive fields:

| Field | Meaning |
| --- | --- |
| `schema_version` | Integer 1. |
| `operation_id` | Stable UUID; reuse it to reconcile an ambiguous reply. |
| `approval_ref` | Safe filename stem selecting an operator-issued approval. |
| `expected_issue_id`, `issue_number` | Exact GitHub node identity and inbox issue number. |
| `target` | Configured target repository. The issue's target label must select it. |
| `expected_brief_digest` | SHA-256 from the Go builder over its entire Title/Body JSON. |
| `tier`, `role`, `profile` | Exact approved routing and profile scope. |
| `ownerNativeOverride` | Existing Eric-authorized rationale and complete native route. |

The approval file is `<approval_ref>.json`, mode 0600, with fields
`schema_version: 1`, `authorizer: "eric"`, `inbox`, `matrix_revision`,
`target_revision`, and `request` containing the exact request above. Both revisions
must be the current full lowercase commit pins. The approval digest also binds the
exact approval bytes. Rewriting an issued approval invalidates that record.
Use the existing Go matrix builder to obtain the full brief binding for approval;
do not hash a preview or generate the grant in a frontend. The port re-fetches the
whole issue independently and cannot take binding fields from caller-supplied grants.

The native route uses `harness`, `provider`, `model`, and `effort`. All four CLIs
are supported. OpenCode `model` is the native CLI-qualified model required by the
existing override validator. This private CLI route does not change the public
provider-budget convention, where provider and provider-native model are separate.
Unknown, duplicate, null, missing required, malformed UTF-8 and oversized fields
are refused. Frames, issues and private files are bounded to 1 MiB.

## Transport and reconciliation

One newline-terminated JSON request and one JSON reply per authenticated Unix
connection, with a 30-second deadline. The supported methods are:

```json
{"method":"install","install":{"schema_version":1}}
```

The abbreviated `install` object above must contain every required field in the
request table. Status requires the existing operation UUID:

```json
{"method":"readStatus","operation_id":"11111111-1111-4111-8111-111111111111"}
```

The UUID above is a synthetic example, not an operational grant. The operator CLI
wraps these methods:

```sh
divybot owner-native-grant install -socket "$SOCKET" -request "$REQUEST"
divybot owner-native-grant status -socket "$SOCKET" -operation "$OPERATION"
```

Request files must be private and owned by the caller. `-server-uid` explicitly
pins a different daemon UID; the client verifies both the socket and its actual
peer. Filesystem checks use the caller/operator UID independently of this server
UID pin. For a root daemon and an unprivileged operator, pass `-server-uid 0`;
endpoint ownership never substitutes for authenticating the actual server. An
operator-created replacement socket is refused before sending an authority
request. The Linux listener holds and validates its created no-follow socket
inode before ownership/mode changes, and rechecks the endpoint identity after
preparation. A swapped path never receives privileged changes or becomes the
registered listener. The CLI emits a private JSON acknowledgement and exits 0 only for LIVE.
It never edits configuration, writes approval records, applies labels or launches
an agent. It never retries a failed request automatically.

Replies use `state: LIVE`, `UNKNOWN`, or `REFUSED` and fixed reasons
`override-invalid` or `grant-conflict`. LIVE includes the operation, immutable
record checksum, inbox/node/issue identity, full brief digest, target, profile,
full revision pins, and exact route. Keep this proof private. The consumer must
compare every scope field against the approved operation before treating it as
triggerable; this CLI does not replace that comparison. Public ownerAuthority
should expose only minimal state and a plain reason, through a separately reviewed
Cockpit adapter.

## Durable publication and admission

Installation requires OPEN, no `harness` trigger, and the expected target label.
The Go builder validates the full policy and re-reads the issue before publishing.
A private per-issue intent directory and immutable operation index are synced
before the grant record. Both index and record use exclusive staging, file fsync,
collision-protected hard-link publication, directory fsync and exact readback.
The store then activates an independent snapshot and revalidates current issue,
approval, durable bytes and selection before acknowledging LIVE. Startup configuration
and shared grant slices are never mutated.

An edited/closed/redirected issue, revoked approval, missing or corrupt grant/index,
unavailable store, or inactive record refuses owner admission. Explicit owner
intent cannot fall back to ordinary routing. Common admission independently reads
the whole current issue, so a stale poll snapshot also refuses. Existing static
native grants remain usable and fence edited briefs when the port is enabled.
Ordinary issues without owner intent retain their default path.

Identical operation/content is idempotent; changed content under an old operation
or a second grant for the same brief conflicts. A new approved brief uses a new
operation and approval. Intact old brief records remain immutable history and do
not block the current approved brief merely because an old approval was revoked.
Missing history cannot establish its scope and stays unavailable. Per-issue index
reads are bounded to 32 entries, including crash leftovers; startup/installation
record reads are bounded to 1024. At these bounds new authority is refused and the
operator must reconcile storage; the port never deletes authority history itself.

Lost replies, failed fsync/publication and failed activation are UNKNOWN. They
must stay untriggered. `readStatus` only reports an independently verified active
record; it never activates a pending record or installs another grant. On restart,
valid durable records are recovered and synced before admissions or listener
availability. Corrupt or unauthorized records never activate. Known intent remains
fenced across missing grants, including an empty per-issue intent directory.

The consumer order is owner approval, inert issue, Go install, verify exact LIVE,
read back the complete issue, then the existing normal trigger exactly once.
Keep a consumer-side trigger fence before the label write. After that normal
trigger, status can still prove the exact record; install remains inert-only.
Ambiguous publication or trigger effects must be reconciled, never retried with
a fresh grant automatically. This port adds no public HTTP endpoint, billing
exception, model alias or teardown cause.
