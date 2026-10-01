package main

// Registration and prompt delivery are different effects. Pending legacy Codex
// jobs cannot acquire a completion verdict from an operator deadline either.
func goalDeliveryUnconfirmed(j *Job) bool {
	return j != nil && !j.RunMode && (j.GoalDelivery == "pending" || j.GoalDelivery == "blocked" ||
		(j.Agent == "codex" && j.GoalDelivery != "confirmed") ||
		(j.NativeGoal != nil && (j.GoalDelivery != "confirmed" || !j.NativeGoal.PromptConfirmed)))
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
