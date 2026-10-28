package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type checkoutFixture struct {
	remote, stale, head string
	git                 func(string, ...string) string
	host                Host
}

func runCheckoutFixture(t *testing.T) checkoutFixture {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal("synthetic git operation failed")
		}
		return strings.TrimSpace(string(out))
	}
	if os.MkdirAll(remote, 0700) != nil {
		t.Fatal("fixture directory")
	}
	git(remote, "init", "-b", "trunk")
	git(remote, "-c", "user.name=Fixture", "-c", "user.email=fixture", "commit", "--allow-empty", "-m", "old")
	stale := git(remote, "rev-parse", "HEAD")
	git(remote, "-c", "user.name=Fixture", "-c", "user.email=fixture", "commit", "--allow-empty", "-m", "current")
	head := git(remote, "rev-parse", "HEAD")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+remote+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/fixture/upstream")
	bin := t.TempDir()
	writeFixture(t, filepath.Join(bin, "ssh"), `#!/bin/sh
for a; do last="$a"; done
case "$last" in *"# orchid-checkout-root"*|*"git clone"*) exec sh -c "$last";; esac
exit 0
`)
	if os.Chmod(filepath.Join(bin, "ssh"), 0700) != nil {
		t.Fatal("fixture mode")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	h := Host{Name: "fixture", SSH: "fixture", Home: t.TempDir(), WorkdirRoot: root, DiskWorkdirRoot: root}
	return checkoutFixture{remote: remote, stale: stale, head: head, git: git, host: h}
}

// Exercise production admission and checkout, using APIs present before the fix.
func TestRunCheckoutFreshHeadDespiteAdmissionPin(t *testing.T) {
	f := runCheckoutFixture(t)
	c := sourceRepoCoord(t)
	routing, deps, refusal, _ := unpinnedAttemptFixture(t, f.head)
	c.cfg.Matrix.Revision = routing.cfg.Matrix.Revision
	c.cfg.Matrix.Source = routing.cfg.Matrix.Source
	c.cfg.Governor = routing.cfg.Governor
	c.gov.q = routing.gov.q
	c.cfg.Matrix.TargetRevisions["fixture/upstream"] = f.stale
	is := Issue{ID: "fixture", Number: 7, Title: "synthetic task", Body: "/swarm\nharness: claude\nrepo: fixture/upstream\n\nImplement the task", Labels: []string{"harness"}}
	var receipt *durableMatrixReceipt
	deps.host = func(Target, string) (Host, bool) { return f.host, true }
	deps.launch = func(ctx context.Context, n int, issue Issue, host Host, agent string, o Overrides, r *durableMatrixReceipt) error {
		receipt = r
		return c.spawn(ctx, n, issue, host, agent, o, r)
	}
	// Actual admission populates the old base/new admission field without a test-only adapter.
	c.matrixAttempt(context.Background(), 7, is, Target{Repo: "fixture/upstream"}, map[string]int{"claude": 1}, deps)
	if receipt == nil {
		t.Fatalf("fixture admission did not reach production spawn: %s", refusal.ReasonCode)
	}
	checkout := filepath.Join(f.host.WorkdirRoot, "issue-7")
	if f.git(checkout, "rev-parse", "HEAD") != f.head {
		t.Fatal("run branch started from the stale admission pin instead of fetched default head")
	}
	var start struct{ StartSHA, BaseRef string }
	raw, readErr := os.ReadFile(filepath.Join(filepath.Dir(receipt.file), "binding.json"))
	if readErr != nil || json.Unmarshal(raw, &start) != nil || start.StartSHA != f.head || start.BaseRef != "HEAD" {
		t.Fatal("verified run start was not persisted before agent registration")
	}
}
