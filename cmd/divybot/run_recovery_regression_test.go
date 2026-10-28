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

// Exercise the production clone/fetch/checkout path against a real synthetic remote.
func TestRunCheckoutFreshHeadDespiteAdmissionPin(t *testing.T) {
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
	c := sourceRepoCoord(t)
	c.cfg.Matrix.TargetRevisions["fixture/upstream"] = stale
	root := t.TempDir()
	h := Host{Name: "fixture", SSH: "fixture", Home: t.TempDir(), WorkdirRoot: root, DiskWorkdirRoot: root}
	is := Issue{ID: "fixture", Number: 7, Title: "synthetic task", Body: "/swarm\nharness: claude\nrepo: fixture/upstream\n\nImplement the task", Labels: []string{"harness"}}
	o := parseOverrides(is.Body)
	receipt := registrationReceipt(t, "claude", o)
	// The original defect had a stale matrix receipt despite a fresh clone.
	receipt.admission = stale
	if os.WriteFile(filepath.Join(filepath.Dir(receipt.file), "binding.json"), goalJSON(map[string]any{"AdmissionRevision": stale}), 0600) != nil {
		t.Fatal("fixture binding")
	}
	_ = c.spawn(context.Background(), 7, is, h, "claude", o, receipt)
	checkout := filepath.Join(root, "issue-7")
	var start struct{ StartSHA, BaseRef string }
	raw, readErr := os.ReadFile(filepath.Join(filepath.Dir(receipt.file), "binding.json"))
	if readErr != nil || json.Unmarshal(raw, &start) != nil || start.StartSHA != head || start.BaseRef != "HEAD" {
		t.Fatal("verified run start was not persisted before agent registration")
	}
	if git(checkout, "rev-parse", "HEAD") != head {
		t.Fatal("run branch started from the stale admission pin instead of fetched default head")
	}
	// Existing local refs and a reused run branch must not become implicit bases.
	git(checkout, "fetch", "--depth=1", "origin", stale)
	git(checkout, "checkout", "-fB", "fixture/7", stale)
	git(checkout, "update-ref", "refs/remotes/origin/old", stale)
	git(checkout, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/old")
	got, err := h.prepareRunCheckout(context.Background(), "fixture/upstream", checkout, "fixture/7", Overrides{})
	if err != nil || got != head || git(checkout, "rev-parse", "HEAD") != head {
		t.Fatal("cached origin/HEAD or reused run branch overrode remote default HEAD")
	}
	git(remote, "branch", "release", stale)
	for _, base := range []string{stale, "release", "refs/heads/release"} {
		got, err = h.prepareRunCheckout(context.Background(), "fixture/upstream", checkout, "fixture/7", Overrides{Base: base})
		if err != nil || got != stale || git(checkout, "rev-parse", "HEAD") != stale {
			t.Fatal("explicit brief base did not select its fetched commit")
		}
	}
	if _, err = h.prepareRunCheckout(context.Background(), "fixture/upstream", checkout, "fixture/7", Overrides{Base: "missing-base"}); err == nil || git(checkout, "rev-parse", "HEAD") != stale {
		t.Fatal("failed fetch used a local fallback")
	}
	git(remote, "symbolic-ref", "HEAD", "refs/heads/release")
	if got, err = h.prepareRunCheckout(context.Background(), "fixture/upstream", checkout, "fixture/7", Overrides{}); err != nil || got != stale {
		t.Fatal("changed remote default branch used cached local HEAD")
	}
	git(checkout, "config", "remote.origin.url", "https://github.com/fixture/other")
	if _, err = h.prepareRunCheckout(context.Background(), "fixture/upstream", checkout, "fixture/7", Overrides{}); err != errWorktreeOrigin {
		t.Fatal("foreign checkout origin was reused")
	}
}
