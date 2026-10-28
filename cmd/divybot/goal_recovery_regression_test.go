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
	j.DispatchKey = j.NativeGoal.ReceiptKey
	h := goalTransportHost(t, "capacity-stall")
	j.Host = h.Name
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, st: loadState(filepath.Join(t.TempDir(), "state.json")), hosts: map[string]Host{h.Name: h}}
	c.st.Jobs[j.Issue] = j
	complete, err := c.completionEvidence(context.Background(), h, j, "fixture-thread")
	if complete || err != goalError("goal-capacity-recovering") {
		t.Fatal("native capacity stall was left idle instead of scheduled for recovery")
	}
}
