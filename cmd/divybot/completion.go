package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Private completion fence. It outlives job removal: an open inbox issue is
// never permission to restart a successful run. Cleanup holds capacity until
// the recorded seat AND its captured native process are independently absent.
type completedRun struct {
	DispatchKey     string `json:"dispatchKey"`
	NativeSessionID string `json:"nativeSessionId"`
	StateChangeSeq  uint64 `json:"stateChangeSeq"`
	Phase           string `json:"phase"` // retiring | close-failed | close-sent | observed
	CloseAttempts   int    `json:"closeAttempts"`
}

func finalCommentMarker(key string) string {
	if !digestPattern.MatchString(key) {
		return ""
	}
	runID := "orchid-" + key
	return "<!-- orchid-run " + actionOpaque("assignment", runID) + " " + actionOpaque("agent", runID) + " -->"
}

func finalCommentInstruction(key string) string {
	marker := finalCommentMarker(key)
	if marker == "" {
		return ""
	}
	return "\n## Final comment identity\nWhen the brief requires a final GitHub comment, append this exact hidden marker once to that comment at the destination specified by the brief. Keep its requested visible format. Never substitute native identifiers.\n\n" + marker + "\n"
}

// A marker is a claim; author and dispatch time must corroborate it.
func markedCompletionComment(body, author, bot, createdAt string, spawned time.Time, key string) bool {
	marker := finalCommentMarker(key)
	at, err := time.Parse(time.RFC3339, createdAt)
	return marker != "" && bot != "" && author == bot && err == nil && !spawned.IsZero() &&
		!at.Before(spawned.Truncate(time.Second)) && !at.After(time.Now()) &&
		strings.Count(body, "<!-- orchid-run ") == 1 && strings.Count(body, marker) == 1
}

func (c *Coord) completionEvidence(ctx context.Context, h Host, j *Job, native string) (bool, error) {
	if c.actions.completed != nil {
		return c.actions.completed(ctx, h, j, native)
	}
	if j.Agent == "codex" {
		var complete bool
		goalAllowed := j.NativeGoal == nil
		err := h.withGoalConnection(ctx, native, func(p *goalRPC) error {
			var e error
			complete, e = p.lastTurnCompleted()
			if e == nil && j.NativeGoal != nil {
				goal, err := p.get()
				if err != nil {
					return err
				}
				goalAllowed = completionGoalAllowsRetirement(j, goal)
			}
			return e
		})
		if j.NativeGoal != nil && (err != nil || !goalAllowed) {
			return false, err // a marked comment cannot override an active or unknown goal
		}
		if err == nil && complete {
			return true, nil
		}
	}
	var issue struct {
		Comments []struct {
			Body      string                 `json:"body"`
			CreatedAt string                 `json:"createdAt"`
			Author    struct{ Login string } `json:"author"`
		} `json:"comments"`
	}
	if err := ghJSON(ctx, &issue, "issue", "view", fmt.Sprint(j.Issue), "--repo", c.cfg.Inbox, "--json", "comments"); err != nil {
		return false, err
	}
	for _, comment := range issue.Comments {
		if markedCompletionComment(comment.Body, comment.Author.Login, c.cfg.BotLogin, comment.CreatedAt, j.SpawnedAt, j.DispatchKey) {
			return true, nil
		}
	}
	return false, nil
}

func completionGoalAllowsRetirement(j *Job, goal *nativeGoal) bool {
	return j.NativeGoal == nil || goal != nil && goal.Status == "complete" && sameGoalIntent(goal, j.NativeGoal.Intent)
}

func (c *Coord) noteCompletionUnproven(j *Job, seq uint64) {
	c.st.mu.Lock()
	if j.CompletionUnprovenSeq != nil && *j.CompletionUnprovenSeq == seq {
		c.st.mu.Unlock()
		return
	}
	j.CompletionUnprovenSeq = &seq
	_ = c.st.saveLocked()
	c.st.mu.Unlock()
	log.Printf("issue #%d: completion-unproven; no done poke", j.Issue)
	c.notify(fmt.Sprintf("issue #%d: completed turn needs inspection; assignment completion unproven", j.Issue))
}

