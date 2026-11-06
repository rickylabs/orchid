package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	clockGenerationA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	clockGenerationB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var clockQuery = []nativeRunQuery{{Key: "701", Label: "codex-701"}}

// clockFixture is the read fixture with a launched job sampled by generation A.
func clockFixture(t *testing.T, spawned time.Time) *Coord {
	t.Helper()
	c := nativeRunFixture(t)
	c.instance = clockGenerationA
	c.st.nativeRuns.generation = clockGenerationA
	c.st.Jobs[701].SpawnedAt = spawned
	c.st.mu.Lock()
	c.recordLaunchClockLocked(c.st.Jobs[701])
	c.st.mu.Unlock()
	return c
}

func onlyRecord(t *testing.T, result nativeRunLookup) nativeRunRecord {
	t.Helper()
	if len(result.Records) != 1 || len(result.Missing) != 0 {
		t.Fatalf("expected one record: %+v", result)
	}
	return result.Records[0]
}

func TestNativeRunLaunchInstantIsMillisecondUTCAndClocked(t *testing.T) {
	spawned := time.Date(2026, 11, 2, 9, 30, 15, 123456789, time.FixedZone("fixture", 3600))
	c := clockFixture(t, spawned)
	result := c.lookupNativeRuns(clockQuery)
	r := onlyRecord(t, result)
	if result.SchemaVersion != 2 {
		t.Fatalf("schema version %d", result.SchemaVersion)
	}
	want := nativeRunInstant{At: "2026-11-02T08:30:15.123Z", Generation: clockGenerationA}
	if r.Launch == nil || *r.Launch != want {
		t.Fatalf("launch %+v, want %+v", r.Launch, want)
	}
	if r.TaskStart != nil {
		t.Fatal("a task start was invented without certified acceptance")
	}
}

// Reads, durable saves, restart and a new source generation never advance or re-clock the launch.
func TestNativeRunLaunchInstantNeverAdvances(t *testing.T) {
	spawned := time.Now().Add(-time.Hour)
	c := clockFixture(t, spawned)
	first := onlyRecord(t, c.lookupNativeRuns(clockQuery))
	time.Sleep(2 * time.Millisecond)
	second := onlyRecord(t, c.lookupNativeRuns(clockQuery))
	if first.ObservedAt == second.ObservedAt || *first.Launch != *second.Launch {
		t.Fatal("a read advanced the launch instant or reused the read time")
	}
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	// Restart: a new process with a new source generation loads the same durable job.
	c.st = loadState(c.st.path)
	c.instance = clockGenerationB
	c.st.nativeRuns = &nativeRunWatch{generation: clockGenerationB, subs: map[chan struct{}]struct{}{}}
	c.st.mu.Lock()
	c.recordLaunchClockLocked(c.st.Jobs[701]) // a resume path that re-ran capture must not re-clock it
	c.st.mu.Unlock()
	result := c.lookupNativeRuns(clockQuery)
	restarted := onlyRecord(t, result)
	if result.Generation != clockGenerationB || *restarted.Launch != *first.Launch {
		t.Fatalf("restart re-clocked the launch: generation %s launch %+v", result.Generation, restarted.Launch)
	}
}

func TestNativeRunLaunchInstantOmittedForLegacyAndAdoptedJobs(t *testing.T) {
	c := clockFixture(t, time.Now())
	c.st.Jobs[701].LaunchGeneration = "" // a job persisted before this field existed
	if r := onlyRecord(t, c.lookupNativeRuns(clockQuery)); r.Launch != nil {
		t.Fatal("legacy job exposed an unclocked launch")
	}
	c.st.Jobs[701].LaunchGeneration = "fixture-generation" // never a process instance identity
	if r := onlyRecord(t, c.lookupNativeRuns(clockQuery)); r.Launch != nil {
		t.Fatal("an uncertified clock identity was exposed")
	}

	cfg := &Config{Inbox: "example/inbox", Targets: []Target{{Label: "fixture", Repo: "example/work"}}}
	a := &Coord{cfg: cfg, st: &State{Jobs: map[int]*Job{}}, instance: clockGenerationA}
	a.st.nativeRuns = &nativeRunWatch{generation: clockGenerationA, subs: map[chan struct{}]struct{}{}}
	ref := agentRef{Host: "fixture-host", Agent: "codex", HerdrScope: &herdrScope{Session: "dispatch"}}
	j, ok := a.adopt(7, Issue{Number: 7, Labels: []string{"fixture"}}, map[int]agentRef{7: ref})
	if !ok || j.SpawnedAt.IsZero() || j.LaunchGeneration != "" {
		t.Fatalf("adoption fixture: ok=%v job=%+v", ok, j)
	}
	j.DispatchKey, j.NativeSessionID = strings.Repeat("a", 64), "fixture-native-adopted"
	a.st.mu.Lock()
	r := a.st.nativeRunRecordLocked(7, j, cfg.Inbox)
	a.st.mu.Unlock()
	if r == nil || r.Launch != nil {
		t.Fatalf("adopted job exposed its adoption time as a launch: %+v", r)
	}
}

