# Implementation dispositions

The review's six narrow corrections and the owner's two approvals are implemented. Runtime process cancellation uses Unix process groups, matching the daemon's existing Unix host operation; CI runs Linux. The ignored `divybot` pattern also matched the source directory, so it is now anchored to the root build artifact.

The resolver's worktree field is only echoed by the inspected first-party policy; routing is computed before host selection with a neutral context. The chosen target source commit is pinned and used during worktree preparation. The durable receipt is persisted before any launch preparation, which is stricter than the minimum before-spawn requirement. Pre-launch failures remain fenced for explicit reconciliation rather than automatic retry.

No observation adapter is added. No post-launch observation write can fail because all unavailable observations remain explicitly unknown. Existing job state carries operational supervision; the separately durable reservation prevents replay after ambiguous launch/job-persistence failures.
