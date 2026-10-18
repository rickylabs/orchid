package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeCockpitTargets(t *testing.T, root, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, cockpitTargetsFile)
	_ = os.Remove(path)
	if os.WriteFile(path, []byte(body), 0600) != nil || os.Chmod(path, mode) != nil {
		t.Fatal("snapshot fixture")
	}
}

func setCockpitTargets(cfg *Config, targets ...Target) {
	if cfg.cockpit == nil {
		cfg.cockpit = &cockpitTargetSource{}
	}
	for i := range targets {
		targets[i].cockpit = true
	}
	cfg.cockpit.list.Store(&targets)
}

func cockpitRepos(targets []Target) string {
	var out []string
	for _, t := range targets {
		if t.cockpit {
			out = append(out, t.Repo)
		}
	}
	return strings.Join(out, ",")
}

func TestCockpitTargetsSnapshotLoadSwapKeepLastGoodMissing(t *testing.T) {
	root := privateTestRoot(t)
	cfg := &Config{Inbox: "example/inbox", ActionRequestRoot: root, Targets: []Target{{Label: "harness", Repo: "example/inbox"}}}
	c := newCoord(cfg)
	logs := captureLog(t)
	count := func(s string) int { return strings.Count(logs.String(), s) }

	c.refreshCockpitTargets() // no snapshot yet: no cockpit list, nothing logged
	if cockpitRepos(cfg.effectiveTargets()) != "" || logs.Len() != 0 {
		t.Fatalf("missing snapshot produced a list or a log: %q", logs.String())
	}
	writeCockpitTargets(t, root, `{"schema_version":1,"targets":[{"repo":"example/project","agents":["claude","codex"]}]}`, 0600)
	c.refreshCockpitTargets()
	c.refreshCockpitTargets() // unchanged: no second log
	writeCockpitTargets(t, root, `{ "schema_version": 1, "targets": [ {"repo": "example/project", "agents": ["claude", "codex"]} ] }`, 0600)
	c.refreshCockpitTargets() // other bytes, same list: no swap log
	if cockpitRepos(cfg.effectiveTargets()) != "example/project" || count("dispatch targets from cockpit: 1 (example/project)") != 1 {
		t.Fatalf("valid snapshot not loaded once: %q", logs.String())
	}
	for _, bad := range []struct{ name, body, reason string }{
		{"agent", `{"schema_version":1,"targets":[{"repo":"example/canary-private","agents":["claude","invented"]}]}`, "invalid-agents"},
		{"no-agents", `{"schema_version":1,"targets":[{"repo":"example/canary-private","agents":[]}]}`, "invalid-agents"},
		{"duplicate", `{"schema_version":1,"targets":[{"repo":"example/canary-private","agents":["claude"]},{"repo":"Example/Canary-Private","agents":["codex"]}]}`, "duplicate-repository"},
		{"repo", `{"schema_version":1,"targets":[{"repo":"canary-private","agents":["claude"]}]}`, "invalid-repository"},
		{"schema", `{"schema_version":2,"targets":[]}`, "schema-version-or-targets"},
		{"unknown-field", `{"schema_version":1,"targets":[],"canary-private":true}`, "invalid-json"},
	} {
		writeCockpitTargets(t, root, bad.body, 0600)
		c.refreshCockpitTargets()
		c.refreshCockpitTargets()
		if cockpitRepos(cfg.effectiveTargets()) != "example/project" || count("unusable ("+bad.reason+")") < 1 {
			t.Fatalf("%s: last good list not kept or problem not logged: %q", bad.name, logs.String())
		}
	}
	if count("unusable (invalid-agents)") != 1 {
		t.Fatalf("one reason logged more than once in a row: %q", logs.String())
	}
	writeCockpitTargets(t, root, `{"schema_version":1,"targets":[{"repo":"example/project","agents":["claude"]}]}`, 0644)
	c.refreshCockpitTargets()
	big := `{"schema_version":1,"targets":[],"pad":"` + strings.Repeat("x", cockpitTargetsLimit) + `"}`
	writeCockpitTargets(t, root, big, 0600)
	c.refreshCockpitTargets()
	writeCockpitTargets(t, root, `{"schema_version":1,"targets":[]}`, 0600)
	link := filepath.Join(root, "elsewhere.json")
	_ = os.Rename(filepath.Join(root, cockpitTargetsFile), link)
	if os.Symlink(link, filepath.Join(root, cockpitTargetsFile)) != nil {
		t.Fatal("symlink fixture")
	}
	c.refreshCockpitTargets()
	if cockpitRepos(cfg.effectiveTargets()) != "example/project" || count("unusable (mode-not-private)") != 1 || count("unusable (oversized)") != 1 || count("unusable (not-a-regular-file)") != 1 {
		t.Fatalf("unsafe snapshot file accepted: %q", logs.String())
	}
	if strings.Contains(logs.String(), "canary") {
		t.Fatal("snapshot contents reached the log")
	}
	_ = os.Remove(filepath.Join(root, cockpitTargetsFile))
	writeCockpitTargets(t, root, `{"schema_version":1,"targets":[{"repo":"example/other","agents":["agy"]},{"repo":"example/project","agents":["codex","opencode"]}]}`, 0660)
	c.refreshCockpitTargets()
	got := cfg.effectiveTargets()
	if cockpitRepos(got) != "example/other,example/project" || count("dispatch targets from cockpit: 2 (example/other, example/project)") != 1 || got[2].Label != "" || strings.Join(got[2].Agents, ",") != "codex,opencode" {
		t.Fatalf("changed snapshot not swapped: %+v %q", got, logs.String())
	}
	_ = os.Remove(filepath.Join(root, cockpitTargetsFile)) // removed after a load: keep the last good list
	c.refreshCockpitTargets()
	c.refreshCockpitTargets()
	if cockpitRepos(cfg.effectiveTargets()) != "example/other,example/project" || count("unusable (missing)") != 1 {
		t.Fatalf("a removed snapshot dropped the list: %q", logs.String())
	}
	writeCockpitTargets(t, root, `{"schema_version":1,"targets":[]}`, 0600)
	c.refreshCockpitTargets()
	if cockpitRepos(cfg.effectiveTargets()) != "" || count("dispatch targets from cockpit: 0 ()") != 1 {
		t.Fatalf("an empty list did not turn every cockpit target off: %q", logs.String())
	}
}

