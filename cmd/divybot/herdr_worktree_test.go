package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHerdrWorktreePolicyRequiresExactExplicitPath(t *testing.T) {
	for _, repoName := range []string{"repo", "sample-project"} {
		t.Run(repoName, func(t *testing.T) {
			testHerdrWorktreePolicy(t, repoName)
		})
	}
}

func testHerdrWorktreePolicy(t *testing.T, repoName string) {
	t.Helper()
	for _, mode := range []string{"valid", "missing", "outside", "symlink", "existing", "foreign-root", "discovered-elsewhere", "linked-create", "linked-open", "unregistered-source", "outside-source", "same-root", "nonsibling-root"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, repoName)
			worktrees := filepath.Join(root, "worktrees")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(worktrees, 0700); err != nil {
				t.Fatal(err)
			}
			if mode == "same-root" {
				repo = worktrees
			}
			if mode == "nonsibling-root" {
				worktrees = filepath.Join(root, "nested", "worktrees")
				if err := os.MkdirAll(worktrees, 0700); err != nil {
					t.Fatal(err)
				}
			}
			p := herdrWorktreePolicy{RepoRoot: repo, WorktreesRoot: worktrees}
			c := herdrPluginContext{WorkspaceID: "w1", WorkspaceCwd: repo}
			r := herdrWorktreeRequest{Branch: "topic", Path: filepath.Join(worktrees, "topic")}
			switch mode {
			case "missing":
				r.Path = ""
			case "outside":
				r.Path = filepath.Join(root, "other")
			case "symlink":
				os.Symlink(root, r.Path)
			case "existing":
				os.Mkdir(r.Path, 0700)
			}
			if strings.HasPrefix(mode, "linked-") || mode == "unregistered-source" || mode == "outside-source" {
				c.WorkspaceCwd = filepath.Join(worktrees, "source")
				if mode == "outside-source" {
					c.WorkspaceCwd = filepath.Join(root, "other")
				}
				if err := os.Mkdir(c.WorkspaceCwd, 0700); err != nil {
					t.Fatal(err)
				}
				if mode == "linked-open" {
					if err := os.Mkdir(r.Path, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			creates, opens := 0, 0
			call := func(_ context.Context, args ...string) (string, error) {
				if args[1] == "list" {
					source := repo
					if mode == "foreign-root" {
						source = filepath.Join(root, "other")
					}
					trees := []map[string]any{}
					if strings.HasPrefix(mode, "linked-") || mode == "outside-source" {
						trees = append(trees, map[string]any{"path": c.WorkspaceCwd, "branch": "source"})
					}
					if mode == "existing" || mode == "linked-open" {
						trees = append(trees, map[string]any{"path": r.Path, "branch": r.Branch})
					}
					if mode == "discovered-elsewhere" {
						trees = append(trees, map[string]any{"path": filepath.Join(root, "old"), "branch": r.Branch})
					}
					// Herdr normalizes linked discovery to the parent checkout.
					return string(fixtureJSON(t, map[string]any{"result": map[string]any{"type": "worktree_list", "source": map[string]any{"repo_root": source, "source_checkout_path": repo}, "worktrees": trees}})), nil
				}
				if strings.HasPrefix(mode, "linked-") {
					if len(args) < 4 || args[2] != "--cwd" || args[3] != repo {
						t.Fatal("native create/open requires the verified parent checkout")
					}
				}
				if args[1] == "create" {
					creates++
				} else if args[1] == "open" {
					opens++
				}
				if !strings.Contains(strings.Join(args, "|"), "--path|"+r.Path) {
					t.Fatal("implicit native path")
				}
				return `{"result":{"type":"fixture"}}`, nil
			}
			err := createPolicyWorktree(context.Background(), p, c, r, call)
			if mode == "valid" || mode == "linked-create" {
				if err != nil || creates != 1 || opens != 0 {
					t.Fatal("authorized path failed", err)
				}
			} else if mode == "existing" || mode == "linked-open" {
				if err != nil || creates != 0 || opens != 1 {
					t.Fatal("finished checkout recreated", err)
				}
			} else if err == nil || creates != 0 || opens != 0 {
				t.Fatal("unapproved creation path reached native effect")
			}
		})
	}
}
