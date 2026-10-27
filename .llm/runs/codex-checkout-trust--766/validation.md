# Codex checkout trust — validation

PASS for execution gates. Independent implementation evaluation remains UNPROVEN; see implementation-eval.md. These local results are not independent certification.

| Gate | Result |
| --- | --- |
| Local Go 1.25: go vet ./... | PASS |
| Local Go 1.25: go test ./... | PASS |
| golang:bookworm: go vet ./... | PASS |
| golang:bookworm: go build ./cmd/divybot | PASS |
| golang:bookworm: go test ./... | PASS, package verdict observed |
| golang:bookworm: go test -race ./... | PASS, package verdict observed, no race reports |
| Isolated root two-UID endpoint, socket ownership and registration guards | PASS |
| Focused grant/removal, exact key quoting, malformed/overridden writes, preparation and cleanup tests | PASS |
| Live API grant/read/removal and TUI red → green → red control | PASS; live-check.md |
| gofmt and git diff --check | PASS |
| Public diff privacy scan | PASS |
| Operator-selected GLM review | UNPROVEN: expired provider credential before review |

The official bookworm image reported Go 1.27.3; Deno 2.9.7 was supplied for the repository bridge gate. The module cache was seeded before execution; go.mod has no external module dependencies. Ordinary tests ran as an unprivileged user so permission-refusal controls remained effective. The root-only CI endpoint subset ran separately in the same isolated, owned container. Go test JSON output provided explicit package verdicts for both normal and race runs, and the gate container exited zero.

Reconciliation: a fresh fetch still resolves upstream main and the branch merge-base to `3f82fcd612fc20ea5513747ba7bf0ab063eb5787`. Functional changes were tested at `a86874c`; subsequent changes are comments and run artifacts. Changed files match the declared surface plus the test-fixture expansion recorded in drift.md. No assignment file, private host path, endpoint, native runtime identifier or credential is committed. The draft was opened early and progress was pushed in separate commits. No remote CI was polled.

No architectural debt was accepted. The outstanding review gate requires an independent reviewer before merge. Merge and deployment remain coordinator-owned.
