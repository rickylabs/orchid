package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeWorkingObservationBoundAndInvalidated(t *testing.T) {
	root, job, _ := claudeLiveBindingFixture(t)
	id := privateTestID(t)
	info := claudeAgentInfo(t, id)
	if bound, err := retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, job,
		func(context.Context, string) (json.RawMessage, error) { return fixtureJSON(t, info), nil }); err != nil || !bound {
		t.Fatal("fixture did not bind its native session", err)
	}
	path := filepath.Join(root, job.DispatchKey, "record", "claude-status.json")
	now := time.Now().UTC()
	read := func(_ context.Context, pane string) (json.RawMessage, error) {
		if pane != job.Pane {
			t.Fatal("status read escaped the bound pane")
		}
		return fixtureJSON(t, info), nil
	}
	info["agent"].(map[string]any)["agent_status"] = "working"
	if !observeClaudeWorking(context.Background(), root, "fixture/inbox", nil, job, read, now) {
		t.Fatal("matching live Claude status was not observed")
	}
	var row claudeStatusObservation
	if err := readPrivateActionJSON(path, &row); err != nil || row.Status != "working" ||
		row.NativeSessionID != id || row.RunID != "orchid-"+job.DispatchKey || row.PaneID != job.Pane ||
		row.WorkspaceID != job.Workspace || row.Host != job.Host {
		t.Fatal("status snapshot lost its bound identity", err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("status snapshot is not owner-only", err)
	}
	for _, change := range []string{"blocked", "unknown", "wrong-session", "wrong-pane", "wrong-source", "missing", "wrong-dispatch"} {
		info = claudeAgentInfo(t, id)
		info["agent"].(map[string]any)["agent_status"] = "working"
		agent := info["agent"].(map[string]any)
		switch change {
		case "blocked", "unknown":
			agent["agent_status"] = change
		case "wrong-session":
			agent["agent_session"].(map[string]any)["value"] = "other-session"
		case "wrong-pane":
			agent["pane_id"] = "other:pane"
		case "wrong-source":
			agent["agent_session"].(map[string]any)["source"] = "herdr:codex"
		case "missing":
			delete(agent, "agent_session")
		case "wrong-dispatch":
			job.Host = "other-host"
		}
		if observeClaudeWorking(context.Background(), root, "fixture/inbox", nil, job, read, now) {
			t.Fatalf("%s was recorded as a status", change)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s preserved stale working state", change)
		}
		job.Host = ""
		info = claudeAgentInfo(t, id)
		info["agent"].(map[string]any)["agent_status"] = "working"
		if !observeClaudeWorking(context.Background(), root, "fixture/inbox", nil, job, read, now) {
			t.Fatal("valid observation did not restore")
		}
	}
}

// RUN-6 (harness #516, 2026-09-30): the root ended its turn at 11:22:26 and sat
// at its prompt, which herdr reports as done. divybot removed the working row, so
// the app showed Unknown rather than Not running. A verified tick now records the
// stopped status too, and a later prompt brings "working" back.
func TestClaudeStoppedAtPromptIsObservedNotRemoved(t *testing.T) {
	root, job, _ := claudeLiveBindingFixture(t)
	id := privateTestID(t)
	info := claudeAgentInfo(t, id)
	read := func(context.Context, string) (json.RawMessage, error) { return fixtureJSON(t, info), nil }
	if bound, err := retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, job, read); err != nil || !bound {
		t.Fatal("fixture did not bind its native session", err)
	}
	path := filepath.Join(root, job.DispatchKey, "record", "claude-status.json")
	tick := time.Date(2026, 9, 30, 11, 22, 0, 0, time.UTC)
	for _, step := range []struct{ herdr, want string }{
		{"working", "working"}, {"done", "done"}, {"idle", "idle"}, {"blocked", ""}, {"done", "done"}, {"working", "working"},
	} {
		tick = tick.Add(30 * time.Second)
		info["agent"].(map[string]any)["agent_status"] = step.herdr
		wrote := observeClaudeWorking(context.Background(), root, "fixture/inbox", nil, job, read, tick)
		if step.want == "" {
			if _, err := os.Stat(path); wrote || !os.IsNotExist(err) {
				t.Fatalf("%s at %s left a status row", step.herdr, tick.Format(time.TimeOnly))
			}
			continue
		}
		var row claudeStatusObservation
		if err := readPrivateActionJSON(path, &row); !wrote || err != nil || row.Status != step.want ||
			row.ObservedAt != tick.Format(time.RFC3339Nano) || row.NativeSessionID != id {
			t.Fatalf("%s at %s: want a fresh %q row, got %+v (%v)", step.herdr, tick.Format(time.TimeOnly), step.want, row, err)
		}
	}
}
