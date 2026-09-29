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
	for _, change := range []string{"done", "wrong-session", "wrong-pane", "wrong-source", "missing", "wrong-dispatch"} {
		info = claudeAgentInfo(t, id)
		info["agent"].(map[string]any)["agent_status"] = "working"
		agent := info["agent"].(map[string]any)
		switch change {
		case "done":
			agent["agent_status"] = "done"
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
			t.Fatalf("%s was promoted to running", change)
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
