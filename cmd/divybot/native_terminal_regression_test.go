package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeTerminalImmutableAcrossCleanupAndRestart(t *testing.T) {
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	c.st.mu.Lock()
	if err := c.recordNativeEndLocked(701, j, "retired"); err != nil {
		t.Fatal(err)
	}
	first := c.st.NativeTerminals[j.DispatchKey]
	if err := c.recordNativeEndLocked(701, j, "cancelled"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, c.st.NativeTerminals[j.DispatchKey]) {
		t.Fatal("terminal fact was rewritten")
	}
	delete(c.st.Jobs, 701)
	delete(c.st.SourceBindings, 701)
	c.st.CompletedRuns = map[int]completedRun{701: {Phase: "observed"}}
	if err := c.st.saveLocked(); err != nil {
		t.Fatal(err)
	}
	c.st.mu.Unlock()
	c.pruneCompletedRuns(map[int]bool{}, true, func(int) string { return "CLOSED" })
	c.st = loadState(c.st.path)
	c.st.nativeRuns = &nativeRunWatch{generation: "restarted", subs: map[chan struct{}]struct{}{}}
	got := c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}})
	if len(got.Records) != 1 || got.Records[0].Phase != "completed" || got.Records[0].CompletedAt != first.Record.CompletedAt {
		t.Fatal("cleanup pruning or restart lost the immutable assignment end")
	}
	if got.Records[0].Issue != first.Record.Issue || got.Records[0].Scope != first.Record.Scope || c.st.reserveLaunch(701) {
		t.Fatal("terminal association drifted or automatic redispatch became possible")
	}
	// Existing strict v2 clients see exactly the established record shape.
	raw, _ := json.Marshal(got.Records[0])
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, added := fields["reason"]; added {
		t.Fatal("unnegotiated record field broke old readers")
	}
	if strings.Contains(string(raw), "retired") {
		t.Fatal("private terminal metadata escaped into v2")
	}
	// An explicit new assignment on the same coordinate does not inherit the end.
	j.DispatchKey = strings.Repeat("e", 64)
	c.st.Jobs[701] = j
	if c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}).Records[0].Phase != "active" || len(c.st.NativeTerminals) != 1 {
		t.Fatal("new assignment replaced or inherited an old immutable end")
	}
}

func TestNativeTerminalCommitFailureRetainsUnknown(t *testing.T) {
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	c.st.fs = &stateFS{syncFile: func(*os.File) error { return errors.New("fixture loss") }}
	c.st.mu.Lock()
	err := c.recordNativeEndLocked(701, j, "ended_unobserved")
	c.st.mu.Unlock()
	if err == nil || len(c.st.NativeTerminals) != 0 || len(c.st.NativeTerminalKeys) != 0 || c.st.Jobs[701] != j {
		t.Fatal("failed durability published an end or released its job")
	}
	if len(c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}).Records) != 0 {
		t.Fatal("failed write remained certified")
	}
	c.st.fs = nil
	c.st.mu.Lock()
	err = c.recordNativeEndLocked(701, j, "ended_unobserved")
	c.st.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	first := c.st.NativeTerminals[j.DispatchKey]
	j.NativeSessionID = "different-fixture-session"
	c.st.mu.Lock()
	err = c.recordNativeEndLocked(701, j, "cancelled")
	c.st.mu.Unlock()
	if err == nil || !reflect.DeepEqual(first, c.st.NativeTerminals[j.DispatchKey]) {
		t.Fatal("conflicting native identity rewrote the end")
	}
}

