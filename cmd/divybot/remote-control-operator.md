# Private Remote Control observation, schema 1

Producer: `remote_control.go`, `writeRemoteObservation` and the coordinator's
bounded per-run refresh. Read through the existing configured private dispatch
root, at `<dispatchRoot>/<reservation>/record/remote-control.json`. There is no
new public endpoint or public agent-tree field. The path is server-derived;
never accept a file path, native identity or vendor target from an HTTP client.

The envelope has exactly these fields:

| Field | Type and meaning |
| --- | --- |
| schemaVersion | integer 1 |
| runId | private dispatch RunID; exact dispatched root, no child inheritance |
| nativeSessionId | private native join key; never disclose this field |
| host, paneId, workspaceId | exact private dispatch placement |
| vendor | `claude` or `chatgpt`, corroborated by dispatch source |
| state | `connected` or `unconfirmed` |
| reason | null for connected; a closed refusal string for unconfirmed |
| sessionName | observed native name, or null; never a requested-only name |
| link | null in this producer revision; no URL construction |
| observedAt | UTC RFC3339Nano native observation time |
| validUntil | observedAt plus exactly 30 seconds |

Read only owner/operator files: regular, no symlinks, mode 0600, bounded to
262144 bytes, with parent reservation and record directories mode 0700. Reject
duplicate keys, unknown fields, trailing documents and source/schema changes.
Join the private reservation digest and dispatched snapshot through the same
existing supplier boundary. Match RunID, issue/repository, source/route, native
root, host/pane/workspace against `dispatch.json` and `binding.json`, rereading
both after the observation. Missing, uncertain, replaced, ambiguous or child-only
bindings cannot certify a root or another agent. The public dispatch/agent IDs
come from the existing verified projection; never derive a new public key from
the native identity here.

Names are native readback. Codex uses `thread/name/set` followed by `thread/read`
against the exact prepared/resumed thread on the canonical connected daemon.
Claude's launch uses the required name flags, but the requested string is not
published until its native title source corroborates it. Null means unavailable,
even when connection is proven. Both vendors have `link:null` in this revision;
an Open action is unavailable. A later target producer needs separate native
provenance, accepted origin/format and disclosure review; it cannot concatenate
an identifier or title into a URL.

The connection observation is independent of native liveness and completion.
It grants no authority to launch, pair, enable, consent, reconnect, Steer or Stop.
Refresh uses the retained run mode, not today's launch switch. Explicit switch
off opts future managed launches out of Orchid's remote integration; missing
observations on those runs grant no capability.

On failed proof the producer replaces the row with `unconfirmed`, clears its
name/target, or removes it if binding or publication cannot be verified. Stop,
teardown, completion retirement and uncertain dispatch remove it. The reader
must also refuse terminal/fenced roots independently, and reject future times,
expired observations, invalid validity windows and results arriving after a
binding change. Do not cache beyond validUntil; stream expiry must revoke the
decoration even without a new native event.

Possession of the operator file is not a web disclosure grant. The consumer
requires its current authenticated owner/operator policy and repository/run
scope for name/state disclosure; ordinary repository read authorization alone
does not approve private Remote Control disclosure. Without such a policy the
decoration is unavailable. Recheck session/grant/policy revocation on each
response and stream publication. Never include this envelope in a public tree,
event log, operation receipt, durable query cache, metrics, traces, public capture,
issue, comment or PR text. A future approved target would require the same live
policy and no cross-principal cache sharing.

The exact closed envelope is `remote-control.schema.json`. Synthetic consumer
fixtures are `testdata/remote-control/chatgpt-connected.json`,
`claude-connected-unnamed.json`, and `chatgpt-unconfirmed.json`. Their clock is
2026-01-01T00:00:00Z; tests inject that clock, then move it to expiry. All values
are synthetic. Producer boundary controls live in `remote_control_*_test.go`. Operational values remain private. Consumer READY
must pin the reviewed merged producer revision, not a candidate branch.


Managed launch configuration defaults to `remote_control: {"claude": true,
"codex": true}`. Set either boolean false to opt future launches out; existing
runs retain their transport until cleanup. Unknown keys/nulls are refused.
Noninteractive `claude-run`/`codex-run` cannot silently bypass an enabled remote
requirement. This change does not start or configure the canonical daemon.
Owner pairing, Enable, consent and reconnect remain owner actions.

Codex records `identitySource: codex-native-status` in private
`remote-control-run.json` after ONE pre-goal status read checking the exact
prepared ID, name, cwd, model and effort. The official hook, wherever present,
remains a separate `hookConfirmed` check; Claude uses `herdr-session-start`. An invocation-only
`-c tui.status_line=["thread-id"]` supplies current attached-thread identity;
it is never native Done. Hooks remain a second check wherever present, and a
conflicting hook/footer blocks further input and stops only the prepared work.
The canonical daemon inherits its own hook environment, so a missing Herdr hook
is not manufactured or written as an official integration report.

Native `turn/started` and `turn/completed` notifications are filtered to the exact
prepared thread and corroborated against its full canonical turn readback. A
late/restarted observer may miss a transient notification; the exact canonical
persisted full-turn response remains structured evidence. A conflicting live
notification, absent/ambiguous native response, active queue/background terminal,
nonterminal tool or unverified descendant leaves completion unconfirmed. Terminal
text, the footer, a report file and a marked comment never substitute native Done.
Completion and Stop reconcile scoped root/child terminal proofs after the last
native read. Every notice received after that proof must agree; a later matching
notice cannot erase an intervening conflict. Verified child notices remain in
scope during root reads, without admitting foreign daemon tenants.
Stop additionally pauses the owned native goal, interrupts in-progress turns,
terminates scoped background terminals and requires terminal readback before
closing. Neither interrupt/archive acknowledgement nor TUI absence releases
daemon capacity. Existing completion fences and paired seat/process observations
remain required. Both archived and current catalog descendants plus loaded ephemeral
descendants are included; truncated inventories
and unreadable/unloaded descendants retain capacity for operator inspection.


The explicit native remote resume omits redundant `--cd`: the installed TUI
otherwise performs a separate daemon-global folder-trust lookup that cannot see
the approved per-thread `config.projects` map. Preparation/resume and the first
status proof still require the exact owned checkout. No consent answer or global
trust write is used.

Run compiled guard controls with `python3
.github/scripts/check-remote-control-mutants.py`. AF_UNIX controls exercise the
real owned transport and positive kernel credential shape. Foreign owner/peer
controls substitute only the measured stat/credential input; they do not claim a
local test ran under a second UID. The existing CI two-UID owner endpoint gates
remain separate. Managed phone/Stop witnesses require coordinator GO after
review, merge and deployment.
