package main

import (
	"context"
	"path/filepath"
	"testing"
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
