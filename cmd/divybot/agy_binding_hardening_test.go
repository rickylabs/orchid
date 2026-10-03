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

func agyLiveBindingFixture(t *testing.T) (string, *Job, *durableMatrixReceipt) {
	return agyLiveBindingAuthorityFixture(t, "fixture-7", strings.Repeat("b", 64))
}

func agyLiveBindingAuthorityFixture(t *testing.T, issueID, brief string) (string, *Job, *durableMatrixReceipt) {
	t.Helper()
	root := privateTestRoot(t)
	repo := "fixture/repo"
	key := shaText([]byte(issueID + "\x00" + repo + "\x00" + brief))
	binding := map[string]any{"IssueID": issueID, "Repo": repo, "BriefDigest": brief, "Host": "fixture-host",
		"Route": map[string]string{"transport": "agy", "provider": "fixture-provider", "model": "fixture-model", "effort": "high"}}
	r, err := persistMatrixReceipt(root, key, "fixture command", receiptFor(MatrixConfig{}, syntheticRoute()), binding)
	if err != nil {
		t.Fatal(err)
	}
	r.dispatch = &dispatchBinding{SchemaVersion: 1, RunID: "orchid-" + key,
		Issue: dispatchIssue{Repo: "fixture/inbox", Number: 7}, Source: "agy", Host: "fixture-host",
		Provider: "fixture-provider", Model: "fixture-model", Effort: "high"}
	if r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}) != nil {
		t.Fatal("fixture dispatch")
	}
	j := &Job{Issue: 7, Agent: "agy", Repo: repo, Label: "fixture-agent", Pane: "w1:p1", Workspace: "w1",
		Host: "fixture-host", DispatchKey: key, GoalDelivery: "confirmed",
		Overrides: Overrides{Model: "fixture-model", Effort: "high"}}
	return root, j, r
}

func TestAGYBindingRequiresValidWholeBriefAndPrivateOwner(t *testing.T) {
	for _, change := range []string{"empty-issue", "invalid-brief", "wrong-owner"} {
		t.Run(change, func(t *testing.T) {
			issue, brief := "fixture-7", strings.Repeat("b", 64)
			if change == "empty-issue" {
				issue = ""
			}
			if change == "invalid-brief" {
				brief = "invalid"
			}
			root, j, r := agyLiveBindingAuthorityFixture(t, issue, brief)
			directory, _ := agyStoreDirectory(filepath.Join(t.TempDir(), "checkout"), r.dispatch.RunID)
			if r.writeAGYStore(&nativeStore{Source: "agy", Directory: directory}) != nil {
				t.Fatal("fixture store")
			}
			var owner *receiptOwner
			if change == "wrong-owner" {
				owner = &receiptOwner{uid: os.Getuid() + 1, gid: os.Getgid()}
			}
			reads := 0
			agent := func(context.Context, string) (AgentInfo, error) {
				reads++
				return AgentInfo{}, errMatrix
			}
			native := func(context.Context, *nativeStore) (string, nativeIdentityReason) {
				reads++
				return syntheticAGYID, ""
			}
			if retryAGYNativeBinding(context.Background(), root, "fixture/inbox", owner, j, agent, native) || reads != 0 {
				t.Fatal("invalid whole-brief authority or owner reached native reads")
			}
		})
	}
}

