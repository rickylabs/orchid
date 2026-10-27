# Codex checkout trust — round 3 implementation

The coordinator requested a narrow follow-up on existing PR https://github.com/rickylabs/orchid/pull/128 at baseline 1bbc5bb. This round retains raw fleet working folders, persists adopted cwd, shares retained paths globally and moves recovery after observation/adoption. Per-host attempts are interval-bounded; failed launches/revokes and bounded releases trigger immediate recovery. Down and non-Codex hosts are skipped. Code owner: cmd/divybot/codex_checkout_trust.go.

Focused behavioral tests PASS. The expanded compiled mutation gate and official bookworm vet/build/full normal/full race gates are in progress. Update validation.md with actual results before READY. Prior live API/TUI proof is historical and is not rerun this round because only recovery scheduling/retention changed.

Same branch and PR only. The operator explicitly requests no external review this round because the review credential expired; coordinator reviews the final head. No independent PASS, merge, deployment, service restart or live configuration mutation. The canonical final report must be handed off through the assignment checkout's excluded final-report file. Writing queues publication; verify acceptance separately if available. Leave tracking issue 766 open.
