package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScopedCodexReadPreservesVerifiedDeferredHook(t *testing.T) {
	for _, mode := range []string{"deferred", "conflicting-hook", "missing-proof", "changed-terminal", "changed-thread"} {
		t.Run(mode, func(t *testing.T) {
			run := provenRun(t)
			a := promptFixture().Agent
			a.Cwd = run.Cwd
			j := &Job{Agent: "codex", Label: a.Name, Pane: a.PaneID, Workspace: a.WorkspaceID, Workdir: a.Cwd, TerminalID: a.TerminalID, NativeSessionID: run.NativeSessionID, RemoteControl: run, HerdrScope: &herdrScope{Session: "dispatch"}}
			switch mode {
			case "conflicting-hook":
				a.AgentSession = json.RawMessage(`{"source":"herdr:codex","agent":"codex","kind":"id","value":"foreign-thread"}`)
			case "missing-proof":
				run.Resume = nil
			case "changed-terminal":
				a.TerminalID = "replacement-terminal"
			case "changed-thread":
				j.NativeSessionID = "foreign-thread"
			}
			root := t.TempDir()
			bin := filepath.Join(root, ".local", "bin")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			body := fixtureJSON(t, map[string]any{"result": map[string]any{"type": "agent_info", "agent": a}})
			if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\nprintf '%s\\n' "+shq(string(body))+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			h := (Host{Home: root}).forJob(j)
			err := verifyCodexAttachment(context.Background(), j.Label, run, &dispatchLocation{PaneID: j.Pane, WorkspaceID: j.Workspace}, func() (AgentInfo, error) { return h.agentInfoOf(context.Background(), j.Pane) })
			if (err == nil) != (mode == "deferred") {
				t.Fatal("scoped native attachment result", mode, err)
			}
		})
	}
}

func TestIdleClaudeAcquiresDelayedBindingWithoutContinuationInput(t *testing.T) {
	root, j, receipt := claudeLiveBindingFixture(t)
	j.Host, j.TerminalID, j.GoalDelivery = "fixture-host", "fixture-terminal", "confirmed"
	receipt.dispatch.Host = j.Host
	if err := receipt.writeDispatch("dispatched", receipt.dispatch.Location); err != nil {
		t.Fatal(err)
	}
	id := privateTestID(t)
	raw := claudeAgentInfo(t, id)
	var response struct {
		Agent AgentInfo `json:"agent"`
	}
	if err := json.Unmarshal(fixtureJSON(t, raw), &response); err != nil {
		t.Fatal(err)
	}
	a := response.Agent
	a.AgentStatus = "idle"
	a.CompletionSeq = 0
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(home, "calls")
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> "+shq(calls)+"\nprintf '%s\\n' '{}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' '[]'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := Host{Name: j.Host, Home: home}
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, hosts: map[string]Host{j.Host: h}, st: loadState(filepath.Join(home, "state.json"))}
	c.st.Jobs[j.Issue] = j
	c.fleet = &herdrFleet{hosts: map[string]Host{h.observerKey(): h}, agents: map[string][]AgentInfo{h.observerKey(): {a}}, up: map[string]bool{h.observerKey(): true}, wake: make(chan struct{}, 1)}
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	ref := agentRef{Host: j.Host, Agent: j.Agent, Status: "idle", Pane: j.Pane, Workspace: j.Workspace, TerminalID: j.TerminalID}
	c.superviseActive(context.Background(), j.Issue, j, map[int]agentRef{j.Issue: ref}, Issue{Number: j.Issue})
	_, bound, err := loadNativeBindingReceipt(root, j.DispatchKey, j, c.cfg.Inbox, nil, j.Agent)
	if err != nil || bound != id || loadState(c.st.path).Jobs[j.Issue].NativeSessionID != id {
		t.Fatal("idle Claude left its delayed identity unbound", err)
	}
	logged, _ := os.ReadFile(calls)
	if strings.Contains(string(logged), "prompt") || strings.Contains(string(logged), "send-keys") {
		t.Fatal("binding acquisition sent continuation input")
	}
}