// A bounded rotating window prunes only observed cleanup of confirmed CLOSED
// issues. Unlabelled OPEN issues and failed GitHub polls retain their fences.
func (c *Coord) pruneCompletedRuns(allOpen map[int]bool, pollOK bool, state func(int) string) {
	if !pollOK || c.dry {
		return
	}
	c.st.mu.Lock()
	var candidates []int
	for n, f := range c.st.CompletedRuns {
		if f.Phase == "observed" && c.st.Jobs[n] == nil && !allOpen[n] {
			candidates = append(candidates, n)
		}
	}
	c.st.mu.Unlock()
	sort.Ints(candidates)
	if len(candidates) == 0 {
		return
	}
	start := int(time.Now().Unix()/30) % len(candidates)
	for i := 0; i < len(candidates) && i < 8; i++ {
		n := candidates[(start+i)%len(candidates)]
		if state(n) != "CLOSED" {
			continue
		}
		c.st.mu.Lock()
		prior, ok := c.st.CompletedRuns[n]
		if ok && prior.Phase == "observed" && c.st.Jobs[n] == nil {
			delete(c.st.CompletedRuns, n)
			if c.st.saveLocked() != nil {
				c.st.CompletedRuns[n] = prior
			}
		}
		c.st.mu.Unlock()
	}
}

// Use the official integration identity, never a cwd or display name as a
// substitute. Any other agent in the workspace makes a close unsafe.
func completionOccupant(agents []AgentInfo, j *Job, native string) (AgentInfo, bool) {
	var found AgentInfo
	count := 0
	for _, a := range agents {
		if a.WorkspaceID != j.Workspace && a.PaneID != j.Pane {
			continue
		}
		count++
		if a.AgentStatus != "done" || a.PaneID != j.Pane || a.WorkspaceID != j.Workspace {
			return AgentInfo{}, false
		}
		raw, err := json.Marshal(map[string]any{"type": "agent_info", "agent": a})
		id, reason := nativeSessionFromResponse(raw, "agent_info", j.Agent, j.Label,
			&dispatchLocation{PaneID: j.Pane, WorkspaceID: j.Workspace})
		if err != nil || reason != "" || id != native {
			return AgentInfo{}, false
		}
		found = a
	}
	return found, count == 1
}

