# Research

The stale-base defect is the implicit routing pin, and capacity stalls have no daemon recovery owner.

- Baseline cmd/divybot/matrix.go:789 gives Matrix.TargetRevisions precedence, matrix.go:1028 caches it in the receipt; main.go:3482-3500 fetches that SHA after cloning and force-checks out FETCH_HEAD. Neither local branch reuse nor a cached origin/HEAD is needed to reproduce the incident.
- Baseline cmd/divybot/main.go:3790 suppresses idle/done continuation; completion.go:107-125 refuses retirement for blocked goals but never resumes them; main.go:3940 can re-block a resumed goal from a lagging pane state.
- Native capacity enum: https://github.com/openai/codex/blob/main/codex-rs/protocol/src/error.rs (retrieved 2026-10-28). ServerOverloaded renders the reported capacity message.
- Native error envelope: https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/schema/typescript/v2/TurnError.ts and CodexErrorInfo.ts (retrieved 2026-10-28). Turn error has codexErrorInfo; serverOverloaded is distinct from usageLimitExceeded and rateLimitExceeded.
- Goal lifecycle: https://github.com/openai/codex/blob/main/codex-rs/ext/goal/src/runtime.rs (retrieved 2026-10-28), lines 175-225 apply_external_goal_set marks active then calls continue_if_idle; lines 293-319 map turn-error to blocked and usage-limit to usageLimited.
- Native goal parameters/status: https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/schema/typescript/v2/ThreadGoalSetParams.ts and ThreadGoalStatus.ts (retrieved 2026-10-28). Status-only active update preserves objective and budget; origin distinguishes automatic recovery from user action.
- Native API has no expected-revision/status CAS in ThreadGoalSetParams. Re-read after persisting an attempt, and document the remaining external-write race.

- Turn metadata shape and item-detail projection verified at https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/schema/typescript/v2/Turn.ts and ThreadTurnsListParams.ts (retrieved 2026-10-28): error/status are turn metadata, while itemsView controls item detail.
