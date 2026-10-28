# Independent implementation review

FAIL_FIX in the first implementation review. All three blockers addressed in the single repair pass described below; retained evaluator verification pending.

**FAIL_FIX.** The checkout repair is correct, but I found three blockers, all in capacity recovery. I only read files; shell and web access were denied, so I didn't run any gates and I don't know whether the gate runs still in progress pass.

### R1. After a resume, "watching" recovery never ends
- **Where:** `cmd/divybot/goal_recovery.go:169-174`, together with `goal_recovery.go:275-291` and `main.go:2361`.
- **Reproduction:**
  1. A successful resume sets the state to `watching` (`goal_recovery.go:239-241`).
  2. Suppose Codex then reports the goal as `active` while its newest turn is still the old `failed`/`serverOverloaded` turn, because no new turn started.
  3. Every later call goes to line 169. It sets `watching` again and `NextAttemptAt = now+15s`, and it never increases `Attempts`. So the run never reaches `exhausted` and never notifies anyone.
- **Effects:**
  - Completion keeps returning `goal-capacity-recovering` and never retires the run (`completion.go:101-106,374`).
  - Pokes and other continuation input stay suppressed (`main.go:3815-3816`).
  - The run keeps its slot (`main.go:2928`).
  - `nextGoalRecoveryDelay` wakes a full `tick` (GitHub polls for every target) every 15 seconds instead of every 5 minutes, indefinitely.
- **Why this matters:** it breaks both "bounded" and "exhaustion is visible".
- **Fix:** count `watching` time or calls against the same limit. For example, only clear `watching` once the latest turn ID has changed, and move to `exhausted` once the deadline passes.

### R2. It isn't shown that a resume restarts work in the live Codex pane
- **Where:** `native_goal_rpc.go:369-373` and `remote_control_transport.go:217-247`.
- **Cause:** for Codex runs in a herdr pane (no Remote Control, so `CanonicalCodex` is false), every goal connection starts a new `codex app-server` process over SSH. That process is killed as soon as the readback finishes. The thread itself is owned by a different process, the TUI in the pane.
- **What's unproven:** `research.md:9` says that setting `active` calls `continue_if_idle`. That only restarts work in the live pane if Codex passes the change across processes. Otherwise one of two things happens:
  - no new turn starts, which is exactly the R1 trigger: a permanent silent stall;
  - a turn starts inside the short-lived process and dies when that process is killed.
- **My earlier review was wrong here:** in the plan re-review (now quoted in `plan-eval.md:23`) I accepted this claim without considering the process boundary.
- **Tests don't reach the real boundary:** `goal_recovery_test.go` and `goal_recovery_regression_test.go` use synthetic app-server output that just reports `active`. Nothing shows a new turn starting in the pane's process.
- **Needed:** one of these:
  - evidence from a real Codex app-server that a status-only set made from a separate process starts a turn in the live TUI;
  - limit recovery to connections that share the TUI's app-server (the Remote Control/canonical daemon), and refuse it elsewhere with a visible reason.

  Either way, add a test at that boundary. I couldn't fetch `runtime.rs` to settle this either way, so this is "unproven", not "proven broken".

### R3. Every idle Codex run now costs a native process start on every tick
- **Where:** `goal_recovery.go:249-270`, called from `main.go:3815`.
- **Cause:** for any matched Codex run whose pane is idle, done or blocked, even with no recovery in progress, each `tick` starts SSH, launches `codex app-server`, runs initialize and reads the goal. Each of these waits up to 12 seconds, one job after another, inside the main dispatcher loop. Herdr events trigger extra ticks on top of the 5-minute schedule.
- **Why it's new:** an idle Codex run waiting on PR review is the normal steady state. Before this change, a native read for such a run only happened when completion was due (`completion.go:327` checks for a new completion sequence or 5 minutes elapsed). Done panes now also get two connections per tick, one from completion and one from supervision.
- **Fix:** only open a connection when recovery owns the run and an attempt is due, or when the same completion trigger fires, for example a new completion sequence or a pane status change.

### Checked, no blocker
- **Checkout (defect 1):** `prepareRunCheckout` is now the only owner. It:
  - fetches `origin HEAD` or the explicit base directly, without trusting a cached `origin/HEAD`;
  - force-resets only the dedicated run branch;
  - verifies `HEAD == FETCH_HEAD^{commit}`;
  - records `StartSHA`/`BaseRef` in `binding.json` before staging and spawning (`main.go:3494-3507`).

  The test `run_recovery_regression_test.go` runs the real production `c.spawn` path against a real local Git remote through an SSH shim. It covers: a stale pin, a cached `origin/HEAD`, a reused branch, an explicit base, a missing base, a changed default branch and a foreign origin. Admission and grant revisions are clearly kept as metadata only. The final-report digest still matches because `StartSHA` is written before `stageFinalReport`.
