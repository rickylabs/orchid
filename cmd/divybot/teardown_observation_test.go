package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func teardownFixture(t *testing.T) (*Coord, string, *Job, *bool, *bool) {
	t.Helper()
	c, _, receipts := actionFixture(t)
	boundActionFixture(t, c, receipts)
	j := c.st.Jobs[7]
	record := filepath.Join(receipts, j.DispatchKey, "record")
	if err := os.WriteFile(filepath.Join(record, "binding.json"), []byte(`{"NativeSessionID":"thread-fixture","Repo":"example/repo"}`), 0600); err != nil {
		t.Fatal(err)
	}
	seatGone, processGone := false, false
	c.actions.stopProcess = func(_ context.Context, _ Host, pane, agent string) (*actionStopProcess, error) {
		if pane != j.Pane || agent != j.Agent {
			t.Fatal("captured a different process")
		}
		return &actionStopProcess{SchemaVersion: 1, GroupID: 301, RootPID: 301,
			Members: []actionProcessIdentity{{PID: 301, Start: 9}}}, nil
	}
	c.actions.workspaceGone = func(context.Context, Host, string) (bool, error) { return seatGone, nil }
	c.actions.processGone = func(_ context.Context, _ Host, a actionStopProcess) (bool, error) {
		if a.RootPID != 301 || len(a.Members) != 1 || a.Members[0].Start != 9 {
			t.Fatal("process anchor changed")
		}
		return processGone, nil
	}
	c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
		if seatGone {
			return nil, nil
		}
		return []AgentInfo{{Agent: j.Agent, AgentStatus: "working", Cwd: "/fixture/issue-7",
			PaneID: j.Pane, WorkspaceID: j.Workspace}}, nil
	}
	return c, record, j, &seatGone, &processGone
}

func TestTeardownRequiresIndependentSeatAndProcessAbsence(t *testing.T) {
	c, record, j, seatGone, processGone := teardownFixture(t)
	c.teardownStart(context.Background(), 7, j, j.Workspace, "operator-timeout")
	var intent teardownIntent
	if err := readPrivateActionJSON(filepath.Join(record, "teardown-intent.json"), &intent); err != nil ||
		intent.Cause != "operator-timeout" || intent.NativeRunID != "orchid-"+j.DispatchKey || intent.NativeSessionID != "thread-fixture" {
		t.Fatalf("unbound timeout intent: %+v %v", intent, err)
	}
	var anchor actionStopProcess
	if err := readPrivateActionJSON(filepath.Join(record, "teardown-anchor.json"), &anchor); err != nil || anchor.RootPID != 301 {
		t.Fatalf("missing process anchor: %+v %v", anchor, err)
	}
	c.teardownObservePending(context.Background())
	if _, err := os.Stat(filepath.Join(record, "teardown-seat-observed.json")); !os.IsNotExist(err) {
		t.Fatal("seat absent while occupant still present")
	}
	*seatGone = true
	c.teardownObservePending(context.Background())
	var seat teardownObservation
	if err := readPrivateActionJSON(filepath.Join(record, "teardown-seat-observed.json"), &seat); err != nil || seat.Kind != "seat_absent" {
		t.Fatalf("seat observation missing: %+v %v", seat, err)
	}
	if _, err := os.Stat(filepath.Join(record, "teardown-process-observed.json")); !os.IsNotExist(err) {
		t.Fatal("surviving native process was reported absent")
	}
	*processGone = true
	c.teardownObservePending(context.Background())
	var process teardownObservation
	if err := readPrivateActionJSON(filepath.Join(record, "teardown-process-observed.json"), &process); err != nil || process.Kind != "process_absent" {
		t.Fatalf("process observation missing: %+v %v", process, err)
	}
	if _, err := os.Stat(filepath.Join(c.teardownPendingRoot(), j.DispatchKey+".json")); !os.IsNotExist(err) {
		t.Fatal("completed observation remained pending")
	}
}

