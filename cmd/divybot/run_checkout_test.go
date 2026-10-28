package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCheckoutBaseGrammarAndStartFence(t *testing.T) {
	for _, body := range []string{"/swarm\nbase:", "/swarm\nbase: main\nbase: other", "/swarm\nbase: main # ignored", "/swarm\nbase: -other", "/swarm\nbase: main~1", "/swarm\nbase: ../main", "/swarm\nbase: main.lock", "/swarm\nbase: refs//heads/main"} {
		if _, err := runBaseRef(parseOverrides(body)); err == nil {
			t.Fatal("invalid or ambiguous explicit base accepted")
		}
	}
	for _, base := range []string{"main", "topic/fix", "refs/heads/trunk", strings.Repeat("a", 40)} {
		if got, err := runBaseRef(parseOverrides("/swarm\nbase: " + base)); err != nil || got != base {
			t.Fatal("valid explicit base lost")
		}
	}
	r := registrationReceipt(t, "claude", Overrides{})
	path := filepath.Join(filepath.Dir(r.file), "binding.json")
	if os.WriteFile(path, goalJSON(map[string]any{"AdmissionRevision": strings.Repeat("b", 40)}), 0600) != nil {
		t.Fatal("fixture")
	}
	sha := strings.Repeat("c", 40)
	if r.recordRunStart(Overrides{}, sha) != nil {
		t.Fatal("verified start not durable")
	}
	if r.recordRunStart(Overrides{}, strings.Repeat("d", 40)) == nil {
		t.Fatal("one launch acquired two start commits")
	}
	if r.recordRunStart(Overrides{Base: "other"}, sha) == nil {
		t.Fatal("one launch acquired two base selections")
	}
	if r.recordRunStart(Overrides{}, "unknown") == nil {
		t.Fatal("unproven commit persisted")
	}
}

func TestRunCheckoutRemoteBaseAndReuse(t *testing.T) {
	f := runCheckoutFixture(t)
	checkout := filepath.Join(f.host.WorkdirRoot, "issue-7")
	if got, err := f.host.prepareRunCheckout(context.Background(), "fixture/upstream", checkout, "fixture/7", Overrides{}); err != nil || got != f.head {
		t.Fatal("fixture checkout preparation failed")
	}
	// Cached refs and a reused run branch must not become implicit bases.
	f.git(checkout, "fetch", "--depth=1", "origin", f.stale)
	f.git(checkout, "checkout", "-fB", "fixture/7", f.stale)
	f.git(checkout, "update-ref", "refs/remotes/origin/old", f.stale)
	f.git(checkout, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/old")
	got, err := f.host.prepareRunCheckout(context.Background(), "fixture/upstream", checkout, "fixture/7", Overrides{})
	if err != nil || got != f.head || f.git(checkout, "rev-parse", "HEAD") != f.head {
		t.Fatal("cached origin/HEAD or reused run branch overrode remote default HEAD")
	}
	f.git(f.remote, "branch", "release", f.stale)
	for _, base := range []string{f.stale, "release", "refs/heads/release"} {
		got, err = f.host.prepareRunCheckout(context.Background(), "fixture/upstream", checkout, "fixture/7", Overrides{Base: base})
		if err != nil || got != f.stale || f.git(checkout, "rev-parse", "HEAD") != f.stale {
			t.Fatal("explicit brief base did not select its fetched commit")
		}
	}
	if _, err = f.host.prepareRunCheckout(context.Background(), "fixture/upstream", checkout, "fixture/7", Overrides{Base: "missing-base"}); err == nil || f.git(checkout, "rev-parse", "HEAD") != f.stale {
		t.Fatal("failed fetch used a local fallback")
	}
	f.git(f.remote, "symbolic-ref", "HEAD", "refs/heads/release")
	if got, err = f.host.prepareRunCheckout(context.Background(), "fixture/upstream", checkout, "fixture/7", Overrides{}); err != nil || got != f.stale {
		t.Fatal("changed remote default branch used cached local HEAD")
	}
	f.git(checkout, "config", "remote.origin.url", "https://github.com/fixture/other")
	if _, err = f.host.prepareRunCheckout(context.Background(), "fixture/upstream", checkout, "fixture/7", Overrides{}); err != errWorktreeOrigin {
		t.Fatal("foreign checkout origin was reused")
	}
}