func TestNativeTerminalUncertainDirectorySyncNeverRewritesFact(t *testing.T) {
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	c.st.fs = &stateFS{syncDir: func(*os.File) error { return errors.New("fixture directory loss") }}
	c.st.mu.Lock()
	err := c.recordNativeEndLocked(701, j, "retired")
	c.st.mu.Unlock()
	first := loadState(c.st.path).NativeTerminals[j.DispatchKey]
	if err == nil || first.Record.CompletedAt == "" || !reflect.DeepEqual(first, c.st.NativeTerminals[j.DispatchKey]) {
		t.Fatal("uncertain committed pathname was rolled back or clocked differently")
	}
	if len(c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}).Records) != 0 {
		t.Fatal("uncertain directory durability was certified")
	}
	c.st.mu.Lock()
	err = c.recordNativeEndLocked(701, j, "cancelled")
	c.st.mu.Unlock()
	if err == nil {
		t.Fatal("idempotent retry skipped its durability fence")
	}
	c.st.fs = nil
	c.st.mu.Lock()
	err = c.recordNativeEndLocked(701, j, "cancelled")
	c.st.mu.Unlock()
	if err != nil || !reflect.DeepEqual(first, loadState(c.st.path).NativeTerminals[j.DispatchKey]) ||
		len(c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}).Records) != 1 {
		t.Fatal("durable retry rewrote the first fact or failed to restore readback")
	}
}

func TestNativeTerminalLegacyFencePromotedBeforePruning(t *testing.T) {
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	r := c.st.nativeRunRecordLocked(701, j, c.cfg.Inbox)
	r.Phase, r.CompletedAt = "completed", stamp(time.Now().Add(-time.Hour))
	r.ObservedAt = r.CompletedAt
	c.st.CompletedRuns = map[int]completedRun{701: {DispatchKey: j.DispatchKey, NativeSessionID: j.NativeSessionID, NativeRun: r, Phase: "observed"}}
	delete(c.st.Jobs, 701)
	c.pruneCompletedRuns(map[int]bool{}, true, func(int) string { return "CLOSED" })
	c.st = loadState(c.st.path)
	c.st.nativeRuns = &nativeRunWatch{generation: "fixture", subs: map[chan struct{}]struct{}{}}
	got := c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}})
	if len(c.st.CompletedRuns) != 0 || len(got.Records) != 1 || got.Records[0].CompletedAt != r.CompletedAt ||
		c.st.NativeTerminals[j.DispatchKey].Reason != "ended_unobserved" {
		t.Fatal("pruning erased or re-clocked a valid legacy terminal identity")
	}
}

func TestNativeTerminalOrdinaryCancellationPrecedesClose(t *testing.T) {
	c, spool, receipts := actionFixture(t)
	req := boundActionFixture(t, c, receipts)
	j := c.st.Jobs[7]
	j.NativeSessionID, j.Label = "fixture-native-session", "claude-7"
	j.HerdrScope = &herdrScope{Session: "default"}
	c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
		return []AgentInfo{{Agent: "claude", Name: j.Label, AgentStatus: "working", Cwd: "/fixture/issue-7", PaneID: j.Pane, WorkspaceID: j.Workspace,
			AgentSession: json.RawMessage(`{"agent":"claude","kind":"id","source":"herdr:claude","value":"fixture-native-session"}`)}}, nil
	}
	closes := 0
	c.actions.stopProcess = func(context.Context, Host, string, string) (*actionStopProcess, error) {
		return nil, errors.New("fixture unknown")
	}
	c.actions.close = func(context.Context, Host, string) error {
		f := loadState(c.st.path).NativeTerminals[j.DispatchKey]
		if f.Reason != "cancelled" || f.Record.CompletedAt == "" {
			t.Fatal("workspace close preceded immutable cancellation")
		}
		closes++
		return nil
	}
	actionDrop(t, spool, req)
	c.actionTick(context.Background())
	if closes != 1 || c.st.Jobs[7] != nil || actionResult(t, receipts).Outcome != "accepted" {
		t.Fatal("ordinary cancellation failed to retain its terminal fact")
	}
}

