package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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

// codexClientError keeps the native reason a client was not selected, while
// still being codex-client-unavailable for every existing caller.
type codexClientError struct{ unmatched bool }

func (e codexClientError) Error() string        { return string(codexClientUnavailable) }
func (e codexClientError) Is(target error) bool { return target == codexClientUnavailable }

var (
	// No host client reports the daemon's version (or the version is invalid).
	errCodexClientUnmatched = codexClientError{unmatched: true}
	// The handshake, transport or resolver output failed: no verdict on clients.
	errCodexClientCheck = codexClientError{}
)

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
		return errCodexClientCheck
	}
	binary, err := h.resolveCodexClient(ctx, version)
	if err != nil {
		return err
	}
	run.ClientVersion, run.ClientBinary, run.ClientDir = version, binary, filepath.Dir(binary)
	return nil
}

// codexClientCheck resolves a candidate through every symlink and prints the real
// executable only if it is named codex and itself reports exactly $want. A symlink
// that an updater re-points later can no longer change what was verified.
const codexClientCheck = `check() { r=$(readlink -f -- "$1" 2>/dev/null) || return 1; [ "${r##*/}" = codex ] && [ -f "$r" ] && [ -x "$r" ] && [ "$("$r" --version 2>/dev/null)" = "$want" ] && printf '%s\n' "$r"; }`

// resolveCodexClient returns the real path of a host codex whose own --version is
// exactly codex-cli <version>: the version-named standalone release first, then
// the codex on the launch PATH. Nothing is installed, updated or restarted.
func (h Host) resolveCodexClient(ctx context.Context, version string) (string, error) {
	if !codexVersionPattern.MatchString(version) { // guard:client-version-valid
		// No valid daemon version was observed, so no installed client was compared.
		return "", errCodexClientCheck
	}
	script := fmt.Sprintf(`export HOME=%s; export PATH="$HOME/.opencode/bin:$HOME/.local/bin:/usr/local/bin:$PATH"; v=%s; want="codex-cli $v"
%s
for d in "$HOME"/.codex/packages/standalone/releases/"$v"-*/bin; do
	check "$d/codex" && exit 0
done
c=$(command -v codex 2>/dev/null) && check "$c" && exit 0
exit 3`, shq(h.agentHome()), shq(version), codexClientCheck)
	out, err := h.runRemote(ctx, script)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 3 { // guard:client-unmatched-exit
		return "", errCodexClientUnmatched
	}
	if err != nil {
		return "", errCodexClientCheck
	}
	binary := strings.TrimSuffix(out, "\n")
	if filepath.Base(binary) != "codex" || !validClientDir(filepath.Dir(binary)) || filepath.Clean(binary) != binary { // guard:client-binary-valid
		return "", errCodexClientCheck
	}
	return binary, nil
}

// verifyCodexClient re-checks the pin immediately before Herdr starts the agent:
// the pinned directory's codex must still resolve to the verified real binary,
// and that binary must still report the daemon's exact version.
func (h Host) verifyCodexClient(ctx context.Context, run *remoteControlRun) error {
	if run == nil || !codexVersionPattern.MatchString(run.ClientVersion) || run.ClientBinary == "" || filepath.Dir(run.ClientBinary) != run.ClientDir {
		return codexClientUnavailable
	}
	script := fmt.Sprintf(`v=%s; want="codex-cli $v"
%s
[ "$(check %s)" = %s ]`, shq(run.ClientVersion), codexClientCheck, shq(filepath.Join(run.ClientDir, "codex")), shq(run.ClientBinary))
	if _, err := h.runRemote(ctx, script); err != nil {
		return codexClientUnavailable
	}
	return nil
}

// One absolute clean directory that is safe as a PATH element.
func validClientDir(dir string) bool {
	return filepath.IsAbs(dir) && filepath.Clean(dir) == dir && dir != "/" && len(dir) <= 4096 && utf8.ValidString(dir) &&
		!strings.ContainsAny(dir, ":\n") && strings.IndexFunc(dir, unicode.IsControl) < 0
}
