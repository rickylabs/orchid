# RFC 0001: Native dispatch binding and goals

- Status: proposed; live dispatch gate pending
- Date: 2026-09-16
- Scope: divybot Codex dispatch binding, native goal creation and lifecycle writes
- Evidence snapshot: Orchid `78899f493a36431690f897478d588b0aa6f6e82e`; Harness `13aa853bf46acc0f879dfaeaa827f0c3cbd4bbad`; NetScript `f3324909e0896cedc9729005bac5f508e122d6c6`; Codex CLI `0.154.0`; herdr integration contract `18061191fdc019498610aee81f0df93f6c2ebd31`
- Delivery tracking: [Harness #381](https://github.com/rickylabs/harness/issues/381). Orchid issues are disabled; the implementation PR references that issue.

## Claim labels

Every material statement is classified as CURRENT (inspected baseline source), CITED (external contract), PLANNED (this change or its rollout), or MEASURED (an executed check). An incomplete measurement is not a passing gate.

## Summary

**PLANNED.** Divybot remains the dispatcher. After successful prompt delivery, it obtains the official native session report for the already registered Codex occupant, writes the existing private NativeSessionID binding and creates the native goal. Authorized assignment `max-tokens` supplies the budget; absence stays null. Lifecycle changes update status only.

**PLANNED.** No replacement launcher, guessed native identity, fabricated parent or route-derived token budget is introduced. Harness PR #383's read-only transport and refusals remain unchanged.

## Context and current state

**CURRENT.** Registration reads the integration report before first-prompt delivery, writes any available identity, and never attempts a post-prompt binding: [baseline spawnAgent](https://github.com/rickylabs/orchid/blob/78899f493a36431690f897478d588b0aa6f6e82e/cmd/divybot/main.go#L608). Prompt delivery happens later in `Coord.spawn`. Thus a report arriving after the registration read is missed permanently. This explains a timing vulnerability, not proof that timing is the only live cause.

**CITED.** The official herdr Codex integration uses native SessionStart stdin and reports the session through IPC; it does not extract identity from screen text. See the [pinned integration](https://github.com/herdrdev/herdr/blob/18061191fdc019498610aee81f0df93f6c2ebd31/src/integration/assets/codex/herdr-agent-state.sh). Divybot already validates its source, kind and registered occupant in `native_identity.go`.

**CURRENT.** The Harness routing contract excludes the budget and assigns it to the launcher: [resolve.ts](https://github.com/rickylabs/harness/blob/13aa853bf46acc0f879dfaeaa827f0c3cbd4bbad/packages/routing/src/resolve.ts#L243). The coordinator explicitly authorized the existing `/swarm max-tokens` assignment value for native goals; no separate route-budget policy is added.

**MEASURED.** The read-only discovery census found 133 native child records among 421 threads, all with resolving parents and depth. One child returned the exact matching parent through thread/read. Flattened thread/list fields did not expose those parents. This is evidence that native spawning already records ancestry, not evidence of issue linkage. The write-side fix supplies the root binding that existing telemetry joins to native ancestry.

## Identity and ownership

**PLANNED.** The post-prompt read accepts only the official native ID report for the exact original registration and a receipt still marked dispatched. Missing reports get bounded retries; invalid reports refuse. Pane, workspace and name check the report's occupant; none becomes a native identity. A failed persistence operation never authorizes a native goal write. Existing private ownership and 0700/0600 modes are retained.

**PLANNED.** The dispatcher job stores an opaque receipt key and expected goal intent. Native IDs remain in the existing private binding and in memory. The writer sends them over stdin only. Process stderr and raw daemon error messages are never copied to diagnostics.

**PLANNED.** Goal creation requires a null goal. Any existing goal, including identical intent, refuses creation. The dispatcher earns lifecycle ownership only after a matching response and updated notification and successful local state persistence. A crash in this interval remains inconclusive; automatic adoption or replacement would require a separate reconciliation contract.

**CITED.** Native goal setters do not expose a compare-and-set revision in the inspected protocol. A preflight null read is not a cross-client transaction. **PLANNED.** This hook owns initialization of a freshly dispatched thread; concurrent external goal editing during that initialization is outside that ownership contract. There is no claim of atomic exclusion against arbitrary external writers.

## Objective and budget

**PLANNED.** The objective is the assignment repository/issue reference plus exact title, within the native length limit. The complete task continues through existing prompt delivery. Objective text is private runtime/state data and is omitted from public evidence and logs.

**PLANNED.** Budget parsing uses exact decimal arithmetic. Accepted syntax is an unsigned decimal number with optional k/K or m/M suffix, with multipliers 1000 and 1000000. The result must be a whole token count within JavaScript's safe-integer range. An absent key sends explicit null; numeric zero remains zero. Explicit empty, negative, fractional-token, overflowing or unsupported input refuses before launching. No budget is inferred from effort, provider quota or concurrent-job capacity.

## Lifecycle and notification evidence

**CITED.** The native goal API provides active, paused, blocked, usageLimited, budgetLimited and complete. Replacing objective text can reset accounting: [app-server goal contract](https://learn.chatgpt.com/docs/app-server).

**PLANNED.** Creation sends threadId, objective, tokenBudget and active. Later writes send only threadId and status and require current intent to match the saved assignment. Response identity and notification must match, and lifecycle responses must not regress counters or replace the original creation identity.

| PLANNED source event | Action |
| --- | --- |
| Confirmed prompt, binding and absent goal | Create active goal |
| Explicit GitHub CLOSED/COMPLETED | Complete an owned goal |
| Explicit CLOSED/NOT_PLANNED or configured dispatch deadline reached | Pause, preserving native complete or limit states |
| Dispatcher blocked or abandonment event | Block only an active owned goal |
| Native budgetLimited or usageLimited | Preserve the native verdict; do not infer it from timeout or subscription quota |
| Idle/done pane, one merged PR, unknown issue state | No completion verdict |
| Missing binding, unsupported native contract, uncertain creation | Unavailable/inconclusive with a closed reason |

**PLANNED.** An updated notification on the writer's actual stdio stream is required for write confirmation. This is not a claim that independent app-server processes broadcast all changes to one another; the live proof must state which stream was observed.

## Consequences

**PLANNED.** Unbound dispatches remain visible through the existing observation contract. A failed goal write can coexist with a successfully bound dispatch: ancestry is then readable while native goal fields remain unavailable until their source reports values. No blended cost or fabricated zero is introduced.

**PLANNED.** Legacy jobs without writer ownership are not silently adopted. Native goal availability depends on the official integration and app-server contract on the execution host. Closed reasons distinguish missing identity, malformed evidence, daemon refusal, transport failure, pre-existing goals and ownership mismatch.

## Validation and rollout

**MEASURED.** `go test ./... -count=1` and the dispatcher build pass. The new process client holds the existing child-reaper gate throughout its process lifetime. The mutation suite currently records 121 distinct killed mutations, zero survivors, 121 restored controls and zero broken controls. Historical failed/invalid attempts remain recorded rather than being discarded. Exact commands, exit codes and output are in `.llm/runs/native-dispatch-goals--381/`. The real-dispatch verdict remains INCONCLUSIVE: deployment connectivity has not been supplied, so no new dispatch or native goal write has been attempted.

**PLANNED.** One explicitly created issue will exercise the authorized dispatcher path. Before the trigger label, read back the entire brief and announce its issue and lane. Verify its new private binding, active goal and authorized budget, actual updated notification, status-only transition and restoration to the initial null goal. Never substitute an unrelated native thread.

**PLANNED.** Deploy only through an authorized host connection and restart only the dispatcher. Keep the agent container running. Whole-diff leak scanning, mutation controls and the real-dispatch proof precede the PR.

## Revisit triggers

**PLANNED.** Revisit if native SessionStart identity/schema changes; a compare-and-set goal API becomes available; a durable reconciliation policy is approved; native goals become authoritative across multiple concurrent writers; or the owner assigns token budgets to the routing contract instead of assignments. The reader's flattened ancestry projection discrepancy remains a separate read-side concern.
