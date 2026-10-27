# Codex checkout trust — context

Implement tracker rickylabs/harness#766 in Orchid. Baseline `3f82fcd`; branch `orch/divybot-6442619662`. The remote TUI consults daemon config despite process/thread trust overrides. Use the native config API on the existing connection; revoke the exact checkout on cleanup. Only synthetic data belongs in committed evidence. Draft PR early; stop at READY without deploying or merging. Final report is handed to Orchid through the excluded file in the original assignment checkout.

First implementation and focused tests complete. Remaining: full container gates, live native readiness positive/negative control, GLM blocker review, final PR evidence and report publication.
