package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Trust only the checkout prepared by this dispatch, for this process. Codex
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
	return kind, args, nil
}

func codexStartupTrustDialog(screen string) bool {
	if len(screen) > 64*1024 {
		return false
	}
	text := strings.Join(strings.Fields(screen), " ")
	for _, marker := range []string{"Folder access", "Trust this folder?", "1. Trust and continue", "2. Quit", "enter continue"} {
		if !strings.Contains(text, marker) {
			return false
		}
	}
	return true
}

// The current Herdr manifest misses a line-wrapped trust dialog and returns a
// timeout. Read only the exact owned Codex occupant; never send keys or retry.
// Keep cancellation/busy/native blocked errors and uncertain screens unchanged.
func (h Host) registrationStartFailure(ctx context.Context, output, agent, label, cwd, pane, ws string) error {
	failure := registrationFailure(output, ctx.Err())
	if agent != "codex" || registrationFailureKind(failure) != "startup_timeout" || ctx.Err() != nil {
		return failure
	}
	before, err := h.agentInfoOf(ctx, pane)
	if err != nil || !startupCodexOccupant(before, label, cwd, pane, ws) {
		return failure
	}
	screen, err := h.visiblePromptScreen(ctx, pane)
	if err != nil || !codexStartupTrustDialog(screen) {
		return failure
	}
	after, err := h.agentInfoOf(ctx, pane)
	if err != nil || !startupCodexOccupant(after, label, cwd, pane, ws) || before.Name != after.Name || before.StateChangeSeq != after.StateChangeSeq {
		return failure
	}
	return &agentRegistrationFailure{kind: "startup_blocked"}
}

func startupCodexOccupant(a AgentInfo, label, cwd, pane, ws string) bool {
	// Herdr releases the pending name at timeout, but preserves the pane occupant.
	return a.Agent == "codex" && a.PaneID == pane && a.WorkspaceID == ws && a.Cwd == cwd && (a.Name == label || a.Name == "") && a.AgentStatus == "unknown"
}
