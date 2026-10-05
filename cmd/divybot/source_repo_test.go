package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sourceRepoCoord(t *testing.T) *Coord {
	t.Helper()
	root := privateTestRoot(t)
	return &Coord{cfg: &Config{Inbox: "fixture/inbox", BranchPrefix: "fixture/", Targets: []Target{
		{Label: "harness", Repo: "fixture/inbox"},
		{Label: "upstream", Repo: "fixture/upstream"},
	}, Matrix: MatrixConfig{ReceiptRoot: root, TargetRevisions: map[string]string{
		"fixture/inbox": strings.Repeat("a", 40), "fixture/upstream": strings.Repeat("b", 40)}}},
		auth: &AuthStore{}, st: loadState(filepath.Join(root, "state.json"))}
}

func TestSwarmRepoKey(t *testing.T) {
	for _, tc := range []struct {
		body, repo string
		invalid    bool
	}{
		{"/swarm\nharness: codex\nrepo: Fixture/UpStream\n\ndo it", "Fixture/UpStream", false},
		{"/swarm\nrepo: fixture/upstream\nrepo: fixture/inbox\n\ndo it", "fixture/inbox", true},
		{"/swarm\nrepo: https://github.com/fixture/upstream\n\ndo it", "https://github.com/fixture/upstream", true},
		{"/swarm\nrepo: fixture/upstream#12\n\ndo it", "fixture/upstream", false}, // a trailing comment is stripped like every key
		{"/swarm\nharness: codex\n\ndo it", "", false},
	} {
		o := parseOverrides(tc.body)
		if o.Repo != tc.repo || o.RepoInvalid != tc.invalid {
			t.Fatalf("repo key parsed wrong for %q: %+v", tc.body, o)
		}
	}
}

// The work repository is the source repository: the repo key is authoritative,
// and nothing falls back to the inbox/label target.
func TestResolveTargetSourceRepo(t *testing.T) {
	c := sourceRepoCoord(t)
	for _, tc := range []struct {
		name, title, body string
		labels            []string
		repo, reason      string
		ours              bool
	}{
		{"repo-key-routes-to-source", "[fixture/upstream#41] fix", "/swarm\nrepo: fixture/upstream\n\nfix", []string{"harness"}, "fixture/upstream", "", true},
		{"repo-key-case-insensitive", "x", "/swarm\nrepo: Fixture/UpStream\n\nfix", []string{"harness"}, "fixture/upstream", "", true},
		{"repo-key-inbox-source", "[fixture/inbox#5] task", "/swarm\nrepo: fixture/inbox\n\nfix", []string{"harness"}, "fixture/inbox", "", true},
		{"repo-key-unconfigured", "x", "/swarm\nrepo: fixture/elsewhere\n\nfix", []string{"harness"}, "", "source-repo-unavailable", true},
		{"repo-key-invalid", "x", "/swarm\nrepo: fixture/a\nrepo: fixture/b\n\nfix", []string{"harness"}, "", "source-repo-invalid", true},
		{"repo-key-beats-title", "[fixture/inbox#1] x", "/swarm\nrepo: fixture/upstream\n\nfix", []string{"harness"}, "fixture/upstream", "", true},
		{"old-binding-title-mismatch", "[fixture/upstream#41] fix", "/swarm\nharness: codex\n\nfix", []string{"harness"}, "", "source-repo-mismatch", true},
		{"old-binding-same-repo", "[fixture/inbox#7] task", "/swarm\nharness: codex\n\nfix", []string{"harness"}, "fixture/inbox", "", true},
		{"native-inbox-issue", "plain task", "do it", []string{"harness"}, "fixture/inbox", "", true},
		{"mirrored-target-issue", "[fixture/upstream#9] upstream", "body", []string{"upstream"}, "fixture/upstream", "", true},
		{"not-orchids", "[fixture/upstream#9] x", "/swarm\nrepo: fixture/upstream\n\nfix", nil, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := Issue{Number: 7, Title: tc.title, Body: tc.body, Labels: tc.labels}
			tgt, reason, ours := c.resolveTarget(is)
			if ours != tc.ours || reason != tc.reason || tgt.Repo != tc.repo {
				t.Fatalf("resolved %q/%q/%v, want %q/%q/%v", tgt.Repo, reason, ours, tc.repo, tc.reason, tc.ours)
			}
			if got, ok := c.targetFor(is); ok != (tc.ours && tc.reason == "") || got.Repo != tc.repo {
				t.Fatal("targetFor disagrees with the resolution")
			}
		})
	}
}

