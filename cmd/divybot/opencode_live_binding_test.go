package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeOnlyFinalStopWithoutContinuationCanComplete(t *testing.T) {
	run := &openCodeRun{Route: openCodeRoute{"fixture-provider", "fixture-model", "high"}, Cwd: "/fixture/checkout", SessionID: "ses_fixture", NotBefore: 1000, ExpectedPromptDigest: shaText([]byte(runPointer))}
	for _, finish := range []string{"unknown", "length", "content-filter", "tool-calls", ""} {
		raw := strings.Replace(string(openCodeExportFixture(run, "OK")), `"finish":"stop"`, `"finish":"`+finish+`"`, 1)
		if confirmed, done, err := inspectOpenCodeExport([]byte(raw), run); !confirmed || done || err != nil {
			t.Fatal("nonfinal native finish became completion or lost prompt confirmation", finish)
		}
	}
	var record map[string]any
	_ = json.Unmarshal(openCodeExportFixture(run, "OK"), &record)
	assistant := record["messages"].([]any)[1].(map[string]any)
	assistant["parts"] = append(assistant["parts"].([]any), map[string]any{"type": "tool", "sessionID": run.SessionID, "messageID": "fixture-assistant", "state": map[string]any{"status": "completed"}})
	raw, _ := json.Marshal(record)
	if confirmed, done, err := inspectOpenCodeExport(raw, run); !confirmed || done || err != nil {
		t.Fatal("stop with completed continuation tool passed as final")
	}
	tool := assistant["parts"].([]any)[1].(map[string]any)
	tool["metadata"] = map[string]any{"providerExecuted": true}
	raw, _ = json.Marshal(record)
	if confirmed, done, err := inspectOpenCodeExport(raw, run); !confirmed || !done || err != nil {
		t.Fatal("native provider-executed tool blocked a final stop")
	}
	delete(tool, "metadata")
	tool["state"] = map[string]any{"status": "error", "metadata": map[string]any{"interrupted": true}}
	raw, _ = json.Marshal(record)
	if confirmed, done, err := inspectOpenCodeExport(raw, run); !confirmed || !done || err != nil {
		t.Fatal("native cleanup orphan blocked a final stop")
	}
	summary := strings.Replace(string(openCodeExportFixture(run, "OK")), `"finish":"stop"`, `"summary":true,"finish":"stop"`, 1)
	if confirmed, done, err := inspectOpenCodeExport([]byte(summary), run); !confirmed || done || err != nil {
		t.Fatal("compaction summary became final answer")
	}
	badClock := strings.Replace(string(openCodeExportFixture(run, "OK")), `"completed":2002`, `"completed":2000`, 1)
	if _, done, err := inspectOpenCodeExport([]byte(badClock), run); done || err == nil {
		t.Fatal("completion precedes native assistant start")
	}
	ignored := strings.Replace(string(openCodeExportFixture(run, "OK")), `"text":"OK"`, `"ignored":true,"text":"OK"`, 1)
	if _, done, err := inspectOpenCodeExport([]byte(ignored), run); done || err != matrixReason("opencode-empty-answer") {
		t.Fatal("ignored text passed as an answer")
	}
}

func openCodeBindingFixture(t *testing.T) (string, *Job, *durableMatrixReceipt) {
	t.Helper()
	root := privateTestRoot(t)
	issueID, repo, brief := "fixture-7", "fixture/repo", strings.Repeat("b", 64)
	key := shaText([]byte(issueID + "\x00" + repo + "\x00" + brief))
	route := openCodeRoute{"fixture-provider", "fixture-model", "high"}
	binding := map[string]any{"IssueID": issueID, "Repo": repo, "BriefDigest": brief, "Host": "fixture-host",
		"Route": map[string]any{"transport": "opencode", "provider": route.Provider, "model": route.qualifiedModel(), "effort": "high"}}
	r, err := persistMatrixReceipt(root, key, "fixture command", receiptFor(MatrixConfig{}, syntheticRoute()), binding)
	if err != nil {
		t.Fatal(err)
	}
	r.dispatch = &dispatchBinding{SchemaVersion: 1, RunID: "orchid-" + key, Issue: dispatchIssue{Repo: "fixture/inbox", Number: 7}, Source: "opencode", Host: "fixture-host", Provider: route.Provider, Model: route.qualifiedModel(), Effort: "high"}
	if r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}) != nil {
		t.Fatal("fixture dispatch")
	}
	j := &Job{Issue: 7, Agent: "opencode", Repo: repo, Label: "fixture-agent", Pane: "w1:p1", Workspace: "w1", Host: "fixture-host", DispatchKey: key, GoalDelivery: "confirmed", OpenCode: &openCodeRun{Route: route, Cwd: "/fixture/checkout", SessionID: "ses_fixture", NotBefore: 1000, CreatedAt: 2000, ExpectedPromptDigest: shaText([]byte(runPointer))}}
	return root, j, r
}
func TestOpenCodePrivateReceiptCanJoinItsNativeSession(t *testing.T) {
	root, j, _ := openCodeBindingFixture(t)
	if _, _, err := loadNativeBindingReceipt(root, j.DispatchKey, j, "fixture/inbox", nil, "opencode"); err != nil {
		t.Fatal("valid OpenCode receipt remains unsupported")
	}
}

