package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// The persistent root is supplied by the deployment, never created here: a
// missing bind mount must not silently put new clones on the container layer.
const defaultDiskWorkdirRoot = "/var/lib/orchid/worktrees"

func (h Host) legacyCheckout(n int) string {
	root := h.WorkdirRoot
	if root == "" {
		root = "/root/orch-work"
	}
	return filepath.Join(root, fmt.Sprintf("issue-%d", n))
}

func (h Host) checkoutDirectory(ctx context.Context, n int) (string, error) {
	root := h.DiskWorkdirRoot
	if root == "" {
		root = defaultDiskWorkdirRoot
	}
	old := h.legacyCheckout(n)
	next := filepath.Join(root, fmt.Sprintf("issue-%d", n))
	if n <= 0 || !validRemoteCwd(root) || !validRemoteCwd(old) {
		return "", fmt.Errorf("checkout-root-invalid")
	}
	// Existing paths win over the new default. Refuse duplicates instead of
	// resetting either copy. No running checkout or dispatch reference is moved.
	script := fmt.Sprintf(`# orchid-checkout-root
old=%s; next=%s; root=%s
if [ "$old" = "$next" ]; then printf '%%s' "$old"
elif [ -e "$old" ] && [ -e "$next" ]; then exit 44
elif [ -e "$old" ]; then printf '%%s' "$old"
elif [ -e "$next" ] || [ -d "$root" ]; then printf '%%s' "$next"
else printf '%%s' "$old"; fi`, shq(old), shq(next), shq(root))
	out, err := h.runRemote(ctx, script)
	if err != nil {
		return "", fmt.Errorf("checkout-root-unavailable")
	}
	if out != old && out != next {
		return "", fmt.Errorf("checkout-root-unconfirmed")
	}
	return out, nil
}

// Cleanup uses the launch's recorded cwd. Old state stays on its old root;
// changing configuration cannot redirect deletion to a different checkout.
func (h Host) jobCheckout(j *Job) string {
	if j.Workdir != "" {
		return j.Workdir
	}
	if j.RemoteControl != nil && j.RemoteControl.Cwd != "" {
		return j.RemoteControl.Cwd
	}
	if j.OpenCode != nil && j.OpenCode.Cwd != "" {
		return j.OpenCode.Cwd
	}
	return h.legacyCheckout(j.Issue)
}

func validJobCheckout(path string, n int) bool {
	return validRemoteCwd(path) && filepath.Base(path) == fmt.Sprintf("issue-%d", n) && !strings.ContainsAny(path, "\x00\r\n")
}

// cleanupCheckout pairs managed Codex trust removal with recorded-path deletion.
// Revoke first so a failed deletion cannot leave a trusted abandoned checkout.
func (h Host) cleanupCheckout(ctx context.Context, j *Job) error {
	wd := h.jobCheckout(j)
	if !validJobCheckout(wd, j.Issue) {
		return goalError("checkout-root-invalid")
	}
	if j.Agent == "codex" && j.RemoteControl != nil {
		if j.RemoteControl.Cwd != wd {
			return goalError("remote-control-binding-invalid")
		}
		if err := h.removeCheckoutTrust(ctx, wd); err != nil {
			return err
		}
	}
	if _, err := h.runRemote(ctx, "rm -rf "+shq(wd)); err != nil {
		return goalError("checkout-cleanup-unconfirmed")
	}
	return nil
}
