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
	b.TerminalID = "replacement-terminal"
	if jobOccupant(b, j) {
		t.Fatal("replacement occupant accepted")
	}
}
