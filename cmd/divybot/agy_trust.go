package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
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
	return h.prepareAGYTrustAt(ctx, cwd, filepath.Join(cwd, ".divybot-agy"), false)
}

func (h Host) prepareAGYTrustAt(ctx context.Context, cwd, root string, retained bool) ([]string, error) {
	bad := matrixReason("agy-settings-unavailable")
	if !agyTrustScope(cwd) {
		return nil, bad
	}
	gemini := filepath.Join(h.agentHome(), ".gemini")
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
	// The CLI's own typed onboarding flag replaces the removed screen check. An
	// incomplete or unproven cache refuses here, before any workspace or agent.
	cache, err := h.agyTrustCommand(ctx, "exec head -c "+strconv.Itoa(agyOnboardingMax+1)+" "+shq(onboarding)+" 2>/dev/null")
	if err != nil {
		cache = nil // a missing or unreadable cache is unproven
	}
	if err := agyOnboardingComplete(cache); err != nil {
		return nil, err
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
	preparation := ""
	if retained {
		parent := filepath.Dir(filepath.Dir(root))
		reservation := filepath.Dir(root)
		alias := filepath.Join(cwd, ".divybot-agy")
		// The compatibility link remains inside the checkout, but the fresh native
		// store is private and survives removal of that checkout. Never reuse a run.
		preparation = "test ! -e " + shq(alias) + " && test ! -L " + shq(alias) +
			" && { test -d " + shq(parent) + " || mkdir " + shq(parent) + "; }" +
			" && test ! -L " + shq(parent) + " && test -O " + shq(parent) +
			" && test \"$(stat -c '%a' " + shq(parent) + ")\" = 700" +
			" && test \"$(cd " + shq(parent) + " && pwd -P)\" = " + shq(parent) +
			" && mkdir " + shq(reservation) + " && "
	}
	script := "umask 077; " + preparation + "test ! -e " + shq(root) + " && test ! -L " + shq(root) +
		" && mkdir " + shq(root) + " && printf '%s' " + shq(string(merged)) +
		" > " + shq(filepath.Join(root, "settings.json")) +
		" && " + agyFileReference(credential, filepath.Join(root, "antigravity-oauth-token")) +
		" && mkdir " + shq(filepath.Join(root, "cache")) +
		" && " + agyFileReference(onboarding, filepath.Join(root, "cache", "onboarding.json")) +
		" && printf '\\n.divybot-agy/\\n' >> " + shq(filepath.Join(cwd, ".git", "info", "exclude"))
	if retained {
		script += " && ln -s " + shq(root) + " " + shq(filepath.Join(cwd, ".divybot-agy"))
	}
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
