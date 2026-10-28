package main

import (
	"context"
	"encoding/json"
	"log"
	"reflect"
	"time"
)

const (
	capacityRetryLimit    = 5
	capacityRetryInitial  = 15 * time.Second
	capacityRetryMaximum  = 4 * time.Minute
	capacityResumeTimeout = 30 * time.Second
	recoveryWaiting       = "waiting"
	recoveryResuming      = "resuming"
	recoveryWatching      = "watching"
	recoveryExhausted     = "exhausted"
)

// Attempts and the next wake survive restart, including an uncertain resume write.
// Attempts are bounded for the lifetime of this run, not reset on every overload.
type goalRecoveryObservation struct {
	At            time.Time `json:"at"`
	StateSeq      uint64    `json:"state_seq"`
	CompletionSeq uint64    `json:"completion_seq"`
	Status        string    `json:"status"`
}

type goalRecovery struct {
	WatchUntil    time.Time `json:"watch_until,omitempty"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at,omitempty"`
	TurnID        string    `json:"turn_id,omitempty"`
	State         string    `json:"state,omitempty"`
	Reason        string    `json:"reason,omitempty"`
}

func capacityRetryDelay(attempts int) time.Duration {
	delay := capacityRetryInitial
	for i := 0; i < attempts && delay < capacityRetryMaximum; i++ {
		delay *= 2
	}
	if delay > capacityRetryMaximum {
		return capacityRetryMaximum
	}
	return delay
}

func capacityRecoveryOwns(j *Job) bool {
	return j != nil && j.GoalRecovery != nil && j.GoalRecovery.State != ""
}

// Read only the newest native turn, distinguishing provider capacity from quota,
// manual blockers and text that merely quotes an error. No screen input is read.
func (p *goalRPC) capacityTurn() (string, string, bool, error) {
	raw, err := p.request("thread/turns/list", map[string]any{"threadId": p.thread, "limit": 1, "sortDirection": "desc", "itemsView": "notLoaded"})
	if err != nil {
		return "", "", false, err
	}
	var response struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Code json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
		} `json:"data"`
	}
	if decodeNativeJSON(raw, &response) != nil || len(response.Data) != 1 {
		return "", "", false, goalError("goal-turn-source-unavailable")
	}
	turn := response.Data[0]
	if !privateNativeID(turn.ID) || !p.nativeTurnAgrees(turn.ID, turn.Status) {
		return "", "", false, goalError("goal-turn-source-unavailable")
	}
	switch turn.Status {
	case "failed", "completed", "interrupted", "inProgress":
	default:
		return "", "", false, goalError("goal-turn-source-unavailable")
	}
	code := ""
	if turn.Error != nil {
		_ = json.Unmarshal(turn.Error.Code, &code)
	}
	return turn.ID, turn.Status, turn.Status == "failed" && code == "serverOverloaded", nil
}

// A narrow resume operation preserves the objective, budget and accumulated usage.
func resumeCapacityGoal(p *goalRPC, before *nativeGoal, intent goalIntent) error {
	if !sameGoalIntent(before, intent) || before.Status != "blocked" {
		return goalError("goal-ownership-mismatch")
	}
	after, err := p.set(map[string]any{"threadId": p.thread, "status": "active", "origin": "automatic"}, intent, "active")
	if err != nil {
		return err
	}
	if after.CreatedAt != before.CreatedAt || after.TokensUsed < before.TokensUsed || after.SecondsUsed < before.SecondsUsed || after.UpdatedAt < before.UpdatedAt {
		return goalError("goal-accounting-regressed")
	}
	read, err := p.get()
	if err != nil {
		return err
	}
	if !sameGoalIntent(read, intent) || read.Status != "active" || read.CreatedAt != before.CreatedAt || read.TokensUsed < after.TokensUsed || read.SecondsUsed < after.SecondsUsed {
		return goalError("goal-readback-mismatch")
	}
	return nil
}