func TestEffectiveTargetsStaticWinsAndEmptyLabelIsNeverPolled(t *testing.T) {
	cfg := &Config{Inbox: "example/inbox", Targets: []Target{
		{Label: "harness", Repo: "example/inbox"},
		{Label: "proj", Repo: "Example/Project", AutoMerge: true},
		{Label: "proj", Repo: "example/project-mirror"},
	}}
	setCockpitTargets(cfg, Target{Repo: "example/project", Agents: []string{"codex"}}, Target{Repo: "example/other", Agents: []string{"claude"}})
	got := cfg.effectiveTargets()
	if len(got) != 4 || got[1].Repo != "Example/Project" || got[1].cockpit || !got[1].AutoMerge || got[3].Repo != "example/other" || !got[3].cockpit || got[3].Label != "" {
		t.Fatalf("effective list is not static then new cockpit repos: %+v", got)
	}
	// A config copy, as the owner-grant store keeps, reads the same live list.
	clone, err := cloneOwnerNativeConfig(cfg)
	if err != nil || cockpitRepos(clone.effectiveTargets()) != "example/other" {
		t.Fatal("the grant store copy lost the cockpit list")
	}
	setCockpitTargets(cfg, Target{Repo: "example/third", Agents: []string{"agy"}})
	if cockpitRepos(clone.effectiveTargets()) != "example/third" {
		t.Fatal("the grant store copy did not see the swap")
	}
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "gh-calls")
	t.Setenv("GH_CALLS", calls)
	writeFixture(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GH_CALLS\"\nprintf '[]'\n")
	_ = os.Chmod(filepath.Join(bin, "gh"), 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := &Coord{cfg: cfg, st: loadState(filepath.Join(t.TempDir(), "state.json")), source: newFakeSource()}
	if _, _, ok := c.pollIssues(context.Background()); !ok {
		t.Fatal("poll failed")
	}
	raw, _ := os.ReadFile(calls)
	if strings.Count(string(raw), "--label harness ") != 1 || strings.Count(string(raw), "--label proj ") != 1 || strings.Contains(string(raw), "--label  ") || strings.Count(string(raw), "issue list") != 2 {
		t.Fatalf("inbox poll is not one call per distinct non-empty label: %q", raw)
	}
}

// A /swarm comment on a repository only the cockpit lists binds and resolves to
// that repository, and refuses once the cockpit list drops it.
func TestSourceBindingOnCockpitOnlyRepository(t *testing.T) {
	c, f, _ := sourceFixture(t)
	c.cfg.Targets = c.cfg.Targets[:1] // only the inbox is static
	setCockpitTargets(c.cfg, Target{Repo: sourceRepo, Agents: []string{"codex"}})
	at := startSource(t, c)
	f.post(sourceRepo, 2063, 3100000040, ownerGitHubID, sourceTrigger, at)
	c.sourceTick(context.Background())
	open, all := map[int]Issue{}, map[int]bool{}
	if !c.pollSourceBindings(context.Background(), open, all) {
		t.Fatal("poll failed")
	}
	is, ok := open[3100000040]
	if !ok || is.Source == nil || is.Source.Repo != sourceRepo {
		t.Fatalf("cockpit repository comment not bound: %+v", is)
	}
	if tgt, reason, ok := c.resolveTarget(is); !ok || reason != "" || tgt.Repo != sourceRepo || !tgt.cockpit {
		t.Fatalf("target %+v %q %v, want the cockpit repository", tgt, reason, ok)
	}
	setCockpitTargets(c.cfg)
	open = map[int]Issue{}
	c.pollSourceBindings(context.Background(), open, map[int]bool{})
	if _, reason, ok := c.resolveTarget(open[3100000040]); !ok || reason != "source-repo-unavailable" {
		t.Fatalf("a dropped repository still resolves: %q %v", reason, ok)
	}
	for _, repo := range c.sourceRepos() {
		if repo == sourceRepo {
			t.Fatal("a dropped repository is still scanned for comments")
		}
	}
}