func TestAGYWholeBriefAuthorityAndCancellation(t *testing.T) {
	for _, change := range []string{"", "wrong-issue", "wrong-brief", "invalid-brief", "wrong-host", "wrong-provider", "wrong-model", "wrong-effort", "wrong-transport", "wrong-job-model", "wrong-job-effort", "changed-binding", "changed-brief", "changed-route", "changed-goal", "changed-run-mode", "cancel-before", "expired-before", "cancel-first-occupant", "cancel-native", "cancel-second-occupant"} {
		t.Run(change, func(t *testing.T) {
			root, j, r := agyLiveBindingFixture(t)
			cwd := filepath.Join(t.TempDir(), "checkout")
			directory, _ := agyStoreDirectory(cwd, r.dispatch.RunID)
			if r.writeAGYStore(&nativeStore{Source: "agy", Directory: directory}) != nil {
				t.Fatal("fixture store")
			}
			path := filepath.Join(filepath.Dir(r.file), "binding.json")
			edit := func(field string, value any) {
				t.Helper()
				raw, err := os.ReadFile(path)
				var b map[string]any
				if err != nil || json.Unmarshal(raw, &b) != nil {
					t.Fatal("fixture binding read")
				}
				b[field] = value
				raw, _ = json.Marshal(b)
				if os.WriteFile(path, raw, 0600) != nil {
					t.Fatal("fixture binding change")
				}
			}
			route := map[string]string{"transport": "agy", "provider": "fixture-provider", "model": "fixture-model", "effort": "high"}
			switch change {
			case "wrong-issue":
				edit("IssueID", "other-issue")
			case "wrong-brief":
				edit("BriefDigest", strings.Repeat("c", 64))
			case "invalid-brief":
				edit("BriefDigest", "invalid")
			case "wrong-host":
				edit("Host", "other-host")
			case "wrong-provider", "wrong-model", "wrong-effort", "wrong-transport":
				route[strings.TrimPrefix(change, "wrong-")] = "foreign"
				edit("Route", route)
			case "wrong-job-model":
				j.Overrides.Model = "foreign-model"
			case "wrong-job-effort":
				j.Overrides.Effort = "low"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if change == "cancel-before" {
				cancel()
			}
			if change == "expired-before" {
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer stop()
			}
			reads, nativeReads := 0, 0
			readAgent := func(_ context.Context, pane string) (AgentInfo, error) {
				reads++
				if pane != j.Pane {
					t.Fatal("read escaped registered pane")
				}
				if change == "cancel-first-occupant" && reads == 1 || change == "cancel-second-occupant" && reads == 2 {
					cancel()
				}
				return AgentInfo{Agent: "agy", Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace,
					Cwd: cwd, InteractiveReady: true, AgentStatus: "working", StateChangeSeq: 1}, nil
			}
			readID := func(_ context.Context, got *nativeStore) (string, nativeIdentityReason) {
				nativeReads++
				if got.Source != "agy" || got.Directory != directory {
					t.Fatal("read escaped private native store")
				}
				switch change {
				case "changed-binding":
					edit("OwnerProvenance", "changed")
				case "changed-brief":
					edit("BriefDigest", strings.Repeat("c", 64))
				case "changed-route":
					route["model"] = "foreign-model"
					edit("Route", route)
				case "changed-goal":
					j.GoalDelivery = "pending"
				case "changed-run-mode":
					j.RunMode = true
				case "cancel-native":
					cancel()
				}
				return syntheticAGYID, ""
			}
			bound := retryAGYNativeBinding(ctx, root, "fixture/inbox", nil, j, readAgent, readID)
			if bound != (change == "") {
				t.Fatal("unverified or late AGY identity acquired a binding")
			}
			_, id, _ := loadNativeBindingReceipt(root, j.DispatchKey, j, "fixture/inbox", nil, "agy")
			if change == "" && id != syntheticAGYID || change != "" && id != "" {
				t.Fatal("unverified or late AGY identity remained durable")
			}
			if (change == "cancel-before" || change == "expired-before") && (reads != 0 || nativeReads != 0) ||
				change == "cancel-first-occupant" && (reads != 1 || nativeReads != 0) ||
				change == "cancel-native" && (reads != 1 || nativeReads != 1) {
				t.Fatal("cancelled proof kept reading native state")
			}
		})
	}
}

func TestAGYMissingFirstSessionBindsOnLaterReadWithoutReplay(t *testing.T) {
	root, j, r := agyLiveBindingFixture(t)
	cwd := filepath.Join(t.TempDir(), "checkout")
	directory, _ := agyStoreDirectory(cwd, r.dispatch.RunID)
	if r.writeAGYStore(&nativeStore{Source: "agy", Directory: directory}) != nil {
		t.Fatal("fixture store")
	}
	reads := 0
	agent := func(context.Context, string) (AgentInfo, error) {
		return AgentInfo{Agent: "agy", Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, Cwd: cwd,
			InteractiveReady: true, AgentStatus: "done", StateChangeSeq: 1}, nil
	}
	native := func(context.Context, *nativeStore) (string, nativeIdentityReason) {
		reads++
		if reads == 1 {
			return "", nativeUnavailable
		}
		return syntheticAGYID, ""
	}
	if retryAGYNativeBinding(context.Background(), root, "fixture/inbox", nil, j, agent, native) {
		t.Fatal("missing first session was invented")
	}
	if !retryAGYNativeBinding(context.Background(), root, "fixture/inbox", nil, j, agent, native) {
		t.Fatal("late native session did not bind")
	}
	if retryAGYNativeBinding(context.Background(), root, "fixture/inbox", nil, j, agent, native) || reads != 2 {
		t.Fatal("bound session was read or replaced again")
	}
}

func TestAGYBindingHonorsAuthorizedNativeDefaultEffort(t *testing.T) {
	for _, change := range []string{"default", "wrong-rendered", "wrong-source", "wrong-flag"} {
		t.Run(change, func(t *testing.T) {
			root, j, r := agyLiveBindingFixture(t)
			r.dispatch.Effort = "provider_default"
			if change == "wrong-flag" {
				r.dispatch.Effort = "high"
			}
			r.dispatch.MatrixSource = ownerNativeSource
			if change == "wrong-source" {
				r.dispatch.MatrixSource = matrixSourceRepository
			}
			if r.writeDispatch("dispatched", r.dispatch.Location) != nil {
				t.Fatal("fixture default effort dispatch")
			}
			path := filepath.Join(filepath.Dir(r.file), "binding.json")
			raw, _ := os.ReadFile(path)
			raw = []byte(strings.Replace(string(raw), `"effort":"high"`, `"effort":"`+r.dispatch.Effort+`"`, 1))
			if os.WriteFile(path, raw, 0600) != nil {
				t.Fatal("fixture default effort binding")
			}
			j.Overrides.Effort = "" // Matrix omits the native-default flag.
			if change == "wrong-rendered" {
				j.Overrides.Effort = "high"
			}
			cwd := filepath.Join(t.TempDir(), "checkout")
			directory, _ := agyStoreDirectory(cwd, r.dispatch.RunID)
			if r.writeAGYStore(&nativeStore{Source: "agy", Directory: directory}) != nil {
				t.Fatal("fixture store")
			}
			agent := func(context.Context, string) (AgentInfo, error) {
				return AgentInfo{Agent: "agy", Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, Cwd: cwd, InteractiveReady: true, AgentStatus: "done"}, nil
			}
			native := func(context.Context, *nativeStore) (string, nativeIdentityReason) { return syntheticAGYID, "" }
			if retryAGYNativeBinding(context.Background(), root, "fixture/inbox", nil, j, agent, native) != (change == "default") {
				t.Fatal("authorized native default was rejected or changed rendered effort accepted")
			}
		})
	}
}
