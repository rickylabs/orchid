package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type herdrWorktreePolicy struct {
	RepoRoot      string `json:"repoRoot"`
	WorktreesRoot string `json:"worktreesRoot"`
}
type herdrWorktreeRequest struct {
	Branch string `json:"branch"`
	Path   string `json:"path"`
}
type herdrPluginContext struct {
	WorkspaceID  string `json:"workspace_id"`
	WorkspaceCwd string `json:"workspace_cwd"`
	SelectedText string `json:"selected_text"`
}

var errWorktreePolicy = errors.New("explicit authorized worktree path required")
var worktreeBranch = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// This gate has no implicit destination and does not manage Git itself. Native
// discovery/open retains finished checkouts and Herdr's repository grouping.
func createPolicyWorktree(ctx context.Context, p herdrWorktreePolicy, c herdrPluginContext, r herdrWorktreeRequest, call func(context.Context, ...string) (string, error)) error {
	canonical := func(path string) bool {
		real, err := filepath.EvalSymlinks(path)
		return err == nil && filepath.IsAbs(path) && filepath.Clean(path) == path && real == path
	}
	if !canonical(p.RepoRoot) || !canonical(p.WorktreesRoot) || filepath.Base(p.RepoRoot) != "repo" || filepath.Base(p.WorktreesRoot) != "worktrees" || filepath.Dir(p.RepoRoot) != filepath.Dir(p.WorktreesRoot) ||
		c.WorkspaceID == "" || !canonical(c.WorkspaceCwd) || !worktreeBranch.MatchString(r.Branch) || strings.Contains(r.Branch, "..") || strings.HasSuffix(r.Branch, ".lock") || strings.HasSuffix(r.Branch, "/") || filepath.Clean(r.Branch) != r.Branch ||
		r.Path == "" || !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || r.Path != filepath.Join(p.WorktreesRoot, r.Branch) {
		return errWorktreePolicy
	}
	if c.WorkspaceCwd != p.RepoRoot {
		rel, err := filepath.Rel(p.WorktreesRoot, c.WorkspaceCwd)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errWorktreePolicy
		}
	}
	// All existing ancestors must be real directories. Never follow a symlink,
	// including one at the explicit destination, even if it resolves inside root.
	for parent := r.Path; parent != p.WorktreesRoot; parent = filepath.Dir(parent) {
		st, err := os.Lstat(parent)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !st.IsDir() || !canonical(parent) {
			return errWorktreePolicy
		}
	}
	out, err := call(ctx, "worktree", "list", "--workspace", c.WorkspaceID)
	if err != nil {
		return errWorktreePolicy
	}
	raw, err := herdrUnwrap(out)
	var listed struct {
		Type   string `json:"type"`
		Source struct {
			RepoRoot     string `json:"repo_root"`
			CheckoutPath string `json:"source_checkout_path"`
		} `json:"source"`
		Worktrees []struct {
			Path   string `json:"path"`
			Branch string `json:"branch"`
		} `json:"worktrees"`
	}
	if err != nil || len(raw) > 1024*1024 || json.Unmarshal(raw, &listed) != nil || listed.Type != "worktree_list" || listed.Source.RepoRoot != p.RepoRoot || listed.Source.CheckoutPath != p.RepoRoot || listed.Worktrees == nil {
		return errWorktreePolicy
	}
	method := "create"
	sourceFound := c.WorkspaceCwd == p.RepoRoot
	for _, tree := range listed.Worktrees {
		if tree.Path == c.WorkspaceCwd {
			sourceFound = true
		}
		if tree.Path == r.Path || tree.Branch == r.Branch {
			if tree.Path != r.Path || tree.Branch != r.Branch || !canonical(r.Path) {
				return errWorktreePolicy
			}
			method = "open"
		}
	}
	if !sourceFound {
		return errWorktreePolicy
	}
	if _, err := os.Lstat(r.Path); err == nil && method != "open" {
		return errWorktreePolicy
	}
	// Discovery accepts linked sources, but native create/open require the
	// parent checkout. Its provenance was verified against the private policy.
	args := []string{"worktree", method, "--cwd", p.RepoRoot, "--path", r.Path, "--no-focus"}
	if method == "create" {
		args = append(args, "--branch", r.Branch)
	}
	_, err = call(ctx, args...)
	return err
}

// Plugin invocation uses the socket Herdr injected, never the focused default.
// The owner supplies a private policy in this plugin's native state directory.
func herdrWorktreeCLI(out io.Writer) int {
	state, socket := os.Getenv("HERDR_PLUGIN_STATE_DIR"), os.Getenv("HERDR_SOCKET_PATH")
	var p herdrWorktreePolicy
	var c herdrPluginContext
	var r herdrWorktreeRequest
	raw := os.Getenv("HERDR_PLUGIN_CONTEXT_JSON")
	policy, policyErr := ownerNativePrivateRead(filepath.Join(state, "checkout-policy.json"), os.Getuid())
	if !ownerNativePrivateDir(state, os.Getuid()) || !filepath.IsAbs(socket) || len(raw) > 64*1024 || decodeNativeJSON([]byte(raw), &c) != nil || c.WorkspaceID != os.Getenv("HERDR_WORKSPACE_ID") || len(c.SelectedText) > 4096 || strictJSON([]byte(c.SelectedText), &r) != nil || policyErr != nil || len(policy) > 4096 || strictJSON(policy, &p) != nil {
		fmt.Fprintln(out, errWorktreePolicy)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := createPolicyWorktree(ctx, p, c, r, func(ctx context.Context, args ...string) (string, error) { return run(ctx, "herdr", args...) })
	if err != nil {
		fmt.Fprintln(out, errWorktreePolicy)
		return 1
	}
	fmt.Fprintln(out, "Worktree ready")
	return 0
}
