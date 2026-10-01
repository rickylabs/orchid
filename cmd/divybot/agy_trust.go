package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const agySettingsUnreadable matrixReason = "agy-settings-unreadable"

// Only this named pre-workspace site proves that no native agent was started.
func agySettingsBlocked(err error) bool {
	return errors.Is(err, agySettingsUnreadable) && matrixCause(err) == "spawn.agy-trust"
}

// Report permissions without reading or exposing settings or credential bytes.
// Missing/non-regular optional state retains the existing preparation checks.
func (h Host) agyUnreadableFile(ctx context.Context, path string) (bool, error) {
	script := "if test -f " + shq(path) + "; then if test -r " + shq(path) +
		"; then printf 'readable'; else printf 'unreadable'; fi; else printf 'unavailable'; fi"
	raw, err := h.agyTrustCommand(ctx, script)
	if err != nil {
		return false, matrixReason("agy-settings-unavailable")
	}
	switch string(raw) {
	case "unreadable":
		return true, nil
	case "readable", "unavailable":
		return false, nil
	default:
		return false, matrixReason("agy-settings-unavailable")
	}
}

// AGY 1.2.14 uses exact-path trustedWorkspaces entries. Its registered hidden
// gemini_dir/app_data_dir flags resolve CLI settings independently of HOME.
// Scope CLI settings to this launch and reference the existing native OAuth
// credential file. Never copy credentials, modify standing settings or
// substitute defaults when settings cannot be read.
func (h Host) prepareAGYTrust(ctx context.Context, cwd string) ([]string, error) {
	bad := matrixReason("agy-settings-unavailable")
	if !agyTrustScope(cwd) {
		return nil, bad
	}
	gemini := filepath.Join(h.agentHome(), ".gemini")
	root := filepath.Join(cwd, ".divybot-agy")
	relative, err := filepath.Rel(gemini, root)
	if err != nil || !filepath.IsAbs(gemini) || filepath.Clean(gemini) != gemini || !cleanText(gemini) {
		return nil, bad
	}
	// Read-only, bounded, on the dispatch host. Reject a symlink spelling of
	// the checkout so the typed trust entry matches the native canonical path.
	canonical, err := h.agyTrustCommand(ctx, "cd "+shq(cwd)+" && pwd -P 2>/dev/null")
	if err != nil || strings.TrimSuffix(string(canonical), "\n") != cwd {
		return nil, bad
	}
	settings := filepath.Join(gemini, "antigravity-cli", "settings.json")
	credential := filepath.Join(gemini, "antigravity-cli", "antigravity-oauth-token")
	onboarding := filepath.Join(gemini, "antigravity-cli", "cache", "onboarding.json")
	for _, path := range []string{settings, onboarding} {
		unreadable, err := h.agyUnreadableFile(ctx, path)
		if err != nil {
			return nil, err
		}
		if unreadable {
			return nil, agySettingsUnreadable
		}
	}
	raw, err := h.agyTrustCommand(ctx, "exec head -c 1048577 "+shq(settings)+" 2>/dev/null")
	if err != nil {
		return nil, bad
	}
	merged, err := agyScopedSettings(raw, cwd)
	if err != nil {
		return nil, err
	}
	// Native 1.2.14 NewCLITokenStorage/token_storage.go:60 resolves this token
	// beneath app_data_dir. Its read/refresh use os.ReadFile/os.WriteFile, so a
	// symlink preserves the native credential store without copying its bytes.
	// Other native authentication mechanisms retain their existing inputs;
	// absence of this optional file never substitutes credentials or signs in.
	// mkdir, without -p, is the exclusive ownership boundary. Never follow a
	// repository-provided file/symlink or reuse state from an earlier launch.
	script := "umask 077; test ! -e " + shq(root) + " && test ! -L " + shq(root) +
		" && mkdir " + shq(root) + " && printf '%s' " + shq(string(merged)) +
		" > " + shq(filepath.Join(root, "settings.json")) +
		" && " + agyFileReference(credential, filepath.Join(root, "antigravity-oauth-token")) +
		" && mkdir " + shq(filepath.Join(root, "cache")) +
		" && " + agyFileReference(onboarding, filepath.Join(root, "cache", "onboarding.json")) +
		" && printf '\\n.divybot-agy/\\n' >> " + shq(filepath.Join(cwd, ".git", "info", "exclude"))
	if _, err := h.agyTrustCommand(ctx, script); err != nil {
		return nil, bad
	}
	return []string{"--gemini_dir", gemini, "--app_data_dir", relative}, nil
}

func agyTrustScope(cwd string) bool {
	return filepath.IsAbs(cwd) && filepath.Clean(cwd) == cwd && cwd != "/" && len(cwd) <= 4096 &&
		utf8.ValidString(cwd) && strings.IndexFunc(cwd, unicode.IsControl) < 0
}

