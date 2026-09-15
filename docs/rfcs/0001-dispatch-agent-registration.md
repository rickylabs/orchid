# RFC 0001: Register dispatched agents before goal delivery

- Status: implemented, pending review and live dispatch acceptance
- Date: 2026-09-14
- Scope: divybot interactive agent registration and bounded launch failure
- Evidence snapshot: Orchid matrix branch `007e8ba987830b2ca0447a4073ed10fae08da46a`;
  Orchid main `d344bd0`; installed herdr `0.9.0`
- Delivery tracking: [Harness #352](https://github.com/rickylabs/harness/issues/352),
  stacked on [Orchid #2](https://github.com/rickylabs/orchid/pull/2)

## Claim labels

- **CURRENT**: inspected in the pinned source or installed CLI help.
- **CITED**: supported by a named upstream document or issue.
- **PLANNED**: behavior awaiting review, deployment or acceptance.
- **MEASURED**: a command result recorded in the verification artifact.

## Summary

**PLANNED.** Interactive dispatch prepares environment in the dedicated pane, then calls
`herdr agent start` with the configured kind and native arguments. Goal delivery follows
confirmed registration. Failed or uncertain registration abandons automatic launch for that
inbox issue, persists that refusal, and posts a comment. The ceiling is one automatic attempt;
a new dispatch requires a new inbox issue after inspection.

## Context and current state

**CURRENT.** The pinned matrix branch claims a durable matrix receipt but launches the process
with `pane run` and `exec` in `cmd/divybot/main.go` (`spawnAgent`). Downstream goal delivery and
supervision address herdr's agent registry.

**CITED.** Harness #352 records repeated `agent_not_found` and workspace churn. That report
is the production symptom; it is not a live success measurement by this implementation.

**CURRENT.** Installed `herdr agent start --help` requires an existing pane at its interactive
shell prompt, accepts native arguments after `--`, and defines success as detection of the
expected agent in the same terminal, ready for input. `herdr --skill` distinguishes pane
process control from agent lifecycle control. NetScript MCP was queried first; its returned
public documentation did not establish a herdr registration adapter.

## Registration and evidence

**PLANNED.** Preserve the mandatory single-use matrix receipt. Persist exact pane/workspace
handles before any launch effect. Environment preparation leaves the shell alive. Derive
command rendering and registration argv from one implementation so configured model arguments
are preserved without parsing a shell string. Write `dispatched` only after registration
succeeds; any failure after location binding writes `uncertain`. No observed model/session
identity is invented from registration. Existing non-interactive run modes retain their
PR/deadline supervision and do not claim interactive registration.

## Failure ceiling and restart behavior

**PLANNED.** The existing private dispatcher state holds a launch fence before workspace
creation. State writes flush the file and its atomic replacement before permitting the effect.
A fully registered job replaces the fence in the same persisted state write. A crash or
ambiguous result retains the fence. A persistently missing agent also receives a durable
fence before tracking is removed, ending the prior unlimited respawn loop.

**PLANNED.** Retry an unavailable GitHub comment, never the failed launch. A crash after
posting but before saving notification status may duplicate the explanatory comment.
Comments use closed reason names and contain no native handles, paths or raw process output.
Cleanup targets only the workspace returned by the failed launch.

## Verification

**MEASURED.** The real busy-pane negative control returned exit 1 with `agent_pane_busy`;
cleanup returned exit 0. No model turn was launched. Fixture tests cover exact argv/location,
failed registration, malformed responses, persistence refusal, repeated restarts and comment
retry. Mutation checks remove registration and persistence independently and must fail.
See [verification](../dispatch-registration-verification.md) for commands, exits and output.

## Consequences

**PLANNED.** Failed registration remains visible and cannot spend more workspaces automatically.
The conservative ceiling can abandon a transient failure; this is preferable to relaunching
an uncertain effect. The owner/coordinator retains authority to request a new dispatch.

**CURRENT.** Orchid #1 changes PID-1 orphan reaping. It does not supply registration or
goal delivery and is not a dependency of this fix. Orchid merge authorization remains with
the owner; this source change does not claim deployment or the #316 / leaf acceptance proof.

## Revisit triggers

**PLANNED.** Revisit if herdr changes its readiness/argv contract, if an explicit authorized
retry operation is required, or if observed registration identity becomes available. Keep
those changes separate from inferred native identity, live steering and cost enrollment.
