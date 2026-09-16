# Findings

The absence of flattened ancestry in `thread/list` does not establish absence of native ancestry. The goal write hook belongs to Orchid; a route token ceiling and a receipt-bound live proof are still unresolved prerequisites.

## Claim labels

- CURRENT: directly inspected source at the pinned revisions in `supervisor.md`.
- CITED: retrieved official documentation or installed generated protocol contract.
- MEASURED: the read-only native census or private receipt inspection in this run.
- PLANNED: design proposals, not shipped behavior.

## Evidence

1. **MEASURED:** a bounded read-only census found 421 threads, zero flattened `parentThreadId` values in database-only `thread/list`, and 133 native `source.subAgent.thread_spawn` records. All 133 carry a parent and depth, every parent resolves in the listing, and the maximum native depth is one. A `thread/read` on one of those children returns a parent exactly matching its native spawn record. See `native-source-census.json`. This is a store-wide census, not proof of assignment linkage for an issue.
2. **CURRENT:** the reader currently projects only `t.parentThreadId`; it does not read the source ancestry or hydrate from `thread/read`: `packages/telemetry/src/codex-thread-project.ts:36` and `packages/telemetry/src/codex-threads.ts:48`. Thus this census contradicts the causal claim in #381 that no native ancestry is written. It does not authorize invented ancestry or a replacement launcher.
3. **CURRENT:** Harness declares that the caller owns the budget and excludes it from routing output: `packages/routing/src/resolve.ts:243`. The existing assignment budget is an uninterpreted `DispatchRequest.maxTokens` string, including spellings such as `500k`: `packages/subagents/src/dispatch.ts:94`.
4. **CURRENT:** the inspected NetScript `ResolvedDelegationRoute` has no token-budget field: [.llm/tools/agentic/runtime/routing-policy.ts](https://github.com/rickylabs/netscript/blob/f3324909e0896cedc9729005bac5f508e122d6c6/.llm/tools/agentic/runtime/routing-policy.ts#L101). Neither that file nor the typed delegation matrix contains `tokenBudget`, `token_budget`, `maxTokens`, or `max_tokens`. This is a claim about those contracts, not all NetScript behavior. MCP guidance and documentation search did not identify a goal-write adapter.
5. **CURRENT:** Orchid owns registration and receives the authoritative native identity. Its `spawnAgent` persists that identity only after dispatch succeeds: [main.go](https://github.com/rickylabs/orchid/blob/78899f493a36431690f897478d588b0aa6f6e82e/cmd/divybot/main.go#L608). Its current `matrixRoute` contains routing identity but no token ceiling. Assignment `Overrides.MaxTokens` is a separate string in [overrides.go](https://github.com/rickylabs/orchid/blob/78899f493a36431690f897478d588b0aa6f6e82e/cmd/divybot/overrides.go#L47).
6. **MEASURED:** all three readable dispatched records in the configured private store, for issues 368, 375 and 378, lack `NativeSessionID`. No receipt contents, native identifiers or storage locations are retained. This says nothing about another deployment's store. It prevents this lane from safely selecting an issue-bound thread using these receipts.
7. **CITED:** installed experimental app-server protocol has `thread/start`, `thread/resume` and `thread/fork`, but no sub-agent spawn RPC. `thread/fork` records `forkedFromId`, which is not parent ancestry. Native children carry `source.subAgent.thread_spawn.parent_thread_id` and `depth`. Official [sub-agent documentation](https://learn.chatgpt.com/docs/agent-configuration/subagents) describes native in-agent orchestration. The measured native records prove that path is already used in this store.
8. **CITED:** [app-server goal documentation](https://learn.chatgpt.com/docs/app-server) documents goal set/get/clear, the six statuses, and accounting reset when replacing an objective. Status-only transitions must omit objective and budget. Replacing a non-null goal is not a generally reversible proof because the setter does not restore arbitrary usage counters.
9. **CURRENT:** Orchid's supervisor treats `done` as potentially between turns, and a merged PR can trigger further work. Neither implies assignment completion: [main.go](https://github.com/rickylabs/orchid/blob/78899f493a36431690f897478d588b0aa6f6e82e/cmd/divybot/main.go#L2825). A timeout is not proof of token-budget exhaustion; the governor's concurrency/headroom admission is not a token ceiling.

## Consequence

PLANNED: preserve the existing native spawn path; separately diagnose the reader's missing parent projection. Implement goal writes only against an authoritative dispatch binding, with a budget from a confirmed authority and state transitions from actual lifecycle events. Never use pane identity, recency, a fork source, a concurrency allowance or an arbitrary native thread as a substitute.
