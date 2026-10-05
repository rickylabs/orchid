package main

import (
	"context"
	"os"
	"os/exec"
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
		{"/swarm\nrepo: fixture/upstream#12\n\ndo it", "fixture/upstream", true},     // a #n suffix refuses, never stripped
		{"/swarm\nrepo: fixture/upstream # c\n\ndo it", "fixture/upstream", true},    // so does a comment
		{"/swarm\nrepo:\n\ndo it", "", true},                                         // an empty declaration refuses
		{"/swarm\nrepo:   \n\ndo it", "", true},                                      // whitespace only
		{"/swarm\nrepo: fixture/upstream\nrepo:\n\ndo it", "fixture/upstream", true}, // a later empty duplicate
		{"/swarm\nrepo:\nrepo: fixture/upstream\n\ndo it", "", true},                 // an earlier empty duplicate
		{"/swarm\nrepo: # only a comment\n\ndo it", "", true},
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
	for _, body := range []string{"/swarm\nharness: codex\nrepo:\n\nfix", "/swarm\nharness: codex\nrepo: fixture/inbox # c\n\nfix"} {
		native := Issue{ID: "fixture-node", Number: 7, Title: "plain native task", Body: body, Labels: []string{"harness"}}
		before := posts
		if _, ok := c.admissionTarget(context.Background(), 7, native, post); ok || posts != before+1 {
			t.Fatalf("invalid repo declaration admitted: %q", body)
		}
	}
	posts = 1
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
		{"empty-repo-key-stops", "plain native task", "/swarm\nharness: claude\nrepo:\n\nfix it", "", ""},
		{"empty-duplicate-stops", "plain native task", "/swarm\nharness: claude\nrepo: fixture/inbox\nrepo:\n\nfix it", "", ""},
		{"suffixed-repo-stops", "[fixture/upstream#41] fix", "/swarm\nharness: claude\nrepo: fixture/inbox#42\n\nfix it", "", ""},
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
				if matrixCause(err) != "launch.target" || len(scripts) != 0 {
					t.Fatalf("a refused binding reached the host: err=%v scripts=%q", err, scripts)
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

// The checkout proves its origin is the resolved repository before any agent
// starts. Real local repositories stand in for GitHub via insteadOf; the fake
// SSH runs only the workspace preparation, locally.
func TestSpawnWorkspaceOriginMustBeSourceRepo(t *testing.T) {
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=f", "GIT_AUTHOR_EMAIL=f@example.invalid", "GIT_COMMITTER_NAME=f", "GIT_COMMITTER_EMAIL=f@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	hub := t.TempDir()
	seed := filepath.Join(hub, "seed")
	_ = os.MkdirAll(seed, 0700)
	git(seed, "init", "-q")
	_ = os.WriteFile(filepath.Join(seed, "f"), []byte("x"), 0600)
	git(seed, "add", "f")
	git(seed, "commit", "-qm", "seed")
	shared := git(seed, "rev-parse", "HEAD")
	for _, r := range []string{"inbox", "upstream"} { // forks sharing the pinned commit
		git(hub, "clone", "-q", "--bare", seed, filepath.Join(hub, "fixture", r))
	}
	// GitHub names are case-insensitive; the local stand-in path is not.
	_ = os.MkdirAll(filepath.Join(hub, "Fixture"), 0700)
	if os.Symlink(filepath.Join(hub, "fixture", "upstream"), filepath.Join(hub, "Fixture", "UpStream.git")) != nil {
		t.Fatal("fixture")
	}
	other := filepath.Join(hub, "unrelated")
	_ = os.MkdirAll(other, 0700)
	git(other, "init", "-q")
	gitconfig := filepath.Join(hub, "gitconfig")
	_ = os.WriteFile(gitconfig, []byte("[url \"file://"+hub+"/\"]\n\tinsteadOf = https://github.com/\n[protocol \"file\"]\n\tallow = always\n"), 0600)
	t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	for _, tc := range []struct {
		name, existing string // existing origin in issue-7 ("" = fresh clone)
		ok             bool
	}{
		{"fresh-clone", "", true},
		{"existing-correct-origin", "https://github.com/fixture/upstream", true},
		{"existing-correct-origin-case", "https://github.com/Fixture/UpStream.git", true},
		{"existing-other-origin-shared-commit", "https://github.com/fixture/inbox", false},
		{"existing-unrelated-origin", "https://github.com/fixture/unrelated", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := sourceRepoCoord(t)
			c.cfg.Matrix.TargetRevisions = map[string]string{"fixture/inbox": shared, "fixture/upstream": shared}
			root := t.TempDir()
			work := filepath.Join(root, "issue-7")
			if tc.existing != "" {
				_ = os.MkdirAll(work, 0700)
				git(work, "init", "-q")
				git(work, "remote", "add", "origin", tc.existing)
			}
			bin := t.TempDir()
			log := filepath.Join(t.TempDir(), "remote-calls")
			t.Setenv("SRC_REPO_LOG", log)
			writeFixture(t, filepath.Join(bin, "ssh"), `#!/bin/sh
for a; do last="$a"; done
case "$last" in
 *"git checkout -fB"*) printf 'prep\n' >> "$SRC_REPO_LOG"; exec sh -c "$last";;
 *herdr*) printf 'herdr\n' >> "$SRC_REPO_LOG"; exit 1;;
esac
exit 0
`)
			_ = os.Chmod(filepath.Join(bin, "ssh"), 0700)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			h := Host{Name: "fixture-host", SSH: "fixture-host", Home: t.TempDir(), WorkdirRoot: root}
			is := Issue{ID: "fixture-node", Number: 7, Title: "[fixture/upstream#41] fix", Body: "/swarm\nharness: claude\nrepo: fixture/upstream\n\nfix it", Labels: []string{"harness"}}
			r := registrationReceipt(t, "claude", parseOverrides(is.Body))
			err := c.spawn(context.Background(), 7, is, h, "claude", parseOverrides(is.Body), r)
			calls, _ := os.ReadFile(log)
			if !tc.ok {
				if matrixCause(err) != "launch.worktree-origin" || strings.Contains(string(calls), "herdr") {
					t.Fatalf("a workspace with another origin was used: err=%v calls=%q", err, calls)
				}
				if got := git(work, "config", "--get", "remote.origin.url"); got != tc.existing {
					t.Fatal("the existing workspace was replaced")
				}
				return
			}
			if matrixCause(err) == "launch.worktree-origin" || matrixCause(err) == "launch.worktree" {
				t.Fatalf("a correct workspace was refused: %v", err)
			}
			if got := strings.ToLower(strings.TrimSuffix(git(work, "config", "--get", "remote.origin.url"), ".git")); got != "https://github.com/fixture/upstream" {
				t.Fatalf("workspace origin %q is not the source repository", got)
			}
			if git(work, "rev-parse", "HEAD") != shared || git(work, "rev-parse", "--abbrev-ref", "HEAD") != "fixture/7" {
				t.Fatal("workspace not at the pinned revision on the issue branch")
			}
		})
	}
}
