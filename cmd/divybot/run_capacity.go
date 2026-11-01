package main

import (
	"context"
	"errors"
	"log"
	"maps"
	"reflect"
	"slices"
	"time"
)

const (
	runCapacityLive       = "live"
	runCapacityDone       = "done"
	runCapacityUnknown    = "unconfirmed"
	runCapacityReadBudget = 8 * time.Second
)

// Capacity observations are deliberately not serialized: restart is unconfirmed.
// A warm done run retains its job, source binding, workspace and PR supervision.
type runCapacityObservation struct {
	Kind      string
	Native    string
	Key       string
	Ref       agentRef
	At        time.Time
	CheckedAt time.Time
}

func nativeCapacityDone(j *Job, now time.Time) bool {
	if j == nil || j.RemoteControl == nil || j.CapacityObservation == nil ||
		capacityRecoveryOwns(j) || j.RemoteCleanup != "" || j.RemoteStopOperation != "" || !deliveryConfirmed(j) {
		return false
	}
	o := j.CapacityObservation
	return o.Kind == runCapacityDone && o.Key == j.DispatchKey && o.Native == j.RemoteControl.NativeSessionID &&
		!o.At.After(now) && now.Sub(o.At) < fleetRecoveryEvery
}

func nativeCapacityDoneForRef(j *Job, ref agentRef, known bool) bool {
	return nativeCapacityDone(j, time.Now()) && known && jobRefMatches(ref, j) &&
		(ref.Status == "idle" || ref.Status == "done") && ref.StateChangeSeq == j.CapacityObservation.Ref.StateChangeSeq &&
		ref.CompletionSeq == j.CapacityObservation.Ref.CompletionSeq
}

// Reuse the tick's existing fleet snapshot; neither governor nor admission probes.
func (c *Coord) refreshRunCapacity(status map[int]agentRef) {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	for n, j := range c.st.Jobs {
		ref, known := status[n]
		if nativeCapacityDoneForRef(j, ref, known) {
			continue
		}
		checked := time.Time{}
		if old := j.CapacityObservation; old != nil && old.Ref == ref {
			checked = old.CheckedAt
		}
		kind := runCapacityUnknown
		if known && jobRefMatches(ref, j) {
			kind = runCapacityLive
		}
		j.CapacityObservation = &runCapacityObservation{Kind: kind, Ref: ref, At: time.Now(), CheckedAt: checked}
	}
}

// PR-pending completion must be read-only: never freeze or post a final report.
func (c *Coord) capacityCompletionEvidence(ctx context.Context, h Host, j *Job, native string) (bool, error) {
	if j.Agent == "codex" {
		complete := false
		err := h.withCanonicalConnection(ctx, native, func(p *goalRPC) error {
			if err := p.remoteWorkIdle(false); err != nil {
				return err
			}
			var err error
			complete, err = p.lastTurnCompleted()
			if err != nil || !complete {
				return err
			}
			goal, err := p.get()
			if err != nil {
				return err
			}
			complete = completionGoalAllowsRetirement(j, goal)
			return p.reconcileNativeLifecycle()
		})
		return complete && err == nil, err
	}
	if j.FinalReportManaged {
		_, scope, err := c.finalScope(j)
		if err != nil || c.finalProof(ctx, h, j, scope) != nil {
			return false, errFinalPublication
		}
		report, err := c.finalReport(ctx, h, scope)
		if err != nil {
			return false, err
		}
		if _, err = finalRenderedBody(report, scope.Key); err != nil {
			return false, err
		}
		return c.finalProof(ctx, h, j, scope) == nil && ctx.Err() == nil, nil
	}
	return c.completionEvidence(ctx, h, j, native) // legacy Claude: read existing comments only
}

func (c *Coord) observeRemoteDone(ctx context.Context, h Host, j *Job, ref agentRef) {
	if j.RemoteControl == nil || !deliveryConfirmed(j) || capacityRecoveryOwns(j) ||
		!jobRefMatches(ref, j) || (ref.Status != "idle" && ref.Status != "done") {
		return
	}
	c.st.mu.Lock()
	old := j.CapacityObservation
	if old != nil && old.Ref == ref && time.Since(old.CheckedAt) < fleetRecoveryEvery {
		c.st.mu.Unlock()
		return
	}
	j.CapacityObservation = &runCapacityObservation{Kind: runCapacityUnknown, Ref: ref, At: time.Now(), CheckedAt: time.Now()}
	c.st.mu.Unlock()
	check, cancel := context.WithTimeout(ctx, runCapacityReadBudget)
	defer cancel()
	before, id, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, j.Agent)
	if err != nil || id != j.RemoteControl.NativeSessionID {
		return
	}
	agents, err := c.actionList(check, h)
	first, valid := completionOccupant(agents, j, id)
	if err != nil || !valid || first.StateChangeSeq != ref.StateChangeSeq || first.CompletionSeq != ref.CompletionSeq {
		return
	}
	complete, err := c.capacityCompletionEvidence(check, h, j, id)
	if err != nil || !complete {
		return
	}
	agents, err = c.actionList(check, h)
	second, valid := completionOccupant(agents, j, id)
	after, current, bindingErr := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, j.Agent)
	if err != nil || !valid || first.StateChangeSeq != second.StateChangeSeq || completionSequence(first) != completionSequence(second) ||
		bindingErr != nil || current != id || !reflect.DeepEqual(before.dispatch, after.dispatch) ||
		!c.checkRemoteHook(check, h, j) || check.Err() != nil {
		return
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	if c.st.Jobs[j.Issue] != j {
		return
	}
	j.CapacityObservation = &runCapacityObservation{Kind: runCapacityDone, Key: j.DispatchKey, Native: id, Ref: ref, At: time.Now(), CheckedAt: time.Now()}
	if old == nil || old.Kind != runCapacityDone {
		log.Printf("issue #%d: capacity released evidence=native-assignment-complete; warm run retained", j.Issue)
	}
}

