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
	if _, err := os.Stat(filepath.Join(c.cfg.Matrix.ReceiptRoot, "teardown-pending", j.DispatchKey+".json")); !os.IsNotExist(err) {
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
	if _, err := os.Stat(filepath.Join(c.cfg.Matrix.ReceiptRoot, "teardown-pending", strings.Repeat("d", 64)+".json")); !os.IsNotExist(err) {
		t.Fatal("unbound native session entered pending index")
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
