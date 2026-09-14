# Matrix before spawn — implementation evidence

2026-09-14: implemented the independently reviewed Harness dispatcher plan in Orchid. The source baseline is d344bd037bcf10150fd12daef8ffa277576cd94a. [Plan gate and owner disposition](https://github.com/rickylabs/harness/pull/339); [independent plan review](https://github.com/rickylabs/harness/issues/347#issuecomment-5661595770). Scope: dispatcher source, bridge/tests, reference documentation, CI, and the ignore rule needed to track new dispatcher files. No deployment.

Owner decisions: evaluator launches refuse without a real observed model/session contract; no observer now. Pins skipping the normal selected route require the existing trusted owner override. Pins are configurable values. NetScript documents the override grant but no default configurable pin set was established; no default set is invented.

Validation: Go 1.27.1, Deno 2.9.6. `go test -race ./...`, `go vet ./...` and dispatcher build passed. The initial local test invocation could not execute on the scratch filesystem; rerunning with executable scratch completed the tests. The real bridge resolved read-only against clean NetScript f3324909e0896cedc9729005bac5f508e122d6c6. No provider was called and no agent launched for that probe.

Negative controls removed the actual common-attempt resolver and persistence calls separately. Both caused semantic test failures, not compilation failures. Restoring each call returned the gate to green.

Harness PR #342's actual CLI checked an emitted synthetic receipt: unknown observations returned unproven/2, matching synthetic observations pass/0, and a synthetic model mismatch fail/1. This establishes schema compatibility only. It is not a live observation receipt or an independent implementation certification.

Independent source review and supervisor sign-off remain pending. The evaluator refusal is preserved; this run does not launch an evaluator through a legacy bypass to get around it. The source remains a draft. A separately evidenced model/session review contract and controlled live acceptance are required before certification or deployment.