func TestCockpitTargetAllowedAgentsRefuseOtherTransports(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target Target
		ok     bool
	}{
		{"static", Target{Repo: "example/project", Agents: []string{"codex"}}, true},
		{"cockpit-allows", Target{Repo: "example/project", Agents: []string{"codex", "claude"}, cockpit: true}, true},
		{"cockpit-refuses", Target{Repo: "example/project", Agents: []string{"codex"}, cockpit: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, deps, refusal, _ := unpinnedAttemptFixture(t, strings.Repeat("e", 40))
			deps.preflight = true
			_, ok := c.matrixAttempt(context.Background(), 1, attemptIssue, tc.target, map[string]int{"claude": 1}, deps)
			if ok != tc.ok || (!tc.ok && refusal.ReasonCode != "harness-conflict") {
				t.Fatalf("launch %v refusal %+v", ok, *refusal)
			}
			cfg := &Config{Inbox: "example/inbox", Matrix: c.cfg.Matrix}
			if tc.target.cockpit {
				setCockpitTargets(cfg, tc.target)
			} else {
				cfg.Targets = []Target{tc.target}
			}
			p := validateMatrixIssue(context.Background(), cfg, attemptIssue, "example/project", matrixConfigDeps{read: deps.read, resolve: deps.resolve})
			if (len(p) == 0) != tc.ok || (!tc.ok && p[0].Reason != "harness-conflict") {
				t.Fatalf("preflight %+v", p)
			}
		})
	}
}

var attemptIssue = Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic", Body: "/swarm\ntier: feature\nrole: implementation\n\nSynthetic task"}

// unpinnedAttemptFixture is a matrix attempt with no target pin whose head
// reader answers head ("" = unreadable). The returned launch records the base.
func unpinnedAttemptFixture(t *testing.T, head string) (*Coord, matrixAttemptDeps, *matrixRefusal, *string) {
	t.Helper()
	root := privateTestRoot(t)
	cfg := &Config{Inbox: "example/inbox", Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: strings.Repeat("b", 40)}, Governor: Gov{WeeklyCeiling: 92}}
	c := &Coord{cfg: cfg, st: loadState(filepath.Join(root, "state.json"))}
	now := time.Now()
	c.gov.q = map[string]quota{"claude": {ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()}}}
	refusal, launched := &matrixRefusal{}, new(string)
	var mu sync.Mutex
	deps := matrixAttemptDeps{
		report: func(r matrixRefusal) { *refusal = r },
		read: func(context.Context, string, string, string) (string, error) {
			return "| `routing` | matrix `implementation` row |", nil
		},
		resolve: func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) { return syntheticRoute(), nil },
		host:    func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, true },
		persist: persistMatrixReceipt,
		head: func(context.Context, string) (string, error) {
			if head == "" {
				return "", errMatrix
			}
			return head + "\n", nil
		},
		launch: func(_ context.Context, _ int, _ Issue, _ Host, _ string, _ Overrides, r *durableMatrixReceipt) error {
			mu.Lock()
			defer mu.Unlock()
			*launched = r.baseRevision()
			return nil
		},
	}
	return c, deps, refusal, launched
}

