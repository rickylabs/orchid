# Plan evaluation

PASS. Same independent Claude Opus 5.5 reviewer, high effort, read only. Round 1 FAIL_FIX addressed by the plan amendment; round 2 PASS. Raw outputs retained privately.

**PASS.** The amendment and the new `research.md` resolve B1–B3, and I found no new blockers. I only read files and changed nothing.

**B1 (one owner for the checkout base): resolved.**
- **Renamed:** `targetBase` becomes `targetAdmissionRevision`, and receipt `BaseRevision` becomes `AdmissionRevision`. That removes both from the checkout path; `matrix.go` is already on the change list.
- **Owner grant:** `owner_native_grant_store.go` and `matrix_config.go` are now on the change list (`supervisor.md:8`). The grant's `TargetRevision` is declared to be authorization evidence only, not the checkout base.
- **Single base value:** the base comes only from a base named in the brief, otherwise from a fresh fetch. `BaseRef` and a verified `StartSHA` are recorded before the agent is spawned.
- **Retry and restart:** a new retry fetches again, and restoring an existing run never prepares its checkout again. So no operation records two bases.
- **Worklog:** the owner worklog is still read at the admission revision. That is now a stated decision rather than an accident; it is the owner's rationale text, not the checked-out code.

**B2 (existing supervision fighting recovery): resolved.**
- **Ownership:** while a recovery is pending or exhausted, `goal_recovery.go` owns continuation input and goal status for idle or blocked panes.
- **Deferring paths:** the existing re-block on a blocked pane (`main.go:3939-3943`) and the stranded poke (`main.go:3930`) now defer to recovery.
- **Completion:** gets a distinct "recovering" result. That fits `completionGoalAllowsRetirement` (`completion.go:152`), which already refuses retirement for any goal that isn't complete.
- **Overrides:** operator timeout and explicit stop still win.
- **Accounting:** a pane that is stalled on capacity still shows as idle or blocked, not done. `occupiesAdmissionSlot` (`main.go:2915-2926`) therefore keeps its seat, which matches the plan's "retain active run accounting".

**B3 (missing protocol evidence): resolved.** `research.md` now cites the Codex sources:
- `error.rs` and `CodexErrorInfo.ts`/`TurnError.ts`: `serverOverloaded` is a distinct typed signal, separate from `usageLimitExceeded` and `rateLimitExceeded`.
- `runtime.rs`: a turn error leaves the goal in `blocked`, and an external set to `active` calls `continue_if_idle`. That shows a status-only resume really restarts work.
- `ThreadGoalSetParams.ts`: there is no conditional update. The plan's mitigation is to re-read after reserving each attempt and to document the race that remains.

**Things to watch during implementation (not blockers):**
- **Status change:** `transitionDispatchGoal` (`native_goal.go:391-393`) only allows complete, blocked or paused today. Recovery will need a separate, narrow `blocked`→`active` path rather than widening that function.
- **Backoff timing:** keep it driven by the existing tick using a stored next-attempt time, not a new goroutine. That keeps all background work inside `Coord.run`'s lifetime and makes it survive restarts.
- **Disappeared agents:** the agent-disappeared path (`main.go:2685`) still sets the goal to `blocked`. That is correct when the agent is really gone, but it should end any pending recovery as well.