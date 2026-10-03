package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Sanitized 1.18.34 stored message shape: user summary is metadata with diffs,
// whereas assistant summary is a boolean compaction flag. No runtime payload.
func openCode11834SummaryFixture(t *testing.T, run *openCodeRun, userSummary, assistantSummary any) []byte {
	t.Helper()
	var record map[string]any
	if json.Unmarshal(openCodeExportFixture(run, "fixture answer"), &record) != nil {
		t.Fatal("fixture export")
	}
	messages := record["messages"].([]any)
	if userSummary != nil {
		messages[0].(map[string]any)["info"].(map[string]any)["summary"] = userSummary
	}
	if assistantSummary != nil {
		messages[1].(map[string]any)["info"].(map[string]any)["summary"] = assistantSummary
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOpenCodeSummaryRoleContractKeepsAssistantCompactionAndInvalidShapesClosed(t *testing.T) {
	_, j, _ := openCodeBindingFixture(t)
	user := map[string]any{"title": "fixture", "body": "fixture metadata", "diffs": []any{}}
	for _, tc := range []struct {
		name            string
		user, assistant any
		done            bool
		invalid         bool
	}{
		{"object-user", user, false, true, false},
		{"absent", nil, nil, true, false},
		{"assistant-compaction", user, true, false, false},
		{"user-bool", true, false, false, true},
		{"user-string", "true", false, false, true},
		{"user-array", []any{}, false, false, true},
		{"user-number", 1, false, false, true},
		{"assistant-object", user, map[string]any{"diffs": []any{}}, false, true},
		{"assistant-array", user, []any{}, false, true},
		{"assistant-string", user, "true", false, true},
		{"assistant-number", user, 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := openCode11834SummaryFixture(t, j.OpenCode, tc.user, tc.assistant)
			confirmed, done, err := inspectOpenCodeExport(raw, j.OpenCode)
			if tc.invalid {
				if err != matrixReason("opencode-output-unconfirmed") || confirmed || done {
					t.Fatal("invalid role summary granted acceptance")
				}
				return
			}
			if !confirmed || done != tc.done || err != nil {
				t.Fatal("role metadata altered native completion", err)
			}
		})
	}
	for _, role := range []string{"user", "assistant"} {
		if compact, err := openCodeSummary(role, json.RawMessage("null")); err != nil || compact {
			t.Fatal("nullable optional metadata changed native meaning")
		}
	}
	if _, err := openCodeSummary("unknown", json.RawMessage(`{"diffs":[]}`)); err == nil {
		t.Fatal("unsupported role gained summary interpretation")
	}
}

func TestOpenCodeUserSummaryCannotRelaxAnyExactNativeChecks(t *testing.T) {
	_, j, _ := openCodeBindingFixture(t)
	good := string(openCode11834SummaryFixture(t, j.OpenCode, map[string]any{"diffs": []any{}}, false))
	for _, tc := range []struct{ name, old, replacement string }{
		{"prompt", runPointer, "different prompt"},
		{"route", `"providerID":"fixture-provider"`, `"providerID":"foreign"`},
		{"parent", `"parentID":"fixture-user"`, `"parentID":"foreign"`},
		{"clock", `"completed":2002`, `"completed":2000`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(good, tc.old) {
				t.Fatal("mutant control did not hit fixture")
			}
			raw := []byte(strings.Replace(good, tc.old, tc.replacement, 1))
			if _, done, err := inspectOpenCodeExport(raw, j.OpenCode); err == nil || done {
				t.Fatal("summary object bypassed exact native guard")
			}
		})
	}
	var record map[string]any
	_ = json.Unmarshal([]byte(good), &record)
	assistant := record["messages"].([]any)[1].(map[string]any)
	assistant["parts"] = append(assistant["parts"].([]any), map[string]any{"type": "tool", "sessionID": j.OpenCode.SessionID, "messageID": "fixture-assistant", "state": map[string]any{"status": "completed"}})
	raw, _ := json.Marshal(record)
	if confirmed, done, err := inspectOpenCodeExport(raw, j.OpenCode); !confirmed || done || err != nil {
		t.Fatal("user summary promoted tool continuation to final stop")
	}
	assistant["info"].(map[string]any)["finish"] = "tool-calls"
	raw, _ = json.Marshal(record)
	if confirmed, done, err := inspectOpenCodeExport(raw, j.OpenCode); !confirmed || done || err != nil {
		t.Fatal("user summary changed tool-calls meaning")
	}
	duplicate := strings.Replace(good, `"summary":{"diffs":[]}`, `"summary":{"diffs":[],"diffs":[]}`, 1)
	if duplicate == good {
		t.Fatal("duplicate fixture unchanged")
	}
	if _, _, err := inspectOpenCodeExport([]byte(duplicate), j.OpenCode); err == nil {
		t.Fatal("duplicate summary metadata bypassed strict JSON")
	}
}

func TestOpenCode11834UserSummaryDoesNotBlockExactPromptOrNativeBinding(t *testing.T) {
	root, j, receipt := openCodeBindingFixture(t)
	raw := openCode11834SummaryFixture(t, j.OpenCode, map[string]any{"diffs": []any{}}, false)
	confirmed, complete, err := inspectOpenCodeExport(raw, j.OpenCode)
	if err != nil || !confirmed || !complete {
		t.Fatalf("valid 1.18.34 native record lost acceptance: confirmed=%v complete=%v err=%v", confirmed, complete, err)
	}
	read := func(context.Context, string) (AgentInfo, error) {
		return AgentInfo{Agent: j.Agent, Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, Cwd: j.OpenCode.Cwd, InteractiveReady: true, StateChangeSeq: 4}, nil
	}
	observe := func(_ context.Context, j *Job) (bool, bool, error) { return inspectOpenCodeExport(raw, j.OpenCode) }
	if !retryOpenCodeNativeBinding(context.Background(), root, "fixture/inbox", nil, j, read, observe) {
		t.Fatal("valid role metadata prevented certified late identity")
	}
	var binding map[string]any
	b, err := os.ReadFile(filepath.Join(filepath.Dir(receipt.file), "binding.json"))
	if err != nil || json.Unmarshal(b, &binding) != nil || binding["NativeSessionID"] != j.OpenCode.SessionID {
		t.Fatal("certified native identity was not persisted")
	}
}
