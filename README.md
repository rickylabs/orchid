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

## More

- [docs/architecture.md](docs/architecture.md) — what runs where
- [docs/throttling.md](docs/throttling.md) — the governor
- [docs/memory.md](docs/memory.md) — shared memory
- [SKILL.md](SKILL.md) — operating a live swarm

MIT License
