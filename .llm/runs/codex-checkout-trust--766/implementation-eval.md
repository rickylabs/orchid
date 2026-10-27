# Codex checkout trust — independent implementation evaluation

UNPROVEN: the requested GLM evaluator could not execute. No PASS is claimed.

The exact requested OpenCode command was attempted with stdin closed. Installed OpenCode 2.1.2 rejects the 1.x `--variant` flag. Its supported equivalent was then invoked as `opencode run --standalone -m opencode-go/glm-5.3-flash#max --agent plan --auto`, with edits denied and a separate private database. The provider refused it because the API key expired, before any substantive review. Stored credentials match the environment credential; unsetting the environment would not change that refusal.

No review findings or fix round exist, and no unrequested model was substituted. The PR must disclose this missing independent gate; local tests and native live proof are execution evidence, not independent certification. External review is required before merge. The agent will not merge or deploy.

## Coordinator-supplied Opus review

CHANGES at de9b9f9. The independent reviewer ran Go vet and the full package suite successfully, but found that non-Codex cleanup failures incorrectly block teardown, several exit paths can leak trust, and grants accept checkouts cleanup rejects. The coordinator explicitly authorized the repair and tests on this PR. No PASS is claimed for the repaired implementation yet. The original GLM credential failure remains historical evidence, not the reason to stop this authorized repair.