// The completion fence freezes the launch; a legacy fence without one never gains it.
func TestNativeRunLaunchInstantFrozenIntoCompletionFence(t *testing.T) {
	c := clockFixture(t, time.Now().Add(-time.Minute))
	c.st.mu.Lock()
	fenced := c.st.nativeRunRecordLocked(701, c.st.Jobs[701], c.cfg.Inbox)
	c.st.mu.Unlock()
	fenced.Phase, fenced.ObservedAt = "completed", stamp(time.Now())
	fenced.CompletedAt = fenced.ObservedAt
	fence := completedRun{NativeRun: fenced, DispatchKey: fenced.DispatchKey, NativeSessionID: fenced.NativeSessionID, Phase: "observed"}
	c.st.CompletedRuns = map[int]completedRun{701: fence}
	want := *fenced.Launch
	delete(c.st.Jobs, 701)
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	c.st = loadState(c.st.path)
	c.st.nativeRuns = &nativeRunWatch{generation: clockGenerationB, subs: map[chan struct{}]struct{}{}}
	r := onlyRecord(t, c.lookupNativeRuns(clockQuery))
	if r.Phase != "completed" || r.Launch == nil || *r.Launch != want {
		t.Fatalf("fence lost or re-clocked the launch: %+v", r)
	}
	legacy := *fenced
	legacy.Launch = nil
	c.st.CompletedRuns[701] = completedRun{NativeRun: &legacy, DispatchKey: legacy.DispatchKey, NativeSessionID: legacy.NativeSessionID, Phase: "observed"}
	if r := onlyRecord(t, c.lookupNativeRuns(clockQuery)); r.Launch != nil {
		t.Fatal("a legacy fence gained a launch instant")
	}
	corrupt := *fenced
	corrupt.Launch = &nativeRunInstant{At: "2026-11-02T08:30:15.123456Z", Generation: clockGenerationA}
	c.st.CompletedRuns[701] = completedRun{NativeRun: &corrupt, DispatchKey: corrupt.DispatchKey, NativeSessionID: corrupt.NativeSessionID, Phase: "observed"}
	if result := c.lookupNativeRuns(clockQuery); len(result.Records) != 0 || result.Missing[0].Reason != "native_identity_unavailable" {
		t.Fatal("a non-canonical fenced instant was served")
	}
}

// The first certified accepted turn is persisted once, with its own clock, and never renewed.
func TestNativeRunTaskStartPersistedOnceThroughTheDeliveryCommit(t *testing.T) {
	future := time.Now().Add(time.Minute)
	accepted := time.Date(2026, 11, 2, 9, 31, 0, 987654321, time.UTC)
	f := newCommitFixture(t)
	f.c.instance = clockGenerationA
	if err := f.c.commitRemoteCodexDelivery(context.Background(), future, f.j, accepted); err != nil {
		t.Fatal(err)
	}
	want := clockedInstant{At: accepted, Generation: clockGenerationA}
	if f.j.TaskStart == nil || *f.j.TaskStart != want {
		t.Fatalf("task start %+v", f.j.TaskStart)
	}
	restored := f.restart()
	if restored.TaskStart == nil || !restored.TaskStart.At.Equal(accepted) || restored.TaskStart.Generation != clockGenerationA {
		t.Fatalf("task start not durable: %+v", restored.TaskStart)
	}
	// A duplicate or out-of-order acceptance under a new generation never renews the first instant.
	f.c.instance = clockGenerationB
	for _, later := range []time.Time{accepted.Add(time.Second), accepted.Add(-time.Second)} {
		if err := f.c.commitRemoteCodexDelivery(context.Background(), future, f.j, later); err != nil {
			t.Fatal(err)
		}
		if *f.j.TaskStart != want {
			t.Fatalf("a later acceptance renewed the task start: %+v", f.j.TaskStart)
		}
	}
	r := projectedJob(t, f.j)
	if r.TaskStart == nil || *r.TaskStart != (nativeRunInstant{At: "2026-11-02T09:31:00.987Z", Generation: clockGenerationA}) {
		t.Fatalf("projected task start %+v", r.TaskStart)
	}

	// No certified acceptance: no task start.
	f = newCommitFixture(t)
	f.c.instance = clockGenerationA
	if err := f.c.commitRemoteCodexDelivery(context.Background(), future, f.j, time.Time{}); err != nil || f.j.TaskStart != nil {
		t.Fatal("an uncertified delivery invented a task start")
	}
	// A failure before the rename restores the job without a task start.
	f = newCommitFixture(t)
	f.c.instance = clockGenerationA
	f.c.st.fs = &stateFS{rename: func(string, string) error { return errors.New("injected") }}
	if f.c.commitRemoteCodexDelivery(context.Background(), future, f.j, accepted) == nil || f.j.TaskStart != nil || f.restart().TaskStart != nil {
		t.Fatal("a refused commit kept its task start")
	}
}

