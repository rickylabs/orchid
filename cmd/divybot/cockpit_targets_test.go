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
	writeCockpitTargets(t, root, `{"schema_version":1,"targets":[{"repo":"example/project","agents":["claude","codex"],"since":"2026-01-01T00:00:00.000Z"}]}`, 0600)
	c.refreshCockpitTargets()
	c.refreshCockpitTargets() // unchanged: no second log
	writeCockpitTargets(t, root, `{ "schema_version": 1, "targets": [ {"repo": "example/project", "agents": ["claude", "codex"], "since": "2026-01-01T00:00:00.000Z"} ] }`, 0600)
	c.refreshCockpitTargets() // other bytes, same list: no swap log
	if cockpitRepos(cfg.effectiveTargets()) != "example/project" || count("dispatch targets from cockpit: 1 (example/project)") != 1 {
		t.Fatalf("valid snapshot not loaded once: %q", logs.String())
	}
	for _, bad := range []struct{ name, body, reason string }{
		{"agent", `{"schema_version":1,"targets":[{"repo":"example/canary-private","agents":["claude","invented"],"since":"2026-01-01T00:00:00.000Z"}]}`, "invalid-agents"},
		{"no-agents", `{"schema_version":1,"targets":[{"repo":"example/canary-private","agents":[],"since":"2026-01-01T00:00:00.000Z"}]}`, "invalid-agents"},
		{"duplicate", `{"schema_version":1,"targets":[{"repo":"example/canary-private","agents":["claude"],"since":"2026-01-01T00:00:00.000Z"},{"repo":"Example/Canary-Private","agents":["codex"],"since":"2026-01-01T00:00:00.000Z"}]}`, "duplicate-repository"},
		{"repo", `{"schema_version":1,"targets":[{"repo":"canary-private","agents":["claude"],"since":"2026-01-01T00:00:00.000Z"}]}`, "invalid-repository"},
		{"schema", `{"schema_version":2,"targets":[]}`, "schema-version-or-targets"},
		{"no-since", `{"schema_version":1,"targets":[{"repo":"example/canary-private","agents":["claude"]}]}`, "invalid-since"},
		{"bad-since", `{"schema_version":1,"targets":[{"repo":"example/canary-private","agents":["claude"],"since":"yesterday"}]}`, "invalid-since"},
		{"future-since", `{"schema_version":1,"targets":[{"repo":"example/canary-private","agents":["claude"],"since":"` + time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z") + `"}]}`, "invalid-since"},
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
	writeCockpitTargets(t, root, `{"schema_version":1,"targets":[{"repo":"example/project","agents":["claude"],"since":"2026-01-01T00:00:00.000Z"}]}`, 0644)
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
	writeCockpitTargets(t, root, `{"schema_version":1,"targets":[{"repo":"example/other","agents":["agy"],"since":"2026-01-01T00:00:00.000Z"},{"repo":"example/project","agents":["codex","opencode"],"since":"2026-01-01T00:00:00.000Z"}]}`, 0660)
	c.refreshCockpitTargets()
	got := cfg.effectiveTargets()
	if cockpitRepos(got) != "example/other,example/project" || count("dispatch targets from cockpit: 2 (example/other, example/project)") != 1 || len(got) != 2 || got[1].Label != "" || strings.Join(got[1].Agents, ",") != "codex,opencode" {
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
	if len(cfg.effectiveTargets()) != 0 || count("dispatch targets from cockpit: 0 ()") != 1 || count("static targets not enabled in the cockpit: 1 (example/inbox)") != 1 {
		t.Fatalf("an empty list did not turn every cockpit target off: %q", logs.String())
	}
}

func TestEffectiveTargetsCockpitListIsAuthoritativeOnceLoaded(t *testing.T) {
	cfg := &Config{Inbox: "example/inbox", Targets: []Target{
		{Label: "harness", Repo: "example/inbox"},
		{Label: "proj", Repo: "Example/Project", AutoMerge: true, Priority: 3, NeedCap: "build", PromptHint: "hint", MirrorAssignments: true, Agents: []string{"claude", "codex"}},
		{Label: "proj", Repo: "example/project-mirror"},
	}}
	if got := cfg.effectiveTargets(); len(got) != 3 || cockpitRepos(got) != "" {
		t.Fatal("without a snapshot the static list is not the fallback")
	}
	setCockpitTargets(cfg, Target{Repo: "example/project", Agents: []string{"codex"}}, Target{Repo: "example/other", Agents: []string{"claude"}})
	got := cfg.effectiveTargets()
	p := got[0]
	if len(got) != 2 || cockpitRepos(got) != "example/project,example/other" || p.Label != "proj" || !p.AutoMerge || p.Priority != 3 || p.NeedCap != "build" || p.PromptHint != "hint" || !p.MirrorAssignments || strings.Join(p.Agents, ",") != "codex" || got[1].Label != "" {
		t.Fatalf("loaded list is not exactly the cockpit repositories with static metadata: %+v", got)
	}
	// A repository the cockpit turned off is no target, even though it is static.
	if _, _, ok := resolveTargetIn(got, Issue{Labels: []string{"harness"}}); ok {
		t.Fatal("a static repository missing from the cockpit list still resolves")
	}
	// The cockpit allow-list binds a static repository too: codex only refuses claude.
	c, deps, refusal, _ := unpinnedAttemptFixture(t, strings.Repeat("e", 40))
	c.cfg.Targets, c.cfg.cockpit = cfg.Targets, cfg.cockpit
	deps.preflight = true
	tgt, reason, ok := c.resolveTarget(Issue{Labels: []string{"proj"}, Body: attemptIssue.Body})
	if !ok || reason != "" || !tgt.cockpit {
		t.Fatalf("static label did not resolve to the cockpit target: %+v %q", tgt, reason)
	}
	if _, launched := c.matrixAttempt(context.Background(), 1, attemptIssue, tgt, map[string]int{"claude": 1}, deps); launched || refusal.ReasonCode != "harness-conflict" {
		t.Fatalf("a codex-only repository launched claude: %+v", *refusal)
	}
	setCockpitTargets(cfg)
	if len(cfg.effectiveTargets()) != 0 {
		t.Fatal("an empty loaded list left targets")
	}
	if problems := validateMatrixConfig(context.Background(), &Config{Inbox: "example/inbox", Matrix: cfg.Matrix}); len(problems) > 0 {
		for _, p := range problems {
			if p.Field == "targets" {
				t.Fatal("config validation still requires a static target")
			}
		}
	}
	// A config copy, as the owner-grant store keeps, reads the same live list.
	setCockpitTargets(cfg, Target{Repo: "example/other", Agents: []string{"claude"}})
	clone, err := cloneOwnerNativeConfig(cfg)
	if err != nil || cockpitRepos(clone.effectiveTargets()) != "example/other" {
		t.Fatal("the grant store copy lost the cockpit list")
	}
	setCockpitTargets(cfg, Target{Repo: "example/project", Agents: []string{"agy"}}, Target{Repo: "example/third", Agents: []string{"agy"}})
	if cockpitRepos(clone.effectiveTargets()) != "example/project,example/third" {
		t.Fatal("the grant store copy did not see the swap")
	}
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "gh-calls")
	t.Setenv("GH_CALLS", calls)
	writeFixture(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$GH_CALLS\"\nprintf '[]'\n")
	_ = os.Chmod(filepath.Join(bin, "gh"), 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg.Targets = append(cfg.Targets, Target{Label: "proj", Repo: "example/third"})
	pc := &Coord{cfg: cfg, st: loadState(filepath.Join(t.TempDir(), "state.json")), source: newFakeSource()}
	if _, _, ok := pc.pollIssues(context.Background()); !ok {
		t.Fatal("poll failed")
	}
	raw, _ := os.ReadFile(calls)
	if strings.Count(string(raw), "--label proj ") != 1 || strings.Contains(string(raw), "--label harness ") || strings.Contains(string(raw), "--label  ") || strings.Count(string(raw), "issue list") != 1 {
		t.Fatalf("inbox poll is not one call per distinct non-empty label of an enabled target: %q", raw)
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
			*launched = r.admission
			return nil
		},
	}
	return c, deps, refusal, launched
}

func TestUnpinnedLaunchRecordsTheHeadAsAdmissionRevision(t *testing.T) {
	head := strings.Repeat("e", 40)
	c, deps, refusal, launched := unpinnedAttemptFixture(t, head)
	if _, ok := c.matrixAttempt(context.Background(), 1, attemptIssue, Target{Repo: "example/project"}, map[string]int{"claude": 1}, deps); !ok || *launched != head {
		t.Fatalf("unpinned launch did not start from the head: ok=%v base=%q refusal=%+v", ok, *launched, *refusal)
	}
	files, _ := filepath.Glob(filepath.Join(c.cfg.Matrix.ReceiptRoot, "*", "record", "binding.json"))
	var binding struct{ AdmissionRevision string }
	if len(files) != 1 {
		t.Fatal("binding record missing")
	}
	raw, _ := os.ReadFile(files[0])
	if json.Unmarshal(raw, &binding) != nil || binding.AdmissionRevision != head {
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
	r.admission = base
	delete(c.cfg.Matrix.TargetRevisions, "fixture/upstream")
	_ = c.spawn(context.Background(), 7, is, h, "claude", parseOverrides(is.Body), r)
	raw, _ := os.ReadFile(scripts)
	if !strings.Contains(string(raw), "git fetch --depth=1 --no-tags origin 'HEAD'") {
		t.Fatalf("the checkout did not fetch the default branch independently of admission metadata: %q", raw)
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

// Enabling a repository in the cockpit and posting /swarm before the first scan
// launches it; a comment from before the switch never does. A re-enable moves
// the start forward.
func TestCockpitRepositoryScanStartsAtSince(t *testing.T) {
	c, f, _ := sourceFixture(t)
	c.cfg.Targets = c.cfg.Targets[:1]
	now := time.Now().UTC()
	enabled := now.Add(-4 * time.Minute)
	setCockpitTargets(c.cfg, Target{Repo: "example/inbox", Agents: []string{"codex"}, since: enabled}, Target{Repo: sourceRepo, Agents: []string{"codex"}, since: enabled})
	f.post(sourceRepo, 2063, 3100000050, ownerGitHubID, sourceTrigger, enabled.Add(-time.Minute)) // before agents were on
	f.post(sourceRepo, 2063, 3100000051, ownerGitHubID, sourceTrigger, enabled.Add(time.Minute))  // after, before the first scan
	c.sourceTick(context.Background())
	if c.st.SourceBindings[3100000051] == nil || c.st.SourceBindings[3100000050] != nil {
		t.Fatalf("first scan did not start at since: %+v", c.st.SourceBindings)
	}
	if got := c.st.SourceStarts[sourceRepo]; !got.Equal(enabled.Truncate(time.Second)) {
		t.Fatalf("start %v, want since", got)
	}
	again := now.Add(-time.Minute) // turned off, then on again
	setCockpitTargets(c.cfg, Target{Repo: "example/inbox", Agents: []string{"codex"}, since: enabled}, Target{Repo: sourceRepo, Agents: []string{"codex"}, since: again})
	f.issues[sourceRepo+"#2064"] = sourceIssueView{NodeID: "I_source_2064", Title: "Fix more", State: "OPEN"}
	f.post(sourceRepo, 2064, 3100000052, ownerGitHubID, sourceTrigger, again.Add(-20*time.Second)) // while it was off
	c.sourceTick(context.Background())
	if c.st.SourceBindings[3100000052] != nil || !c.st.SourceStarts[sourceRepo].Equal(again.Truncate(time.Second)) {
		t.Fatalf("a re-enable did not move the start forward: start=%v bindings=%d", c.st.SourceStarts[sourceRepo], len(c.st.SourceBindings))
	}
}

// A persisted LIVE comment grant for a repository only the cockpit lists is
// recovered on restart, because the snapshot loads before grant recovery.
func TestOwnerGrantForCockpitRepositoryRecoversAfterRestart(t *testing.T) {
	s, r, _ := ownerPortFixture(t, "claude")
	_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
	r.Trigger = ownerNativeCommentTrigger
	r.ExpectedBriefDigest = shaText([]byte("/swarm\nharness: claude\nrepo: example/project\n\nFix it."))
	r.NativeOverride.Route = ownerNativeRoute{Harness: "claude", Provider: "anthropic", Model: "claude-opus-5-5", Effort: "high"}
	spool := privateTestRoot(t)
	writeCockpitTargets(t, spool, `{"schema_version":1,"targets":[{"repo":"example/project","agents":["claude"],"since":"2026-01-01T00:00:00.000Z"}]}`, 0600)
	boot := func() *Config {
		cfg, err := cloneOwnerNativeConfig(s.cfg)
		if err != nil {
			t.Fatal("config copy")
		}
		cfg.Targets, cfg.ActionRequestRoot, cfg.OwnerNativeGrantPort = nil, spool, &s.options
		cfg.cockpit = &cockpitTargetSource{}
		return cfg
	}
	first := boot()
	(&Coord{cfg: first}).refreshCockpitTargets()
	store, err := newOwnerNativeGrantStore(context.Background(), first, s.options, s.deps)
	if err != nil {
		t.Fatal("store")
	}
	if ack := store.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("cockpit-only comment grant not LIVE: %+v", ack)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Coord{cfg: boot()} // a restart: a fresh, empty cockpit source
	c.startOwnerGrantsWithTargets(ctx)
	if c.ownerGrants == nil {
		t.Fatal("grant store not started")
	}
	c.ownerGrants.mu.Lock()
	_, recovered := c.ownerGrants.active[r.OperationID]
	c.ownerGrants.mu.Unlock()
	if !recovered {
		t.Fatal("persisted cockpit grant not recovered")
	}
}

// The cockpit list controls admission, not the lifetime of an existing worker.
// An unrelated open issue must not make a dropped repository look closed.
func TestCockpitTargetsKeepRunningInboxJobsInPoll(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		partial     bool
		wantOpen    bool
		wantOK      bool
	}{
		{"partial-open", "OPEN", true, true, true},
		{"empty-open", "OPEN", false, true, true},
		{"partial-closed", "CLOSED", true, false, true},
		{"partial-unreadable", "error", true, false, false},
		{"partial-unknown", "UNKNOWN", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Inbox: "example/inbox", Targets: []Target{{Label: "old", Repo: "example/old"}, {Label: "kept", Repo: "example/kept"}}}
			setCockpitTargets(cfg)
			if tc.partial {
				setCockpitTargets(cfg, Target{Repo: "example/kept", Agents: []string{"claude"}})
			}
			c := &Coord{cfg: cfg, st: loadState(filepath.Join(t.TempDir(), "state.json")), source: newFakeSource()}
			c.st.Jobs[395] = &Job{Repo: "example/old"}
			if tc.partial {
				c.st.Jobs[396] = &Job{Repo: "example/kept"}
			}
			bin := t.TempDir()
			calls := filepath.Join(t.TempDir(), "calls")
			t.Setenv("GH_CALLS", calls)
			t.Setenv("GH_STATE", tc.state)
			writeFixture(t, filepath.Join(bin, "gh"), `#!/bin/sh
