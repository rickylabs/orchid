package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A Remote Control Codex TUI attaches to the running canonical app-server. A
// newer TUI can leave during bootstrap against an older daemon, before it resumes
// the prepared thread: Herdr then sees the agent exit and orchid waits out the
// whole start budget. The client must be the daemon's exact version, read from
// the daemon's own initialize userAgent as Codex's daemon client reads it.
const codexClientUnavailable matrixReason = "codex-client-unavailable"

var codexVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z][0-9A-Za-z.-]{0,63})?$`)

// codexServerVersion parses "<originator>/<version> ..." and returns "" otherwise.
func codexServerVersion(userAgent string) string {
	_, rest, ok := strings.Cut(userAgent, "/")
	if !ok {
		return ""
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 || !codexVersionPattern.MatchString(fields[0]) {
		return ""
	}
	return fields[0]
}

// A refusal before any workspace, thread or agent exists; no launch effect occurred.
func codexClientBlocked(err error) bool {
	return errors.Is(err, codexClientUnavailable) && matrixCause(err) == "spawn.codex-client"
}

// launchBlockReason names a failed registration's durable block: the specific
// no-seat classes, otherwise the generic registration failure.
func launchBlockReason(err error) string {
	switch {
	case agySettingsBlocked(err):
		return string(agySettingsUnreadable)
	case codexClientBlocked(err): // guard:block-reason
		return string(codexClientUnavailable)
	}
	return "registration_failed"
}

// selectCodexClient reads the canonical daemon's version over a handshake-only
// connection, then pins the run to a host client that reports that exact version.
func (h Host) selectCodexClient(ctx context.Context, run *remoteControlRun) error {
	version := ""
	if err := h.withCanonicalConnection(ctx, "", func(p *goalRPC) error {
		version = p.serverVersion
		return nil
	}); err != nil {
		return err
	}
	dir, err := h.resolveCodexClient(ctx, version)
	if err != nil {
		return err
	}
	run.ClientVersion, run.ClientDir = version, dir
	return nil
}

// resolveCodexClient returns the directory of a host codex whose own --version is
// exactly codex-cli <version>: the version-named standalone release first, then
// the codex on the launch PATH. Nothing is installed, updated or restarted.
func (h Host) resolveCodexClient(ctx context.Context, version string) (string, error) {
	if !codexVersionPattern.MatchString(version) { // guard:client-version-valid
		return "", codexClientUnavailable
	}
	script := fmt.Sprintf(`export HOME=%s; export PATH="$HOME/.opencode/bin:$HOME/.local/bin:/usr/local/bin:$PATH"; v=%s; want="codex-cli $v"
for d in "$HOME"/.codex/packages/standalone/releases/"$v"-*/bin; do
	if [ -x "$d/codex" ] && [ "$("$d/codex" --version 2>/dev/null)" = "$want" ]; then printf '%%s\n' "$d"; exit 0; fi
done
if c=$(command -v codex 2>/dev/null) && [ "$("$c" --version 2>/dev/null)" = "$want" ]; then dirname "$c"; exit 0; fi
exit 3`, shq(h.agentHome()), shq(version))
	out, err := h.runRemote(ctx, script)
	if err != nil {
		return "", codexClientUnavailable
	}
	dir := strings.TrimSuffix(out, "\n")
	if !validClientDir(dir) { // guard:client-dir-valid
		return "", codexClientUnavailable
	}
	return dir, nil
}

// One absolute clean directory that is safe as a PATH element.
func validClientDir(dir string) bool {
	return filepath.IsAbs(dir) && filepath.Clean(dir) == dir && dir != "/" && len(dir) <= 4096 && utf8.ValidString(dir) &&
		!strings.ContainsAny(dir, ":\n") && strings.IndexFunc(dir, unicode.IsControl) < 0
}
