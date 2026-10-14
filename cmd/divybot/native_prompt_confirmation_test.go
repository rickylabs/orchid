package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativePromptWaitsPastHerdrShortGateWithoutResubmitting(t *testing.T) {
	before := AgentInfo{NativeLifecycle: true, Agent: "opencode", Name: "codex-396", PaneID: "w1:p1", WorkspaceID: "w1", AgentStatus: "idle", StateChangeSeq: 7}
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
		{"stale-done", AgentInfo{Agent: "opencode", Name: "codex-396", PaneID: "w1:p1", WorkspaceID: "w1", AgentStatus: "done", StateChangeSeq: 7}},
		{"changed-but-idle", AgentInfo{Agent: "opencode", Name: "codex-396", PaneID: "w1:p1", WorkspaceID: "w1", AgentStatus: "idle", StateChangeSeq: 8}},
		{"wrong-workspace", AgentInfo{Agent: "opencode", Name: "codex-396", PaneID: "w1:p1", WorkspaceID: "w2", AgentStatus: "working", StateChangeSeq: 8}},
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

func TestPromptSubmissionIsSingleAndTimeoutIsUncertain(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(strconv.FormatBool(fail), func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, ".local", "bin")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(root, "calls")
			t.Setenv("PROMPT_FIXTURE_CALLS", calls)
			t.Setenv("PROMPT_FIXTURE_FAIL", strconv.FormatBool(fail))
			t.Setenv("PROMPT_FIXTURE_SOCKET", herdrEventFixture(t))
			script := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[1:]
with open(os.environ['PROMPT_FIXTURE_CALLS'],'a') as f:f.write(' '.join(args[:2])+'\n')
if args[:2] in (['agent','get'],['agent','wait']):
 print(json.dumps({'result':{'agent':{'agent':'claude','name':'fixture-agent','terminal_id':'fixture-terminal','pane_id':'w1:p1','workspace_id':'w1','cwd':'/fixture/project','interactive_ready':True,'screen_detection_skipped':True,'agent_status':'idle'}}}))
elif args[:2]==['status','server']:print(json.dumps({'running':True,'socket':os.environ['PROMPT_FIXTURE_SOCKET']}))
elif args[:2]==['agent','prompt'] and os.environ['PROMPT_FIXTURE_FAIL']=='true':
 print(json.dumps({'error':{'code':'timeout','message':'delivery uncertain'}}));sys.exit(1)
else:print(json.dumps({'result':{}}))
`
			if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			h := Host{Home: root}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var err error
			if fail {
				err = h.injectGoal(ctx, "w1:p1", "goal", false)
			} else {
				err = h.send(ctx, "w1:p1", "goal")
			}
			log, readErr := os.ReadFile(calls)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Count(string(log), "agent prompt") != 1 || strings.Contains(string(log), "pane send-keys") {
				t.Fatalf("submission was repeated: %s", log)
			}
			if fail && (err == nil || !strings.Contains(err.Error(), "unconfirmed")) {
				t.Fatalf("timeout lost uncertainty: %v", err)
			}
		})
	}
}
