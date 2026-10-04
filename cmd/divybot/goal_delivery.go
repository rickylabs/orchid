package main

import (
	"context"
	"errors"
	"log"
)

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

// The goal commit for every job that is not Remote Control Codex. One on-time
// decision, recorded by one save: nothing after the save can refuse, so a
// refusal never leaves `confirmed` on disk. Before the rename the disk never
// changed and memory gets its prior pair back; after it the pathname names
// `confirmed`, and only its crash durability is uncertain, as for RC Codex.
func (c *Coord) confirmGoalDelivery(ctx context.Context, j *Job) error {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	if ctx.Err() != nil { // guard:confirm-decision
		return errPromptUnconfirmed
	}
	priorDelivery := j.GoalDelivery
	priorPrompt := j.NativeGoal != nil && j.NativeGoal.PromptConfirmed
	j.GoalDelivery = "confirmed"
	if j.NativeGoal != nil {
		j.NativeGoal.PromptConfirmed = true
	}
	err := c.st.saveLocked()
	var saved stateSaveError
	if err != nil && !(errors.As(err, &saved) && saved.afterRename) { // guard:confirm-before-rename
		j.GoalDelivery = priorDelivery
		if j.NativeGoal != nil {
			j.NativeGoal.PromptConfirmed = priorPrompt
		}
		return errPromptUnconfirmed
	}
	if err != nil {
		log.Printf("issue #%d: goal-delivery-durability-uncertain", j.Issue)
	}
	return nil
}
