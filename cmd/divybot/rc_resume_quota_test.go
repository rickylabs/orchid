package main

import (
	"context"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Codex refuses permission overrides on a remote resume; the prepared thread
// already carries its approval and sandbox policy. Plain launches keep the flag.
func TestRemoteCodexResumeDropsOnlyThePermissionOverride(t *testing.T) {
	cwd := "/fixture/issue-7"
	_, plain, err := managedInteractiveAgentArgs("codex", Overrides{Model: "fixture-model", Effort: "high"}, cwd)
	if err != nil || plain[0] != "--dangerously-bypass-approvals-and-sandbox" {
		t.Fatalf("plain managed codex lost its policy flag: %q", plain)
	}
	id := syntheticRemoteRun(t).NativeSessionID
	remote, err := remoteCodexArgs(plain, cwd, id)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{}, plain[1:]...), "-c", `tui.status_line=["thread-id"]`, "--remote", "unix://", "resume", id)
	if strings.Join(remote, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("remote resume argv changed beyond the permission override: %q", remote)
	}
}

// Preparation materializes the fresh thread (a paginated thread/read with
// turns) after naming it and before verification, so a TUI resume finds it.
func TestRemoteCodexPreparationMaterializesThread(t *testing.T) {
	run := syntheticRemoteRun(t)
	run.ClientVersion = canonicalFixtureVersion
	expected := *run
	var mu sync.Mutex
	var calls []string
	h := canonicalFixtureHost(t, "valid", func(method string, params map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		if method == "thread/read" {
			if params["includeTurns"] == true {
				method += "+turns"
			}
		}
		calls = append(calls, method)
		switch method {
		case "remoteControl/status/read":
			return map[string]string{"status": "connected"}
		case "thread/start", "thread/read", "thread/read+turns":
			return remoteThreadFixture(&expected)
		}
		return map[string]any{}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.prepareRemoteCodex(ctx, run, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	got := strings.Join(calls, ",")
	if !strings.Contains(got, "thread/start,thread/name/set,thread/read+turns,thread/read") {
		t.Fatalf("fresh thread not materialized between naming and verification: %s", got)
	}
}

func TestOwnerQuotaRefusesOnlyOnVendorLimit(t *testing.T) {
	now := time.Now()
	future, past := now.Add(time.Hour).Unix(), now.Add(-time.Hour).Unix()
	for _, tc := range []struct {
		name string
		q    quota
		want string
	}{
		{"absent", quota{}, ""},
		{"unread", quota{ok: false, at: now}, ""},
		{"stale-but-low", quota{ok: true, at: now.Add(-time.Hour), five: RateLimit{UsedPct: 50, ResetsAt: future}}, ""},
		{"over-governor-ceiling-not-vendor-limit", quota{ok: true, at: now, seven: RateLimit{UsedPct: 95, ResetsAt: future}}, ""},
		{"five-hour-at-vendor-limit", quota{ok: true, at: now, five: RateLimit{UsedPct: 100, ResetsAt: future}}, availabilityFiveHourCeiling},
		{"weekly-at-vendor-limit", quota{ok: true, at: now, seven: RateLimit{UsedPct: 100, ResetsAt: future}}, availabilityWeeklyCeiling},
		{"limit-window-already-reset", quota{ok: true, at: now, five: RateLimit{UsedPct: 100, ResetsAt: past}}, ""},
	} {
		if got := ownerQuotaReason(tc.q, now); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// An owner-native launch is admitted with an absent/stale meter, over the pacing
// ceiling and at the governor cap, and refused only at a native vendor limit.
// Autonomous dispatch (production matrixAttempt) keeps every existing guard.
func freshAt(p float64) map[string]quota {
	return map[string]quota{"claude": {ok: true, at: time.Now(), seven: RateLimit{UsedPct: p, ResetsAt: time.Now().Add(time.Hour).Unix()}}}
}

func TestMatrixOwnerLaunchNotRefusedOnMissingMeter(t *testing.T) {
	for _, tc := range []struct {
		name     string
		owner    bool
		q        map[string]quota
		budget   int
		launched bool
	}{
		{"owner-absent-meter", true, map[string]quota{}, 1, true},
		{"owner-governor-cap", true, map[string]quota{}, 0, true},
		{"owner-over-pacing-ceiling", true, freshAt(95), 1, true},
		{"owner-vendor-limit", true, freshAt(100), 1, true},
		{"autonomous-absent-meter", false, map[string]quota{}, 1, false},
		{"autonomous-stale-meter", false, map[string]quota{"claude": {ok: true, at: time.Now().Add(-time.Hour), seven: RateLimit{UsedPct: 10, ResetsAt: time.Now().Add(time.Hour).Unix()}}}, 1, false},
		{"autonomous-over-pacing-ceiling", false, freshAt(95), 1, false},
		{"autonomous-governor-cap", false, freshAt(10), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTestRoot(t)
			is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic", Body: "/swarm\ntier: feature\nrole: implementation\n\nSynthetic task"}
			cfg := &Config{Inbox: "example/inbox", Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: strings.Repeat("b", 40),
				TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)}},
				Governor: Gov{WeeklyCeiling: 92}}
			c := &Coord{cfg: cfg, st: loadState(filepath.Join(root, "state.json"))}
			c.gov.q = tc.q
			var refusal matrixRefusal

			launched := false
			deps := matrixAttemptDeps{
				report: func(r matrixRefusal) { refusal = r },
				read: func(context.Context, string, string, string) (string, error) {
					return "| `routing` | matrix `implementation` row |", nil
				},
				resolve: func(_ context.Context, _ MatrixConfig, req matrixRequest) (matrixRoute, error) {
					if !containsString(req.Available, "claude") {
						return matrixRoute{}, matrixReason("route-unavailable")
					}
					return syntheticRoute(), nil
				},
				host:    func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, true },
				persist: persistMatrixReceipt,
				launch: func(context.Context, int, Issue, Host, string, Overrides, *durableMatrixReceipt) error {
					launched = true
					return nil
				},
			}
			if tc.owner {
				// Owner-native launches route natively; test their admission directly.
				req := matrixRequest{owner: true}
				c.quotaAvailability(&req, map[string]int{"claude": tc.budget}, time.Now())
				if containsString(req.Available, "claude") != tc.launched {
					t.Fatalf("owner admission wrong: available=%v", req.Available)
				}
				return
			}
			c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, map[string]int{"claude": tc.budget}, deps)
			if launched != tc.launched {
				t.Fatalf("launched=%v want %v (refusal %+v)", launched, tc.launched, refusal)
			}
			if !tc.launched && refusal.ReasonCode != "quota-unavailable" {
				t.Fatalf("refusal lost its plain reason: %+v", refusal)
			}
		})
	}
}

