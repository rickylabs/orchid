# Codex checkout trust — context

Implementation and execution gates are complete. PR: https://github.com/rickylabs/orchid/pull/128, branch `orch/divybot-6442619662`, tracker https://github.com/rickylabs/harness/issues/766. The PR is to be marked READY after this evidence commit, with independent review still explicitly unproven.

The old process-only trust flag and per-thread config did not satisfy the remote TUI's daemon config lookup. `codex_checkout_trust.go` now owns the exact project-entry API grant and null removal. Preparation grants on the existing canonical connection before Herdr startup; checkout cleanup revokes before deletion; failed startup attempts bounded rollback. Per-thread trust remains for creation, and remote CLI trust is removed.

Local Go 1.25 vet/full tests and golang:bookworm vet/build/full/race tests PASS; root-only endpoint guards PASS. The same disposable Git checkout went timeout/no resume → ready/proven resume → timeout/no resume as daemon trust was added and removed. All proof-owned workspaces, threads, folders and trust entries were cleaned up. See validation.md and live-check.md.

The mandated separate GLM review could not execute because its provider credential expired. Installed OpenCode 2.x uses the supported #max model suffix; no alternate model or independent PASS was substituted. See implementation-eval.md. Independent review is required before merge.

Baseline remains upstream main `3f82fcd612fc20ea5513747ba7bf0ab063eb5787`. Functionality was tested at `a86874c`; later changes only update comments and run artifacts. Merge and deployment belong to the coordinator. Publish the final report through the excluded assignment file handoff, confirm the visible issue comment, and leave the tracker open.
