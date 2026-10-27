# Codex checkout trust — context

Implement tracker rickylabs/harness#766 in Orchid. Baseline `3f82fcd`; branch `orch/divybot-6442619662`. The remote TUI consults daemon config despite process/thread trust overrides. Use the native config API on the existing connection; revoke the exact checkout on cleanup. Only synthetic data belongs in committed evidence. Draft PR early; stop at READY without deploying or merging. Final report is handed to Orchid through the excluded file in the original assignment checkout.

First implementation and focused tests complete. Remaining: full container gates, live native readiness positive/negative control, GLM blocker review, final PR evidence and report publication.

Live API and TUI controls passed; see live-check.md. Focused regressions passed after updating the native client-selection fixture. The independent GLM gate is unproven because the provider key expired before review (implementation-eval.md). Full local/image suites are running; record their actual results before marking PR 128 ready. All proof-owned threads/workspaces/checkout trust were cleaned up.
