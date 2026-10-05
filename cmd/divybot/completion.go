package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Private completion fence. It outlives job removal: an open inbox issue is
// never permission to restart a completed run. Cleanup holds capacity until
// the recorded seat AND its captured native process are independently absent.
type completedRun struct {
	DispatchKey            string `json:"dispatchKey"`
	NativeSessionID        string `json:"nativeSessionId"`
	StateChangeSeq         uint64 `json:"stateChangeSeq"`
	Phase                  string `json:"phase"` // retiring | close-failed | close-sent | observed
	CloseAttempts          int    `json:"closeAttempts"`
	PublicationUnconfirmed bool   `json:"publicationUnconfirmed,omitempty"`
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

// A final turn must not retire a worker whose PR still needs supervision.
func (c *Coord) completionPRPending(ctx context.Context, j *Job) (bool, error) {
	if c.actions.completionPR != nil {
		return c.actions.completionPR(ctx, j)
	}
	if j.PR != 0 {
		v, err := ghPRView(ctx, j.Repo, j.PR)
		return err != nil || (v.State != "MERGED" && v.State != "CLOSED"), err
	}
	if j.Repo == "" || j.Branch == "" {
		return true, nil // absent PR scope is not proof of absence
	}
	var prs []struct {
		Number int `json:"number"`
	}
	err := ghJSON(ctx, &prs, "pr", "list", "--repo", j.Repo, "--head", j.Branch,
		"--state", "open", "--json", "number")
	return err != nil || len(prs) != 0, err
}

func (c *Coord) noteOwnerMismatch(j *Job, ref agentRef) {
	key := shaText([]byte(fmt.Sprintf("%s/%s/%s/%s/%s/%d", ref.Host, ref.Agent, ref.Pane, ref.Workspace, ref.Status, ref.StateChangeSeq)))
	c.st.mu.Lock()
	if j.OwnerMismatchNotice == key {
		c.st.mu.Unlock()
		return
	}
	j.OwnerMismatchNotice = key
	_ = c.st.saveLocked()
	c.st.mu.Unlock()
	log.Printf("issue #%d: owner-mismatch; continuation input suppressed", j.Issue)
	c.notify(fmt.Sprintf("issue #%d: recorded seat differs from fleet; inspect ownership", j.Issue))
}

func (c *Coord) completionEvidence(ctx context.Context, h Host, j *Job, native string) (bool, error) {
	if c.actions.completed != nil {
		return c.actions.completed(ctx, h, j, native)
	}
	if j.Agent == "opencode" {
		return c.openCodeCompletionEvidence(ctx, h, j, native)
	}
	if j.Agent == "agy" {
		return c.agyCompletionEvidence(ctx, h, j, native)
	}
	if j.Agent == "codex" {
		var complete bool
		goalAllowed := j.NativeGoal == nil
		err := h.forJob(j).withGoalConnection(ctx, native, func(p *goalRPC) error {
			var e error
			if j.RemoteControl != nil {
				if e = p.remoteWorkIdle(false); e != nil {
					return e
				}
			}
			complete, e = p.lastTurnCompleted()
			if e == nil && j.NativeGoal != nil {
				goal, err := p.get()
				if err != nil {
					return err
				}
				goalAllowed = completionGoalAllowsRetirement(j, goal)
			}
			if e == nil && j.RemoteControl != nil {
				e = p.reconcileNativeLifecycle()
			}
			return e
		})
		if j.RemoteControl != nil && (err != nil || !complete) {
			return false, err
		}
		if j.NativeGoal != nil && (err != nil || !goalAllowed) {
			return false, err // a marked comment cannot override an active or unknown goal
		}
		if err == nil && complete {
			return true, nil
		}
	}
	if j.FinalReportManaged {
		_, err := c.publishFinalReport(ctx, j)
		return err == nil, err
	}
	var issue struct {
		Comments []struct {
			Body      string                 `json:"body"`
			CreatedAt string                 `json:"createdAt"`
			Author    struct{ Login string } `json:"author"`
		} `json:"comments"`
	}
	home := jobHome(c.cfg.Inbox, j)
	if err := ghJSON(ctx, &issue, "issue", "view", fmt.Sprint(home.Number), "--repo", home.Repo, "--json", "comments"); err != nil {
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

// Use each adapter's certified native identity and exact occupant. Any other
// agent in the workspace makes a close unsafe.
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
		if j.Agent == "opencode" {
			// Herdr supplies the occupant, while the certified native store
			// supplies OpenCode's identity and terminal evidence.
			if !openCodeBindingEligible(j) || j.OpenCode.SessionID != native || !openCodeOccupant(a, j, j.OpenCode) {
				return AgentInfo{}, false
			}
			found = a
			continue
		}
		if j.Agent == "agy" {
			if !agyBindingEligible(j) || !agyConversationID.MatchString(native) || !agyIdentityOccupant(a, j) || issueFromCwd(a.Cwd) != j.Issue {
				return AgentInfo{}, false
			}
			found = a
			continue
		}
		if j.Agent == "codex" && j.RemoteControl != nil {
			raw, err := json.Marshal(map[string]any{"type": "agent_info", "agent": a})
			if err != nil || native != j.RemoteControl.NativeSessionID || !remoteStatusOccupant(raw, j.Agent, j.Label, j.RemoteControl.Cwd, native, &dispatchLocation{PaneID: j.Pane, WorkspaceID: j.Workspace}) {
				return AgentInfo{}, false
			}
			found = a
			continue
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
	if j != nil && j.RemoteCleanup != "" {
		return false
	}
	c.st.mu.Lock()
	fence, fenced := c.st.CompletedRuns[n]
	c.st.mu.Unlock()
	if c.dry {
		return fenced
	}
	if j == nil {
		return fenced
	}
	if j.Agent != "codex" && j.Agent != "claude" && j.Agent != "opencode" && j.Agent != "agy" {
		return fenced // other native adapters own their completion evidence
	}
	if !fenced && j.Agent == "opencode" && !openCodeBindingEligible(j) {
		return false
	}
	if !fenced && j.Agent == "agy" && !agyBindingEligible(j) {
		return false
	}
	if !fenced && (!known || j == nil || j.Issue != n || !deliveryConfirmed(j) || j.RunMode ||
		ref.Status != "done" || ref.Host != j.Host || ref.Pane != j.Pane || ref.Workspace != j.Workspace || ref.Agent != j.Agent) {
		return false
	}
	host, ok := c.hosts[j.Host]
	if !ok {
		return fenced
	}
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if j.RemoteControl != nil {
		if fenced {
			c.clearRemoteControl(j)
		} else if !c.checkRemoteHook(check, host, j) {
			return false
		}
	}
	if !fenced {
		pending, err := c.completionPRPending(check, j)
		if err != nil || pending {
			return false // PR supervision continues, with no input to a done seat
		}
	}
	receipt, native, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, j.Agent)
	if err != nil || native == "" || receipt.dispatch.Location == nil || receipt.dispatch.Host != j.Host ||
		receipt.dispatch.Location.PaneID != j.Pane || receipt.dispatch.Location.WorkspaceID != j.Workspace {
		c.noteCompletionUnproven(j, 0)
		return fenced
	}
	if !fenced {
		agents, err := c.actionList(check, host)
		first, valid := completionOccupant(agents, j, native)
		if err != nil || !valid {
			return false
		}
		complete, err := c.completionEvidence(check, host, j, native)
		publicationUnconfirmed := false
		if err != nil || !complete {
			c.noteCompletionUnproven(j, first.StateChangeSeq)
			// A file/publication request is not native terminal proof. A stable
			// done Claude seat may instead be retired as an explicit failure,
			// after a bounded grace, without claiming assignment success.
			if !c.finalPublicationFailureDue(j, first.StateChangeSeq) || check.Err() != nil {
				return false // supervision continues without continuation input
			}
			publicationUnconfirmed = true
		}
		agents, err = c.actionList(check, host)
		second, valid := completionOccupant(agents, j, native)
		if err != nil || !valid || second.StateChangeSeq != first.StateChangeSeq || !c.checkRemoteHook(check, host, j) || check.Err() != nil {
			return false
		}
		fence = completedRun{DispatchKey: j.DispatchKey, NativeSessionID: native, StateChangeSeq: second.StateChangeSeq, Phase: "retiring", PublicationUnconfirmed: publicationUnconfirmed}
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
			return false
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
		if host.stopRemoteRun(check, j) != nil || check.Err() != nil {
			return true
		}
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
			if fence.PublicationUnconfirmed {
				log.Printf("issue #%d: final publication unconfirmed; seat and process absence observed", n)
			} else {
				log.Printf("issue #%d: completed; seat and process absence observed", n)
			}
		}
		return true
	}
	if fence.Phase == "close-sent" || fence.Phase == "observed" || fence.CloseAttempts >= 3 {
		return true // delivery is not proof; observe on subsequent ticks
	}
	agents, err := c.actionList(check, host)
	before, valid := completionOccupant(agents, j, native)
	if err != nil || !valid || before.StateChangeSeq != fence.StateChangeSeq || !c.checkRemoteHook(check, host, j) {
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
	if err != nil || !valid || after.StateChangeSeq != before.StateChangeSeq || !c.checkRemoteHook(check, host, j) {
		return true
	}
	if err = host.stopRemoteRun(check, j); err != nil {
		return true
	}
	c.clearRemoteControl(j)
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

// A pane Done or a marked comment cannot substitute OpenCode's exact native
// terminal proof. Pin its private dispatch/binding before and after that read.
func (c *Coord) openCodeCompletionEvidence(ctx context.Context, h Host, j *Job, native string) (bool, error) {
	return c.openCodeCompletionWithObserver(ctx, j, native, h.observeOpenCode)
}

func (c *Coord) openCodeCompletionWithObserver(ctx context.Context, j *Job, native string, observe func(context.Context, *Job) (bool, bool, error)) (bool, error) {
	if ctx.Err() != nil || !openCodeBindingEligible(j) || j.OpenCode.SessionID != native {
		return false, matrixReason("opencode-output-unconfirmed")
	}
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err != nil {
		return false, matrixReason("opencode-output-unconfirmed")
	}
	read := func() (*durableMatrixReceipt, string, error) {
		r, id, e := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, owner, "opencode")
		if e != nil || id != native || r.dispatch.Host != j.Host || r.dispatch.Location == nil ||
			r.dispatch.Location.PaneID != j.Pane || r.dispatch.Location.WorkspaceID != j.Workspace {
			return nil, "", matrixReason("opencode-output-unconfirmed")
		}
		digest, e := readOpenCodeBindingAuthority(r, j)
		return r, digest, e
	}
	before, digest, err := read()
	if err != nil {
		return false, err
	}
	proof := *j.OpenCode
	confirmed, completed, err := observe(ctx, j)
	if err != nil || !confirmed || !completed || ctx.Err() != nil || !reflect.DeepEqual(proof, *j.OpenCode) {
		return false, matrixReason("opencode-output-unconfirmed")
	}
	after, secondDigest, err := read()
	if err != nil || secondDigest != digest || !reflect.DeepEqual(before.dispatch, after.dispatch) || ctx.Err() != nil {
		return false, matrixReason("opencode-output-unconfirmed")
	}
	return true, nil
}

// This is failure cleanup, not a new definition of native completion. Codex
// active-goal ownership and the other adapters' terminal guards stay intact.
func (c *Coord) finalPublicationFailureDue(j *Job, seq uint64) bool {
	if !j.FinalReportManaged || j.Agent != "claude" {
		return false
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	now := time.Now()
	if j.FinalDoneAt.IsZero() || j.FinalDoneSeq != seq {
		priorAt, priorSeq := j.FinalDoneAt, j.FinalDoneSeq
		j.FinalDoneAt, j.FinalDoneSeq = now, seq
		if c.st.saveLocked() != nil {
			j.FinalDoneAt, j.FinalDoneSeq = priorAt, priorSeq
		}
		return false
	}
	return !j.FinalDoneAt.After(now) && now.Sub(j.FinalDoneAt) >= 2*time.Minute
}
