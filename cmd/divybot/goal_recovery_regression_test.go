package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A failed capacity turn must enter recovery instead of completion-unproven.
func TestCapacityStallEntersRecoveryFromNativeSignal(t *testing.T) {
	_, j, root := fixtureGoalBinding(t)
	j.NativeGoal.Owned = true
	j.NativeGoal.PromptConfirmed = true
	j.GoalDelivery = "confirmed"
	j.GoalDeliveryCommit = deliveryCommitMark
	run := syntheticRemoteRun(t)
	run.NativeSessionID = "fixture-thread"
	j.RemoteControl = run
	j.DispatchKey = j.NativeGoal.ReceiptKey
	base := remoteIdleHandler(run, "valid")
	h := canonicalFixtureHost(t, "valid", func(method string, params map[string]any) any {
		switch method {
		case "thread/goal/get":
			return map[string]any{"goal": fixtureGoal("blocked")}
		case "thread/turns/list":
			return map[string]any{"data": []any{map[string]any{"id": "fixture-turn", "status": "failed", "error": map[string]any{"codexErrorInfo": "serverOverloaded"}}}}
		}
		return base(method, params)
	})
	j.Host = h.Name
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, st: loadState(filepath.Join(t.TempDir(), "state.json")), hosts: map[string]Host{h.Name: h}}
	c.st.Jobs[j.Issue] = j
	complete, err := c.completionEvidence(context.Background(), h, j, "fixture-thread")
	if complete || err != goalError("goal-capacity-recovering") {
		t.Fatal("native capacity stall was left idle instead of scheduled for recovery")
	}
}

func TestCapacityRecoveryManualResumeReleasesExhaustedOwnership(t *testing.T) {
	c, j := capacityFixture(t)
	j.GoalRecovery = &goalRecovery{State: recoveryExhausted, Attempts: capacityRetryLimit, TurnID: "capacity-turn", Reason: "goal-capacity-retry-exhausted"}
	p, input := capacityReadPort(fixtureGoal("active"), "capacity-turn", "failed", "serverOverloaded")
	owns, err := c.recoverCapacityWithPort(context.Background(), j, p, time.Now())
	if err != nil || owns || capacityRecoveryOwns(j) {
		t.Fatal("manual native resume did not release exhausted recovery ownership")
	}
	saved := loadState(c.st.path).Jobs[j.Issue]
	if saved == nil || capacityRecoveryOwns(saved) || saved.GoalRecovery.Attempts != capacityRetryLimit || saved.GoalRecovery.TurnID != "capacity-turn" {
		t.Fatal("ownership release lost its durable lifetime retry history")
	}
	if strings.Contains(input.String(), "thread/goal/set") {
		t.Fatal("manual resume caused an automatic native write")
	}
}

// An open PR must not hide the native resume from the recovery owner or its relays.
func TestCapacityRecoveryManualResumeWithOpenPRRestoresRelay(t *testing.T) {
	c, j := capacityFixture(t)
	run := syntheticRemoteRun(t)
	run.NativeSessionID = "fixture-thread"
	run.ClientVersion = canonicalFixtureVersion
	run.Resume = &codexResumeProof{Producer: strings.Repeat("ab", 32), Connection: "252"}
	j.RemoteControl = run
	j.GoalDeliveryCommit = deliveryCommitMark
	j.PR = 8
	j.LastPoke = time.Now()
	j.GoalRecovery = &goalRecovery{State: recoveryExhausted, Attempts: capacityRetryLimit, TurnID: "capacity-turn", Reason: "goal-capacity-retry-exhausted"}
	base := remoteIdleHandler(run, "valid")
	h := canonicalFixtureHost(t, "multi", func(method string, params map[string]any) any {
		switch method {
		case "thread/goal/get":
			return map[string]any{"goal": fixtureGoal("active")}
		case "thread/turns/list":
			return capacityTurnResult("capacity-turn", "failed", "serverOverloaded")
		case "remoteControl/status/read":
			return map[string]string{"status": "connected"}
		}
		return base(method, params)
	})
	h.Name = j.Host
	c.hosts = map[string]Host{h.Name: h}
	bin := filepath.Join(h.Home, ".local", "bin")
	calls := filepath.Join(t.TempDir(), "effects")
	report := goalJSON(map[string]any{"result": map[string]any{"type": "agent_info", "agent": map[string]any{
		"agent": j.Agent, "name": j.Label, "pane_id": j.Pane, "workspace_id": j.Workspace, "cwd": run.Cwd,
		"agent_status": "idle", "interactive_ready": true, "state_change_seq": 4,
		"agent_session": map[string]string{"source": "herdr:codex", "agent": "codex", "kind": "id", "value": run.NativeSessionID},
	}}})
	writeFixture(t, filepath.Join(bin, "herdr"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shq(calls)+"\nprintf '%s' "+shq(string(report))+"\n")
	writeFixture(t, filepath.Join(bin, "gh"), `#!/bin/sh
printf '%s' '{"number":8,"state":"OPEN","reviews":[{"id":"review-one","author":{"login":"reviewer"},"state":"CHANGES_REQUESTED","body":"Cover the edge case."}]}'
`)
	for _, name := range []string{"herdr", "gh"} {
		if os.Chmod(filepath.Join(bin, name), 0700) != nil {
			t.Fatal("fixture executable unavailable")
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ref := agentRef{Host: j.Host, Agent: j.Agent, Pane: j.Pane, Workspace: j.Workspace, Status: "idle", StateChangeSeq: 4, CompletionSeq: 2}
	c.supervise(context.Background(), j.Issue, j, map[int]agentRef{j.Issue: ref}, Issue{Number: j.Issue})
	if capacityRecoveryOwns(j) {
		t.Fatal("open PR prevented native manual resume from releasing exhausted recovery ownership")
	}
	effects, _ := os.ReadFile(calls)
	if !strings.Contains(string(effects), "New activity on your PR") {
		t.Fatal("open PR relay stayed suppressed after native manual resume")
	}
	if c.st.Jobs[j.Issue] != j || len(c.st.CompletedRuns) != 0 || j.PR != 8 {
		t.Fatal("manual resume retired the open PR worker")
	}
}
