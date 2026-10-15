package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeNativeStopChecksDeliveryWithoutHerdrCompletionCounter(t *testing.T) {
	for _, mode := range []string{"delivered", "no-final", "no-hook", "session-start", "foreign-terminal", "resumed"} {
		t.Run(mode, func(t *testing.T) {
			c, j, _, _, closes := completionFixture(t)
			j.Agent = "claude"
			p := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record", "dispatch.json")
			var d dispatchBinding
			if err := readPrivateActionJSON(p, &d); err != nil {
				t.Fatal(err)
			}
			d.Source = "claude"
			b, _ := json.Marshal(d)
			if err := os.WriteFile(p, b, 0600); err != nil {
				t.Fatal(err)
			}
			a := completionAgent(j)
			a.AgentStatus, a.CompletionSeq, a.TerminalID = "idle", 0, "fixture-terminal"
			a.AgentSession = json.RawMessage(`{"agent":"claude","kind":"id","source":"herdr:claude","value":"thread-fixture"}`)
			a.VendorEvent = &vendorLifecycleEvent{Agent: j.Agent, Name: j.Label, Pane: j.Pane, Workspace: j.Workspace, Cwd: a.Cwd, Terminal: a.TerminalID, Session: "thread-fixture", Event: "Stop", Seq: 45}
			switch mode {
			case "no-hook":
				a.VendorEvent = nil
			case "session-start":
				a.VendorEvent.Event = "SessionStart"
			case "foreign-terminal":
				a.VendorEvent.Terminal = "replacement-terminal"
			}
			reads := 0
			c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
				reads++
				if mode == "resumed" && reads > 1 {
					a.AgentStatus = "working"
				}
				return []AgentInfo{a}, nil
			}
			c.actions.completed = func(context.Context, Host, *Job, string) (bool, error) { return mode != "no-final", nil }
			ref := completionRef(j)
			ref.Status, ref.CompletionSeq = "idle", 0
			got := c.retireCompleted(context.Background(), j.Issue, j, ref, true)
			if mode == "delivered" {
				if !got || *closes != 1 || c.st.CompletedRuns[j.Issue].Phase != "close-sent" {
					t.Fatal("verified Stop and delivery did not retire Claude")
				}
			} else if got || *closes != 0 {
				t.Fatal("unproven or resumed Claude retired")
			}
		})
	}
}

