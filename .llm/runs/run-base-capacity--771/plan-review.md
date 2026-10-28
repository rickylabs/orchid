# Independent plan review

FAIL_FIX: three blockers; amend and re-steer the same evaluator.

**Verdict: not a PASS.** I found three blockers. I only read files and changed nothing.

### B1. The checkout base would still have several owners
The plan moves the base decision into `run_checkout.go` (fresh remote HEAD, or a base named in the brief). But the existing admission-time base code isn't on the plan's change list, and it's still used in places outside the checkout:

- `cmd/divybot/matrix.go:851-862,990-1001,1028`: `matrixAttempt` resolves `targetBase`, which is the pin or `gh api …/commits/HEAD`. It writes that value as `BaseRevision` into the hashed receipt binding and sets `handle.base`. The comment at `matrix.go:573` says "the checkout uses it".
- `matrix.go:886-889`: the owner override worklog is read at that admission revision. After the change the worker would get a worklog from commit X while checked out at a fresh HEAD Y.
- `cmd/divybot/owner_native_grant_store.go:364-410`: owner approval binds `TargetRevision`, using the pin or a HEAD read through `targetBase`, and `revisionAllowed` requires it to equal the pin. The design says "one operation never binds two bases". This file is **not** in the supervisor's mutation list, so after the change the owner-approved revision and the commit actually checked out can differ.
- `main.go:3482-3484`: falls back to `TargetRevisions`.

**Cause:** with StartSHA added, a run ends up with two or three "base" values (receipt `BaseRevision`, grant `TargetRevision`, StartSHA) from different reads at different times. Nothing says which one is authoritative. Retries and restarts can also bind a different base for the same operation.

**Needed in the plan:** say what happens to `targetBase`/`readTargetHead`, receipt `BaseRevision`, the worklog read revision, and the owner-grant `TargetRevision`. Either remove them from the base path or rename them to admission metadata. Add `owner_native_grant_store.go` to the mutation list or justify leaving it. Also say whether a relaunch reuses the recorded StartSHA or fetches again.

### B2. Existing supervision would fight the new recovery
`goal_recovery.go` is meant to be the only owner of recovery, but `superviseActive` already writes the same goal status and sends input to the pane:

- `main.go:3939-3943`: whenever herdr reports the agent as `blocked`, it runs `transitionGoal(…, "blocked")`, emits an alert and notifies. `transitionDispatchGoal` (`native_goal.go:410`) blocks any goal that is `active`.
- `main.go:2685`: the agent-disappeared path also transitions the goal to `blocked`.
- `main.go:3930-3936`: `strandedPoke` re-sends the brief to an idle pane with no PR every 10 minutes.
- `main.go:3866-3869`: `retryBoundGoal` runs on `working`/`blocked`.

**Cause:** after a status-only resume to `active`, the pane can still show `blocked` or `idle` until a new turn starts. On the next tick the supervise path re-blocks the goal or pokes the pane. That burns the bounded attempts in a loop, makes the backoff meaningless, and keeps feeding input after recovery has given up, which breaks the plan's "exhaustion is visible" and "no fresh work" rules.

**Needed in the plan:** while a capacity recovery is in progress or exhausted, `goal_recovery.go` should own the goal's status, and these paths should defer to it. Add a test for that interaction.

### B3. The native protocol evidence the plan relies on doesn't exist
The plan says "Native protocol sources and repository paths are captured in research.md". There is no `research.md` in `.llm/runs/run-base-capacity--771/` (it holds only `context-pack`, `drift`, `plan`, `supervisor` and `worklog`), and there isn't one for this run in the private project run directory either.

**Cause:** the safety of the design depends on unverified protocol facts:
- **The capacity signal:** current turn parsing keeps only `ThreadID`/`ID`/`Status` (`remote_control_native_turn.go:5-9`; `native_goal_rpc.go:207` treats any `failed` turn the same), so no typed error exists yet.
- **Telling capacity apart from usage limits:** this rests on knowing the error shape, which isn't captured anywhere.
- **What a capacity stall looks like:** which goal status Codex leaves the goal in, and which status herdr shows for the pane.
- **Whether a status-only `thread/goal/set` to `active` actually restarts work.**

Without that evidence, the "typed native error only" rule can't be checked, and the stuck-pane case behind B2 can't be pinned down.

**Needed:** commit the cited `research.md` with the native sources before implementation starts.

### Checked, no blocker
- **Background work:** all of divybot's existing background loops are started inside `run()` and stop when its context ends (`main.go:2319-2360`).
- **Git:** fetching the remote HEAD directly, then running `checkout -fB` on the dedicated branch is idiomatic.
- **Performance:** nothing in the plan is a hot-path concern.
- **Repository doctrine:** I found no other violation.