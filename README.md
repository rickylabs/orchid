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

Before seat creation, read-only native catalog and resolved-agent commands on the
dispatch host must confirm the provider, exact model, and requested variant. A
missing variant refuses instead of lowering effort. The full TUI receives a
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

The published v1 subscription availability snapshot retains its three native
rows for existing strict Harness readers. Provider availability projection and
config/discovery admission of matrix alternates are separate follow-up work;
this adapter does not certify app picker availability or evaluator verdicts.

## More

- [docs/architecture.md](docs/architecture.md) — what runs where
- [docs/throttling.md](docs/throttling.md) — the governor
- [docs/memory.md](docs/memory.md) — shared memory
- [SKILL.md](SKILL.md) — operating a live swarm

MIT License