func TestTeardownMissingNativeBindingCannotPublishIntent(t *testing.T) {
	c, record, j, _, _ := teardownFixture(t)
	if err := os.WriteFile(filepath.Join(record, "binding.json"), []byte(`{"NativeSessionID":"","Repo":"example/repo"}`), 0600); err != nil {
		t.Fatal(err)
	}
	c.teardownStart(context.Background(), 7, j, j.Workspace, "teardown")
	if _, err := os.Stat(filepath.Join(record, "teardown-intent.json")); !os.IsNotExist(err) {
		t.Fatal("unbound native session acquired teardown proof")
	}
	if _, err := os.Stat(filepath.Join(c.teardownPendingRoot(), strings.Repeat("d", 64)+".json")); !os.IsNotExist(err) {
		t.Fatal("unbound native session entered pending index")
	}
}

func TestTeardownQueueIsPrivateStateAndReceiptTreeHasOnlyRecordOwner(t *testing.T) {
	c, record, j, _, _ := teardownFixture(t)
	owner := &receiptOwner{uid: os.Getuid(), gid: os.Getgid()}
	c.cfg.Matrix.ReceiptOwnerUID = &owner.uid
	c.cfg.Matrix.ReceiptOwnerGID = &owner.gid
	c.teardownStart(context.Background(), 7, j, j.Workspace, "operator-timeout")
	if err := lstatRegular(filepath.Join(c.teardownPendingRoot(), j.DispatchKey+".json")); err != nil {
		t.Fatalf("private pending intent absent: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(c.cfg.Matrix.ReceiptRoot, "teardown-pending")); !os.IsNotExist(err) {
		t.Fatal("Orchid work queue entered reader-facing receipt tree")
	}
	if err := lstatRegular(filepath.Join(record, "teardown-intent.json")); err != nil {
		t.Fatalf("receipt intent absent: %v", err)
	}
	checkOwnerTree(t, c.cfg.Matrix.ReceiptRoot, owner.uid, owner.gid)
}

func TestTeardownQueueMigratesOldReceiptDirectoryOnce(t *testing.T) {
	c, _, j, _, _ := teardownFixture(t)
	old := filepath.Join(c.cfg.Matrix.ReceiptRoot, "teardown-pending")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	intent := teardownIntent{SchemaVersion: 1, Issue: 7, DispatchKey: j.DispatchKey,
		NativeRunID: "orchid-" + j.DispatchKey, NativeSessionID: "thread-fixture",
		Host: j.Host, PaneID: j.Pane, WorkspaceID: j.Workspace,
		Cause: "operator-timeout", StartedAt: "2026-09-28T00:00:00Z"}
	if err := actionImmutableJSON(old, j.DispatchKey+".json", intent); err != nil {
		t.Fatal(err)
	}
	current := c.teardownPendingRoot()
	if current == "" || current == old {
		t.Fatal("queue did not move to private state")
	}
	var migrated teardownIntent
	if err := readPrivateActionJSON(filepath.Join(current, j.DispatchKey+".json"), &migrated); err != nil || migrated != intent {
		t.Fatalf("pending intent was lost: %+v %v", migrated, err)
	}
	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		t.Fatal("old reader-facing queue remained after migration")
	}
	if c.teardownPendingRoot() != current {
		t.Fatal("second migration changed queue root")
	}
}

func TestTeardownSeatRequiresWorkspaceMissingAsWellAsPaneMissing(t *testing.T) {
	c, record, j, seatGone, _ := teardownFixture(t)
	c.teardownStart(context.Background(), 7, j, j.Workspace, "teardown")
	*seatGone = true // pane disappeared from agent list, workspace still exists
	c.actions.workspaceGone = func(context.Context, Host, string) (bool, error) { return false, nil }
	c.teardownObservePending(context.Background())
	if _, err := os.Stat(filepath.Join(record, "teardown-seat-observed.json")); !os.IsNotExist(err) {
		t.Fatal("pane absence alone asserted seat teardown")
	}
	c.actions.workspaceGone = func(context.Context, Host, string) (bool, error) { return true, nil }
	c.teardownObservePending(context.Background())
	if err := lstatRegular(filepath.Join(record, "teardown-seat-observed.json")); err != nil {
		t.Fatalf("missing independent workspace absence: %v", err)
	}
}
