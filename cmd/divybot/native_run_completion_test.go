package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Drives the production completion path end to end: retireCompleted persists
// the fence, observes paired absence and removes the job; the source binding
// is then pruned and the process restarts from disk. The lookup must still
// return the identity captured at completion, never an inbox fallback.
func TestNativeRunRetireCompletedRetainsIdentityAcrossRestart(t *testing.T) {
	c, j, seatGone, processGone, closes := completionFixture(t)
	scope := &herdrScope{Session: "dispatch", Machine: "fixture-machine"}
	source := dispatchIssue{Repo: "example/source", Number: 3}
	j.Label, j.HerdrScope, j.NativeSessionID, j.Home = "codex-7", scope, "thread-fixture", &source
	dispatchPath := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record", "dispatch.json")
	var dispatch dispatchBinding
	if readPrivateActionJSON(dispatchPath, &dispatch) != nil {
		t.Fatal("fixture dispatch")
	}
	dispatch.HerdrScope, dispatch.Issue = scope, source
	body, _ := json.Marshal(dispatch)
	if os.WriteFile(dispatchPath, body, 0600) != nil {
		t.Fatal("fixture dispatch")
	}
	// A comment dispatch: the source issue differs from the inbox issue.
	c.st.SourceBindings = map[int]*sourceBinding{7: {Repo: source.Repo, Issue: source.Number, Comment: 7}}
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	c.st.mu.Lock()
	active := c.st.nativeRunRecordLocked(7, j, c.cfg.Inbox)
	c.st.mu.Unlock()
	if active == nil {
		t.Fatal("fixture has no native identity before completion")
	}
	list := c.actions.list
	c.actions.list = func(ctx context.Context, h Host) ([]AgentInfo, error) {
		agents, err := list(ctx, h)
		for i := range agents {
			agents[i].HerdrScope = scope
		}
		return agents, err
	}
	ref := completionRef(j)
	ref.HerdrScope = scope
	if !c.retireCompleted(context.Background(), 7, j, ref, true) || *closes != 1 {
		t.Fatalf("completion did not close once: closes=%d", *closes)
	}
	*seatGone, *processGone = true, true
	c.retireCompleted(context.Background(), 7, j, ref, true)
	if c.st.Jobs[7] != nil || c.st.CompletedRuns[7].Phase != "observed" {
		t.Fatal("paired absence did not remove the job")
	}
	c.st.mu.Lock()
	delete(c.st.SourceBindings, 7)
	err := c.st.saveLocked()
	c.st.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c.st = loadState(c.st.path)
	if c.st.Jobs[7] != nil || c.st.SourceBindings[7] != nil {
		t.Fatal("restart restored pruned job or source binding")
	}
	c.st.nativeRuns = &nativeRunWatch{generation: "restarted", subs: map[chan struct{}]struct{}{}}
	c.st.nativeRunInbox = c.cfg.Inbox
	raw, err := c.st.nativeRunsLookup(json.RawMessage(`{"keys":[{"key":"7","label":"codex-7"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var got nativeRunLookup
	if json.Unmarshal(raw, &got) != nil || !got.Complete || len(got.Records) != 1 {
		t.Fatalf("persisted completion identity unavailable after restart: %s", raw)
	}
	r := got.Records[0]
	fence := c.st.CompletedRuns[7]
	if r.Phase != "completed" || r.CompletedAt == "" || r.CompletedAt != fence.NativeRun.CompletedAt || r.ObservedAt != r.CompletedAt {
		t.Fatal("completion time not the persisted fence time")
	}
	if r.Issue != source || r.SourceCommentID != 7 {
		t.Fatal("completion lost the pruned source binding")
	}
	if r.DispatchKey != active.DispatchKey || r.NativeSessionID != "thread-fixture" || r.Scope != active.Scope {
		t.Fatal("completion changed native association")
	}
	if r.Label != "codex-7" || r.Agent != "codex" || r.AgentID != active.AgentID || r.AssignmentID != active.AssignmentID {
		t.Fatal("completion changed run identity")
	}
}

// The split record and fence predicates each refuse their own drift.
func TestNativeRunSplitPredicatesRefuseDrift(t *testing.T) {
	c := nativeRunFixture(t)
	base := c.st.nativeRunRecordLocked(701, c.st.Jobs[701], c.cfg.Inbox)
	if base == nil || !validNativeRunRecord(*base) {
		t.Fatal("fixture record invalid")
	}
	for name, drift := range map[string]func(*nativeRunRecord){
		"machine-text":     func(r *nativeRunRecord) { r.Scope.Machine = "a/b" },
		"comment-key":      func(r *nativeRunRecord) { r.SourceCommentID = 702 },
		"active-completed": func(r *nativeRunRecord) { r.CompletedAt = stamp(time.Now()) },
	} {
		bad := *base
		drift(&bad)
		if validNativeRunRecord(bad) {
			t.Fatal("drifted record accepted", name)
		}
	}
	completed := *base
	completed.Phase, completed.ObservedAt = "completed", stamp(time.Now())
	completed.CompletedAt = completed.ObservedAt
	fence := completedRun{DispatchKey: base.DispatchKey, NativeSessionID: base.NativeSessionID, Phase: "observed"}
	if completedNativeRunReason(fence, &completed) != "" || completedNativeRunReason(fence, base) != "identity_mismatch" {
		t.Fatal("completion fence accepted a non-completed retained record")
	}
	if !activeMatchesCompletion(base, &completed, fence) {
		t.Fatal("same tracked run refused")
	}
	for name, drift := range map[string]func(*nativeRunRecord){
		"scope": func(r *nativeRunRecord) { r.Scope.Session = "other" },
		"label": func(r *nativeRunRecord) { r.Label = "codex-run-701" },
	} {
		active := *base
		drift(&active)
		if activeMatchesCompletion(&active, &completed, fence) {
			t.Fatal("tracked run drift accepted", name)
		}
	}
}