- **Retry bookkeeping:**
  - Each attempt is saved to disk before the native write.
  - A restart in `resuming` waits for the persisted due time.
  - A lost acknowledgement uses up its attempt.
  - Exact checks catch a manual pause, a changed binding or occupant, or changed accounting.
  - Paused, usage- and budget-limited, and completed goals are never resumed.
  - Timeout and explicit stop still take precedence.
  - The single timer lives inside `Coord.run`.
  - Completion is only reached from `tick`, so no app read triggers effects.

Repair disposition:

- R1: one persisted 30-second native-start deadline, never renewed by observation; expired/unproven starts exhaust visibly. A dedicated timer handles only pending recovery work and is disabled otherwise. Native progress and unchanged-start controls cover this boundary across restart.
- R2: the physical canonical transport marks the RPC capability; automatic resume requires that shared daemon capability. Legacy recovery refuses visibly, and a transport-boundary test proves it cannot gain native write authority. The integration regression now uses the canonical Unix-socket protocol boundary.
- R3: native observations are debounced by state/completion sequence, status and a five-minute backstop. Completion marks the same checkpoint, so one tick cannot double-read an unchanged idle run. Native source failures postpone observations rather than busy-looping.

## Same-session repair verification

**PASS.** I found no remaining blockers. I verified the code and tests directly rather than relying on the dispositions in `implementation-review.md`. I read files only; I didn't run any gates. The final full image run and the expanded mutation runs are still in progress, so this PASS covers the code, not those results.

**R1 (endless "watching"): fixed.**
- `WatchUntil` is set once, when an attempt is reserved (`goal_recovery.go:212`). Later observations never extend it: lines 181-189 point the next wake at it and nothing more.
- Once the deadline passes, if the goal is `active` but its newest turn is still the old failed one, recovery stops with `goal-capacity-resume-unproven` and sends a notification (`goal_recovery.go:184-185, 258-267`).
- The recovery timer now runs `capacityRecoveryPass`, which does no GitHub polling, and only when a recovery wake is pending. With nothing pending the timer is off (`main.go:2351-2381`, `goal_recovery.go:366-413`).
- `TestCapacityRecoveryWatchHasOnePersistedDeadline` covers this, including across a restart.

**R2 (resume might not reach the live Codex pane): fixed by limiting recovery.**
- Only a canonical shared-daemon connection gets `p.canonical` (`remote_control_transport.go:200`).
- A resume needs that capability (`goal_recovery.go:178-180`), and Codex runs without Remote Control are refused before any connection is opened (`goal_recovery.go:314-327`).
- No state is saved before the canonical check, so a short-lived connection can never become the recovery owner.
- `goal_recovery_regression_test.go` now drives `completionEvidence` through the canonical socket fixture with the native thread and turn shapes. `TestCapacityRecoveryCanonicalTransportCapability` checks the capability on both kinds of transport.

**R3 (a native connection on every tick): fixed.**
- When recovery doesn't own a run, a native read only happens when the pane's status, state sequence or completion sequence changes, or at most once every 5 minutes. The checkpoint is saved to disk (`goal_recovery.go:270-291, 332-345`).
- Completion records the same checkpoint before reading (`completion.go:366`), so one tick doesn't read the same unchanged run twice.
- When the native source fails, the next attempt is pushed back by at least the backoff delay (`goal_recovery.go:293-307, 348-361`).

**Not blockers, but you should know:**
- **Codex panes without Remote Control get no recovery.** That was the R2 option I offered, but it means the operator's "must be resumed" requirement holds only for Remote Control Codex. A capacity stall in an ordinary herdr Codex pane still ends in completion-unproven.
- **Misleading diagnostic.** `goal_recovery.go:314-322` sets `NativeGoal.Reason = "goal-capacity-canonical-required"` and logs it once for every owned Codex run without Remote Control, even when there's no stall. That reason only feeds internal retry checks for goals that aren't owned, so it's noise, not a fault.
- **Tight loop if state saves keep failing.** If `saveLocked` fails persistently, `markCapacityObservation`/`saveGoalRecovery` roll the change back and the due time stays in the past. `nextGoalRecoveryDelay` then returns 1ms, and `capacityRecoveryPass` repeats `fleetStatus` and `checkRemoteHook` (an SSH call) in a tight loop. This only happens while divybot's state saves are failing, but it is a hot loop of SSH calls.

Nonblocking timer note addressed: an overdue wake has a 15-second floor when persistence cannot advance it, with a failure-injection control and compiled mutation. Positive future deadlines remain exact. No further review repair loop was entered.
