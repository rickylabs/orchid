# Round 2 plan evaluation

**PASS.** The amendment closes the round-1 blocker, and I found no new blockers.

**Why it holds:**
- **Retry exhaustion:** it is only reached from the blocked branch (`cmd/divybot/goal_recovery.go:191–208`), with the goal observed as `blocked` at `:138`. After that the daemon makes no native write. So an active goal seen later can only come from outside the daemon, and releasing ownership on it is correct.
- **Unproven exhaustion:** this happens at `:185` while the goal is active, and at `:299` once watching expires. Both see the goal as active. `watching` is only entered from an active observation: either the returned readback (`:248`) or the observation at `:187`. So the recorded status is `active`, and an unchanged active goal with the same failed turn stays owned. The new no-write/no-second-notice regression covers this.
- **Lost acknowledgement:** if a resume write lands but its acknowledgement is lost (`:244–246`), the state is `waiting` and the last observation is `blocked`. The next tick goes through the active branch (`:181–189`) and moves to `watching` with an active observation before any exhaustion can happen. No early release.
- **Old records:** inferring `blocked` from the `goal-capacity-retry-exhausted` reason is sound, because that reason is only set from the blocked branch. Requiring turn progress for old unproven records is the conservative choice.
- **Returning the readback from `resumeCapacityGoal`:** this avoids an extra RPC and doesn't widen the port.
- **`ExhaustedTurnID`:** this is the right durable key for de-duplicating notices. `TurnID` and `Reason` alone are overwritten at `:197–201`.

**Non-blocking notes for implementation and tests:**
1. **Record a fresh observation on every exhaustion.** The `:185` exhaustion should record the `goal` it read at `:138`, not carry the previous transition's value forward. A test should fail if the lost-acknowledgement path followed by unproven exhaustion records `blocked`.
2. **Keep exhausted jobs on the debounce path.** Once the early return at `:334` is removed, an exhausted state has a zero `NextAttemptAt` and would pass the owned check at `:337` on every tick. It must take the non-owned debounce at `:340` (sequence/status change or `fleetRecoveryEvery`), and a test should assert this.
3. **Define "proves progress" tightly.** It should mean the newest turn ID differs from `ExhaustedTurnID`, or its status is not `failed`. It should not mean a bare change in status.
4. **Same-turn re-entry after release.** If the goal goes back to `blocked` with the same failed turn after release, `:197` puts the job back into `waiting` with Attempts already at 5, and it exhausts again about 4 minutes later without a notice. That holds re-pokes for one backoff period. It's acceptable, but the test should pin down whether that silent re-ownership is intended.

I used only Read and Grep this round; nothing was run or changed.

Independent Claude Opus 5.5, high effort, stdin closed, retained across rounds. Round 1 FAIL_FIX preserved privately; native automatic exhaustion distinction amended before code changes.
