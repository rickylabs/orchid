package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func claudeLiveBindingFixture(t *testing.T) (string, *Job, *durableMatrixReceipt) {
	t.Helper()
	r := registrationReceipt(t, "claude", Overrides{})
	r.dispatch.Issue.Repo = "fixture/inbox"
	location := &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}
	if e := r.writeDispatch("dispatched", location); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(filepath.Dir(r.file), "binding.json"), []byte(`{"Repo":"fixture/repo"}`), 0600); e != nil {
		t.Fatal(e)
	}
	key := strings.Repeat("d", 64)
	return filepath.Dir(filepath.Dir(filepath.Dir(r.file))), &Job{
		Issue: 7, Agent: "claude", Repo: "fixture/repo", Label: "fixture-agent",
		Pane: location.PaneID, Workspace: location.WorkspaceID, DispatchKey: key,
		SpawnedAt: time.Now(),
	}, r
}

func claudeAgentInfo(t *testing.T, id string) map[string]any {
	t.Helper()
	v := nativeStartFixture(t, id)
	v["type"] = "agent_info"
	a := v["agent"].(map[string]any)
	a["agent"] = "claude"
	s := a["agent_session"].(map[string]any)
	s["source"], s["agent"] = "herdr:claude", "claude"
	return v
}

func TestClaudeLiveBindingDelayedHook(t *testing.T) {
	root, j, _ := claudeLiveBindingFixture(t)
	id := privateTestID(t)
	reads := 0
	read := func(_ context.Context, pane string) (json.RawMessage, error) {
		reads++
		if pane != j.Pane {
			t.Fatal("read escaped the registered pane")
		}
		v := claudeAgentInfo(t, id)
		if reads == 1 {
			delete(v["agent"].(map[string]any), "agent_session")
		}
		return fixtureJSON(t, v), nil
	}
	if !liveNativeBindingEligible(j, time.Now()) {
		t.Fatal("fresh Claude job not eligible")
	}
	if bound, e := retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, j, read); e != nil || bound {
		t.Fatal("missing hook produced a binding")
	}
	if bound, e := retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, j, read); e != nil || !bound || reads != 2 {
		t.Fatal("late Claude hook was not bound")
	}
	_, got, e := loadNativeBindingReceipt(root, j.DispatchKey, j, "fixture/inbox", nil, "claude")
	if e != nil || got != id {
		t.Fatal("Claude private binding was not persisted")
	}
	if bound, e := retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, j, read); e != nil || bound || reads != 2 {
		t.Fatal("bound Claude job was polled again")
	}
}

func TestClaudeLiveBindingRejectsOtherEvidence(t *testing.T) {
	for _, change := range []string{"pane", "workspace", "source", "agent", "dispatch-source", "changed-dispatch", "uncertain-dispatch"} {
		t.Run(change, func(t *testing.T) {
			root, j, receipt := claudeLiveBindingFixture(t)
			v := claudeAgentInfo(t, privateTestID(t))
			a := v["agent"].(map[string]any)
			s := a["agent_session"].(map[string]any)
			switch change {
			case "pane":
				a["pane_id"] = "w2:p1"
			case "workspace":
				a["workspace_id"] = "w2"
			case "source":
				s["source"] = "herdr:codex"
			case "agent":
				s["agent"] = "codex"
			case "dispatch-source":
				receipt.dispatch.Source = "codex"
				if e := receipt.writeDispatch("dispatched", receipt.dispatch.Location); e != nil {
					t.Fatal(e)
				}
			}
			read := func(context.Context, string) (json.RawMessage, error) {
				if change == "changed-dispatch" || change == "uncertain-dispatch" {
					if change == "changed-dispatch" {
						receipt.dispatch.Profile = "changed"
						if e := receipt.writeDispatch("dispatched", receipt.dispatch.Location); e != nil {
							t.Fatal(e)
						}
					} else if e := receipt.writeDispatch("uncertain", receipt.dispatch.Location); e != nil {
						t.Fatal(e)
					}
				}
				return fixtureJSON(t, v), nil
			}
			if bound, e := retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, j, read); bound || e == nil {
				t.Fatal("unverified Claude evidence gained a binding")
			}
			_, id, e := loadNativeBindingReceipt(root, j.DispatchKey, j, "fixture/inbox", nil, "claude")
			if id != "" || (e != nil && change != "dispatch-source" && change != "uncertain-dispatch") {
				t.Fatal("unverified Claude identity was persisted")
			}
		})
	}
}

func TestClaudeLateBindingWindow(t *testing.T) {
	_, j, _ := claudeLiveBindingFixture(t)
	now := time.Now()
	for _, age := range []time.Duration{0, nativeGoalStartTimeout - time.Millisecond} {
		j.SpawnedAt = now.Add(-age)
		if !liveNativeBindingEligible(j, now) {
			t.Fatal("bounded Claude retry was refused")
		}
	}
	for _, age := range []time.Duration{nativeGoalStartTimeout, nativeGoalStartTimeout + time.Second} {
		j.SpawnedAt = now.Add(-age)
		if liveNativeBindingEligible(j, now) {
			t.Fatal("expired Claude retry was allowed")
		}
	}
	j.SpawnedAt = now.Add(time.Second)
	if liveNativeBindingEligible(j, now) {
		t.Fatal("future Claude job was allowed")
	}
	j.SpawnedAt = time.Time{}
	if liveNativeBindingEligible(j, now) {
		t.Fatal("unbound Claude start time was allowed")
	}
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
