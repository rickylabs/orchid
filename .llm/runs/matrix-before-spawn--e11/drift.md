# Implementation dispositions

The review's six narrow corrections and the owner's two approvals are implemented. Runtime process cancellation uses Unix process groups, matching the daemon's existing Unix host operation; CI runs Linux. The ignored `divybot` pattern also matched the source directory, so it is now anchored to the root build artifact.

The resolver's worktree field is only echoed by the inspected first-party policy; routing is computed before host selection with a neutral context. The chosen target source commit is pinned and used during worktree preparation. The durable receipt is persisted before any launch preparation, which is stricter than the minimum before-spawn requirement. Pre-launch failures remain fenced for explicit reconciliation rather than automatic retry.

No observation adapter is added. No post-launch observation write can fail because all unavailable observations remain explicitly unknown. Existing job state carries operational supervision; the separately durable reservation prevents replay after ambiguous launch/job-persistence failures.

Owner clarification recorded: this lane owns the Orchid spawn hook; upstream PR2 remains draft
and Orchid merge is unauthorized. Fork 3 refuses evaluator launches with an explicit inconclusive
`observer-unavailable` diagnostic from the existing receipt vocabulary. No native observer is
added. Fork 4 retains configurable pins and the trusted owner override; the override must also
name the configured pin it authorizes. The conversation reply supplies authority for all three
questions. There is no separate unanswered question set and no mailbox wait. Harness PRs remain
drafts until the owner signs off.