func projectedJob(t *testing.T, j *Job) nativeRunRecord {
	t.Helper()
	s := &State{Jobs: map[int]*Job{}}
	p := *j
	p.Issue, p.Label, p.Agent, p.HerdrScope = 7, "codex-7", "codex", &herdrScope{Session: "dispatch"}
	p.NativeSessionID, p.RemoteControl = "fixture-native-task", nil
	r := s.nativeRunRecordLocked(7, &p, "example/inbox")
	if r == nil {
		t.Fatal("projection unavailable")
	}
	return *r
}

// Only a published, on-time native acceptance certifies the first accepted turn.
func TestNativeRunTaskStartOnlyFromCertifiedAcceptance(t *testing.T) {
	h := newAccHarness(t)
	at := time.Now().Add(time.Second)
	h.acc.now = func() time.Time { return at }
	accAccepted(t, h, h.accept(accSent))
	if !h.acc.acceptedInstant().Equal(at) {
		t.Fatalf("accepted instant %v, want %v", h.acc.acceptedInstant(), at)
	}
	h = newAccHarness(t)
	h.newTurn(func(s string) []any { return []any{accTurn(accNewTurn, "inProgress", accUser(s[:len(s)-1]+"X"))} })
	accRefused(t, h, h.accept(accSent), "input-mismatch")
	if !h.acc.acceptedInstant().IsZero() {
		t.Fatal("a refused acceptance certified a task start")
	}
	var none *codexAcceptance
	if !none.acceptedInstant().IsZero() {
		t.Fatal("a missing acceptance certified a task start")
	}
}

// Real spawn with a synthetic SSH endpoint: the launch is clocked by this process's generation
// once the seat is registered, and is durable before any goal delivery.
func TestNativeRunSpawnClocksLaunchAtRegistration(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "issue-7")
	if err := os.MkdirAll(filepath.Join(workdir, ".git", "info"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "--quiet", workdir).Run(); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	fake := `#!/usr/bin/env python3
import json,subprocess,sys
script=sys.argv[-1]
if 'git fetch --depth=1' in script:print('a'*40);sys.exit(0)
if '# orchid-checkout-root' in script or 'command -v awk' in script or '__DIVYBOT_EOF__' in script:
 sys.exit(subprocess.run(['/bin/sh','-c',script]).returncode)
if "'workspace' 'create'" in script:
 print(json.dumps({'result':{'workspace':{'workspace_id':'fixture-seat'},'root_pane':{'pane_id':'fixture-pane'}}}))
else:print(json.dumps({'result':{}}))
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Inbox: "fixture/inbox", BranchPrefix: "fixture/", Targets: []Target{{Label: "fixture-target", Repo: "fixture/project"}}}
	c := &Coord{cfg: cfg, auth: &AuthStore{}, st: loadState(filepath.Join(root, "state.json")), instance: clockGenerationA}
	h := Host{Name: "fixture-host", SSH: "fixture-host", Home: root, WorkdirRoot: root}
	is := Issue{Number: 7, Title: "synthetic", Body: "/swarm\nrepo: fixture/project\n\nsynthetic", Labels: []string{"fixture-target"}}
	before := time.Now()
	if err := c.spawn(context.Background(), 7, is, h, "codex-run", Overrides{}, registrationReceipt(t, "codex-run", Overrides{})); err != nil {
		t.Fatalf("synthetic launch failed: %v", err)
	}
	durable := loadState(c.st.path).Jobs[7]
	if durable == nil || durable.LaunchGeneration != clockGenerationA || durable.SpawnedAt.Before(before) || durable.TaskStart != nil {
		t.Fatalf("launch clock not durable at registration: %+v", durable)
	}
	raw, _ := json.Marshal(durable)
	if !strings.Contains(string(raw), `"launch_generation":"`+clockGenerationA+`"`) {
		t.Fatalf("persisted job lacks the launch clock: %s", raw)
	}
}
