package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
