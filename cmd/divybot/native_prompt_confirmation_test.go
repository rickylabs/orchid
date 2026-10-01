package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativePromptWaitsPastHerdrShortGateWithoutResubmitting(t *testing.T) {
	before := AgentInfo{Agent: "codex", Name: "codex-396", PaneID: "w1:p1", WorkspaceID: "w1", AgentStatus: "idle", StateChangeSeq: 7}
	checks := 0
	waits := 0
	err := awaitNativePrompt(context.Background(), before, func(context.Context) (AgentInfo, error) {
		checks++
		after := before
		if checks > 8 { // More than Herdr's five-second fixed transition gate.
			after.AgentStatus = "working"
			after.StateChangeSeq++
		}
		return after, nil
	}, func(context.Context) bool { waits++; return waits < 15 })
	if err != nil || checks != 9 || waits != 8 {
		t.Fatalf("late real transition was refused: err=%v checks=%d waits=%d", err, checks, waits)
	}
	for _, tc := range []struct {
		name  string
		after AgentInfo
	}{
		{"stale-done", AgentInfo{Agent: "codex", Name: "codex-396", PaneID: "w1:p1", WorkspaceID: "w1", AgentStatus: "done", StateChangeSeq: 7}},
		{"changed-but-idle", AgentInfo{Agent: "codex", Name: "codex-396", PaneID: "w1:p1", WorkspaceID: "w1", AgentStatus: "idle", StateChangeSeq: 8}},
		{"wrong-workspace", AgentInfo{Agent: "codex", Name: "codex-396", PaneID: "w1:p1", WorkspaceID: "w2", AgentStatus: "working", StateChangeSeq: 8}},
		{"wrong-agent", AgentInfo{Agent: "claude", Name: "codex-396", PaneID: "w1:p1", WorkspaceID: "w1", AgentStatus: "working", StateChangeSeq: 8}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if nativePromptObserved(before, tc.after) {
				t.Fatal("unrelated or stale status confirmed this prompt")
			}
		})
	}
	after := before
	after.AgentStatus = "done"
	after.StateChangeSeq++
	if nativePromptObserved(before, after) {
		t.Fatal("done without prompt evidence confirmed a turn")
	}
}

func TestNativePromptUsesOneUnwaitedSubmission(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "calls")
	t.Setenv("PROMPT_FIXTURE_CALLS", calls)
	script := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[1:]
path=os.environ['PROMPT_FIXTURE_CALLS']
with open(path,'a') as f:f.write(' '.join(args[:2])+(' wait' if '--wait' in args else '')+'\n')
if args[:2]==['agent','get']:
 n=sum(1 for x in open(path) if x.startswith('agent get'))
 a={'agent':'opencode','name':'fixture-opencode','pane_id':'w1:p1','workspace_id':'w1','agent_status':'working' if n>=4 else 'idle','state_change_seq':2 if n>=4 else 1}
 print(json.dumps({'result':{'agent':a}}))
elif args[:2]==['agent','prompt']:
 if '--wait' in args:
  print(json.dumps({'error':{'code':'agent_prompt_stalled','message':'five-second gate'}}));sys.exit(1)
 print(json.dumps({'result':{'accepted':True}}))
else:print(json.dumps({'result':{}}))
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	host := Host{Home: root, Name: "fixture-host"}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := host.injectGoal(ctx, "w1:p1", "read the staged goal", true); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "agent prompt") != 1 || strings.Contains(string(log), "wait") {
		t.Fatalf("native prompt was resubmitted or used Herdr's short wait gate: %q", log)
	}
}

func TestLateBindingMayRetryOnlyPreWriteIdentityFailure(t *testing.T) {
	j := &Job{NativeGoal: &dispatchGoal{PromptConfirmed: true, Reason: "native-session-unavailable"}}
	if !retryBoundGoalEligible(j) {
		t.Fatal("confirmed prompt with late identity cannot write its goal")
	}
	j.NativeGoal.PromptConfirmed = false
	if retryBoundGoalEligible(j) {
		t.Fatal("unconfirmed prompt gained a goal write")
	}
	j.NativeGoal.PromptConfirmed = true
	j.NativeGoal.Owned = true
	if retryBoundGoalEligible(j) {
		t.Fatal("owned goal would be written twice")
	}
	j.NativeGoal.Owned = false
	for _, reason := range []string{"goal-source-unavailable", "goal-already-exists", "goal-state-persistence-failed"} {
		j.NativeGoal.Reason = reason
		if retryBoundGoalEligible(j) {
			t.Fatalf("ambiguous remote goal effect %s would be replayed", reason)
		}
	}
	j.NativeGoal.Reason = "goal-identity-source-unavailable"
	if !retryBoundGoalEligible(j) {
		t.Fatal("pre-write identity read failure was not retryable")
	}
}

func TestLateBoundConfirmedPromptWritesGoal(t *testing.T) {
	root, j, receipt := liveBindingFixture(t)
	id := "fixture-thread"
	if err := receipt.writeNativeIdentity(&id); err != nil {
		t.Fatal(err)
	}
	j.NativeGoal.PromptConfirmed = true
	j.NativeGoal.Reason = "native-session-unavailable"
	j.NativeGoal.Intent = fixtureIntent()
	host := goalTransportHost(t, "")
	script := `#!/usr/bin/env python3
import json,os
agent={'agent':'codex','name':'fixture-agent','pane_id':'w1:p1','workspace_id':'w1','interactive_ready':True,
       'agent_session':{'source':'herdr:codex','agent':'codex','kind':'id','value':'fixture-thread'}}
print(json.dumps({'result':{'type':'agent_info','agent':agent}}))
`
	if err := os.WriteFile(filepath.Join(host.Home, ".local", "bin", "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, st: loadState(filepath.Join(t.TempDir(), "state.json"))}
	c.st.Jobs[j.Issue] = j
	c.retryBoundGoal(context.Background(), host, j)
	if !j.NativeGoal.Owned || j.NativeGoal.LastStatus != "active" || !j.NativeGoal.UpdatedNotification || j.NativeGoal.Reason != "" {
		t.Fatalf("late native binding did not complete the goal write: %+v", j.NativeGoal)
	}
	saved := loadState(c.st.path).Jobs[j.Issue]
	if saved == nil || saved.NativeGoal == nil || !saved.NativeGoal.Owned {
		t.Fatal("late goal ownership was not persisted")
	}
}

func TestLateGoalRetryWaitsForPrivateBinding(t *testing.T) {
	root, j, _ := liveBindingFixture(t)
	j.NativeGoal.PromptConfirmed = true
	j.NativeGoal.Reason = "native-session-unavailable"
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, st: loadState(filepath.Join(t.TempDir(), "state.json"))}
	c.st.Jobs[j.Issue] = j
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.retryBoundGoal(ctx, Host{}, j)
	if j.NativeGoal.Owned || j.NativeGoal.Reason != "native-session-unavailable" {
		t.Fatal("goal write was attempted before private identity binding")
	}
}
