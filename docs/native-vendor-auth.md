# Native vendor startup and authentication

Orchid decides vendor startup and authentication state from typed native facts only.
Terminal text never decides it. That covers the visible pane, scrollback, localized
strings, quoted errors and composer hints. Process identity can fence cleanup, but it
never supplies a startup or auth state. The phone renders the backend facts and
infers nothing itself.

Two adapters own this boundary. To extend either one, read its file:

- `cmd/divybot/agy_startup.go` owns AGY startup: preparation facts, the occupant check
  and the named upstream gaps.
- `cmd/divybot/vendor_auth.go` owns authentication for every transport: the typed
  sources, the one effect and its fence.

## States

| State | Meaning |
| --- | --- |
| `ready` | A native fact proves the vendor accepted work. For AGY this is the new user step in its isolated store (`agy_prompt.go`). |
| `blocked` | A typed native fact says the vendor cannot proceed, such as AGY's `onboardingComplete: false`. |
| `unauthenticated` | A typed vendor event says the credential was rejected. |
| `unknown` | Everything else, including every case where the vendor exposes no typed fact. Unknown has no effect. |

## AGY startup

AGY's installed CLI (1.4.12) gives Orchid no typed signal between process start and
its first conversation:

- Its official `hooks.json` events are `SessionStart`, `PreToolUse`, `PostToolUse`,
  `PreInvocation`, `PostInvocation` and `Stop`. The CLI loads them from
  `<gemini_dir>/config/hooks.json` and from workspace `.agents/hooks.json`.
- `SessionStart` fires when the first prompt starts a conversation. It does not fire
  at the idle composer. It never fires while the CLI shows the sign-in screen.
- Herdr's official `antigravity_cli` integration reports only session identity, from
  `PreInvocation`. Its own comment says the pane lifecycle comes from screen detection.

Orchid therefore uses these facts:

| Fact | Source | Outcome |
| --- | --- | --- |
| Trust | The per-launch settings overlay lists only the exact canonical checkout in `trustedWorkspaces`. | Prepared by construction. A preparation failure refuses before any workspace. |
| Onboarding | The CLI's typed cache field `onboardingComplete`, read once and bounded to 64 KiB before any workspace. | `true` lets the launch continue. `false` refuses as `startup_blocked`. Missing, non-boolean, duplicated, oversized or invalid refuses as unproven. No seat is created in either refusal case. |
| Occupant | One Herdr `agent get` for the registered pane. Agent, label, pane, workspace and checkout must match exactly. | A mismatch fails registration. The displayed Herdr status decides nothing here. |
| Auth | None. The CLI exposes no typed pre-conversation auth state. | Unknown. |
| Acceptance | A new native user step in the run's isolated store, woken by Herdr events. | The only `ready` proof. Without it the goal stays unconfirmed and is reported once, never retried. |

For AGY, Herdr's status and its start readiness are display-derived. Orchid uses
them only to withhold input: goal delivery waits for Herdr idle/done before it types
anything, and a blocked or never-ready pane receives no input. The goal then stays
unconfirmed and is reported once. Herdr's own `agent start` error is reported as
Herdr gives it, for example `startup_timeout`, and Orchid adds nothing from the screen.
No displayed status is recorded as a native fact or confirms a goal.

The screen checks are gone: trust, sign-in and onboarding marker groups, and the
`? for shortcuts` composer requirement. So is the screen refinement of a startup
timeout, which now stays `startup_timeout`.

### Upstream gaps (kept as explicit unknown)