func (c *Coord) retireCompleted(ctx context.Context, n int, j *Job, ref agentRef, known bool) bool {
	c.st.mu.Lock()
	fence, fenced := c.st.CompletedRuns[n]
	c.st.mu.Unlock()
	if c.dry {
		return fenced
	}
	if j == nil {
		return fenced
	}
	if j.Agent != "codex" && j.Agent != "claude" {
		return fenced // other native adapters own their completion evidence
	}
	if !fenced && (!known || j == nil || j.Issue != n || j.GoalDelivery != "confirmed" || j.RunMode ||
		ref.Status != "done" || ref.Host != j.Host || ref.Pane != j.Pane || ref.Workspace != j.Workspace || ref.Agent != j.Agent) {
		return false
	}
	host, ok := c.hosts[j.Host]
	if !ok {
		return fenced
	}
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	receipt, native, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, j.Agent)
	if err != nil || native == "" || receipt.dispatch.Location == nil || receipt.dispatch.Host != j.Host ||
		receipt.dispatch.Location.PaneID != j.Pane || receipt.dispatch.Location.WorkspaceID != j.Workspace {
		c.noteCompletionUnproven(j, 0)
		return true
	}
	if !fenced {
		agents, err := c.actionList(check, host)
		first, valid := completionOccupant(agents, j, native)
		if err != nil || !valid {
			return true
		}
		complete, err := c.completionEvidence(check, host, j, native)
		if err != nil || !complete {
			c.noteCompletionUnproven(j, first.StateChangeSeq)
			return true // no continuation effect after a done observation
		}
		agents, err = c.actionList(check, host)
		second, valid := completionOccupant(agents, j, native)
		if err != nil || !valid || second.StateChangeSeq != first.StateChangeSeq {
			return true
		}
		fence = completedRun{DispatchKey: j.DispatchKey, NativeSessionID: native, StateChangeSeq: second.StateChangeSeq, Phase: "retiring"}
		c.st.mu.Lock()
		if c.st.CompletedRuns == nil {
			c.st.CompletedRuns = map[int]completedRun{}
		}
		c.st.CompletedRuns[n] = fence
		err = c.st.saveLocked()
		if err != nil {
			delete(c.st.CompletedRuns, n)
		}
		c.st.mu.Unlock()
		if err != nil {
			return true
		}
	}
	if fence.DispatchKey != j.DispatchKey || fence.NativeSessionID != native {
		return true
	}
	record := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
	var intent teardownIntent
	var anchor actionStopProcess
	readIntent := func() bool {
		return readPrivateActionJSON(filepath.Join(record, "teardown-intent.json"), &intent) == nil &&
			intent.SchemaVersion == 1 && intent.Issue == n && intent.DispatchKey == j.DispatchKey &&
			intent.NativeRunID == "orchid-"+j.DispatchKey && intent.NativeSessionID == native &&
			intent.Host == j.Host && intent.PaneID == j.Pane && intent.WorkspaceID == j.Workspace && intent.Cause == "teardown" &&
			readPrivateActionJSON(filepath.Join(record, "teardown-anchor.json"), &anchor) == nil
	}
	if readIntent() && c.teardownObserveOne(check, intent) {
		c.st.mu.Lock()
		prior := c.st.CompletedRuns[n]
		fence.Phase = "observed"
		c.st.CompletedRuns[n] = fence
		delete(c.st.Jobs, n)
		err = c.st.saveLocked()
		if err != nil {
			c.st.Jobs[n], c.st.CompletedRuns[n] = j, prior
		}
		c.st.mu.Unlock()
		if err == nil {
			c.clearClaudeWorking(j)
			log.Printf("issue #%d: completed; seat and process absence observed", n)
		}
		return true
	}
	if fence.Phase == "close-sent" || fence.Phase == "observed" || fence.CloseAttempts >= 3 {
		return true // delivery is not proof; observe on subsequent ticks
	}
	agents, err := c.actionList(check, host)
	before, valid := completionOccupant(agents, j, native)
	if err != nil || !valid || before.StateChangeSeq != fence.StateChangeSeq {
		log.Printf("issue #%d: completion-cleanup-unproven", n)
		return true
	}
	if !readIntent() {
		c.teardownStart(check, n, j, j.Workspace, "teardown")
	}
	if !readIntent() {
		return true // anchor and intent must be durable before the close effect
	}
	agents, err = c.actionList(check, host)
	after, valid := completionOccupant(agents, j, native)
	if err != nil || !valid || after.StateChangeSeq != before.StateChangeSeq {
		return true
	}
	fence.CloseAttempts++
	c.st.mu.Lock()
	prior := c.st.CompletedRuns[n]
	c.st.CompletedRuns[n] = fence
	err = c.st.saveLocked()
	if err != nil {
		c.st.CompletedRuns[n] = prior
	}
	c.st.mu.Unlock()
	if err != nil {
		return true
	}
	err = c.actionClose(check, host, j.Workspace)
	fence.Phase = "close-sent"
	if err != nil {
		fence.Phase = "close-failed"
		log.Printf("issue #%d: completion-close-failed", n)
	}
	c.st.mu.Lock()
	c.st.CompletedRuns[n] = fence
	_ = c.st.saveLocked()
	c.st.mu.Unlock()
	// Observe immediately so the same tick's admission sees released capacity.
	if c.teardownObserveOne(check, intent) {
		return c.retireCompleted(ctx, n, j, ref, known)
	}
	return true
}
