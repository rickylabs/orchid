package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHerdrRoleBindingsSurviveConfigurationChanges(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "calls")
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+shq(calls)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	h := Host{Home: root, HerdrSession: "diagnostics"}
	var j Job
	if err := json.Unmarshal([]byte(`{"herdr_scope":{"session":"dispatch"}}`), &j); err != nil {
		t.Fatal(err)
	}
	if err := h.forJob(&j).send(context.Background(), "w1:p1", "fixture"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(calls)
	if string(body) != "--session\ndispatch\nagent\nprompt\nw1:p1\nfixture\n" {
		t.Fatal("control followed changed session configuration")
	}
	if got := h.forJob(&Job{}).HerdrSession; got != "" && got != "default" {
		t.Fatal("legacy job moved out of default session")
	}
	root2, job, receipt := claudeLiveBindingFixture(t)
	if err := json.Unmarshal([]byte(`{"herdr_scope":{"session":"dispatch"}}`), job); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadNativeBindingReceipt(root2, job.DispatchKey, job, "fixture/inbox", nil, "claude"); err == nil {
		t.Fatal("default-session receipt authorized a named-session job")
	}
	_ = receipt
}

func TestHerdrWaitRejectsIdenticalOccupantInAnotherSession(t *testing.T) {
	a := promptFixture().Agent
	b := a
	a.HerdrScope = &herdrScope{Session: "dispatch"}
	b.HerdrScope = &herdrScope{Session: "diagnostics"}
	if samePromptOccupant(a, b) {
		t.Fatal("another session satisfied the occupant check")
	}
	j := &Job{Agent: a.Agent, Label: a.Name, Pane: a.PaneID, Workspace: a.WorkspaceID, TerminalID: a.TerminalID, HerdrScope: a.HerdrScope}
	if jobOccupant(b, j) {
		t.Fatal("restored occupant from another session accepted")
	}
	b = a
	j.NativeSessionID = "thread-fixture"
	b.AgentSession = json.RawMessage(`{"source":"herdr:codex","agent":"codex","kind":"id","value":"replacement-thread"}`)
	if jobOccupant(b, j) {
		t.Fatal("changed native conversation accepted")
	}
	b = a
	b.TerminalID = "replacement-terminal"
	if jobOccupant(b, j) {
		t.Fatal("replacement occupant accepted")
	}
}

func TestHerdrRoleObserversPreserveRecordedSessionOnRestart(t *testing.T) {
	c, j, _, _, _ := completionFixture(t)
	h := c.hosts[j.Host]
	h.HerdrSession = "diagnostics"
	c.hosts[j.Host] = h
	j.HerdrScope = &herdrScope{Session: "dispatch"}
	observed := c.herdrObserverHosts()
	if len(observed) != 2 {
		t.Fatal("restart dropped the recorded dispatch observer")
	}
	c.fleet = &herdrFleet{hosts: observed, agents: map[string][]AgentInfo{}, up: map[string]bool{}, wake: make(chan struct{}, 1)}
	a := completionAgent(j)
	for key, host := range observed {
		c.fleet.publish(key, []AgentInfo{a}, true)
		_ = host
	}
	status, up := c.fleetStatus(context.Background())
	if !jobRefMatches(status[j.Issue], j) || !up[h.forJob(j).observerKey()] {
		t.Fatal("another session replaced the recorded occupant")
	}
	c.fleet.publish(h.forJob(j).observerKey(), nil, false)
	status, up = c.fleetStatus(context.Background())
	if _, ok := status[j.Issue]; ok || up[h.forJob(j).observerKey()] {
		t.Fatal("another session satisfied disconnected job scope")
	}

}