Tracked in [#141](https://github.com/rickylabs/orchid/issues/141).

| Gap id | Missing capability | Effect in Orchid |
| --- | --- | --- |
| `agy-cli-no-pre-conversation-ready-event` | A typed event or command that reports the interactive composer ready before the first conversation, or a hook event earlier than `SessionStart`. | Startup readiness falls back to Herdr's display-derived start contract, which only fences input. Acceptance still needs native proof. |
| `agy-cli-no-typed-pre-conversation-auth-state` | A typed auth state, such as a hook event or a `--output-format json` status, before the first conversation. `agy models` prints text and exits 0 even when not signed in. The typed `auth_required` error exists only in the CLI's hidden ACP server mode, which is not the interactive TUI Orchid launches. | AGY auth stays unknown. Sending into a sign-in surface remains possible when a present credential is invalid. The goal is then never confirmed, and the run is reported once and never retried. |
| `agy-cli-stop-hook-error-untyped` | A typed error class on the `Stop` hook. Today it carries `terminationReason: "ERROR"` and a free-text `error`, the same for quota, auth and other failures. | No AGY credential action. Credentials are not centrally synced for AGY anyway. |

The Herdr side has a related gap: the `antigravity_cli` integration could report
working and idle from `PreInvocation` and `Stop` instead of screen detection. That
fix belongs in Herdr. Orchid's AGY acceptance does not depend on it.

## Authentication

The adapter reads only typed sources. Transports without one are never read.

| Transport | Typed source | Unauthenticated when |
| --- | --- | --- |
| Claude | The managed lifecycle hook's `StopFailure` receipt (`herdr_lifecycle.go`). The hook records Claude's closed `error` enum and drops any other value. | `error` is `authentication_failed`, and the receipt matches the exact occupant: terminal, native session, label, pane, workspace and checkout. |
| Codex (Remote Control) | The bound thread's newest turn, read with the same bounded `thread/turns/list` request that capacity recovery uses. | Status is `failed` and `error.codexErrorInfo` is `unauthorized`. |
| Codex (plain) | None. Codex hooks have no failure event. | Never. Stays unknown. |
| AGY | None (`agy-cli-stop-hook-error-untyped`). | Never. Stays unknown. |
| OpenCode | None here. Its credentials are not centrally synced, and its own adapter already blocks provider errors read from its native store. | Never. Stays unknown. |

Effect: each native event (agent, session or thread, sequence or turn) gets one
resynchronization of the central credentials to the run's host and one notification.
Before the effect runs, the event digest is saved in the job state (`auth_signal`).
If that save fails, nothing runs. After the save:

- a repeated read does nothing;
- a restart does nothing;
- a stale receipt from an earlier pane generation does nothing.

A later native event is its own outcome.

A suspected auth failure never tears down, stops, re-prompts or redispatches a
session.

Reads are event-driven, with a bounded backstop. The adapter reads a typed source
whenever the native cursor changes, for every matched occupant that is not under
capacity recovery. Herdr's displayed status never gates the read: the typed source
itself says whether a session failed authentication. The cursor has three parts:

- Herdr's state counter;
- Herdr's completion counter;
- the bound hook receipt's sequence, which the fleet pass already reads for stopped
  and blocked occupants.

Herdr's counters advance only on effective state changes. A change the cursor
cannot show is therefore read at the latest after a five-minute backstop, the same
bound capacity recovery uses. Examples are a receipt replaced while Herdr shows a
stale `working`, or a Codex turn that failed behind an unchanged status.

A change stays pending, and is read again on the next tick, after any of these:

- a failed Herdr read;
- an unreadable receipt (as opposed to an absent or foreign one);
- a failed connection;
- an effect fence that could not be saved.

## Validation

`go test ./cmd/divybot -run 'TestAGY|TestVendorAuth|TestStartupAndAuth'` covers:

- **Screen text has no effect on readiness.** Trust, sign-in, onboarding, localized,
  empty, invalid-UTF-8 and hostile screens, and every displayed Herdr status, all give
  the same startup outcome. The pane is never read.
- **Stale quoted errors trigger nothing.** `Invalid bearer`, `Please run /login` and
  localized text in prompts, assistant messages or error details stay unknown. So do
  receipts from another terminal, session, label or checkout.
- **Missing native capability stays unknown.** AGY, OpenCode and plain Codex perform
  no auth read and have no effect.
- **One native event has one typed outcome.** A typed `authentication_failed` or
  `unauthorized` yields `unauthenticated` with its provenance.
- **Repeated or stale events cannot repeat effects.** Repeats, restarts and failed
  saves leave a single effect. The live job stays.
- **Pending changes are retried.** An undurable fence or an unreadable receipt leaves
  the same native change pending, and it is acted on once after recovery.
- **The Codex path runs end to end.** Remote Control Codex goes through the real
  goal-RPC reader. It is read whatever Herdr displays, including a stale `working`,
  and acted on once.
- **A new receipt behind unchanged counters is caught.** It is read when the fleet's
  receipt sequence moves, or by the backstop when the fleet did not read it.

`scripts/check-vendor-auth-mutations.py` compiles 28 mutants. Each one reintroduces
a screen read, an untyped value, a stale generation, a removed fence or an
unbounded read. Every mutant must fail an assertion, and the restored tree must pass.

No startup latency improvement is claimed. Removing the screen read takes one
bounded Herdr `pane read` and a second `agent get` out of AGY registration. Nobody
has measured AGY launch latency, before or after.
