package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// A proven Remote Control Codex thread is published to the job's Herdr pane as
// identity only; an unproven run, a failed report or a different readback refuses.
func TestPublishCodexSessionIdentityOnly(t *testing.T) {
	for _, mode := range []string{"valid", "unproven", "report-failed", "readback-missing", "readback-foreign"} {
		t.Run(mode, func(t *testing.T) {
			run := provenRun(t)
			if mode == "unproven" {
				run.IdentitySource = "herdr-session-start"
			}
			agent := promptFixture().Agent
			location := &dispatchLocation{PaneID: agent.PaneID, WorkspaceID: agent.WorkspaceID}
			var calls [][]string
			herdr := func(_ context.Context, args ...string) (string, error) {
				calls = append(calls, args)
				if args[0] == "pane" {
					if mode == "report-failed" {
						return "", errors.New("fixture refusal")
					}
					return `{"id":"fixture","result":{"type":"ok"}}`, nil
				}
				a := map[string]any{"agent": "codex", "name": agent.Name, "pane_id": agent.PaneID, "workspace_id": agent.WorkspaceID, "interactive_ready": true}
				switch mode {
				case "valid":
					a["agent_session"] = map[string]string{"source": "herdr:codex", "agent": "codex", "kind": "id", "value": run.NativeSessionID}
				case "readback-foreign":
					a["agent_session"] = map[string]string{"source": "herdr:codex", "agent": "codex", "kind": "id", "value": privateTestID(t)}
				}
				body, _ := json.Marshal(map[string]any{"id": "fixture", "result": map[string]any{"type": "agent_info", "agent": a}})
				return string(body), nil
			}
			err := publishCodexSession(context.Background(), agent.Name, run, location, herdr, 42)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("publish accepted unproven or unconfirmed identity: %v", err)
			}
			if mode == "unproven" && len(calls) != 0 {
				t.Fatal("an unproven run reached Herdr")
			}
			if mode == "valid" {
				want := [][]string{
					{"pane", "report-agent-session", agent.PaneID, "--source", "herdr:codex", "--agent", "codex", "--seq", "42", "--agent-session-id", run.NativeSessionID},
					{"agent", "get", agent.PaneID},
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatalf("unexpected Herdr calls: %q", calls)
				}
			}
		})
	}
}

// The pre-goal Codex identity publishes the proven thread only when no hook did.
func TestPreGoalCodexIdentityPublishesSession(t *testing.T) {
	b, err := os.ReadFile("remote_control_identity.go")
	if err != nil {
		t.Fatal("source unavailable")
	}
	src := string(b)
	body := src[strings.Index(src, "func (h Host) preGoalRemoteIdentity("):]
	body = body[:strings.Index(body, "\n}\n")]
	if !strings.Contains(body, "if !run.HookConfirmed {") ||
		!strings.Contains(body, "publishCodexSession(ctx, label, run, location, h.herdr, time.Now().UnixNano()) // guard:codex-session-publish") {
		t.Fatal("a proven Remote Control Codex thread is never published to its Herdr pane")
	}
}
