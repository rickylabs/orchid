# orchid

Run many `claude` / `codex` agents in parallel and turn GitHub issues into merged
PRs. orchid is a single headless Go binary (`divybot`) that drives agents across a
pool of machines through [herdr](https://github.com/denoland/herdr) over SSH.

What it adds on top of a bare agent:

- **Fan-out** — label a GitHub issue, get an agent on a free host working it; scale
  from a few to hundreds across your machines.
- **Quota pacing** — a governor caps concurrency against your real 5h/weekly usage,
  per account, so you don't blow the window early. Spills claude→codex when capped.
- **PR supervision** — forwards new reviews / CI failures / conflicts to the agent,
  and squash-merges green PRs on opt-in targets.
- **Shared memory** — a git-backed wiki the swarm maintains itself, so it stops
  re-deriving build/test/maintainer facts.
- **Central auth** — coordinator holds the creds and pushes them to every host; no
  per-host token rot.

`herdr --remote <host>` is the UI.

## Run

```bash
go build -o divybot ./cmd/divybot
GH_TOKEN=$(gh auth token) ./divybot -config divybot.json
```

Config is one JSON file (inbox repo, hosts, targets, governor) — see
[divybot.example.json](cmd/divybot/divybot.example.json). Open work with
`gh issue create --label <target>`.

## Interactive startup deadline

Each host may set `agent_start_timeout`, for example `"agent_start_timeout": "120s"`.
The default is 120 seconds; valid durations are 5 through 300 seconds, in whole
milliseconds. Invalid values reject configuration before dispatch. Herdr uses this
budget to wait for interactive readiness, which can follow process detection.

The same budget controls the spawn deadline, with 30 seconds of finite workspace
and environment setup slack. A shorter parent deadline or cancellation still wins.
Noninteractive `*-run` jobs retain their 40-second effect deadline. There is one
startup attempt: a timeout or blocked/error result still records the durable launch
fence and loud refusal, with no automatic respawn. Logs classify startup timeout,
blocked, busy or cancellation without printing native output or environment values.
Changing the deadline never bypasses native identity or confirmed goal delivery.

## Finished runs

Completed turns never receive automatic assigned-goal replays. A managed run retires only
after its recorded occupant is unchanged across fresh reads and an independent
completion source confirms it: the newest native Codex turn has a final answer,
or the bot posted this run's marked final comment. A turn without that evidence
logs `completion-unproven`, notifies the operator once per state change, and
receives no automatic assigned-goal continuation. When a Codex run has a native goal, that
goal must also match the assignment and report `complete`. Pending or unreadable
PRs keep the worker supervised for review; branch-scoped PR absence is checked
before retirement. Matched, unfenced done workers still receive PR-state-driven
review/CI relays, debounced stuck-PR nudges and merged-PR continuation. These
inputs depend on PR events; they never replay the assigned goal. Owner mismatch
and completion fences suppress PR input. PR polling, eligible merging and the
existing operator deadline remain supervised for unfenced jobs. Handle mismatches notify the operator once per observed change
and never replace the recorded owner. Completion is checked once per poll tick.

The dispatcher persists a completion fence before cleanup. It retains the job
and capacity until both the recorded workspace and its captured native process
are independently observed absent. Close failures get at most three attempts;
uncertain cleanup stays counted. A successful close is delivery evidence only.
The fence survives restart and job removal, so an open inbox issue cannot
automatically launch or adopt the completed run again. A new assignment needs
a new inbox issue. A bounded scan prunes fences only after confirmed inbox
closure and observed cleanup. Ordinary teardown receipts keep their existing cause.

When an assignment requires a final GitHub comment, its launch prompt supplies
an exact hidden `orchid-run` marker containing the public opaque assignment and
agent IDs. The visible comment format and destination come from the brief.
Each new worker also receives a locally excluded `.divybot-final-comment.sh`
body helper bound to that launch. Build the requested final comment with
`sh .divybot-final-comment.sh < REPORT.md > FINAL-COMMENT.md`, then post it with
`gh issue comment <issue> --repo <repository> --body-file FINAL-COMMENT.md`
at the brief's destination. The helper preserves visible lines and appends one
exact marker; empty, oversized or already-marked reports fail without output.
It prepares a body and does not post or screen the visible report. Agents must
still follow the brief's privacy rules and use it only for final comments.
Missing or altered markers provide no completion evidence; readers must also
verify bot authorship and dispatch time before accepting a marked comment.

The completion reader follows the installed Codex 0.159.3 protocol generated
with `codex app-server generate-json-schema --experimental --out <output-directory>`.
It requests the newest turn with full items and requires `completed`, an explicit
`final_answer` message, a completion timestamp, and no pending structured questions.
A message without a phase provides no native completion evidence; it needs the
independent marked-comment source. A completed turn is a neutral completion
observation, and does not certify the quality or success of the assignment.

## OpenCode transport

OpenCode routes use an exact `provider/model` from the pinned Harness matrix.
An issue's `router` may name that same CLI provider; it cannot replace the prefix.
Provider IDs and models are configuration/catalog data, with no compiled model
allowlist. Off-matrix overrides and evaluator restrictions remain enforced.

Opt in to provider seats in private dispatcher configuration, for example:

```json
{"opencode":{"providers":{"example-provider":{"max_active":2}}}}
```

Each provider limit is an integer from 0 through 256; omitted or zero disables
that pool. This is a concurrency ceiling for registered seats, not a vendor credit
or subscription meter. OpenCode never spends the Codex subscription's admission
budget. An unbound OpenCode seat conservatively consumes a slot in every provider
pool. Eligible hosts must permit the `opencode` agent and have free host capacity.

For autonomous launches, read-only native catalog and resolved-agent commands
confirm the provider, exact model, and requested variant before seat creation.
Trusted owner-native launches attempt the requested model and variant directly;
catalog membership and discovery failures are informational. The full TUI receives a
process-local model overlay and a private `.divybot-opencode/` state tree with the
variant seeded explicitly; provider default seeds an empty selection. This avoids
the native TUI's unavailable-model fallback and inherited variant preferences.
Existing permission rules remain in effect; no standing user configuration changes.

Goal delivery uses one Herdr prompt and the existing durable registration fence.
Bounded native session/export reads must agree on the exact occupant, checkout,
fresh session, first prompt, variant, and assistant provider/model. Native API
response attestation is not inferred from argv or the startup display. A completed
empty/whitespace answer, provider error, route mismatch, or missing terminal
evidence blocks the run loudly and durably. Blocked runs keep the issue open and
cannot replay the prompt, poke an idle worker, supervise/merge a PR, or close as
completed after a deadline. Existing sessions without this binding need inspection.

The published v1 availability snapshot now appends an `opencode` row after the
three subscriptions. Upgrade Harness readers and contracts to 0.31.0 before
deploying this emitter; those readers accept the old three-row producer during
the transition. OpenCode reports whether any explicitly configured provider pool
has a free seat (`available: true`, `reason: null`), otherwise `no-capacity`. It
reports no subscription quota, credit balance or exact model readiness. Native
provider/model/variant preflight and matrix admission still govern each launch.
Config/discovery admission of matrix alternates and evaluator verdicts remain
separate follow-up work.

AGY launches also prepare trust for the exact canonical checkout. The dispatcher
reads valid effective CLI settings, preserves non-trust fields, and writes a
private per-reservation settings with only that checkout in `trustedWorkspaces`.
Registered per-process directory flags select the CLI overlay. Native OAuth
credentials are bound by symlink to their existing native store, never read or
copied by the dispatcher; the CLI can refresh that same store normally. Other
native authentication inputs stay inherited, and missing credentials are never
fabricated or entered automatically. The existing native onboarding cache is also
bound by reference; completion flags are never fabricated and no history/cache
tree is copied. Standing settings remain unchanged. Missing,
unreadable or invalid settings and reused/symlink state refuse before seat creation.

Before any AGY goal, bounded reads of the same stable owned occupant screen detect
remaining folder consent, sign-in and measured color-scheme onboarding blockers,
including dialogs Herdr can report as idle/ready. These return the existing `startup_blocked` class;
uncertain reads/ownership remain failed registration or the original timeout.
Readiness also requires the native built-in `? for shortcuts` composer hint;
unfamiliar screens or custom status lines fail closed before a goal.
There is no consent keypress, automatic retry or standing-config repair. Operators
must keep effective settings, authentication and onboarding state readable by
the native agent.

AGY's fresh conversation store lives in a mode-0700 `.divybot-native/<reservation>/agy`
directory beside the disposable checkout. A compatibility `.divybot-agy` link
points to it; checkout removal preserves native evidence. Store retention is
independent of checkout cleanup. The private `binding.json` carries `NativeStore`
and, after confirmed prompt delivery, the exact `NativeSessionID`. The host needs
Python 3's standard SQLite module for bounded read-only identity metadata queries.
Supervision accepts one native root with a matching trajectory only while the
registered occupant and durable receipt remain unchanged. Missing or ambiguous
metadata stays unbound; uncertain launches clear both keys. Public receipts,
diagnostics and agent IDs contain neither native IDs nor store paths.

An exact native AGY stop can retire a done seat before its operator timeout.
Bounded read-only summary and step queries require the latest completed response,
native idle state, a successful stop reason, and no pending or active child work.
The dispatcher rechecks private authority and the same occupant around that read,
then uses the existing completion fence and releases capacity only after both the
seat and its captured process are absent. A report file or marked comment alone
does not prove native completion. Open PRs retain their review relay.

## More

- [docs/architecture.md](docs/architecture.md) — what runs where
- [docs/throttling.md](docs/throttling.md) — the governor
- [docs/memory.md](docs/memory.md) — shared memory
- [SKILL.md](SKILL.md) — operating a live swarm

MIT License

## AGY seats and source provider pools

AGY has no native quota source. Admission requires an explicit static concurrency
cap: `{"unmetered_transports":{"agy":{"max_active":1}}}`. Omitted or zero caps
disable AGY; only `agy` is permitted, with an integer cap from 0 through 256.
AGY never samples or consumes Claude's meter. Claude and Codex retain their native
quota admission. Operator logs identify AGY as `meter=unmetered`.

OpenCode provider aliases in the pinned routing catalog are normalized to the
physical `opencode` transport only when the selected executor, exact model prefix,
configured provider pool and source role restriction agree. Provider defaults
remain native defaults; the dispatcher does not guess a concrete variant.

Each admission tick writes `openCodeProviderPools` alongside the availability
rows: sorted records `{provider,maxActive,active}` from configured pools and
managed jobs. Unbound OpenCode jobs count in every pool. An absent list in older
snapshots means unknown per-provider availability; an empty list means no pools.
These are concurrency seats, without quota, credit or model-readiness claims.
Upgrade the Harness reader and contracts decoder to **0.32.0 before deploying this
emitter**: earlier strict readers reject the additive field.
### Private OpenCode live-reader binding

OpenCode registration leaves native identity pending until its first prompt creates
and certifies a session. The dispatcher sends that prompt once and re-reads inside
the existing 120-second budget. An expired or canceled read cannot confirm the
goal or write a private native binding, including a late positive result.

The fresh per-run TUI state disables paste summaries: OpenCode 1.18.34 expands a
long-paste placeholder while retaining an extra trailing space. Literal pastes
preserve the full goal. Terminal line endings are canonicalized before hashing
and sending; the native stored prompt must still match that single exact digest.
The original staged goal remains available, and global TUI settings are unchanged.
The behavior is defined by the [pinned native paste implementation](https://github.com/anomalyco/opencode/blob/aec0b9a6d8898f68f923aaf08b7306d931fd9d76/packages/tui/src/component/prompt/index.tsx).

After durable first-goal confirmation, the dispatcher can publish its certified
OpenCode session ID only in the reservation's private binding. The native export
must still match the full prompt digest, exact selected route and fresh session;
registered occupant, state sequence and durable receipt are checked around the
read. Missing or changed evidence stays unbound, and an existing identity is
never replaced. Supervision and pre-teardown reads do not resend a prompt.

A native `stop` with continuation tool calls is unfinished, even when the tool
has returned. Only a completed final stop with a nonempty nonsynthetic/nonignored
answer and consistent native clocks can complete. Empty answers stay loudly
blocked; process exit, idle status and unknown finishes never supply a verdict.
Deploy this writer after the Harness 0.35.0 reader and cockpit decoder are ready;
native IDs and prompt digests remain private. Quota and provider accounting are
unchanged.
