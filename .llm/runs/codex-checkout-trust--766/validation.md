# Codex checkout trust — final repair validation

PASS for all execution gates on the coordinator-requested repair. Independent Opus 5.5 review at de9b9f9 was CHANGES; its blockers are addressed. The repaired head is ready for coordinator review, and no independent PASS is claimed by the generator.

| Gate | Result |
| --- | --- |
| Local Go 1.25 vet and full tests | PASS |
| Official golang:bookworm vet and build | PASS |
| Bookworm full normal suite | PASS, explicit package verdict, no failed tests |
| Bookworm full race suite | PASS, explicit package verdict, no race reports |
| Root two-UID endpoint, ownership and registration subset | PASS |
| Compiled checkout-trust mutation gate | Green control; 16/16 compiled guard removals fail behavioral assertions |
| Live production reconciliation helper | PASS: orphan removed, tracked grant retained, then removed after release |
| Repeated live TUI control on a canonical issue checkout | PASS: missing trust red, API trust ready/proven resume, removal red again |
| Formatting, diff and privacy checks | PASS |
| Proof resource cleanup | All proof threads archived, directories removed and native trust entries absent |

The official bookworm image reported Go 1.27.3 with Deno 2.9.7. Its module cache was seeded before execution; Go has no external module dependencies here. Ordinary tests ran unprivileged, and the root-only endpoint subset ran separately in the owned gate container. The gate container exited zero. The mutation gate is now a CI step and changes only isolated source copies.

Focused coverage includes invalid-checkout skips and ordinary deletion failures through the production teardown caller, fatal Codex revoke classification, immediate bounded-release revoke, startup and actual tick recovery, failed removal retry, unsaved grants, retained per-host jobs, ownership boundaries and shared positive checkout validation. A native client-selection fixture proves that a comment job key can differ from the source issue number. See review-fixes.md for the review dispositions.

The live recovery test uses a byte-identical copy of the production helper and only a throwaway owned root. Native config is its recovery evidence. The repeated TUI proof uses the same valid issue checkout and arguments across all three states; project layers change disabled → enabled → disabled. No screen is read or answered.

Functional repair was tested at 34c65aa88e968cd8aa3548cfaaa1c49045f1f745; the final evidence commit changes only run artifacts. Fresh upstream main and merge-base remain 3f82fcd612fc20ea5513747ba7bf0ab063eb5787. No remote CI was polled. No merge, deployment or service restart occurred, and the tracker stays open.
