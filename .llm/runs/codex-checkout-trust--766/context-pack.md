# Codex checkout trust — round 4 in progress

Existing PR: https://github.com/rickylabs/orchid/pull/128. Tracking issue: https://github.com/rickylabs/harness/issues/766. Baseline: 7f4b4371b6294f4d886b5f202bdb359866edadc7.

This round fixes only the coordinator's adopted-cwd deletion blocker. Adoption records Workdir only for a known host, canonical positive issue checkout matching the internal job key, and exact direct parent in checkoutTrustRoots. All other observations leave Workdir empty; raw fleet trust retention is unchanged. The existing legacy cleanup fallback is unchanged.

Tests use disposable paths, isolate fleet discovery, reload state, and invoke real teardown cleanup while recording rm arguments. They cover foreign/nested paths, valid disk/legacy roots, wrong issue keys, unclean paths, invalid roots and unknown hosts. The mutation wrapper requires both foreign/nested cases to go assertion-red on direct-parent guard removal, and independently removes canonical-path and job-key checks.

Initial focused adoption tests pass. Bookworm vet/build/full normal/full race and the configured mutation wrappers are running. No final gate verdict yet. Prior round validation remains historical in round3-gate-summary.json and validation.md.

Coordinator review remains pending; no external reviewer is requested and no independent PASS is claimed. Stop at READY without merge, deployment or service restart. The canonical report uses the assignment checkout's excluded final-report file and requires checking accepted publication on issue 766.
