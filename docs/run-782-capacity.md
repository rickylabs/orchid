# Native run capacity and disappearance

Account capacity measures active work, while host capacity measures resident
workspaces. A remote-control run can finish its assignment and keep its warm
workspace, PR supervision, app identity and source binding without occupying an
account slot. Physical host placement still counts that resident workspace.

`cmd/divybot/run_capacity.go` owns remote-control account accounting. A done
observation requires the existing bound completion occupant and native completion
proof, read without publishing a final report. It expires after a normal fleet
recovery interval, is invalidated by changed or missing fleet identity, and is
never serialized. Restart therefore counts the run until it can observe it again.
An ended turn alone is not assignment completion.

Every coordinator continuation send reacquires account capacity before sending.
If the account is full, PR activity stays pending. Even uncertain delivery keeps
the acquired slot occupied. The common launch boundary clamps its tick budget
again, because an earlier PR relay can have reacquired a slot in the same tick.

`remote_run_reaper.go` handles vanished remote-control seats. It checks Herdr's
structured workspace inventory for both the recorded workspace and its label;
an absent agent entry or an unreachable host is insufficient. Consecutive misses
are paced and persisted. Each tick inspects at most two eligible candidates with
bounded native contexts. Completion, explicit stop, timeout cleanup and capacity
recovery keep their own lifecycle ownership.

For Codex, the canonical app-server must prove either that the exact owned tree
is unloaded, or that its goal and tree are inactive before the existing native
archive path unloads it. Queues, background terminals, descendants and native
turn notifications remain part of that proof. ACTIVE or unreadable work stays
counted. The shared daemon is never stopped.

For Claude, a bound native `SessionEnd` receipt must have a terminal exit reason,
and the process captured from Claude's native session record must have ended.
`clear`, `resume`, missing reasons and read failures are unconfirmed. Legacy
launches without these receipts retain capacity until explicit stop or timeout;
they are never inferred dead from process names or terminal text.

A durable `agent_disappeared` launch block precedes native resource retirement.
Only successful native retirement and repeated workspace absence remove the job.
One release log names that evidence. Closing an inbox issue uses the same proof
when its TUI has already vanished: it no longer requires inventing a process
anchor for a missing pane. A native goal-complete notification alone remains
distinct from teardown.

Governor logs expose `active`, `live`, `done` and `unconfirmed`; active is the sum
of live and unconfirmed, including deduplicated in-flight launch reservations.
Existing non-remote-control and OpenCode provider-pool accounting is unchanged.

The regression controls live in `run_capacity_test.go` and
`run_capacity_controls_test.go`. Run `python3 scripts/check-run-capacity-mutations.py`
to break each control in an isolated source copy, require assertion failures, and
rerun the restored controls. The ACTIVE-native-session negative control must keep
a job whose TUI has disappeared.
