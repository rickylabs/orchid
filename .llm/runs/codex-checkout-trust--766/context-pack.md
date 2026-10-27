# Codex checkout trust — context

Coordinator review repair in progress on PR https://github.com/rickylabs/orchid/pull/128, same branch orch/divybot-6442619662. The independent Opus 5.5 review of de9b9f9 was CHANGES; the user authorized addressing its three blockers and posting READY with the new head. No merge, deployment or service restart is authorized.

The code now restores best-effort ordinary cleanup, attempts trust revoke on bounded release, and reconciles orphan grants at startup and before admission on each existing tick. Native config is durable evidence even when Job save never happened. The sweep only removes trusted direct issue checkouts under configured persistent/compatibility roots without a retained per-host job. Grant and revoke share validJobCheckout validation. Comment dispatch checkout keys are comment IDs, distinct from source issue numbers; a native launch fixture covers that distinction.

Focused normal/race tests, native live recovery and TUI red/green/removal proof passed. The final compiled mutation set is 16 guard removals with an assertion-green control. Full final-source local and bookworm normal/race gates are running. See review-fixes.md; update validation.md when final gate verdicts are observed. No independent PASS is claimed for the repaired head.

A repair progress commit has been pushed; the compatibility correction and final evidence still need committing/pushing. Then rewrite the PR body around the final repair and publish the coordinator-requested READY comment on tracker 766 with the new head, confirming it remains open. The earlier one-shot canonical report is already published; the latest user instruction explicitly authorizes this READY follow-up.