type runCapacityCounts struct{ Live, Done, Unconfirmed int }

func (n runCapacityCounts) active() int { return n.Live + n.Unconfirmed }

// One account counter includes deduplicated in-flight launch reservations.
// Physical host residency and non-RC/OpenCode admission semantics are unchanged.
func (c *Coord) runCapacityCountsLocked() map[string]runCapacityCounts {
	out := map[string]runCapacityCounts{}
	keys := map[string]bool{}
	for n, j := range c.st.Jobs {
		a := accountKey(j.Agent)
		v := out[a]
		_, cleaning := c.st.CompletedRuns[n]
		switch {
		case !cleaning && nativeCapacityDone(j, time.Now()):
			v.Done++
		case j.CapacityObservation != nil && j.CapacityObservation.Kind == runCapacityLive && time.Since(j.CapacityObservation.At) < fleetRecoveryEvery:
			v.Live++
		default:
			v.Unconfirmed++
		}
		out[a] = v
		if j.DispatchKey != "" {
			keys[j.DispatchKey] = true
		}
	}
	c.attemptsMu.Lock()
	defer c.attemptsMu.Unlock()
	for key, attempt := range c.attempts {
		if !keys[key] {
			v := out[attempt.account]
			v.Unconfirmed++
			out[attempt.account] = v
		}
	}
	return out
}

var errRunCapacity = errors.New("run-capacity-unavailable")

func cloneTracker(t tracker) tracker {
	t.Reviews = slices.Clone(t.Reviews)
	t.Comments = slices.Clone(t.Comments)
	t.IssueComments = slices.Clone(t.IssueComments)
	t.Checks = maps.Clone(t.Checks)
	return t
}

// All coordinator continuation input reacquires a freed account slot first.
// The cache is in-memory only, so a restart can never resurrect the free slot.
func (c *Coord) acquireRunCapacity(h Host, pane string) error {
	if c.st == nil {
		return nil
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	for _, j := range c.st.Jobs {
		if j.Host != h.Name || j.Pane != pane || !sameHerdrScope(j.HerdrScope, h.herdrScope()) || !nativeCapacityDone(j, time.Now()) {
			continue
		}
		a := accountKey(j.Agent)
		cap := c.cfg.Governor.MaxActive
		if paced, ok := c.st.PrevCap[a]; ok {
			cap = min(cap, paced)
		}
		if c.runCapacityCountsLocked()[a].active() >= cap {
			return errRunCapacity
		}
		j.CapacityObservation = nil // reserve before sending, even if delivery later fails
	}
	return nil
}

// A PR relay can reacquire capacity after admission took its tick snapshot.
// Clamp again at the common launch boundary, including action retries.
func (c *Coord) reconcileRunCapacityBudget(budget map[string]int) {
	if c.st == nil || c.cfg == nil {
		return
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	status := map[int]agentRef{}
	for n, j := range c.st.Jobs {
		if o := j.CapacityObservation; o != nil && time.Since(o.At) < fleetRecoveryEvery {
			status[n] = o.Ref
		}
	}
	running := c.admissionRunningLocked(status)
	for _, a := range []string{"codex", "claude"} {
		if cap, computed := c.st.PrevCap[a]; computed {
			budget[a] = min(budget[a], max(0, cap-running[a]))
		}
	}
}

// Admission retains its existing non-RC and provider accounting. The same
// predicate is used for the tick snapshot and its launch-boundary recheck.
func (c *Coord) admissionRunningLocked(status map[int]agentRef) map[string]int {
	running := map[string]int{}
	for n, j := range c.st.Jobs {
		ref, known := status[n]
		_, completing := c.st.CompletedRuns[n]
		if completing || occupiesAdmissionSlot(j, ref, known) {
			running[accountKey(j.Agent)]++
		}
	}
	return running
}
