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

func TestGoalDeliveryBlockSurvivesRestartAndNeverClosesAtDeadline(t *testing.T) {
	for _, delivery := range []string{"pending", "blocked", "", "adopted-without-native-goal"} {
		t.Run("delivery-"+delivery, func(t *testing.T) {
			root, j, receipt := liveBindingFixture(t)
			j.Host = "fixture-host"
			j.GoalDelivery = delivery
			if delivery == "adopted-without-native-goal" {
				j.Agent, j.GoalDelivery, j.NativeGoal = "codex", "", nil
			}
			j.SpawnedAt = time.Now().Add(-2 * time.Hour)
			j.Deadline = time.Now().Add(-time.Minute)
			bin := filepath.Join(t.TempDir(), "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(root, "commands.jsonl")
			t.Setenv("DELIVERY_CALLS", calls)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			fake := `#!/usr/bin/env python3
import json,os,sys
with open(os.environ['DELIVERY_CALLS'],'a') as f:f.write(json.dumps(sys.argv[1:])+'\n')
print('{}')
`
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			hb := filepath.Join(home, ".local", "bin")
			if err := os.MkdirAll(hb, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(hb, "herdr"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, hosts: map[string]Host{"fixture-host": {Name: "fixture-host", Home: home}}, st: loadState(filepath.Join(root, "state.json"))}
			c.st.Jobs[j.Issue] = j
			if err := c.st.save(); err != nil {
				t.Fatal(err)
			}
			is := Issue{ID: "private-fixture-id", Number: j.Issue, Title: "private fixture title", Body: "private fixture body"}
			status := map[int]agentRef{j.Issue: {Host: j.Host, Agent: "codex", Pane: j.Pane, Workspace: j.Workspace, Status: "done"}}
			for i := 0; i < 3; i++ {
				c.st = loadState(c.st.path)
				j = c.st.Jobs[j.Issue]
				if j == nil {
					t.Fatal("unconfirmed issue was torn down")
				}
				c.supervise(context.Background(), j.Issue, j, status, is)
				if j.GoalDelivery != "blocked" || (j.NativeGoal != nil && j.NativeGoal.PromptConfirmed) || c.st.LaunchBlocks[j.Issue].Reason != "goal-prompt-unconfirmed" {
					t.Fatal("failed delivery was not durably fenced")
				}
			}
			log, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(log)), "\n")
			if len(lines) != 1 {
				t.Fatal("restart replayed notification or a goal effect")
			}
			var argv []string
			if json.Unmarshal([]byte(lines[0]), &argv) != nil || len(argv) < 2 || argv[0] != "issue" || argv[1] != "comment" || !strings.Contains(string(log), "--body-file") {
				t.Fatal("unconfirmed job closed its issue or escaped file-based comments")
			}
			body, err := os.ReadFile(launchStatePath(root, c.cfg.Inbox, is))
			var state launchStateRecord
			if err != nil || json.Unmarshal(body, &state) != nil || state.SchemaVersion != 2 || state.State != "blocked" || state.ReasonCode == nil || *state.ReasonCode != "goal-prompt-unconfirmed" || strings.Contains(string(body), "private-fixture") {
				t.Fatal("safe issue-level blocked receipt missing")
			}
			var dispatch dispatchBinding
			body, err = os.ReadFile(filepath.Join(filepath.Dir(receipt.file), "dispatch.json"))
			if err != nil || json.Unmarshal(body, &dispatch) != nil || dispatch.State != "dispatched" {
				t.Fatal("live registered binding became a false refusal")
			}
			if loadState(c.st.path).reserveLaunch(j.Issue) {
				t.Fatal("blocked issue licensed another automatic dispatch")
			}
		})
	}
}
func TestGoalDeliveryConfirmationRequiresDurablePersistence(t *testing.T) {
	c := &Coord{st: loadState(filepath.Join(t.TempDir(), "missing", "state.json"))}
	j := &Job{GoalDelivery: "pending", NativeGoal: &dispatchGoal{}}
	if c.confirmGoalDelivery(j) == nil || j.NativeGoal.PromptConfirmed || !goalDeliveryUnconfirmed(j) {
		t.Fatal("failed persistence certified delivery")
	}
	c.st = loadState(filepath.Join(t.TempDir(), "state.json"))
	c.st.Jobs[7] = j
	if err := c.confirmGoalDelivery(j); err != nil || goalDeliveryUnconfirmed(j) {
		t.Fatal("persisted confirmation refused")
	}
	if loaded := loadState(c.st.path).Jobs[7]; loaded == nil || goalDeliveryUnconfirmed(loaded) {
		t.Fatal("confirmation lost on restart")
	}
	if goalDeliveryUnconfirmed(&Job{RunMode: true, GoalDelivery: "pending"}) || goalDeliveryUnconfirmed(&Job{Agent: "claude"}) {
		t.Fatal("run-mode or legacy non-Codex job was rerouted")
	}
}
