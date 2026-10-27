# Orphan reaper supervisor

Implement the scoped orphan collection repair; stop at a ready Orchid PR.

- Baseline: Orchid origin/main 6e384a6 (full SHA recorded after checkout).
- Author: assigned Codex implementation session, operator-requested reasoning xhigh.
- Operator routing override: implementation uses assigned Codex; read-only blocker review uses `codex exec -m gpt-6.1-sol -c model_reasoning_effort=high`, stdin closed. This explicitly overrides the feature matrix models and cross-vendor evaluation requirement; the review is a separate session, not a claim of cross-vendor certification.
- Mutation surface: `cmd/divybot/` subprocess creation/lifecycle callers and reaper tests, `scripts/check-reaper-mutations.py`, `.llm/runs/orphan-reaper--owned-pids/`. Operational evidence and memory stay in this project run directory; final report handoff stays in the assignment directory.
- Forbidden: deployment, merge, live service lifecycle, foreign processes, private runtime identifiers in public artifacts, assignment-file commits.