// A source-repo refusal is a plain notice; it is not written as a launch-state
// record the Harness reader's closed vocabulary would reject.
func TestSourceRepoRefusalIsPlainNoticeOnly(t *testing.T) {
	for _, reason := range []string{"source-repo-invalid", "source-repo-unavailable", "source-repo-mismatch"} {
		c := sourceRepoCoord(t)
		is := Issue{ID: "fixture-node", Number: 7, Title: "[fixture/upstream#1] x", Labels: []string{"harness"}}
		var posted string
		r := refusalFor(matrixReason(reason))
		if !validMatrixRefusal(r) || r.Status != "refused" {
			t.Fatalf("%s is not a plain refusal: %+v", reason, r)
		}
		c.reportIssueMatrixRefusal(context.Background(), 7, is, r, func(_ context.Context, _ string, _ int, body string) error { posted = body; return nil })
		if !strings.Contains(posted, "`"+reason+"`") || !strings.Contains(posted, "No agent was launched") {
			t.Fatalf("plain notice missing for %s: %q", reason, posted)
		}
		if files, _ := filepath.Glob(filepath.Join(c.cfg.Matrix.ReceiptRoot, "launch-*.json")); len(files) != 0 {
			t.Fatalf("%s written as a launch-state record: %v", reason, files)
		}
	}
	// Control: a reason the reader knows is still recorded.
	c := sourceRepoCoord(t)
	is := Issue{ID: "fixture-node", Number: 7, Title: "x", Labels: []string{"harness"}}
	c.reportIssueMatrixRefusal(context.Background(), 7, is, refusalFor(matrixReason("routing-invalid")), func(context.Context, string, int, string) error { return nil })
	if files, _ := filepath.Glob(filepath.Join(c.cfg.Matrix.ReceiptRoot, "launch-*.json")); len(files) != 1 {
		t.Fatal("control: a known refusal was not recorded")
	}
}

// Admission reports a source-repository refusal plainly and launches nothing;
// a resolvable binding is admitted to its source target.
func TestAdmissionTargetRefusesPlainly(t *testing.T) {
	c := sourceRepoCoord(t)
	posts := 0
	post := func(context.Context, string, int, string) error { posts++; return nil }
	old := Issue{ID: "fixture-node", Number: 7, Title: "[fixture/upstream#41] fix", Body: "/swarm\nharness: codex\n\nfix", Labels: []string{"harness"}}
	if _, ok := c.admissionTarget(context.Background(), 7, old, post); ok || posts != 1 {
		t.Fatalf("mismatched binding admitted or not reported: posts=%d", posts)
	}
	keyed := old
	keyed.Body = "/swarm\nharness: codex\nrepo: fixture/upstream\n\nfix"
	if tgt, ok := c.admissionTarget(context.Background(), 7, keyed, post); !ok || tgt.Repo != "fixture/upstream" || posts != 1 {
		t.Fatalf("keyed binding not admitted to its source: %q %v posts=%d", tgt.Repo, ok, posts)
	}
}

// Production spawn clones the SOURCE repository at its pinned revision, never
// the inbox; a mismatched binding stops before any host effect.
func TestSpawnClonesSourceRepo(t *testing.T) {
	for _, tc := range []struct {
		name, title, body, clone, rev string
	}{
		{"repo-key", "[fixture/upstream#41] fix", "/swarm\nharness: claude\nrepo: fixture/upstream\n\nfix it", "https://github.com/fixture/upstream", strings.Repeat("b", 40)},
		{"title-mismatch-stops", "[fixture/upstream#41] fix", "/swarm\nharness: claude\n\nfix it", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := sourceRepoCoord(t)
			bin := t.TempDir()
			log := filepath.Join(t.TempDir(), "remote-scripts")
			t.Setenv("SRC_REPO_LOG", log)
			writeFixture(t, filepath.Join(bin, "ssh"), `#!/bin/sh
for a; do last="$a"; done
printf '%s\n----\n' "$last" >> "$SRC_REPO_LOG"
case "$last" in *"git clone"*) exit 1;; esac
exit 0
`)
			if os.Chmod(filepath.Join(bin, "ssh"), 0700) != nil {
				t.Fatal("fixture")
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			h := Host{Name: "fixture-host", SSH: "fixture-host", Home: t.TempDir(), WorkdirRoot: t.TempDir()}
			is := Issue{ID: "fixture-node", Number: 7, Title: tc.title, Body: tc.body, Labels: []string{"harness"}}
			err := c.spawn(context.Background(), 7, is, h, "claude", parseOverrides(is.Body), nil)
			scripts, _ := os.ReadFile(log)
			if tc.clone == "" {
				if matrixCause(err) != "launch.target" || strings.Contains(string(scripts), "git clone") {
					t.Fatalf("a mismatched binding reached the host: err=%v scripts=%q", err, scripts)
				}
				return
			}
			if !strings.Contains(string(scripts), "git clone --depth=1 "+tc.clone+" .") || !strings.Contains(string(scripts), tc.rev) {
				t.Fatalf("spawn did not clone the source repository at its pin: err=%v scripts=%q", err, scripts)
			}
			if strings.Contains(string(scripts), "github.com/fixture/inbox") {
				t.Fatal("spawn touched the inbox repository")
			}
		})
	}
}
