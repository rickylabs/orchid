package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifiedHerdrAlertsAreNativeScopedAndDeduplicated(t *testing.T) {
	for _, mode := range []string{"native-block", "policy", "detected", "foreign-native", "foreign-session", "foreign-terminal", "duplicate-occupant", "unbound", "dry"} {
		t.Run(mode, func(t *testing.T) {
			c, j, _, _, _ := completionFixture(t)
			a := completionAgent(j)
			a.AgentStatus = "blocked"
			a.TerminalID = "fixture-terminal"
			a.VendorEvent = &vendorLifecycleEvent{Agent: a.Agent, Name: a.Name, Pane: a.PaneID, Workspace: a.WorkspaceID, Cwd: a.Cwd, Terminal: a.TerminalID, Session: "thread-fixture", Event: "PermissionRequest", Seq: 44}
			j.TerminalID = a.TerminalID
			kind, reason := "native-block", ""
			switch mode {
			case "policy":
				kind, reason = "policy", "goal-prompt-unconfirmed"
			case "detected":
				a.VendorEvent = nil
			case "foreign-native":
				a.AgentSession = json.RawMessage(`{"source":"herdr:codex","agent":"codex","kind":"id","value":"foreign-thread"}`)
			case "foreign-session":
				a.HerdrScope = &herdrScope{Session: "other"}
			case "foreign-terminal":
				a.TerminalID = "replacement-terminal"
			case "unbound":
				j.DispatchKey = ""
			case "dry":
				c.dry = true
			}
			c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
				if mode == "duplicate-occupant" {
					return []AgentInfo{a, a}, nil
				}
				return []AgentInfo{a}, nil
			}
			if err := c.st.save(); err != nil {
				t.Fatal(err)
			}
			calls := 0
			c.actions.notify = func(_ context.Context, h Host, title, body string) error {
				calls++
				if !sameHerdrScope(h.herdrScope(), j.HerdrScope) {
					t.Fatal("notification escaped server scope")
				}
				if strings.Contains(title+body, "thread-fixture") || strings.Contains(title+body, "fixture-terminal") || strings.Contains(title+body, "/fixture/") {
					t.Fatal("private native data in alert")
				}
				return nil
			}
			c.emitHerdrAlert(context.Background(), j, kind, reason)
			c.st = loadState(c.st.path)
			j = c.st.Jobs[j.Issue]
			c.emitHerdrAlert(context.Background(), j, kind, reason)
			want := 0
			if mode == "native-block" || mode == "policy" {
				want = 1
			}
			if calls != want {
				t.Fatalf("notifications=%d want=%d", calls, want)
			}
		})
	}
}
