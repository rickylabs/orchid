# Codex checkout trust — live check

PASS: existing Codex 0.164 daemon, same throwaway Git checkout, same remote TUI arguments, same per-thread trust. No screen read or consent input.

| Daemon checkout entry | Project layer | Herdr startup | Native resume |
| --- | --- | --- | --- |
| Absent | Disabled | Timeout, not ready | Pending |
| Written through config/value/write | Enabled | Ready, idle, interactive_ready=true in 3.67 seconds | Proven |
| Removed through null API write | Disabled again | Timeout, not ready | Pending |

The negative startup window was ten seconds. Empty projectless folders were rejected as ineffective controls. The proof checkout included a project config layer so folder trust was exercised. Existing native resume-record code verified successful attachment; delayed log persistence was checked once after the later negative window.

Separately, an exact dotted checkout entry was written, read back through config/read, removed, and confirmed absent. The protocol and production bridge were used without editing Codex config files by hand.

Only proof-owned workspaces were closed and proof-owned threads archived. Their exact trust entries and temporary folders were removed. Services were not restarted; no deployment occurred. Private native bodies, runtime identifiers and paths remain outside the repository.