func TestUnpinnedLaunchRecordsTheHeadAsBaseRevision(t *testing.T) {
	head := strings.Repeat("e", 40)
	c, deps, refusal, launched := unpinnedAttemptFixture(t, head)
	if _, ok := c.matrixAttempt(context.Background(), 1, attemptIssue, Target{Repo: "example/project"}, map[string]int{"claude": 1}, deps); !ok || *launched != head {
		t.Fatalf("unpinned launch did not start from the head: ok=%v base=%q refusal=%+v", ok, *launched, *refusal)
	}
	files, _ := filepath.Glob(filepath.Join(c.cfg.Matrix.ReceiptRoot, "*", "record", "binding.json"))
	var binding struct{ BaseRevision string }
	if len(files) != 1 {
		t.Fatal("binding record missing")
	}
	raw, _ := os.ReadFile(files[0])
	if json.Unmarshal(raw, &binding) != nil || binding.BaseRevision != head {
		t.Fatalf("binding.json does not record the base: %s", raw)
	}
	pin := strings.Repeat("c", 40)
	c, deps, _, launched = unpinnedAttemptFixture(t, head)
	c.cfg.Matrix.TargetRevisions = map[string]string{"example/project": pin}
	if _, ok := c.matrixAttempt(context.Background(), 1, attemptIssue, Target{Repo: "example/project"}, map[string]int{"claude": 1}, deps); !ok || *launched != pin {
		t.Fatalf("an optional pin did not override the head: %q", *launched)
	}
	c, deps, refusal, launched = unpinnedAttemptFixture(t, "")
	if _, ok := c.matrixAttempt(context.Background(), 1, attemptIssue, Target{Repo: "example/project"}, map[string]int{"claude": 1}, deps); ok || refusal.ReasonCode != "target-revision-invalid" || *launched != "" {
		t.Fatalf("an unreadable head launched: %+v", *refusal)
	}
}

func TestSpawnChecksOutTheRecordedBase(t *testing.T) {
	c := sourceRepoCoord(t)
	bin := t.TempDir()
	scripts := filepath.Join(t.TempDir(), "remote-scripts")
	t.Setenv("SRC_REPO_LOG", scripts)
	writeFixture(t, filepath.Join(bin, "ssh"), `#!/bin/sh
for a; do last="$a"; done
printf '%s\n----\n' "$last" >> "$SRC_REPO_LOG"
case "$last" in *"# orchid-checkout-root"*) exec sh -c "$last";; *"git clone"*) exit 1;; esac
exit 0
`)
	_ = os.Chmod(filepath.Join(bin, "ssh"), 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := Host{Name: "fixture-host", SSH: "fixture-host", Home: t.TempDir(), WorkdirRoot: t.TempDir()}
	is := Issue{ID: "fixture-node", Number: 7, Title: "fix", Body: "/swarm\nharness: claude\nrepo: fixture/upstream\n\nfix it", Labels: []string{"harness"}}
	r := registrationReceipt(t, "claude", parseOverrides(is.Body))
	base := strings.Repeat("e", 40)
	r.base = base
	delete(c.cfg.Matrix.TargetRevisions, "fixture/upstream")
	_ = c.spawn(context.Background(), 7, is, h, "claude", parseOverrides(is.Body), r)
	raw, _ := os.ReadFile(scripts)
	if !strings.Contains(string(raw), "git fetch --depth=1 origin '"+base+"'") {
		t.Fatalf("the checkout is not the recorded base: %q", raw)
	}
}

func TestOwnerGrantWithoutTargetPinBindsTheHeadAtInstall(t *testing.T) {
	s, r, _ := ownerPortFixture(t, "claude")
	_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json")) // the owner's Approve in the Cockpit
	s.cfg.Matrix.TargetRevisions = nil
	head := strings.Repeat("e", 40)
	s.deps.head = func(context.Context, string) (string, error) { return head, nil }
	ack := s.install(context.Background(), r)
	if ack.State != "LIVE" || ack.TargetRevision != head {
		t.Fatalf("unpinned grant not LIVE at the head: %+v", ack)
	}
	s.deps.head = func(context.Context, string) (string, error) { return strings.Repeat("f", 40), nil } // the branch moved
	if again := s.install(context.Background(), r); again.State != "LIVE" || again.RecordChecksum != ack.RecordChecksum {
		t.Fatalf("a retried install bound a second base: %+v", again)
	}
	if st := s.readStatus(context.Background(), r.OperationID); st.State != "LIVE" || st.TargetRevision != head {
		t.Fatalf("the record is not valid at its own revision: %+v", st)
	}
	restored, err := newOwnerNativeGrantStore(context.Background(), s.cfg, s.options, s.deps)
	if err != nil || restored.readStatus(context.Background(), r.OperationID).State != "LIVE" {
		t.Fatal("unpinned record not recovered")
	}
	s.cfg.Matrix.TargetRevisions = map[string]string{"example/project": strings.Repeat("c", 40)} // a different pin fences it
	if st := s.readStatus(context.Background(), r.OperationID); st.State == "LIVE" {
		t.Fatal("a record off the pin stayed LIVE")
	}
	s.cfg.Matrix.TargetRevisions = nil
	logs := captureLog(t)
	s.deps.head = func(context.Context, string) (string, error) { return "", errMatrix }
	other := r
	other.OperationID = "22222222-2222-4222-8222-222222222222"
	if refused := s.install(context.Background(), other); refused.State != "REFUSED" || !strings.Contains(logs.String(), "the matrix or target revision is not pinned") {
		t.Fatalf("an unreadable head was granted: %+v %q", refused, logs.String())
	}
}
