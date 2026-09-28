package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testActionID = "123e4567-e89b-42d3-a456-426614174000"

func actionFixture(t *testing.T) (*Coord, string, string) {
	t.Helper()
	base := t.TempDir()
	spool, receipts := filepath.Join(base, "spool"), filepath.Join(base, "receipts")
	for _, path := range []string{spool, receipts, filepath.Join(spool, "tmp"), filepath.Join(spool, "new"), filepath.Join(spool, "done")} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	c := &Coord{cfg: &Config{Inbox: "example/repo", ActionRequestRoot: spool, Matrix: MatrixConfig{ReceiptRoot: receipts}},
		st: &State{Jobs: map[int]*Job{}, LaunchBlocks: map[int]launchBlock{}}, hosts: map[string]Host{}}
	return c, spool, receipts
}
func actionTestRequest() actionRequest {
	r := actionRequest{SchemaVersion: 1, OperationID: testActionID, IdempotencyKey: "key-1",
		Repository: "example/repo", IssueNumber: 7,
		AgentID: "agent_" + strings.Repeat("a", 64), DispatchID: "assignment_" + strings.Repeat("b", 64),
		ExpectedAgentRevision: strings.Repeat("c", 64), Action: "stop"}
	return r
}
func actionDrop(t *testing.T, spool string, r actionRequest) []byte {
	t.Helper()
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(spool, "tmp", r.OperationID+".json")
	if err := actionSyncFile(tmp, body); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(spool, "new", r.OperationID+".json")); err != nil {
		t.Fatal(err)
	}
	if err := syncDirectory(filepath.Join(spool, "new")); err != nil {
		t.Fatal(err)
	}
	return body
}
func actionResult(t *testing.T, receipts string) actionReceipt {
	t.Helper()
	r, err := actionReadReceipt(filepath.Join(receipts, "actions", testActionID))
	if err != nil {
		t.Fatal(err)
	}
	return *r
}