// After a restart the governor samples every meter before the first tick.
func TestGovernorSampleRecordsMetersBeforeAdmission(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".claude", "statusline.jsonl"),
		`{"rate_limits":{"five_hour":{"used_percentage":8,"resets_at":`+strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)+`}}}`+"\n")
	c := &Coord{cfg: &Config{Governor: Gov{Enabled: true, WeeklyCeiling: 92}, Hosts: []Host{{Name: "fixture-host", Home: home}}, Targets: []Target{{Repo: "example/project", Agents: []string{"claude"}}}},
		st: loadState(filepath.Join(t.TempDir(), "state.json"))}
	c.governorSample(context.Background())
	if q := c.gov.q["claude"]; !q.ok || q.five.UsedPct != 8 {
		t.Fatalf("meter not recorded by the startup sample: %+v", q)
	}
	_ = os.Remove(filepath.Join(home, ".claude", "statusline.jsonl"))
}

// run samples the meters before the first tick and before the sampling loop
// starts, so the first admission after a restart sees real meter readings.
func TestRunPrimesGovernorBeforeFirstTick(t *testing.T) {
	_, funcs, err := inventoryFile(productionSources(t))
	if err != nil {
		t.Fatal(err)
	}
	run := funcs["run"]
	if run == nil {
		t.Fatal("run missing")
	}
	sample, loop, tick := -1, -1, -1
	for i, stmt := range run.Body.List {
		text := ""
		switch x := stmt.(type) {
		case *ast.IfStmt:
			text = types.ExprString(x.Cond)
			if text == "c.cfg.Governor.Enabled" && len(x.Body.List) > 0 {
				if e, ok := x.Body.List[0].(*ast.ExprStmt); ok && types.ExprString(e.X) == "c.governorSample(ctx)" {
					sample = i
				}
			}
		case *ast.GoStmt:
			if types.ExprString(x.Call) == "c.governorLoop(ctx, primed)" {
				loop = i
			}
		case *ast.ExprStmt:
			if types.ExprString(x.X) == "c.tick(ctx)" && tick < 0 {
				tick = i
			}
		}
	}
	if sample < 0 || loop < 0 || tick < 0 || !(sample < loop && sample < tick) {
		t.Fatalf("governor not primed before the first tick: sample=%d loop=%d tick=%d", sample, loop, tick)
	}
}

// After a primed start, the loop still samples on every later tick.
func TestGovernorLoopSamplesAfterPrimedStart(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".claude", "statusline.jsonl"),
		`{"rate_limits":{"five_hour":{"used_percentage":8,"resets_at":`+strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)+`}}}`+"\n")
	c := &Coord{cfg: &Config{Governor: Gov{Enabled: true, WeeklyCeiling: 92, SampleInterval: "50ms"}, Hosts: []Host{{Name: "fixture-host", Home: home}},
		Targets: []Target{{Repo: "example/project", Agents: []string{"claude"}}}}, st: loadState(filepath.Join(t.TempDir(), "state.json"))}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { c.governorLoop(ctx, true); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.gov.mu.Lock()
		q := c.gov.q["claude"]
		c.gov.mu.Unlock()
		if q.ok {
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the governor loop stopped sampling after a primed start")
}