func TestNativeTerminalReconciliationRequiresProofAndFencesRaces(t *testing.T) {
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	q := nativeRunQuery{"701", "codex-701"}
	now := time.Now().Add(-time.Minute)
	e := nativeEndEvidence{ReportAt: now, ClosedAt: now.Add(time.Second), Idle: true}
	for _, missing := range []nativeEndEvidence{{Idle: true}, {ReportAt: now, Idle: true}, {ClosedAt: now, Idle: true}, {ReportAt: now, ClosedAt: now}} {
		c.actions.endEvidence = func(context.Context, *Job) (nativeEndEvidence, error) { return missing, nil }
		if c.reconcileNativeEnd(context.Background(), q, "", true).State != "unknown" || len(c.st.NativeTerminals) != 0 {
			t.Fatal("idle, closed work or a report alone certified assignment completion")
		}
	}
	c.actions.endEvidence = func(context.Context, *Job) (nativeEndEvidence, error) { return e, nil }
	if c.reconcileNativeEnd(context.Background(), q, "", false).State != "eligible" || len(c.st.NativeTerminals) != 0 {
		t.Fatal("dry-run either missed proof or wrote State")
	}
	c.actions.endEvidence = func(context.Context, *Job) (nativeEndEvidence, error) { j.PR++; return e, nil }
	if c.reconcileNativeEnd(context.Background(), q, "", true).State != "unknown" || len(c.st.NativeTerminals) != 0 {
		t.Fatal("proof crossed an intervening assignment/work change")
	}
	c.actions.endEvidence = func(context.Context, *Job) (nativeEndEvidence, error) { return e, nil }
	if c.reconcileNativeEnd(context.Background(), q, "", true).State != "recorded" {
		t.Fatal("proved end not recorded")
	}
	before := c.st.NativeTerminals[j.DispatchKey]
	if c.reconcileNativeEnd(context.Background(), q, "", true).State != "already_recorded" || !reflect.DeepEqual(before, c.st.NativeTerminals[j.DispatchKey]) {
		t.Fatal("repeat reconciliation changed the immutable fact")
	}
	if c.reconcileNativeEnd(context.Background(), nativeRunQuery{"701", "claude-701"}, "", true).State != "unknown" {
		t.Fatal("wrong vendor selected an end")
	}
}

