# Resume orphan reaper

Implementation and local validation are complete for Orchid PR https://github.com/rickylabs/orchid/pull/129 on fix/orphan-reaper. Stop at READY; the coordinator owns merge/deployment. Tracking issue https://github.com/rickylabs/harness/issues/765 remains open.

Cause at baseline 6e384a6dbe87064068fc4f895174476e1fc95296: reaper.go:58 skips all sweeps under any active hold; herdr_events.go:209 holds through the long-lived forward. The baseline kernel starvation regression was red before production edits. The owned PID helper and positive-PID orphan collector make it green, while preserving os/exec-owned exit statuses and pipe completion. All twelve production constructors use commandContext.

Go vet/build/full test/full race pass in golang:bookworm. Each full suite records 2,976 test/subtest passes and six explicit environment-gated skips; see gate-evidence.md. All 16 compiled mutants fail assertions, with 16 restored green controls. The actual Cmd.Wait removal is included. Separate-session read-only Codex plan and implementation reviews PASS under the operator's explicit routing override; cross-vendor certification is not claimed.

Current main remains the original baseline; no conflicts. All 210 Go files match the full race gate tree. The disposable gate container was removed, with no foreign resource mutations. No deployment or merge occurred. No accepted architectural debt.

The canonical final report uses the atomic .divybot-final-report.md handoff in the original dispatch checkout, followed by a read-back verification on the tracking issue. Raw gate/reviewer logs and operational memory remain in the private project run directory. Public durable evidence is in this run directory.