func TestOpenCodeNativeIdentityRemainsPrivate(t *testing.T) {
	_, _, r := openCodeBindingFixture(t)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(r.file), "dispatch.json"))
	if err != nil || strings.Contains(string(raw), "ses_fixture") {
		t.Fatal("native identity published")
	}
}

func TestOpenCodeLateBindingAuthorityAndOccupantGuards(t *testing.T) {
	for _, change := range []string{"", "pending", "run-mode", "missing-digest", "excluded", "wrong-source", "wrong-host", "wrong-reservation", "wrong-route", "wrong-job-route", "wrong-occupant", "not-ready", "changed-sequence", "unconfirmed", "native-error", "changed-native", "changed-dispatch", "changed-binding", "uncertain", "already-bound"} {
		t.Run(change, func(t *testing.T) {
			root, j, receipt := openCodeBindingFixture(t)
			path := filepath.Join(filepath.Dir(receipt.file), "binding.json")
			changeBinding := func(field string, value any) {
				raw, _ := os.ReadFile(path)
				var b map[string]any
				_ = json.Unmarshal(raw, &b)
				b[field] = value
				raw, _ = json.Marshal(b)
				if os.WriteFile(path, raw, 0600) != nil {
					t.Fatal("fixture binding change")
				}
			}
			switch change {
			case "pending":
				j.GoalDelivery = "pending"
			case "run-mode":
				j.RunMode = true
			case "missing-digest":
				j.OpenCode.ExpectedPromptDigest = ""
			case "excluded":
				j.OpenCode.ExcludedIDs = []string{j.OpenCode.SessionID}
			case "wrong-source":
				j.Agent = "claude"
			case "wrong-host":
				changeBinding("Host", "other-host")
			case "wrong-reservation":
				changeBinding("IssueID", "other-issue")
			case "wrong-route":
				changeBinding("Route", map[string]string{"transport": "opencode", "provider": "foreign", "model": "foreign/model", "effort": "high"})
			case "wrong-job-route":
				j.OpenCode.Route.Model = "foreign-model"
			case "already-bound":
				id := "ses_existing"
				if receipt.writeNativeIdentity(&id) != nil {
					t.Fatal("fixture existing binding")
				}
			}
			reads := 0
			read := func(_ context.Context, pane string) (AgentInfo, error) {
				if pane != j.Pane {
					t.Fatal("read escaped registered pane")
				}
				reads++
				a := AgentInfo{Agent: "opencode", Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, Cwd: j.OpenCode.Cwd, InteractiveReady: true, AgentStatus: "working", StateChangeSeq: 4}
				if change == "wrong-occupant" {
					a.Name = "foreign"
				}
				if change == "not-ready" {
					a.InteractiveReady = false
				}
				if change == "changed-sequence" {
					a.StateChangeSeq = uint64(reads)
				}
				return a, nil
			}
			observe := func(_ context.Context, got *Job) (bool, bool, error) {
				if got != j {
					t.Fatal("read escaped exact job")
				}
				switch change {
				case "unconfirmed":
					return false, false, nil
				case "native-error":
					return false, false, matrixReason("opencode-provider-error")
				case "changed-native":
					j.OpenCode.SessionID = "ses_foreign"
				case "changed-dispatch":
					receipt.dispatch.Profile = "changed"
					_ = receipt.writeDispatch("dispatched", receipt.dispatch.Location)
				case "changed-binding":
					changeBinding("OwnerProvenance", "changed")
				case "uncertain":
					_ = receipt.writeDispatch("uncertain", receipt.dispatch.Location)
				}
				return true, false, nil // Valid native acceptance while the response is still streaming.
			}
			bound := retryOpenCodeNativeBinding(context.Background(), root, "fixture/inbox", nil, j, read, observe)
			if bound != (change == "") {
				t.Fatal("native authority guard failed")
			}
			raw, _ := os.ReadFile(path)
			var b map[string]any
			_ = json.Unmarshal(raw, &b)
			if change == "" {
				if b["NativeSessionID"] != "ses_fixture" {
					t.Fatal("verified private identity missing")
				}
				if retryOpenCodeNativeBinding(context.Background(), root, "fixture/inbox", nil, j, func(context.Context, string) (AgentInfo, error) {
					t.Fatal("bound identity polled again")
					return AgentInfo{}, nil
				}, observe) {
					t.Fatal("existing identity replaced")
				}
			} else if change == "already-bound" {
				if b["NativeSessionID"] != "ses_existing" {
					t.Fatal("foreign identity replaced")
				}
			} else if b["NativeSessionID"] != nil {
				t.Fatal("unverified identity persisted")
			}
			public, _ := os.ReadFile(filepath.Join(filepath.Dir(receipt.file), "dispatch.json"))
			if strings.Contains(string(public), "ses_fixture") || strings.Contains(string(public), "expectedPromptDigest") {
				t.Fatal("native join leaked")
			}
		})
	}
}