func TestNativeTerminalCLIDefaultIsDryAndApplyUsesDispatcher(t *testing.T) {
	c, spool, _ := actionFixture(t)
	f := nativeRunFixture(t)
	c.st.Jobs[701], c.st.SourceBindings = f.st.Jobs[701], f.st.SourceBindings
	c.st.nativeRuns = &nativeRunWatch{generation: "fixture", subs: map[chan struct{}]struct{}{}}
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	config, keys := filepath.Join(root, "config.json"), filepath.Join(root, "keys.json")
	body, _ := json.Marshal(c.cfg)
	// Config serializes StateFile separately from the in-memory State fixture.
	c.cfg.StateFile = c.st.path
	body, _ = json.Marshal(c.cfg)
	if os.WriteFile(config, body, 0600) != nil || os.WriteFile(keys, []byte(`{"keys":[{"key":"701","label":"codex-701"}]}`), 0600) != nil {
		t.Fatal("fixture files")
	}
	now := time.Now().Add(-time.Minute)
	proof := func(context.Context, *Job) (nativeEndEvidence, error) {
		return nativeEndEvidence{now, now.Add(time.Second), true}, nil
	}
	makeCoord := func(cfg *Config) *Coord { v := &Coord{cfg: cfg}; v.actions.endEvidence = proof; return v }
	before, _ := os.ReadFile(c.st.path)
	var out, stderr bytes.Buffer
	args := []string{"--config", config, "--keys-file", keys}
	if code := reconcileCompletionsWith(args, &out, &stderr, makeCoord); code != 0 {
		t.Fatalf("dry-run: %d %s", code, &stderr)
	}
	queued, _ := os.ReadDir(filepath.Join(spool, "new"))
	after, _ := os.ReadFile(c.st.path)
	if !bytes.Equal(before, after) || len(queued) != 0 || !strings.Contains(out.String(), `"dryRun":true`) || !strings.Contains(out.String(), `"eligible"`) {
		t.Fatal("default command wrote State/enqueued or did not plan")
	}
	out.Reset()
	if reconcileCompletionsWith(append(args, "--apply"), &out, &stderr, makeCoord) != 0 {
		t.Fatal("apply enqueue")
	}
	after, _ = os.ReadFile(c.st.path)
	queued, _ = os.ReadDir(filepath.Join(spool, "new"))
	if !bytes.Equal(before, after) || len(queued) != 1 || !strings.Contains(out.String(), "enqueued") {
		t.Fatal("CLI applied offline or did not enqueue")
	}
	c.actions.endEvidence = proof
	c.actionTick(context.Background())
	if len(c.st.NativeTerminals) != 1 || c.st.Jobs[701] == nil {
		t.Fatal("dispatcher did not record end independently of cleanup")
	}
	queued, _ = os.ReadDir(filepath.Join(spool, "new"))
	if len(queued) != 0 {
		t.Fatal("dispatcher did not acknowledge request")
	}
	if c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}).Records[0].Phase != "completed" {
		t.Fatal("existing publication path lost end")
	}
	for _, key := range []int{702, 703} {
		job := *f.st.Jobs[701]
		job.Issue, job.Label = key, "codex-"+strconv.Itoa(key)
		job.DispatchKey = fmt.Sprintf("%064x", key)
		job.NativeSessionID = "fixture-" + strconv.Itoa(key)
		c.st.Jobs[key] = &job
		c.st.SourceBindings[key] = &sourceBinding{Repo: "example/project", Issue: 7, Comment: int64(key)}
		if enqueueNativeEnd(c, nativeRunQuery{strconv.Itoa(key), job.Label}).State != "enqueued" {
			t.Fatal("bounded batch enqueue")
		}
	}
	c.actionTick(context.Background())
	queued, _ = os.ReadDir(filepath.Join(spool, "new"))
	if len(c.st.NativeTerminals) != 2 || len(queued) != 1 {
		t.Fatal("reconciliation monopolized the dispatcher action pass")
	}
	c.actionTick(context.Background())
	if len(c.st.NativeTerminals) != 3 {
		t.Fatal("bounded reconciliation did not resume next pass")
	}
	oversized := `{"keys":[` + strings.Repeat(`{"key":"701","label":"codex-701"},`, 128) + `{"key":"702","label":"codex-702"}]}`
	_ = os.WriteFile(keys, []byte(oversized), 0600)
	if reconcileCompletionsWith(args, &out, &stderr, makeCoord) != 2 {
		t.Fatal("unbounded or duplicate key selection admitted")
	}
}

func TestNativeTerminalRetirementPrecedesUncertainCleanup(t *testing.T) {
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	// No host can confirm stop: the assignment's retirement is still a fact,
	// while the job and its capacity stay tracked for separate cleanup.
	if c.teardown(context.Background(), 701, j, "teardown") {
		t.Fatal("unknown stop certified cleanup")
	}
	f, ok := c.st.NativeTerminals[j.DispatchKey]
	if !ok || f.Reason != "retired" || c.st.Jobs[701] != j {
		t.Fatal("assignment end depended on cleanup")
	}
	if loadState(c.st.path).NativeTerminals[j.DispatchKey].Record.CompletedAt == "" {
		t.Fatal("retirement did not cross durable fence")
	}
	sends := 0
	c.actions.send = func(context.Context, Host, string, string) error { sends++; return nil }
	c.superviseActive(context.Background(), 701, j, map[int]agentRef{}, Issue{})
	if sends != 0 {
		t.Fatal("ended assignment received continuation input")
	}
}