func (c *Coord) saveGoalRecovery(j *Job, next goalRecovery) error {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	if c.st.Jobs[j.Issue] != j {
		return goalError("goal-dispatch-binding-invalid")
	}
	if _, fenced := c.st.CompletedRuns[j.Issue]; fenced {
		return goalError("goal-dispatch-binding-invalid")
	}
	prior := j.GoalRecovery
	j.GoalRecovery = &next
	if err := c.st.saveLocked(); err != nil {
		j.GoalRecovery = prior
		return goalError("goal-state-persistence-failed")
	}
	return nil
}

// Called only by daemon supervision/completion, never an application read.
// The durable attempt reservation precedes every native write. A changed goal,
// turn, receipt or occupant after that checkpoint cannot authorize a resume.
func (c *Coord) recoverCapacityWithPort(ctx context.Context, j *Job, p *goalRPC, now time.Time) (bool, error) {
	if c.dry || j == nil || j.Agent != "codex" || j.NativeGoal == nil || !j.NativeGoal.Owned || !deliveryConfirmed(j) || c.st == nil || (!j.Deadline.IsZero() && !now.Before(j.Deadline)) || j.RemoteCleanup != "" {
		return false, nil
	}
	owned := capacityRecoveryOwns(j)
	goal, err := p.get()
	if err != nil {
		return owned, err
	}
	if !sameGoalIntent(goal, j.NativeGoal.Intent) {
		return owned, goalError("goal-ownership-mismatch")
	}
	if goal.Status != "blocked" && !(owned && goal.Status == "active") {
		if owned {
			next := *j.GoalRecovery
			next.State = ""
			next.NextAttemptAt = time.Time{}
			if err := c.saveGoalRecovery(j, next); err != nil {
				return true, err
			}
		}
		return false, nil // pauses, budget/usage limits and completion are never resumed
	}
	turn, status, capacity, err := p.capacityTurn()
	if err != nil {
		return owned, err
	}
	if !capacity {
		if owned {
			next := *j.GoalRecovery
			next.State = ""
			next.NextAttemptAt = time.Time{}
			if err := c.saveGoalRecovery(j, next); err != nil {
				return true, err
			}
		}
		return false, nil
	}
	next := goalRecovery{}
	if j.GoalRecovery != nil {
		next = *j.GoalRecovery
	}
	if next.Attempts < 0 || next.Attempts > capacityRetryLimit {
		return true, goalError("goal-recovery-state-invalid")
	}
	if !p.canonical {
		return false, goalError("goal-capacity-canonical-required")
	}
	if goal.Status == "active" {
		// A successful status write must produce native progress within one persisted
		// deadline. Polling cannot keep extending an unproven start indefinitely.
		if next.WatchUntil.IsZero() || !now.Before(next.WatchUntil) {
			return true, c.exhaustCapacityRecovery(j, next, "goal-capacity-resume-unproven")
		}
		next.State = recoveryWatching
		next.NextAttemptAt = next.WatchUntil
		return true, c.saveGoalRecovery(j, next)
	}
	if status != "failed" {
		return false, nil
	}
	if next.State == recoveryExhausted {
		return true, nil
	}
	if next.TurnID != turn || next.State == "" || next.State == recoveryWatching {
		next.TurnID = turn
		next.State = recoveryWaiting
		next.NextAttemptAt = now.Add(capacityRetryDelay(next.Attempts))
		next.Reason = "goal-capacity-backoff"
		return true, c.saveGoalRecovery(j, next)
	}
	if now.Before(next.NextAttemptAt) {
		return true, nil
	}
	if next.Attempts >= capacityRetryLimit {
		return true, c.exhaustCapacityRecovery(j, next, "goal-capacity-retry-exhausted")
	}
	// Reserve before the effect; restart or a lost acknowledgement cannot replay it early.
	next.Attempts++
	next.WatchUntil = now.Add(capacityResumeTimeout)
	next.State = recoveryResuming
	next.NextAttemptAt = now.Add(capacityRetryDelay(next.Attempts))
	if err := c.saveGoalRecovery(j, next); err != nil {
		return true, err
	}
	receipt, native, err := loadGoalReceipt(c.cfg.Matrix.ReceiptRoot, j, c.cfg.Inbox, nil)
	if err != nil || native != p.thread || receipt.dispatch.Host != j.Host || receipt.dispatch.Location == nil || receipt.dispatch.Location.PaneID != j.Pane || receipt.dispatch.Location.WorkspaceID != j.Workspace {
		return true, goalError("goal-dispatch-binding-invalid")
	}
	// Re-read after the disk checkpoint, so a manual pause or newer turn wins.
	current, err := p.get()
	if err != nil {
		return true, err
	}
	id, _, stillCapacity, err := p.capacityTurn()
	if err != nil {
		return true, err
	}
	if !reflect.DeepEqual(goal, current) || id != turn || !stillCapacity || ctx.Err() != nil {
		return true, goalError("goal-changed-concurrently")
	}
	if j.RemoteControl != nil {
		host, ok := c.hosts[j.Host]
		if !ok || !c.checkRemoteHook(ctx, host.forJob(j), j) {
			return true, goalError("goal-dispatch-binding-invalid")
		}
	}
	verified, currentID, err := loadGoalReceipt(c.cfg.Matrix.ReceiptRoot, j, c.cfg.Inbox, nil)
	if err != nil || currentID != native || !reflect.DeepEqual(receipt.dispatch, verified.dispatch) || ctx.Err() != nil {
		return true, goalError("goal-dispatch-binding-invalid")
	}
	err = resumeCapacityGoal(p, current, j.NativeGoal.Intent)
	next.State = recoveryWaiting
	next.Reason = closedGoalReason(err)
	if err == nil {
		next.State = recoveryWatching
		next.NextAttemptAt = next.WatchUntil
		next.Reason = ""
	}
	if saved := c.saveGoalRecovery(j, next); saved != nil {
		return true, saved
	}
	return true, err
}

