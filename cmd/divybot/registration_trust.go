package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Plain Codex trusts only this dispatch checkout for its process. Remote Codex
// drops this override and uses codex_checkout_trust.go for daemon trust. Codex
// 0.159.3 shows folder consent even with approvals/sandbox bypassed. Its -c
// dotted-key parser splits literal dots, so the path belongs in an inline TOML
// table value, not in a quoted dotted key. No user config file is changed.
func managedInteractiveAgentArgs(agent string, o Overrides, cwd string) (string, []string, error) {
	kind, args, err := interactiveAgentArgs(agent, o)
	if err != nil || kind != "codex" {
		return kind, args, err
	}
	if !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd || cwd == "/" || len(cwd) > 4096 || !utf8.ValidString(cwd) || strings.IndexFunc(cwd, unicode.IsControl) >= 0 {
		return "", nil, errAgentRegistration
	}
	// JSON basic-string escapes are valid TOML escapes for these validated paths.
	quoted, _ := json.Marshal(cwd)
	args = append(args, "-c", "projects={"+string(quoted)+`={trust_level="trusted"}}`)
	// The TUI runs its interactive update prompt before it connects to any
	// app-server. A client pinned behind the latest release would wait there
	// forever: no daemon attach, no session, no registration.
	args = append(args, "-c", "check_for_update_on_startup=false") // guard:skip-update-prompt
	return kind, args, nil
}

// Classify startup from native Herdr errors. Screen text cannot refine it.
func (h Host) registrationStartFailure(ctx context.Context, output, agent, label, cwd, pane, ws string) error {
	failure := registrationFailure(output, ctx.Err())
	if agent == "agy" && registrationFailureKind(failure) == "startup_timeout" && ctx.Err() == nil {
		if blocked := h.agyRegistrationCheck(ctx, label, cwd, pane, ws, false); registrationFailureKind(blocked) == "startup_blocked" {
			return blocked
		}
		return failure
	}
	return failure
}