func TestNativeTerminalMergedPRIsNotErasedBeforeFinalization(t *testing.T) {
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	j.PR, j.Repo = 17, "example/project"
	bin := t.TempDir()
	if os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' '{\"number\":17,\"state\":\"MERGED\"}'\n"), 0700) != nil {
		t.Fatal("fixture gh")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	now := time.Now().Add(-time.Minute)
	c.actions.endEvidence = func(context.Context, *Job) (nativeEndEvidence, error) {
		return nativeEndEvidence{now, now.Add(time.Second), true}, nil
	}
	c.pollPR(context.Background(), 701, j, Host{}, "done")
	if j.PR != 17 || len(c.st.NativeTerminals) != 1 || c.st.NativeTerminals[j.DispatchKey].Reason != "ended_unobserved" {
		t.Fatal("merged PR evidence was erased/reused instead of finalizing the exact delivered assignment")
	}
}

func TestNativeTerminalTurnCompletionAloneNeverEndsAssignment(t *testing.T) {
	host := goalTransportHost(t, "")
	path := filepath.Join(host.Home, ".local", "bin", "codex")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := "{'id':'fixture-turn','status':'failed','error':{'codexErrorInfo':'serverOverloaded'}}"
	complete := "{'id':'fixture-turn','status':'completed','itemsView':'full','completedAt':int(time.time())-1,'items':[{'type':'agentMessage','phase':'final_answer','text':'finished turn'}]}"
	if !bytes.Contains(script, []byte(old)) {
		t.Fatal("fixture native turn shape changed")
	}
	if os.WriteFile(path, []byte(strings.Replace(string(script), old, complete, 1)), 0700) != nil {
		t.Fatal("fixture native source")
	}
	bin := t.TempDir()
	if os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' '{\"comments\":[]}'\n"), 0700) != nil {
		t.Fatal("fixture report source")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := c.completionEvidence(ctx, host, j, "fixture-thread")
	if err != nil || got {
		t.Fatal("vendor turn was promoted to assignment completion", err)
	}
}

func TestNativeTerminalHistoricalReportLinksAreBoundedAndScoped(t *testing.T) {
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	j.Repo, j.SpawnedAt = "example/project", time.Now().Add(-time.Hour)
	c.cfg.BotLogin = "fixture-bot"
	bin := t.TempDir()
	now := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	writeGH := func(author, body string) {
		t.Helper()
		issue, _ := json.Marshal(map[string]any{"state": "OPEN", "comments": []any{map[string]any{"body": body, "createdAt": now.Format(time.RFC3339), "author": map[string]string{"login": author}}}})
		pr, _ := json.Marshal(map[string]any{"state": "MERGED", "mergedAt": now.Add(time.Second).Format(time.RFC3339)})
		script := "#!/bin/sh\ncase \"$1\" in\nissue) printf '%s\\n' '" + string(issue) + "';;\npr) printf '%s\\n' '" + string(pr) + "';;\n*) exit 1;;\nesac\n"
		if os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700) != nil {
			t.Fatal("fixture gh")
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	marker := finalCommentMarker(j.DispatchKey)
	writeGH("fixture-bot", "https://github.com/example/project/pull/17 "+marker)
	e, err := c.readNativeEndEvidence(context.Background(), j)
	if err != nil || e.ReportAt.IsZero() || e.ClosedAt.IsZero() || e.Idle || provenNativeEnd(e) {
		t.Fatal("lost PR pointer was not recovered conservatively", err)
	}
	writeGH("other-author", "https://github.com/example/project/pull/17 "+marker)
	e, err = c.readNativeEndEvidence(context.Background(), j)
	if err != nil || !e.ReportAt.IsZero() {
		t.Fatal("foreign author supplied completion proof")
	}
	writeGH("fixture-bot", "https://github.com/other/project/pull/17 "+marker)
	if _, err = c.readNativeEndEvidence(context.Background(), j); err == nil {
		t.Fatal("unconfigured report repository admitted")
	}
	writeGH("fixture-bot", strings.Repeat("https://github.com/example/project/pull/17 ", 17)+marker)
	if _, err = c.readNativeEndEvidence(context.Background(), j); err == nil {
		t.Fatal("unbounded report links admitted")
	}
}

func TestNativeTerminalAbandonmentRequiresNativeAbsence(t *testing.T) {
	for _, mode := range []string{"absent", "workspace-present", "same-label", "unknown", "save-failed"} {
		t.Run(mode, func(t *testing.T) {
			c := nativeRunFixture(t)
			j := c.st.Jobs[701]
			j.Workspace = "fixture-workspace"
			j.HerdrScope = &herdrScope{Session: "default"}
			home := t.TempDir()
			h := Host{Name: j.Host, Home: home, SSH: "localhost"}
			c.hosts[j.Host] = h
			rows := `[]`
			if mode == "workspace-present" {
				rows = `[{"workspace_id":"fixture-workspace","label":"other"}]`
			} else if mode == "same-label" {
				rows = `[{"workspace_id":"other","label":"codex-701"}]`
			} else if mode == "unknown" {
				rows = `null`
			}
			bin := filepath.Join(home, ".local", "bin")
			if os.MkdirAll(bin, 0700) != nil || os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\nprintf '%s\\n' '{\"result\":{\"type\":\"workspace_list\",\"workspaces\":"+rows+"}}'\n"), 0700) != nil {
				t.Fatal("native fixture")
			}
			if mode == "save-failed" {
				c.st.fs = &stateFS{syncFile: func(*os.File) error { return errors.New("fixture loss") }}
			}
			ended := c.abandonMissingNativeSeat(context.Background(), j)
			if ended != (mode == "absent") || (c.st.Jobs[701] == nil) != ended || (len(c.st.NativeTerminals) == 1) != ended {
				t.Fatal("missing screen detection or failed native proof certified an end")
			}
			if ended && loadState(c.st.path).NativeTerminals[j.DispatchKey].Reason != "ended_unobserved" {
				t.Fatal("native disappearance fabricated an outcome or failed durability")
			}
		})
	}
}