printf '%s\n' "$*" >> "$GH_CALLS"
case "$1 $2" in
"issue list") printf '%s' '[{"id":"I_396","number":396,"title":"Other","body":"","labels":[{"name":"kept"}]}]' ;;
"issue view")
  [ "$GH_STATE" != error ] || exit 1
  printf '{"id":"I_395","number":395,"title":"Running","body":"Existing brief","labels":[{"name":"old"}],"state":"%s"}' "$GH_STATE" ;;
*) exit 1 ;;
esac
`)
			_ = os.Chmod(filepath.Join(bin, "gh"), 0700)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			open, all, ok := c.pollIssues(context.Background())
			if ok != tc.wantOK || all[395] != tc.wantOpen || (open[395].Number == 395) != tc.wantOpen {
				t.Fatalf("poll ok=%v all=%v open=%v; want ok=%v tracked=%v", ok, all, open, tc.wantOK, tc.wantOpen)
			}
			if tc.wantOpen {
				if teardownEligible(context.Background(), 395, all, ok, func(context.Context, int) string { return "OPEN" }) {
					t.Fatal("target removal made an open job eligible for teardown")
				}
				if _, _, authorized := c.resolveTarget(open[395]); authorized {
					t.Fatal("keeping a running job restored target authorization")
				}
			}
			raw, _ := os.ReadFile(calls)
			if strings.Count(string(raw), "issue view 395 ") != 1 || strings.Contains(string(raw), "issue view 396 ") || strings.Contains(string(raw), "--label old ") {
				t.Fatalf("poll must read only the tracked issue in a dropped target: %s", raw)
			}
		})
	}
}

// The owner turns an agent on and approves at once (atelier-cockpit#916: saved
// 18:56:05, approved 18:56:11, refused 18:56:33 as harness-conflict): the grant
// store reads the snapshot the cockpit just published, not the list of the last
// coordinator tick.
func TestOwnerGrantReadsTheAgentsTheCockpitJustPublished(t *testing.T) {
	s, r, _ := ownerPortFixture(t, "claude")
	spool := privateTestRoot(t)
	writeCockpitTargets(t, spool, `{"schema_version":1,"targets":[{"repo":"example/project","agents":["codex"],"since":"2026-01-01T00:00:00.000Z"}]}`, 0600)
	cfg, err := cloneOwnerNativeConfig(s.cfg)
	if err != nil {
		t.Fatal("config copy")
	}
	cfg.ActionRequestRoot, cfg.OwnerNativeGrantPort = spool, &s.options
	cfg.cockpit = &cockpitTargetSource{}
	(&Coord{cfg: cfg}).refreshCockpitTargets() // the last tick: codex only
	store, err := newOwnerNativeGrantStore(context.Background(), cfg, s.options, s.deps)
	if err != nil {
		t.Fatal("store")
	}
	// The owner allows claude; the cockpit publishes; no coordinator tick yet.
	writeCockpitTargets(t, spool, `{"schema_version":1,"targets":[{"repo":"example/project","agents":["codex","claude"],"since":"2026-01-01T00:00:00.000Z"}]}`, 0600)
	if ack := store.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("grant refused with the list of the last tick: %+v", ack)
	}
}