func (c *Coord) exhaustCapacityRecovery(j *Job, next goalRecovery, reason string) error {
	next.State = recoveryExhausted
	next.NextAttemptAt = time.Time{}
	next.Reason = reason
	if err := c.saveGoalRecovery(j, next); err != nil {
		return err
	}
	log.Printf("issue #%d: native capacity recovery stopped after %d attempts; reason=%s", j.Issue, next.Attempts, reason)
	c.notify("Native capacity recovery stopped; the managed run requires inspection")
	return nil
}

func capacityObservationChanged(j *Job, ref agentRef) bool {
	prior := j.GoalRecoveryObservation
	return prior == nil || prior.StateSeq != ref.StateChangeSeq || prior.CompletionSeq != ref.CompletionSeq || prior.Status != ref.Status
}

func (c *Coord) markCapacityObservation(j *Job, ref agentRef, now time.Time) bool {
	if j == nil || !rcCodex(j) || j.NativeGoal == nil || !j.NativeGoal.Owned || !deliveryConfirmed(j) {
		return true
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	if c.st.Jobs[j.Issue] != j {
		return false
	}
	prior := j.GoalRecoveryObservation
	j.GoalRecoveryObservation = &goalRecoveryObservation{At: now, StateSeq: ref.StateChangeSeq, CompletionSeq: ref.CompletionSeq, Status: ref.Status}
	if c.st.saveLocked() != nil {
		j.GoalRecoveryObservation = prior
		return false
	}
	return true
}

func (c *Coord) postponeCapacityObservation(j *Job, now time.Time) {
	if !capacityRecoveryOwns(j) || j.GoalRecovery.State == recoveryExhausted {
		return
	}
	next := *j.GoalRecovery
	if next.State == recoveryWatching && (next.WatchUntil.IsZero() || !now.Before(next.WatchUntil)) {
		_ = c.exhaustCapacityRecovery(j, next, "goal-capacity-resume-unproven")
		return
	}
	next.NextAttemptAt = now.Add(capacityRetryDelay(next.Attempts))
	if next.State == recoveryWatching && !next.WatchUntil.IsZero() && next.WatchUntil.Before(next.NextAttemptAt) {
		next.NextAttemptAt = next.WatchUntil
	}
	_ = c.saveGoalRecovery(j, next)
}

func (c *Coord) recoverCapacityStall(ctx context.Context, h Host, j *Job, ref agentRef, known bool) bool {
	if c.dry || j == nil || j.Agent != "codex" || j.NativeGoal == nil || !j.NativeGoal.Owned || !known || !jobRefMatches(ref, j) {
		return false
	}
	owned := capacityRecoveryOwns(j)
	if !rcCodex(j) {
		// A separate temporary app-server cannot drive the live TUI's turn loop.
		c.st.mu.Lock()
		if j.NativeGoal.Reason != "goal-capacity-canonical-required" {
			j.NativeGoal.Reason = "goal-capacity-canonical-required"
			_ = c.st.saveLocked()
			log.Printf("issue #%d: native capacity recovery unavailable; reason=goal-capacity-canonical-required", j.Issue)
		}
		c.st.mu.Unlock()
		if owned && j.GoalRecovery.State != recoveryExhausted {
			_ = c.exhaustCapacityRecovery(j, *j.GoalRecovery, "goal-capacity-canonical-required")
		}
		return owned
	}
	if ref.Status != "idle" && ref.Status != "done" && ref.Status != "blocked" && !owned {
		return false
	}
	now := time.Now()
	changed := capacityObservationChanged(j, ref)
	if owned {
		if j.GoalRecovery.State == recoveryExhausted {
			return true
		}
		if now.Before(j.GoalRecovery.NextAttemptAt) && !changed {
			return true
		}
	} else if !changed && now.Sub(j.GoalRecoveryObservation.At) < fleetRecoveryEvery {
		return false
	}
	if !c.markCapacityObservation(j, ref, now) {
		return owned
	}
	native, err := readGoalIdentity(c.cfg.Matrix.ReceiptRoot, j, c.cfg.Inbox)
	if err != nil {
		c.postponeCapacityObservation(j, now)
		return owned
	}
	check, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	owns := owned
	err = h.withGoalConnection(check, native, func(p *goalRPC) error {
		var err error
		owns, err = c.recoverCapacityWithPort(check, j, p, now)
		return err
	})
	if err != nil {
		c.postponeCapacityObservation(j, now)
	}
	return owns
}

// The recovery timer services only due recoveries; it never polls GitHub.
func (c *Coord) capacityRecoveryPass(ctx context.Context) {
	status, _ := c.fleetStatus(ctx)
	c.st.mu.Lock()
	jobs := map[int]*Job{}
	for n, j := range c.st.Jobs {
		if capacityRecoveryOwns(j) && j.GoalRecovery.State != recoveryExhausted {
			jobs[n] = j
		}
	}
	c.st.mu.Unlock()
	for n, j := range jobs {
		if time.Now().Before(j.GoalRecovery.NextAttemptAt) {
			continue
		}
		host, ok := c.hosts[j.Host]
		ref, known := status[n]
		if !ok || !known || !jobRefMatches(ref, j) {
			c.postponeCapacityObservation(j, time.Now())
			continue
		}
		host = host.forJob(j)
		if !c.checkRemoteHook(ctx, host, j) {
			c.postponeCapacityObservation(j, time.Now())
			continue
		}
		c.recoverCapacityStall(ctx, host, j, ref, known)
	}
}

// Zero disables the timer when no recovery owns a future wake.
func (c *Coord) nextGoalRecoveryDelay(now time.Time) time.Duration {
	delay := time.Duration(0)
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	for _, j := range c.st.Jobs {
		if !capacityRecoveryOwns(j) || j.GoalRecovery.State == recoveryExhausted || j.GoalRecovery.NextAttemptAt.IsZero() {
			continue
		}
		next := j.GoalRecovery.NextAttemptAt.Sub(now)
		if next <= 0 {
			next = time.Millisecond
		}
		if delay == 0 || next < delay {
			delay = next
		}
	}
	return delay
}
