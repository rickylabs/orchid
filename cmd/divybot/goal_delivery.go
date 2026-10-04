package main

import "context"

// Registration and prompt delivery are different effects. Pending legacy Codex
// jobs cannot acquire a completion verdict from an operator deadline either.
func goalDeliveryUnconfirmed(j *Job) bool {
	return j != nil && !j.RunMode && (j.GoalDelivery == "pending" || j.GoalDelivery == "blocked" ||
		(j.Agent == "codex" && j.GoalDelivery != "confirmed") ||
		(j.NativeGoal != nil && (j.GoalDelivery != "confirmed" || !j.NativeGoal.PromptConfirmed)) ||
		(rcCodex(j) && !deliveryConfirmed(j)))
}

func rcCodex(j *Job) bool { return j != nil && j.RemoteControl != nil && j.Agent == "codex" }

// The one delivery-authority predicate. For every job that is not Remote
// Control Codex it is the raw confirmation; RC Codex also needs the on-time
// native commit marker and the prompt-confirmation bit written with it.
func deliveryConfirmed(j *Job) bool {
	return j != nil && j.GoalDelivery == "confirmed" &&
		(!rcCodex(j) || (j.GoalDeliveryCommit == deliveryCommitMark && j.NativeGoal != nil && j.NativeGoal.PromptConfirmed))
}

func (c *Coord) blockGoalDelivery(n int, j *Job) {
	c.st.mu.Lock()
	j.GoalDelivery = "blocked"
	if j.NativeGoal != nil {
		j.NativeGoal.PromptConfirmed = false
		j.NativeGoal.Reason = "goal-prompt-unconfirmed"
	}
	_ = c.st.saveLocked() // Earlier persisted pending state also refuses after a failed save.
	c.st.mu.Unlock()
	c.st.blockLaunch(n, "goal-prompt-unconfirmed")
}

func (c *Coord) confirmGoalDelivery(j *Job) error {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	j.GoalDelivery = "confirmed"
	if j.NativeGoal != nil {
		j.NativeGoal.PromptConfirmed = true
	}
	if err := c.st.saveLocked(); err != nil {
		j.GoalDelivery = "blocked"
		if j.NativeGoal != nil {
			j.NativeGoal.PromptConfirmed = false
		}
		return errPromptUnconfirmed
	}
	return nil
}

// A successful save/read arriving after its budget cannot confirm a launch.
func confirmGoalDeliveryBeforeDeadline(ctx context.Context, confirm func() error) error {
	if ctx.Err() != nil {
		return errPromptUnconfirmed
	}
	if err := confirm(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return errPromptUnconfirmed
	}
	return nil
}