func TestNativeTerminalReconciliationRejectsNewerNativeWork(t *testing.T) {
	for _, mode := range []string{"valid", "newer-turn", "last-read-notice"} {
		t.Run(mode, func(t *testing.T) {
			c := nativeRunFixture(t)
			j := c.st.Jobs[701]
			j.SpawnedAt = time.Now().Add(-time.Hour)
			run := syntheticRemoteRun(t)
			j.NativeSessionID = run.NativeSessionID
			base := remoteIdleHandler(run, "valid")
			fullReads := 0
			h := canonicalFixtureHost(t, "valid", func(method string, params map[string]any) any {
				if method == "thread/turns/list" {
					at := time.Now().Add(-2 * time.Minute).Unix()
					if mode == "newer-turn" {
						at = time.Now().Unix()
					}
					return map[string]any{"data": []any{map[string]any{"id": "old-native-turn", "status": "completed", "completedAt": at, "itemsView": "full", "items": []any{}}}}
				}
				return base(method, params)
			}, func(method string, params map[string]any) []any {
				if method == "thread/turns/list" && params["itemsView"] == "full" {
					fullReads++
					if fullReads == 3 && mode == "last-read-notice" {
						return []any{nativeStartedFixture(run.NativeSessionID)}
					}
				}
				return nil
			})
			c.hosts[j.Host] = h
			c.cfg.BotLogin = "fixture-bot"
			at := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
			issue, _ := json.Marshal(map[string]any{"state": "CLOSED", "closedAt": at, "comments": []any{map[string]any{"body": finalCommentMarker(j.DispatchKey), "createdAt": at, "author": map[string]string{"login": "fixture-bot"}}}})
			bin := t.TempDir()
			if os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' '"+string(issue)+"'\n"), 0700) != nil {
				t.Fatal("source fixture")
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			e, err := c.readNativeEndEvidence(context.Background(), j)
			if (err == nil && provenNativeEnd(e)) != (mode == "valid") {
				t.Fatalf("native work crossed the final-report fence: mode=%s reads=%d err=%v evidence=%+v", mode, fullReads, err, e)
			}
		})
	}
}