func TestActionDigestConflictKeepsPrimaryReceipt(t *testing.T) {
	c, spool, receipts := actionFixture(t)
	first := actionTestRequest()
	body := actionDrop(t, spool, first)
	c.actionTick(context.Background())
	primaryPath := filepath.Join(receipts, "actions", testActionID, "result.json")
	primary, err := os.ReadFile(primaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if r := actionResult(t, receipts); r.Outcome != "unknown" || r.Reason != "job_not_found" || r.RequestDigest != shaText(body) {
		t.Fatalf("primary: %+v", r)
	}
	second := first
	second.Payload.Reason = "different request"
	conflictBody := actionDrop(t, spool, second)
	c.actionTick(context.Background())
	after, err := os.ReadFile(primaryPath)
	if err != nil || string(after) != string(primary) {
		t.Fatal("conflicting replay changed primary result")
	}
	conflictPath := filepath.Join(receipts, "actions", testActionID, "conflict-"+shaText(conflictBody)+".json")
	conflict, err := os.ReadFile(conflictPath)
	if err != nil {
		t.Fatal(err)
	}
	var result actionReceipt
	if strictJSON(conflict, &result) != nil || result.Outcome != "rejected" || result.Reason != "digest_conflict" {
		t.Fatalf("conflict: %+v", result)
	}
	if strings.Contains(string(primary), second.Payload.Reason) || strings.Contains(string(conflict), second.Payload.Reason) {
		t.Fatal("payload leaked into receipt")
	}
}

func TestActionMissingResultAfterFenceIsUnknownWithoutDelivery(t *testing.T) {
	c, spool, receipts := actionFixture(t)
	req := actionTestRequest()
	body := actionDrop(t, spool, req)
	root, err := actionDirs(spool, receipts)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, matches, err := actionIntent(root, req.OperationID, shaText(body)); err != nil || !created || !matches {
		t.Fatalf("intent: %v", err)
	}
	// Simulate process death after the intent was synced but before an outcome.
	c.actionTick(context.Background())
	if r := actionResult(t, receipts); r.Outcome != "unknown" || r.Reason != "receipt_missing_after_fence" {
		t.Fatalf("missing receipt: %+v", r)
	}
	if len(c.st.Jobs) != 0 {
		t.Fatal("unknown path changed jobs")
	}
}

func TestActionExactRootIdentityAndBindingRequired(t *testing.T) {
	c, spool, receipts := actionFixture(t)
	req := actionTestRequest()
	c.st.Jobs[7] = &Job{Issue: 7, DispatchKey: strings.Repeat("d", 64)}
	actionDrop(t, spool, req)
	c.actionTick(context.Background())
	if r := actionResult(t, receipts); r.Outcome != "rejected" || r.Reason != "identity_mismatch" {
		t.Fatalf("identity: %+v", r)
	}
}

func TestActionRootSeparationAndPrivateMaildir(t *testing.T) {
	_, spool, receipts := actionFixture(t)
	if _, err := actionDirs(receipts, receipts); err == nil {
		t.Fatal("accepted request spool in receipt root")
	}
	for _, dir := range []string{spool, filepath.Join(spool, "tmp"), filepath.Join(spool, "new"), filepath.Join(spool, "done")} {
		if err := os.Chmod(dir, 0770); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := actionDirs(spool, receipts); err != nil {
		t.Fatalf("private shared-group spool rejected: %v", err)
	}
	if err := os.Chmod(filepath.Join(spool, "new"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := actionDirs(spool, receipts); err == nil {
		t.Fatal("accepted public new directory")
	}
}

func boundActionFixture(t *testing.T, c *Coord, receipts string) actionRequest {
	t.Helper()
	key := strings.Repeat("d", 64)
	runID := "orchid-" + key
	j := &Job{Issue: 7, Host: "fixture_host", Pane: "pane-fixture", Workspace: "workspace-fixture",
		Agent: "claude", Repo: "example/repo", DispatchKey: key}
	c.st.Jobs[7] = j
	c.st.path = filepath.Join(filepath.Dir(receipts), "state.json")
	c.hosts["fixture_host"] = Host{Name: "fixture_host"}
	record := filepath.Join(receipts, key, "record")
	if err := os.Mkdir(filepath.Join(receipts, key), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(record, 0700); err != nil {
		t.Fatal(err)
	}
	d := dispatchBinding{SchemaVersion: 1, RunID: runID, Issue: dispatchIssue{Repo: "example/repo", Number: 7},
		Source: "claude", State: "dispatched", Host: "fixture_host",
		Location: &dispatchLocation{PaneID: j.Pane, WorkspaceID: j.Workspace}}
	body, _ := json.Marshal(d)
	if err := actionSyncFile(filepath.Join(record, "dispatch.json"), body); err != nil {
		t.Fatal(err)
	}
	// Production binding.json contains launch metadata beyond the action
	// authorization fields. A strict decode into a two-field struct rejects it.
	if err := actionSyncFile(filepath.Join(record, "binding.json"), []byte(`{"BriefDigest":"fixture","Host":"fixture_host","IssueID":7,"NativeSessionID":"","ProfileDigest":"fixture","ProfileRevision":"fixture","Repo":"example/repo","Request":{},"Route":{}}`)); err != nil {
		t.Fatal(err)
	}
	r := actionTestRequest()
	r.AgentID = actionOpaque("agent", runID)
	r.DispatchID = actionOpaque("assignment", runID)
	c.actions.list = func(_ context.Context, _ Host) ([]AgentInfo, error) {
		return []AgentInfo{{Agent: "claude", AgentStatus: "working", Cwd: "/fixture/issue-7", PaneID: j.Pane, WorkspaceID: j.Workspace}}, nil
	}
	return r
}

func TestActionStopFencesBeforeEffectAndReplayDoesNotCloseAgain(t *testing.T) {
	c, spool, receipts := actionFixture(t)
	req := boundActionFixture(t, c, receipts)
	closes := 0
	c.actions.close = func(_ context.Context, _ Host, workspace string) error {
		if workspace != "workspace-fixture" {
			t.Fatalf("wrong workspace: %q", workspace)
		}
		if c.st.Jobs[7] != nil || c.st.LaunchBlocks[7].Reason != "cockpit_stop" {
			t.Fatal("stop effect ran before durable state fence")
		}
		if _, err := os.Stat(c.st.path); err != nil {
			t.Fatal("state fence not synced")
		}
		closes++
		return nil
	}
	body := actionDrop(t, spool, req)
	c.actionTick(context.Background())
	if r := actionResult(t, receipts); r.Outcome != "accepted" || r.Reason != "workspace_close_delivered" || r.RequestDigest != shaText(body) {
		t.Fatalf("stop result: %+v", r)
	}
	actionDrop(t, spool, req)
	c.actionTick(context.Background())
	if closes != 1 {
		t.Fatalf("close called %d times", closes)
	}
}

func TestActionSteerSendsExactTextAndRequiresRunning(t *testing.T) {
	c, spool, receipts := actionFixture(t)
	req := boundActionFixture(t, c, receipts)
	req.Action = "steer"
	req.Payload.Text = "Please inspect the failing test."
	sends := 0
	c.actions.send = func(_ context.Context, _ Host, pane, text string) error {
		if pane != "pane-fixture" || text != req.Payload.Text {
			t.Fatal("wrong seat or text")
		}
		sends++
		return nil
	}
	actionDrop(t, spool, req)
	c.actionTick(context.Background())
	if r := actionResult(t, receipts); r.Outcome != "accepted" || r.Reason != "prompt_delivered" || r.MessageID != "" {
		t.Fatalf("steer result: %+v", r)
	}
	if sends != 1 {
		t.Fatalf("send called %d times", sends)
	}
}

func TestActionStopObservationRequiresSeatAndNativeProcessGone(t *testing.T) {
	c, spool, receipts := actionFixture(t)
	req := boundActionFixture(t, c, receipts)
	closed := false
	processGone := false
	c.actions.stopProcess = func(_ context.Context, _ Host, pane, agent string) (*actionStopProcess, error) {
		if pane != "pane-fixture" || agent != "claude" || closed {
			t.Fatal("process identity not captured before close")
		}
		return &actionStopProcess{SchemaVersion: 1, RootPID: 201, GroupID: 201,
			Members: []actionProcessIdentity{{PID: 201, Start: 77}}}, nil
	}
	c.actions.close = func(_ context.Context, _ Host, workspace string) error {
		if workspace != "workspace-fixture" {
			t.Fatal("wrong workspace")
		}
		closed = true
		return nil
	}
	c.actions.list = func(_ context.Context, _ Host) ([]AgentInfo, error) {
		if closed {
			return nil, nil
		}
		return []AgentInfo{{Agent: "claude", AgentStatus: "working", Cwd: "/fixture/issue-7",
			PaneID: "pane-fixture", WorkspaceID: "workspace-fixture"}}, nil
	}
	c.actions.workspaceGone = func(_ context.Context, _ Host, workspace string) (bool, error) {
		return closed && workspace == "workspace-fixture", nil
	}
	c.actions.processGone = func(_ context.Context, _ Host, a actionStopProcess) (bool, error) {
		if a.RootPID != 201 || a.GroupID != 201 || len(a.Members) != 1 || a.Members[0].Start != 77 {
			t.Fatal("process anchor changed")
		}
		return processGone, nil
	}
	actionDrop(t, spool, req)
	c.actionTick(context.Background())
	if r := actionResult(t, receipts); r.Outcome != "accepted" || r.Reason != "workspace_close_delivered" {
		t.Fatalf("delivery: %+v", r)
	}
	indexPath := filepath.Join(receipts, strings.Repeat("d", 64), "record", "stop-action.json")
	var index actionStopIndex
	if err := readPrivateActionJSON(indexPath, &index); err != nil || index.OperationID != req.OperationID ||
		index.RequestDigest != actionResult(t, receipts).RequestDigest {
		t.Fatalf("unbound direct stop index: %+v %v", index, err)
	}
	dir := filepath.Join(receipts, "actions", testActionID)
	if _, err := os.Stat(filepath.Join(dir, "seat-observed.json")); err != nil {
		t.Fatal("seat absence missing", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "process-observed.json")); !os.IsNotExist(err) {
		t.Fatal("process death fabricated while process was alive")
	}
	processGone = true
	c.actionTick(context.Background())
	for name, kind := range map[string]string{"seat-observed.json": "seat_absent", "process-observed.json": "process_absent"} {
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("private observation %s: %v", name, err)
		}
		var observed actionStopObservation
		if err := readPrivateActionJSON(path, &observed); err != nil || observed.Kind != kind ||
			observed.OperationID != req.OperationID || observed.RequestDigest == "" {
			t.Fatalf("unbound observation %s: %+v %v", name, observed, err)
		}
	}
}

func TestActionStopObservationDoesNotInferWorkspaceGoneFromAgentList(t *testing.T) {
	c, spool, receipts := actionFixture(t)
	req := boundActionFixture(t, c, receipts)
	closed := false
	workspaceGone := false
	c.actions.stopProcess = func(context.Context, Host, string, string) (*actionStopProcess, error) {
		return &actionStopProcess{SchemaVersion: 1, RootPID: 301, GroupID: 301,
			Members: []actionProcessIdentity{{PID: 301, Start: 88}}}, nil
	}
	c.actions.close = func(context.Context, Host, string) error { closed = true; return nil }
	c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
		if closed {
			return nil, nil
		}
		return []AgentInfo{{Agent: "claude", AgentStatus: "working", Cwd: "/fixture/issue-7",
			PaneID: "pane-fixture", WorkspaceID: "workspace-fixture"}}, nil
	}
	c.actions.workspaceGone = func(context.Context, Host, string) (bool, error) { return workspaceGone, nil }
	c.actions.processGone = func(context.Context, Host, actionStopProcess) (bool, error) { return true, nil }
	actionDrop(t, spool, req)
	c.actionTick(context.Background())
	dir := filepath.Join(receipts, "actions", testActionID)
	if _, err := os.Stat(filepath.Join(dir, "process-observed.json")); err != nil {
		t.Fatal("process absence missing")
	}
	if _, err := os.Stat(filepath.Join(dir, "seat-observed.json")); !os.IsNotExist(err) {
		t.Fatal("agent list absence was incorrectly treated as workspace disappearance")
	}
	workspaceGone = true
	c.actionTick(context.Background())
	if _, err := os.Stat(filepath.Join(dir, "seat-observed.json")); err != nil {
		t.Fatal("verified workspace absence missing")
	}
}
