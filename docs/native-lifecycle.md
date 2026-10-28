# Native lifecycle

Managed Claude and Codex launches add native lifecycle hooks through their
vendor settings. Each hook checks the dispatched pane, workspace, agent label,
checkout and native session before using Herdr's `pane.report_agent`. Child and
foreign-session events cannot update the occupant. Ordered hook sequences keep
an older report from overwriting a newer one. `SessionEnd` releases that source.
OpenCode uses Herdr's official integration, installed before registration.

The reporters use the supported `herdr:claude` and `herdr:codex` sources and
verify native identity after reporting. Codex also verifies applied state. Claude
is outside Herdr's full lifecycle authority allowlist, so a differing detected
status must not discard its native hook receipt. A private, sequenced receipt
retains the vendor event independently of screen detection; a new prompt or tool
event replaces a previous Stop. Claude retirement still requires a stopped exact
occupant, confirmed final delivery and satisfied PR gates. The
`screen_detection_skipped` flag is specific to Herdr's full lifecycle allowlist;
it is not a universal readiness or provenance flag.

Prompt and tool events report working; permission events report blocked; stopped
turns report idle. Herdr owns status sequencing, completion sequences and event
publication. Identity-only hooks never prove lifecycle or assignment completion.
Claude status receipts require a typed vendor event for the exact terminal and native session. Uncovered
or unavailable native evidence remains unknown. Codex's exact native turn,
prompt digest and delivery receipt checks remain authoritative for its managed
Remote Control transport.

Codex readiness and delivery no longer read its composer or trust-dialog text.
Uncertain delivery never sends an extra Enter or repeats the prompt. Other
vendor adapters retain their existing native receipts and completion fences.

The contracts are documented in [Herdr integrations](https://github.com/herdrdev/herdr/blob/v0.9.3/docs/next/website/src/content/docs/integrations.mdx),
[Herdr reporting](https://github.com/herdrdev/herdr/blob/v0.9.3/docs/next/website/src/content/docs/socket-api.mdx),
[Claude hooks](https://code.claude.com/docs/en/hooks), and
[Codex hooks](https://learn.chatgpt.com/docs/hooks).

Readiness uses a bounded `agent.wait` pinned by Herdr to the resolved occupant.
Orchid independently checks the original terminal and native session on its
Host and discovered socket. A pane-scoped subscription is acknowledged before
the baseline and the single submit. Native transitions wake acceptance reads;
loss or occupant replacement cancels the delivery. A newer native completion
sequence covers a fast turn that already returned to idle. OpenCode retains its
exact persisted prompt proof, reread on event invalidations. Codex retains its
canonical native turn and durable delivery commit.

Fresh OpenCode readiness precedes the first session-bearing prompt; its official
plugin becomes authoritative after session selection. AGY readiness retains the
server startup contract, while acceptance requires a new native user step from
its existing isolated store. Herdr events wake that read. Neither a displayed
working state nor identity alone can supply this native acceptance proof.

## Managed Codex capacity recovery

`cmd/divybot/goal_recovery.go` is the daemon's sole capacity recovery owner.
The exact owned goal must be blocked and its newest native failed turn must carry
`error.codexErrorInfo: serverOverloaded`. A quoted message, idle pane, ordinary
blocker, manual pause, usage limit or token budget limit cannot trigger recovery.

A saved job reserves at most five native resume attempts across restarts, using
15-second initial backoff, exponential delays and a four-minute ceiling. The
existing supervision loop has one timer for persisted due times; application
reads do not trigger recovery. Waiting and exhausted recovery retain the managed
running/admission slot, and ordinary blocked transitions and stranded input defer
to this owner. Explicit stop, operator timeout and normal completion fences remain
authoritative. Exhaustion is saved and notified, requiring inspection rather than
silently launching another run.

Each attempt is durable before its status-only `thread/goal/set` request. The
request preserves the objective, token budget and accumulated usage, verifies its
native update notification and readback, and consumes an attempt even if the
acknowledgement is lost. Private dispatch, goal and latest turn are rechecked
after the disk checkpoint. The native API has no conditional goal-update field,
so a concurrent external pause in the final read/write interval remains a native
protocol limitation. No screen text supplies capacity evidence.

The independent checkout owner is `cmd/divybot/run_checkout.go`, described in
[run checkout base](divybot-matrix.md#run-checkout-base). Extend these two owners
through their focused Git/native protocol controls and
`scripts/check-run-recovery-mutations.py`.
