# Supplemental protocol review disposition

The bounded protocol review returned FAIL_FIX (z-ai/glm-5.3-flash, exact observed model and response identity, exit 0). This is a real verdict, not a passing gate. It reviewed the adapter only, not the full feature.

- D1 accepted and fixed: `goalRPC.set` discards updates already observed before sending the write. `TestNativeGoalFreshWriteNotification` rejects a stale exact match and accepts a fresh one. Mutation 121 removes the reset.
- D2 retained as an explicit fail-closed boundary: server-initiated requests are outside this restricted get/set client and fail correlation. No request is answered or mistaken for a goal response. A distinct unsupported-server-request diagnostic is optional follow-up, not authorization to widen capabilities.
- D3 not reproduced: inspected Go standard library `src/os/exec/exec.go`, `Cmd.Start` failure defer closes `parentIOPipes`; this is the actual implementation used by the tests. Start failure does not rely solely on finalizers. The existing start-failure control passes.
- D4 out of scope for the asserted boundary: native identity, objective and budget travel only over stdin. The existing Host transport supplies its configured home in the remote shell environment. The RFC does not claim arbitrary execution configuration is absent from process argv. No operator value is published.
- D5 acknowledged bounded diagnostic: `goal-source-closed-or-frame-limit` explicitly reports the unresolved distinction, while native daemon refusals have separate reasons. It never reports a successful write. More granular EOF/read/frame-limit diagnostics may be added separately.
- D6 no invented negotiation: the inspected installed InitializeResponse contract provides userAgent; it does not echo experimental capability acceptance. The native stdio protocol accepts these JSONL envelopes without a jsonrpc member. Do not add a refusal on fields the real contract does not publish; unsupported goal methods refuse through the daemon error path.

The next formal feature review receives these dispositions and the repaired code. No live proof is claimed.