// The fixed native onboarding cache also lives below app_data_dir (1.2.14
// server_cache.go:87). Preserve it by reference, never invent completion flags
// or copy a global cache/history tree into this launch.
func agyFileReference(source, destination string) string {
	return "if test -e " + shq(source) + " || test -L " + shq(source) + "; then test -f " + shq(source) +
		" && test -r " + shq(source) + " && ln -s " + shq(source) + " " + shq(destination) + "; fi"
}

func (h Host) agyTrustCommand(ctx context.Context, script string) ([]byte, error) {
	if h.isLocal() {
		return matrixCommand(ctx, "", "bash", nil, "-c", script)
	}
	return matrixCommand(ctx, "", "ssh", nil, append(h.sshBase(), h.SSH, script)...)
}

func agyScopedSettings(raw []byte, cwd string) ([]byte, error) {
	var settings map[string]json.RawMessage
	if len(raw) == 0 || len(raw) > 1024*1024 || !utf8.Valid(raw) || strictJSON(raw, &settings) != nil || settings == nil {
		return nil, matrixReason("agy-settings-unavailable")
	}
	if previous, exists := settings["trustedWorkspaces"]; exists {
		var paths []string
		if json.Unmarshal(previous, &paths) != nil || paths == nil {
			return nil, matrixReason("agy-settings-unavailable")
		}
	}
	settings["trustedWorkspaces"], _ = json.Marshal([]string{cwd})
	return json.Marshal(settings)
}

func agyStartupBlocker(screen string) bool {
	if len(screen) == 0 || len(screen) > 64*1024 || !utf8.ValidString(screen) {
		return false
	}
	text := strings.Join(strings.Fields(screen), " ")
	trust := []string{"Accessing workspace:", "Do you trust the contents of this project?", "requires permission to read, edit, and execute files here.", "Confirm", "n / esc"}
	login := []string{"Welcome to the Antigravity CLI.", "not signed in.", "Select login method:", "1. Google OAuth", "2. Use a Google Cloud project", "enter Select"}
	onboarding := []string{"Welcome to Antigravity CLI!", "Choose your color scheme:", "colorblind-friendly light"}
	for _, markers := range [][]string{trust, login, onboarding} {
		found := true
		for _, marker := range markers {
			found = found && strings.Contains(text, marker)
		}
		if found {
			return true
		}
	}
	return false
}

// Installed 1.2.14 StatusLineModel.RenderBuiltIn (statusline.go:61) renders
// this hint behind the idle/overlay guards. Require positive composer evidence
// as well as Herdr readiness: its manifest marks first-run dialogs idle too.
// Custom status lines or unfamiliar native versions fail closed.
func agyStartupComposer(screen string) bool {
	return len(screen) > 0 && len(screen) <= 64*1024 && utf8.ValidString(screen) &&
		strings.Contains(strings.Join(strings.Fields(screen), " "), "? for shortcuts")
}

// Herdr's current AGY manifest reports idle/ready even on the sign-in screen.
// Inspect only this freshly registered occupant before any goal can be sent.
// A remaining trust/auth dialog is blocked; uncertain ownership/readiness fails
// registration. The same bounded diagnostic refines a native startup timeout.
func (h Host) agyRegistrationCheck(ctx context.Context, label, cwd, pane, ws string, registered bool) error {
	unknown := func() error { return registrationFailure("", ctx.Err()) }
	before, err := h.agentInfoOf(ctx, pane)
	if err != nil || !agyStartupOccupant(before, label, cwd, pane, ws, registered) {
		return unknown()
	}
	screen, err := h.visiblePromptScreen(ctx, pane)
	if err != nil || strings.TrimSpace(screen) == "" || len(screen) > 64*1024 || !utf8.ValidString(screen) {
		return unknown()
	}
	after, err := h.agentInfoOf(ctx, pane)
	if err != nil || !agyStartupOccupant(after, label, cwd, pane, ws, registered) || before.Name != after.Name || before.StateChangeSeq != after.StateChangeSeq {
		return unknown()
	}
	if ctx.Err() != nil {
		return unknown()
	}
	if agyStartupBlocker(screen) || after.AgentStatus == "blocked" {
		return &agentRegistrationFailure{kind: "startup_blocked"}
	}
	if registered && after.AgentStatus == "idle" && after.InteractiveReady && agyStartupComposer(screen) {
		return nil
	}
	return unknown()
}

func agyStartupOccupant(a AgentInfo, label, cwd, pane, ws string, registered bool) bool {
	return a.Agent == "agy" && a.PaneID == pane && a.WorkspaceID == ws && a.Cwd == cwd &&
		(a.Name == label || (!registered && a.Name == "")) &&
		(a.AgentStatus == "idle" || a.AgentStatus == "unknown" || a.AgentStatus == "blocked")
}
