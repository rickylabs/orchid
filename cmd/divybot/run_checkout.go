package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var runBasePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$`)
var errWorktreeOrigin = errors.New("worktree-origin-mismatch")

// Accept remote ref names and full commit IDs, never revision expressions or options.
func validRunBase(ref string) bool {
	if !runBasePattern.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, "/") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func runBaseRef(o Overrides) (string, error) {
	if o.BaseInvalid || o.RoutingInvalid || (o.Base != "" && !validRunBase(o.Base)) {
		return "", goalError("run-base-invalid")
	}
	if o.Base != "" {
		return o.Base, nil
	}
	return "HEAD", nil
}

// One checkout owner: fetch the remote's current default HEAD (not origin/HEAD),
// or the brief's explicit base, and replace only this dedicated run branch.
func (h Host) prepareRunCheckout(ctx context.Context, repo, workdir, branch string, o Overrides) (string, error) {
	base, err := runBaseRef(o)
	if err != nil || !repositoryName.MatchString(repo) || !validRemoteCwd(workdir) || !validRunBase(branch) {
		return "", goalError("run-base-invalid")
	}
	script := fmt.Sprintf(`set -e
mkdir -p %s; cd %s
if [ ! -d .git ]; then find . -mindepth 1 -maxdepth 1 -exec rm -rf {} + 2>/dev/null || true; git clone --depth=1 https://github.com/%s . >/dev/null 2>&1; fi
o=$(git config --get remote.origin.url || true); o=${o%%.git}; o=${o%%/}
[ "$(printf %%s "$o" | tr A-Z a-z)" = "$(printf %%s %s | tr A-Z a-z)" ] || exit %d
git check-ref-format --branch %s >/dev/null
git fetch --depth=1 --no-tags origin %s >/dev/null 2>&1
start=$(git rev-parse --verify 'FETCH_HEAD^{commit}')
git checkout -fB %s "$start" >/dev/null 2>&1
[ "$(git rev-parse --verify 'HEAD^{commit}')" = "$start" ]
printf '%%s' "$start"`, shq(workdir), shq(workdir), repo, shq("https://github.com/"+repo), worktreeOriginMismatch, shq(branch), shq(base), shq(branch))
	out, err := h.runRemote(ctx, script)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == worktreeOriginMismatch {
			return "", errWorktreeOrigin
		}
		return "", goalError("run-checkout-unavailable")
	}
	sha := strings.TrimSpace(out)
	if !sourceRevision.MatchString(sha) || ctx.Err() != nil {
		return "", goalError("run-checkout-unproven")
	}
	return sha, nil
}

// Persist the observed commit before seat creation, preserving all admission metadata.
func (r *durableMatrixReceipt) recordRunStart(o Overrides, sha string) error {
	if r == nil || !sourceRevision.MatchString(sha) {
		return goalError("run-start-unproven")
	}
	base, err := runBaseRef(o)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	path := filepath.Join(filepath.Dir(r.file), "binding.json")
	raw, err := os.ReadFile(path)
	var binding map[string]json.RawMessage
	if err != nil || decodeNativeJSON(raw, &binding) != nil || binding == nil {
		return goalError("run-start-unproven")
	}
	if prior, ok := binding["StartSHA"]; ok && string(prior) != string(goalJSONBytes(sha)) {
		return goalError("run-start-already-recorded")
	}
	binding["StartSHA"] = goalJSONBytes(sha)
	binding["BaseRef"] = goalJSONBytes(base)
	return writePrivateJSON(path, ".run-start-", r.owner, binding)
}

func goalJSONBytes(v string) json.RawMessage { raw, _ := json.Marshal(v); return raw }
