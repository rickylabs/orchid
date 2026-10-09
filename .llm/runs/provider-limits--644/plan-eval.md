**Verdict: PASS.** All four round-one blockers are resolved in the amended plan.md, and the dispositions match what is written there.

**Blocker check**

- **Clock domains:** Decision 4 now requires refusal and success timestamps from the same verified native event clock, adds a private ingestion sequence and event fingerprint against replay, and ignores cross-clock successes for clearance. Resolved.
- **Tombstone wedge:** Success tombstones now carry the Cockpit 542 contract meaning. Capacity is reserved against the finite configured inventory at configuration time, so runtime churn cannot fill the ledger. Over-capacity configurations are rejected rather than fencing dispatch later. Compaction archives only inactive succeeded scopes and installs permanent replay fences. The remaining overflow fence covers storage failure only, which is correct fail-closed behavior. Resolved.
- **Budget-veto default:** The owner has explicitly authorized the source default change in decision 6, and the charter distinction between settings activation and authorized source behavior is recorded. This is now a recorded owner decision, not an unilateral agent change. S4 retains budget-veto mutation regressions. Resolved.
- **Operator import:** Removed entirely in decision 5. Unsupported sources report unknown. Resolved.

**Non-blocking items confirmed**

- Ledger directory 0700 and O_EXCL 0600 temp files recorded in decision 1.
- Snapshot path named and stated as not consumed by any live process in this task.
- Warning constant marked advisory-only in decision 6.
- Orchid race baseline and Harness build baseline recorded in S5.

**Carry into implementation, not a plan blocker**

- Decision 3 does not yet state in words that environment-variable names never appear in the public snapshot or diagnostics. Add that as an S3 fixture assertion when writing the collector.
- S2 should include the round-one test case: a success with a later native time but earlier ingestion sequence must not clear a refusal.

The plan may proceed to the first slice: design commit and draft Harness PR.