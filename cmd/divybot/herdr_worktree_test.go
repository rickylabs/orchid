package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHerdrWorktreePolicyRequiresExactExplicitPath(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "outside", "symlink", "existing", "foreign-root", "discovered-elsewhere"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			worktrees := filepath.Join(root, "worktrees")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(worktrees, 0700); err != nil {
				t.Fatal(err)
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
			creates, opens := 0, 0
			call := func(_ context.Context, args ...string) (string, error) {
				if args[1] == "list" {
					source := repo
					if mode == "foreign-root" {
						source = filepath.Join(root, "other")
					}
					trees := []map[string]any{}
					if mode == "existing" {
						trees = append(trees, map[string]any{"path": r.Path, "branch": r.Branch})
					}
					if mode == "discovered-elsewhere" {
						trees = append(trees, map[string]any{"path": filepath.Join(root, "old"), "branch": r.Branch})
					}
					return string(fixtureJSON(t, map[string]any{"result": map[string]any{"type": "worktree_list", "source": map[string]any{"repo_root": source}, "worktrees": trees}})), nil
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
			if mode == "valid" {
				if err != nil || creates != 1 || opens != 0 {
					t.Fatal("authorized path failed", err)
				}
			} else if mode == "existing" {
				if err != nil || creates != 0 || opens != 1 {
					t.Fatal("finished checkout recreated", err)
				}
			} else if err == nil || creates != 0 || opens != 0 {
				t.Fatal("unapproved creation path reached native effect")
			}
		})
	}
}
