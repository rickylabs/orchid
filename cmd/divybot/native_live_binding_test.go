package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func liveBindingFixture(t *testing.T) (string, *Job, *durableMatrixReceipt) {
	t.Helper()
	r := registrationReceipt(t, "codex", Overrides{})
	r.dispatch.Source = "codex"
	r.dispatch.Issue.Repo = "fixture/inbox"
	if e := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(filepath.Dir(r.file), "binding.json"), []byte(`{"Repo":"fixture/repo"}`), 0600); e != nil {
		t.Fatal(e)
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(r.file)))
	j := &Job{Issue: 7, Agent: "codex", Repo: "fixture/repo", Label: "fixture-agent", Pane: "w1:p1", Workspace: "w1", NativeGoal: &dispatchGoal{ReceiptKey: strings.Repeat("d", 64)}}
	return root, j, r
}

func liveAgentInfo(t *testing.T, id string) json.RawMessage {
	t.Helper()
	v := nativeStartFixture(t, id)
	v["type"] = "agent_info"
	return fixtureJSON(t, v)
}

func TestLiveNativeBindingRetriesMissingHook(t *testing.T) {
	root, j, _ := liveBindingFixture(t)
	id := privateTestID(t)
	reads := 0
	read := func(_ context.Context, pane string) (json.RawMessage, error) {
		reads++
		if pane != "w1:p1" {
			t.Fatal("read used a different occupant")
		}
		v := nativeStartFixture(t, id)
		v["type"] = "agent_info"
		if reads == 1 {
			delete(v["agent"].(map[string]any), "agent_session")
		}
		return fixtureJSON(t, v), nil
	}
	bound, e := retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, j, read)
	if e != nil || bound {
		t.Fatal("missing hook produced a binding")
	}
	bound, e = retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, j, read)
	if e != nil || !bound || reads != 2 {
		t.Fatal("late official hook was not bound on the next tick")
	}
	got, e := readGoalIdentity(root, j, "fixture/inbox")
	if e != nil || got != id {
		t.Fatal("reopened private receipt lost the live identity")
	}
	bound, e = retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, j, read)
	if e != nil || bound || reads != 2 {
		t.Fatal("bound job polled again")
	}
	info, _ := os.Stat(filepath.Join(filepath.Dir(filepath.Join(root, j.NativeGoal.ReceiptKey, "record", "receipt.json")), "binding.json"))
	if info == nil || info.Mode().Perm() != 0600 {
		t.Fatal("private binding mode changed")
	}
}

func TestLiveNativeBindingRejectsWrongOccupantAndUncertainReceipt(t *testing.T) {
	root, j, r := liveBindingFixture(t)
	id := privateTestID(t)
	wrong := nativeStartFixture(t, id)
	wrong["type"] = "agent_info"
	wrong["agent"].(map[string]any)["pane_id"] = "w2:p1"
	read := func(context.Context, string) (json.RawMessage, error) { return fixtureJSON(t, wrong), nil }
	if bound, e := retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, j, read); bound || e == nil {
		t.Fatal("wrong occupant was accepted")
	}
	if _, e := readGoalIdentity(root, j, "fixture/inbox"); e == nil {
		t.Fatal("wrong occupant wrote an identity")
	}
	if e := r.writeDispatch("uncertain", r.dispatch.Location); e != nil {
		t.Fatal(e)
	}
	if bound, e := retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, j, func(context.Context, string) (json.RawMessage, error) {
		t.Fatal("uncertain dispatch was queried")
		return nil, nil
	}); bound || e == nil {
		t.Fatal("uncertain dispatch gained a binding")
	}
}
