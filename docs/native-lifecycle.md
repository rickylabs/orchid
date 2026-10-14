# Native lifecycle

Managed Claude and Codex launches add native lifecycle hooks through their
vendor settings. Each hook checks the dispatched pane, workspace, agent label,
checkout and native session before using Herdr's `pane.report_agent`. Child and
foreign-session events cannot update the occupant. Ordered hook sequences keep
an older report from overwriting a newer one. `SessionEnd` releases that source.
OpenCode uses Herdr's official integration, installed before registration.

Prompt and tool events report working; permission events report blocked; stopped
turns report idle. Herdr owns status sequencing, completion sequences and event
publication. Identity-only hooks never prove lifecycle or assignment completion.
Claude status receipts require Herdr's full lifecycle authority flag. Uncovered
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