// Replay the retained finished smoke's Read/Bash/final turn through the native
// hook interface: submission, tool events, Stop, then
// Herdr's later done observation. Claude is outside Herdr's full lifecycle
// authority allowlist, so its status need not agree while the Stop hook runs.
func TestClaudeStopSurvivesHerdrStatusLagUntilDeliveryRetires(t *testing.T) {
	c, j, seatGone, processGone, closes := completionFixture(t)
	j.Agent = "claude"
	dispatchPath := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record", "dispatch.json")
	var dispatch dispatchBinding
	if err := readPrivateActionJSON(dispatchPath, &dispatch); err != nil {
		t.Fatal(err)
	}
	dispatch.Source = j.Agent
	if err := os.WriteFile(dispatchPath, fixtureJSON(t, dispatch), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a := completionAgent(j)
	a.Cwd, a.TerminalID, a.AgentStatus, a.CompletionSeq = filepath.Join(root, "issue-7"), "fixture-terminal", "idle", 0
	a.AgentSession = json.RawMessage(`{"agent":"claude","kind":"id","source":"herdr:claude","value":"thread-fixture"}`)
	j.Workdir = a.Cwd
	snapshot := filepath.Join(root, "agent.json")
	bin := filepath.Join(root, "herdr")
	t.Setenv("HERDR_BIN_PATH", bin)
	t.Setenv("CLAUDE_REPLAY_SNAPSHOT", snapshot)
	// The production CLI envelope is retained; private identities are fixtures.
	// Replay an unchanged status after reporting; the native session stays exact.
	if err := os.WriteFile(bin, []byte(`#!/usr/bin/env python3
import os,sys
if sys.argv[1:3]==['agent','get']:
 print(open(os.environ['CLAUDE_REPLAY_SNAPSHOT']).read())
else:
 print('{"result":{"type":"ok"}}')
`), 0700); err != nil {
		t.Fatal(err)
	}
	eventFile := vendorLifecycleFile(a.Agent, a.Name, a.PaneID, a.WorkspaceID, a.Cwd)
	host := Host{}
	c.actions.list = func(ctx context.Context, _ Host) ([]AgentInfo, error) {
		if *seatGone {
			return nil, nil
		}
		observed := a
		observed.VendorEvent = host.vendorLifecycle(ctx, observed)
		return []AgentInfo{observed}, nil
	}
	delivered := false
	c.actions.completed = func(context.Context, Host, *Job, string) (bool, error) { return delivered, nil }
	ref := completionRef(j)
	ref.CompletionSeq = 0
	for _, step := range []struct{ event, status, tool string }{
		{"SessionStart", "idle", ""}, {"UserPromptSubmit", "idle", ""},
		{"PreToolUse", "working", "Read"}, {"PostToolUse", "working", "Read"},
		{"PreToolUse", "working", "Bash"}, {"PostToolUse", "working", "Bash"},
		{"Stop", "working", ""},
	} {
		a.AgentStatus, ref.Status = step.status, step.status
		if err := os.WriteFile(snapshot, fixtureJSON(t, map[string]any{"result": map[string]any{"type": "agent_info", "agent": a}}), 0600); err != nil {
			t.Fatal(err)
		}
		input := map[string]any{"session_id": "thread-fixture", "cwd": a.Cwd, "hook_event_name": step.event}
		if step.event == "SessionStart" {
			input["source"] = "startup"
		}
		if step.tool != "" {
			input["tool_name"] = step.tool
		}
		if step.event == "Stop" {
			input["stop_hook_active"] = false
		}
		cmd := exec.Command("python3", "-c", nativeLifecycleHook, a.Agent, a.Name, a.PaneID, a.WorkspaceID, a.Cwd, eventFile)
		cmd.Stdin = bytes.NewReader(fixtureJSON(t, input))
		if out, err := cmd.CombinedOutput(); err != nil || string(out) != "{}\n" {
			t.Fatalf("hook failed: %v %q", err, out)
		}
		event := host.vendorLifecycle(context.Background(), a)
		if event == nil || event.Event != step.event {
			t.Fatalf("%s receipt lost while Herdr still reports %s", step.event, step.status)
		}
		if c.retireCompleted(context.Background(), j.Issue, j, ref, true) || *closes != 0 {
			t.Fatal("turn or Stop alone retired the assignment")
		}
	}
	a.AgentStatus, ref.Status = "done", "done"
	// No completion counter is needed; the native Stop is the turn evidence.
	if c.retireCompleted(context.Background(), j.Issue, j, ref, true) || *closes != 0 {
		t.Fatal("native Stop without final delivery retired Claude")
	}
	delivered = true
	if _, native, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, j.Agent); err != nil || native != "thread-fixture" {
		t.Fatalf("fixture native binding: %v", err)
	}
	agents, _ := c.actionList(context.Background(), host)
	if _, ok := completionOccupant(agents, j, "thread-fixture"); !ok {
		t.Fatal("fixture occupant invalid")
	}
	j.CompletionCheckedAt = time.Time{}
	if !c.retireCompleted(context.Background(), j.Issue, j, ref, true) || *closes != 1 || c.st.CompletedRuns[j.Issue].Phase != "close-sent" {
		t.Fatalf("verified Stop plus final delivery did not retire Claude: closes=%d phase=%s", *closes, c.st.CompletedRuns[j.Issue].Phase)
	}
	*seatGone, *processGone = true, true
	c.retireCompleted(context.Background(), j.Issue, j, ref, true)
	if c.st.Jobs[j.Issue] != nil || loadState(c.st.path).CompletedRuns[j.Issue].Phase != "observed" {
		t.Fatal("completed Claude did not persist paired absence")
	}
}
