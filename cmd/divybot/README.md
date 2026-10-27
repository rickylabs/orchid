# divybot — the new orchid (single Go file)

A GitHub-issue → PR swarm coordinator over a **herdr fabric**. One file
(`main.go`, stdlib only). Replaces the old multi-file orchid: **no** SSH+tmux
shim, **no** capture-pane scrape, **no** paste-buffer pokes, **no** dashboard,
**no** clawpatrol.

## How it works

```
poll GitHub issues → governor-admit → push creds → herdr spawn (BARE claude/codex)
   → inject goal → supervise via herdr agent_status → poll PRs, relay reviews/CI/
   conflicts via herdr send → teardown
```

New work requires the `harness` label or an explicit `/swarm` comment accepted by
the existing source-binding rules. Target labels only route work: filing an issue
with a target label produces no launch attempt or refusal. Existing jobs remain
supervised when their issue lacks the launch label.

- **Transport** is native herdr over plain SSH (`ssh <host> herdr <cmd>`, JSON
  out). herdr is the per-host runtime + perception layer; the coordinator is a
  thin client. No tmux, no shim.
- **Perception** is herdr's native `agent_status` (`idle|working|blocked|done|
  unknown`), one structured call per host per tick — not a 5Hz pane scrape.
  `blocked` → escalate (ntfy); `idle/done` with no PR → re-inject the goal
  (fixes the gcp strandings); a `Invalid bearer`/`not trusted` read → resync
  creds + respawn.
- **Auth is central.** The coordinator owns the canonical `claude` oauth +
  `codex` auth + gh token and pushes them to every host on spawn and every
  20 min (before the oauth access token expires). This kills the entire
  per-host stale-credential 401/JWT class that bit vultr-claude and vultr-codex.
- **Governor kept.** Curls Anthropic's API with the synced oauth token, reads
  the unified rate-limit headers (5h/weekly used%), and caps concurrent agents
  so the swarm never blows the Max weekly quota. Slim threshold control
  (pause above ceiling, throttle within slack, else MaxActive); fail-open.
- **PR review/CI/conflict polling is unchanged** from orchid in spirit (`diff()`
  forwards only NEW reviews/comments/CI-failures/conflicts) — only the delivery
  channel changed (herdr `agent send`, not paste-buffer).

## Run

```sh
go build -o divybot ./cmd/divybot
GH_TOKEN=$(gh auth token) ./divybot -config divybot.json
./divybot -config divybot.json -once   # single tick, for testing
```

## Prerequisites (cutover)

1. **Each host's herdr server runs as the agent user**, and the coordinator
   ssh-targets that user: `orchid@…` on the VMs, `divy@…` on the mac. (On vultr
   that means herdr-as-orchid + `ssh orchid@localhost`, not root.) Bare claude
   refuses `--dangerously-skip-permissions` as root, so the agent user must be
   unprivileged.
2. **The coordinator host holds the canonical creds**: `~/.claude/.credentials.json`,
   `~/.codex/auth.json`, and a gh token (`GH_TOKEN` or `gh auth token`). These
   are the single source of truth pushed to every host. Keep them logged-in.
3. **herdr ≥ 0.7.0** on every host with the claude/codex integrations installed
   (`herdr integration install claude` / `codex`).
4. No tailnet "join" — a host is usable iff it's on the tailnet and its herdr
   answers. Add/remove boxes by editing `hosts` in the config.

## Cutover

```sh
systemctl stop orchid            # stop the old multi-file orchid
# (old workers keep running in their herdr workspaces; divybot adopts live
#  labels by name on its first tick and supervises them)
./divybot -config divybot.json   # or install as a systemd unit
```

## What's gone vs orchid

| orchid (old)                               | divybot (new) |
|--------------------------------------------|---------------|
| SSH ControlMaster + tmux-shim + capture-pane + paste-buffer | native `herdr` over ssh |
| dashboard (`www/`, `http_api.go`, cf relay) | `herdr --remote <host>` is the UI |
| clawpatrol per-workspace                    | bare claude/codex + central auth-sync |
| `join_managed` / `bootstrapVM` / vm-keys gating | tailnet pool, no join |
| ~13k LOC across 25 files                    | ~1.2k LOC, one file |


### Native Codex goals

Before remote Codex registration, divybot grants trust to exactly the prepared
checkout using the daemon's `config/value/write` API on its existing canonical
connection. Codex 0.164's remote TUI checks daemon configuration before resume;
thread and CLI overrides alone cannot satisfy that lookup. The private typed
write capability permits only a checkout trust entry, quotes the whole path as
one key segment, and requires an unoverridden native acknowledgement. No parent
or sibling checkout is trusted, and no config file is edited by Orchid.

`codex_checkout_trust.go` owns both grant and removal. Checkout cleanup removes
that same entry with a null API value before deleting the recorded directory;
a failed acknowledgement retains cleanup responsibility. Failed startup also
attempts removal with a separate bounded context. All work stays in divybot's
launch/teardown paths and introduces no polling or app-side trigger.

Per-thread trust remains necessary for initial thread creation, which precedes
the persistent grant. The redundant process trust argument is removed from
remote resumes; plain Codex launches keep their process-only TOML override.
The startup update-prompt suppression and native registration, thread identity
and goal-delivery checks remain in place. No screen is read or answered to
establish folder trust.

AGY checks effective settings and existing onboarding-cache readability before
creating a native workspace. Unreadable files produce the fixed
`agy-settings-unreadable` blocked log, durable fence and issue comment; no agent
or task delivery is claimed, and the same inbox issue never automatically
respawns after an ownership repair or dispatcher restart. Missing, malformed,
uncertain or post-start failures retain their existing conservative handling.
This diagnostic does not extend the shared reader's closed `issue.launchBlock`
vocabulary; app projection requires a separate reader and decoder change.

For new Codex dispatches, divybot reads the official session report again after
prompt delivery and records the authoritative identity in the existing private
binding. It then creates an active native goal using the assignment title and
reference. The private matrix config may set `budget_defaults` by exact workload
tier and profile, for example `feature` / `leaf`. A `/swarm max-tokens` value
overrides that default. If neither exists the goal receives null; zero is an
explicit numeric budget. Whole-token decimal values and exact decimal k/m suffixes
are accepted for issue overrides; invalid values refuse before launch. The same
resolved value and `issue` / `route` / `unset` source are persisted in private
`dispatch.json` for the Harness issue-agent feed. The coordinator decided this
precedence on 2026-09-27; Eric can overrule.

The execution environment needs the official herdr Codex integration and the
plain `codex app-server` goal contract. Missing identity stays explicitly
unavailable. An existing goal is not overwritten. Completion, cancellation,
deadline and blocked events update only the status of goals this dispatcher has
verified it created. Native budget/usage limitation statuses remain distinct.

See [RFC 0001](../../docs/rfcs/0001-native-dispatch-goals.md) for ownership,
privacy, uncertainty and the live verification gate.

## Confirmed Codex assignment delivery

Codex interactive launch separates registration, assignment delivery and native goal ownership.
The visible pane read accepts Herdr's plain text surface and legacy JSON `result.text`; malformed
read envelopes fail confirmation. A fresh Codex composer must report `interactive_ready` and
remain stable on the exact registered occupant before submission. A fresh private delivery marker brackets the submitted text, so confirmation never requires
the whole worker template or issue body in the last 60 visible lines. The marker must be observed
with a newer state sequence and working/blocked activity, or before a later ready composer after
a fast completed turn. Marker matching tolerates terminal wraps, including a split inside its nonce. A boot-time `done`
sequence by itself is insufficient, including on Codex 0.159.2 and 0.159.3.

There is at most one repair: an observed retained assignment gets Enter only (including the exact known-size collapsed-paste label); an unchanged empty
composer can receive the text once more after repeated observations over ten seconds. Any changed
occupant, state sequence, native-session report, screen or failed read prevents text replay.
Unconfirmed delivery persists a launch fence and blocked job. Supervision reports the block, leaves
the issue open and suppresses idle pokes, PR relay and timeout-completed closure. Restart cannot
license another submission or spawn. A placeholder alone never confirms delivery, and a hidden or wrong marker cannot license replay.
Native goal creation remains behind durable prompt confirmation.

Post-launch blocking uses the bounded six-field launch observation with `schemaVersion: 2`,
`state: "blocked"` and closed `reasonCode: "goal-prompt-unconfirmed"`. Existing pre-launch
refusals and launching/launched observations retain version 1. The real registered dispatch binding
remains `dispatched`; a delivery block never claims no agent was launched. No prompt text, native
identity, host, process output or operator path enters this observation.

The shared reader must expose this as the separate optional `issue.launchBlock`, including when
there is no native thread yet; it can coexist with a real dispatch tree. The current version-1 reader
rejects version 2, so Needs-you acceptance requires the paired Harness reader update and cockpit
alignment. Deploy/restart and a real labeled dispatch proof remain operator-owned. Synthetic process,
restart and mutation controls establish source behavior, not a deployed or paid-turn result.

## Provider/model budgets

The optional private `provider_budgets` block publishes per-model refusals and
checks OpenCode admission before launch effects. This first producer has no paid
allow path: reported exhaustion yields `budget-reached`; missing authoritative
allowance or an enforceable full-run bound yields `budget-unavailable`. See
[provider budgets](../../docs/provider-budgets.md) for the strict config fields,
source scope, published six-field decisions and required reservation gate.

Checkout storage is selected per host. `disk_workdir_root` defaults to
`/var/lib/orchid/worktrees`, a persistent directory mounted by the deployment.
Orchid does not create that root. Until it exists, `workdir_root` (or the legacy
default) remains the fallback. Existing issue checkouts stay at their original
location even after the mount appears; duplicate copies refuse preparation.
The selected path is saved with each new job so cleanup cannot follow a later
configuration change. Stop and reconcile a job before explicitly migrating its
checkout; never copy or reset an in-flight checkout to switch storage.

OpenCode and Antigravity CLI launches install the current official Herdr
integration under that host's launch account before creating the pane. Native
conversation restore is enabled by Herdr's default
`session.resume_agents_on_restore = true`. If an account disabled it, enable it
in that account's Herdr configuration for the next planned restart. Installation
does not restart Herdr or launch an agent. Restored panes must still pass Orchid's
existing exact dispatch, native session, location and vendor evidence checks;
restore never authorizes replaying a goal or adopting an unbound conversation.

Managed Antigravity conversations keep their private native store when restored:
a resume-only Herdr report records the original native store flags and native
conversation ID after the official integration identity agrees with the durable
dispatch. This report supplies no lifecycle state and leaves the official
identity source intact. A replacement occupant or a refused resume report is
never accepted as a successful registration.
Set a host's `herdr_machine` to the saved profile ID (or unique label) in the
coordinator account's Herdr catalog. Keep its existing `ssh`, `home`, and key
settings for account-specific file, credential and integration operations. The
profile's SSH target must exactly equal `ssh`; its session must equal
`herdr_session` (default `default`). Use a distinct host name for each session.
Missing, disabled, mismatched or disconnected profiles fail explicitly, without
fallback to Local or another profile. An unset `herdr_machine` preserves existing
single-host behavior. Each host retains an independent observer and reconnect
loop; no combined cross-machine snapshot is invented.

Manage profiles with Herdr's documented `machine list --json`, `machine add`,
`machine status` and `machine reconnect` commands under the profile-owning
account. Machine setup can install or start a server, so schedule that setup
separately from a running fleet change. Existing Herdr clients automatically
show enabled profiles and reconnect independently; a cached disconnected
machine is never treated as live by Orchid. API commands use
`herdr --machine <id> ...`; streaming subscriptions use the documented raw
socket API. Since saved-machine status labels its endpoint rather than exposing
the remote socket path, streaming discovers that path with the documented
account-local status command for the validated profile target and session. Installation
commands remain in the remote launch account because Herdr does not forward
them with `--machine`.
