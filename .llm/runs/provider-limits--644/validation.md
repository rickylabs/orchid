# Validation — producer/governor 644

These results cover source and isolated fixtures, not live activation. Mandatory independent MuseSpark1.3/xhigh implementation review PASS; both branches await substantive supervisor sign-off. Gate commands are the repository-configured wrappers, not substitute scoped checks.

| Repository / gate | Exit | Result |
| --- | --- | --- |
| Harness `pnpm install --frozen-lockfile` | 0 | Dependencies installed |
| Harness `pnpm run typecheck` | 0 | Full configured stages |
| Harness `pnpm run build` | 0 | Full configured stages, including architecture/publishability/docs checks |
| Harness `pnpm test` | 0 | Full configured stages, including installed-contract checks |
| Orchid `go test -race ./...` | 0 | Final exact source, 452.298 seconds |
| Orchid `go vet ./...` | 0 | Final exact source |
| Orchid `go build ./cmd/divybot` | 0 | Final exact source |
| Orchid compiled source-trigger mutation script | 0 | 41 compiled assertion-red mutations, exact source restored |
| Orchid compiled owner endpoint mutation script without root | 0 | Seven compiled assertion-red mutations, complementary subset only |
| Orchid producer mutations | 0 | Seven compiled assertion-red mutations, exact source restored |
| Orchid configured two-UID kernel test through `sudo` | 127 | UNPROVEN: `sudo` absent |
| Orchid configured owner endpoint mutations `--root` | 1 | UNPROVEN: `sudo` absent before execution |
| Actual generated snapshot → Cockpit schema/reader/Costs/catalog/admission/auth HTTP | 0 | Fixture integration |
| Actual produced binding retirement → retained Cockpit reader and restart | 0 | Fixture integration |
| Actual CLI → offline installed contracts decoder/type declarations | 0 | Fixture installation, no publication |

The final Orchid verification worktree matches all 21 declared product paths by SHA256. It isolates compiled mutation writes from the shipping source. Earlier race failures exposed obsolete budget-policy expectations and were corrected; the final run above supersedes them. Root gates are not called green and the non-root subset does not prove two-UID behavior.

Producer mutations independently restore: a percent cap-zero veto; receipt-time replacement of native observation; account lifetime spend in a key cap; another host's native account sample; omitted retired binding rows; discarded success tombstones that clear retained global refusal; skipped native occupant recheck. All compile and fail assertions. Tests preserve delayed native observation times and reject replay/cross-clock clearance.

The integration scripts in this run accept explicit fixture and read-only Cockpit checkout arguments. The snapshot is produced by actual Orchid collector/ledger code with mocked own-key HTTP and synthetic native evidence. It proves global Codex refusal blocks picker AND admission with native account rows present, subscription 90/100 and named-key 100 warn without usage veto, authenticated Costs 200 and anonymous 401. Binding retirement preserves old scoped refusal in Costs while removing its former route binding through producer/consumer restart.

Live private settings, production processes, secret stores, contracts publication, real-provider inference, mobile sources and the canonical parent report were not changed. Live activation and installed mobile consumption remain unproven; the coordinator owns mobile645 separately. This unit references the whole feature without closing it.
