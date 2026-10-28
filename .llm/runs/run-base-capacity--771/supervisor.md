# Run base and capacity supervisor

Deliver the two repairs in Harness issue 771 through one Orchid PR, stopping at READY.

- Baseline: 22f14e0eca84ba0b600a83eb782db653d66d88b9.
- Author: assigned Codex implementation session, reasoning xhigh.
- Routing: fresh feature matrix queried; operator explicitly overrides author and review routing. Independent review uses Claude Opus 5.5, effort high, read only, blockers only, stdin closed. One repair pass, same evaluator session.
- Mutation surface: cmd/divybot/{main.go,matrix.go,matrix_config.go,owner_native_grant_store.go,overrides.go,completion.go,native_goal.go,run_checkout.go,goal_recovery.go}, their focused tests, scripts/check-run-recovery-mutations.py, docs/{divybot-matrix.md,native-lifecycle.md}, .llm/runs/run-base-capacity--771/.
- Private evidence and reusable memory live in the project run directory. Final publication uses the assignment checkout's excluded report handoff.
- No merge, deploy, service restart, foreign process mutation, assignment-file commit, or private runtime identifiers in public artifacts.
