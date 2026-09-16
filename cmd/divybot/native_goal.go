package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type goalIntent struct {
	Objective   string `json:"objective"`
	TokenBudget *int64 `json:"tokenBudget"`
}
type dispatchGoal struct {
	ReceiptKey          string     `json:"receiptKey"`
	Intent              goalIntent `json:"intent"`
	Owned               bool       `json:"owned"`
	Reason              string     `json:"reason,omitempty"`
	LastStatus          string     `json:"lastStatus,omitempty"`
	UpdatedNotification bool       `json:"updatedNotificationObserved"`
}

var emptyGoalBudgetKey = regexp.MustCompile(`^max[-_]tokens\s*:$`)

var goalBudgetPattern = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?([kKmM]?)$`)

func parseGoalBudget(value string, present bool) (*int64, error) {
	if !present && value == "" {
		return nil, nil
	}
	if len(value) > 128 {
		return nil, goalError("goal-budget-invalid")
	}
	m := goalBudgetPattern.FindStringSubmatch(value)
	if m == nil {
		return nil, goalError("goal-budget-invalid")
	}
	n, _ := new(big.Int).SetString(m[1]+m[2], 10)
	scale := int64(1)
	switch strings.ToLower(m[3]) {
	case "k":
		scale = 1000
	case "m":
		scale = 1000000
	}
	n.Mul(n, big.NewInt(scale))
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(m[2]))), nil)
	remainder := new(big.Int)
	n.QuoRem(n, den, remainder)
	if remainder.Sign() != 0 || !n.IsInt64() || !goalNumber(n.Int64()) {
		return nil, goalError("goal-budget-invalid")
	}
	result := n.Int64()
	return &result, nil
}
func validGoalObjective(s string) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 4000 && !strings.ContainsRune(s, 0)
}
func nativeGoalIntent(repo string, is Issue, o Overrides) (goalIntent, error) {
	budget, e := parseGoalBudget(o.MaxTokens, o.MaxTokensPresent)
	if e != nil {
		return goalIntent{}, e
	}
	objective := fmt.Sprintf("%s#%d: %s", repo, is.Number, is.Title)
	if is.Number <= 0 || !repositoryName.MatchString(repo) || strings.TrimSpace(is.Title) == "" || !validGoalObjective(objective) {
		return goalIntent{}, goalError("goal-objective-invalid")
	}
	return goalIntent{objective, budget}, nil
}

// The ID comes only from the official integration report for the registered
// occupant. Location is a correlation guard, never the identity being recorded.
func acquireNativeGoalBinding(ctx context.Context, r *durableMatrixReceipt, j *Job, read func() (json.RawMessage, error), wait func(context.Context) bool) (string, error) {
	if r == nil || r.dispatch == nil || r.dispatch.State != "dispatched" || r.dispatch.Source != "codex" || r.dispatch.Issue.Number != j.Issue || j.Agent != "codex" || r.dispatch.Location == nil || r.dispatch.Location.PaneID != j.Pane || r.dispatch.Location.WorkspaceID != j.Workspace {
		return "", goalError("goal-dispatch-binding-invalid")
	}
	for attempt := 0; attempt < 40; attempt++ {
		raw, e := read()
		if e != nil {
			return "", goalError("goal-identity-source-unavailable")
		}
		id, reason := nativeSessionFromResponse(raw, "agent_info", "codex", j.Label, r.dispatch.Location)
		if reason == "" {
			if e := r.writeNativeIdentity(&id); e != nil {
				return "", goalError("goal-binding-persistence-failed")
			}
			return id, nil
		}
		if reason != nativeUnavailable {
			return "", goalError(reason)
		}
		if !wait(ctx) {
			return "", goalError("native-session-unavailable")
		}
	}
	return "", goalError("native-session-unavailable")
}
func goalWait(ctx context.Context) bool {
	t := time.NewTimer(250 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func (h Host) bindDispatchGoal(ctx context.Context, r *durableMatrixReceipt, j *Job) (string, error) {
	return acquireNativeGoalBinding(ctx, r, j, func() (json.RawMessage, error) {
		out, e := h.herdr(ctx, "agent", "get", j.Pane)
		if e != nil {
			return nil, e
		}
		return herdrUnwrap(out)
	}, goalWait)
}

func readGoalIdentity(root string, j *Job, inbox string) (string, error) {
	if j.NativeGoal == nil || !digestPattern.MatchString(j.NativeGoal.ReceiptKey) || !privateReceiptRoot(root) {
		return "", goalError("goal-binding-unavailable")
	}
	reservation := filepath.Join(root, j.NativeGoal.ReceiptKey)
	record := filepath.Join(reservation, "record")
	for _, dir := range []string{reservation, record} {
		s, e := os.Lstat(dir)
		if e != nil || !s.IsDir() || s.Mode().Perm() != 0700 {
			return "", goalError("goal-binding-invalid")
		}
	}
	read := func(name string, out any) error {
		p := filepath.Join(record, name)
		s, e := os.Lstat(p)
		if e != nil || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || s.Size() > 1048576 {
			return goalError("goal-binding-invalid")
		}
		b, e := os.ReadFile(p)
		if e != nil || decodeNativeJSON(b, out) != nil {
			return goalError("goal-binding-invalid")
		}
		return nil
	}
	var d dispatchBinding
	if e := read("dispatch.json", &d); e != nil {
		return "", e
	}
	if d.SchemaVersion != 1 || d.State != "dispatched" || d.Source != "codex" || d.Issue.Repo != inbox || d.Issue.Number != j.Issue || d.RunID != "orchid-"+j.NativeGoal.ReceiptKey || d.ParentRunID != nil {
		return "", goalError("goal-dispatch-binding-invalid")
	}
	var b struct {
		NativeSessionID string
		Repo            string
	}
	if e := read("binding.json", &b); e != nil {
		return "", e
	}
	if b.Repo != j.Repo || !privateNativeID(b.NativeSessionID) {
		return "", goalError("goal-identity-unavailable")
	}
	return b.NativeSessionID, nil
}
func createDispatchGoal(p *goalRPC, intent goalIntent) error {
	old, e := p.get()
	if e != nil {
		return e
	}
	if old != nil {
		return goalError("goal-already-exists")
	}
	_, e = p.set(map[string]any{"threadId": p.thread, "objective": intent.Objective, "tokenBudget": intent.TokenBudget, "status": "active"}, intent, "active")
	return e
}
func transitionDispatchGoal(p *goalRPC, intent goalIntent, status string) (bool, error) {
	if status != "complete" && status != "blocked" && status != "paused" {
		return false, goalError("goal-transition-refused")
	}
	old, e := p.get()
	if e != nil {
		return false, e
	}
	if !sameGoalIntent(old, intent) {
		return false, goalError("goal-ownership-mismatch")
	}
	if old.Status == status || old.Status == "complete" {
		return false, nil
	}
	if status != "complete" && (old.Status == "budgetLimited" || old.Status == "usageLimited" || old.Status == "paused") {
		return false, nil
	}
	if status == "blocked" && old.Status != "active" {
		return false, nil
	}
	next, e := p.set(map[string]any{"threadId": p.thread, "status": status}, intent, status)
	if e != nil {
		return false, e
	}
	if next.CreatedAt != old.CreatedAt || next.TokensUsed < old.TokensUsed || next.SecondsUsed < old.SecondsUsed {
		return false, goalError("goal-accounting-regressed")
	}
	return true, nil
}
func (c *Coord) startDispatchGoal(ctx context.Context, host Host, j *Job, r *durableMatrixReceipt) {
	if j.NativeGoal == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	id, e := host.bindDispatchGoal(ctx, r, j)
	if e == nil {
		e = host.withGoalConnection(ctx, id, func(p *goalRPC) error { return createDispatchGoal(p, j.NativeGoal.Intent) })
	}
	c.st.mu.Lock()
	if e == nil {
		j.NativeGoal.Owned = true
		j.NativeGoal.LastStatus = "active"
		j.NativeGoal.UpdatedNotification = true
		j.NativeGoal.Reason = ""
	} else {
		j.NativeGoal.Reason = closedGoalReason(e)
	}
	saved := c.st.saveLocked()
	c.st.mu.Unlock()
	if saved != nil {
		j.NativeGoal.Owned = false
		j.NativeGoal.Reason = "goal-state-persistence-failed"
	}
	if j.NativeGoal.Reason != "" {
		log.Printf("issue #%d: native goal INCONCLUSIVE reason=%s", j.Issue, j.NativeGoal.Reason)
	} else {
		log.Printf("issue #%d: native goal active; updated notification observed", j.Issue)
	}
}
func closedGoalReason(e error) string {
	if v, ok := e.(goalError); ok {
		return string(v)
	}
	return "goal-source-unavailable"
}
func (c *Coord) transitionGoal(ctx context.Context, j *Job, status string) {
	if c.dry || j.NativeGoal == nil || !j.NativeGoal.Owned {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	id, e := readGoalIdentity(c.cfg.Matrix.ReceiptRoot, j, c.cfg.Inbox)
	var changed bool
	if e == nil {
		host, ok := c.hosts[j.Host]
		if !ok {
			e = goalError("goal-host-unavailable")
		} else {
			e = host.withGoalConnection(ctx, id, func(p *goalRPC) error {
				var updateErr error
				changed, updateErr = transitionDispatchGoal(p, j.NativeGoal.Intent, status)
				return updateErr
			})
		}
	}
	if e != nil {
		log.Printf("issue #%d: native goal transition INCONCLUSIVE reason=%s", j.Issue, closedGoalReason(e))
		return
	}
	if changed {
		c.st.mu.Lock()
		j.NativeGoal.LastStatus = status
		j.NativeGoal.UpdatedNotification = true
		_ = c.st.saveLocked()
		c.st.mu.Unlock()
	}
}
func assignmentGoalStatus(state, reason string) string {
	if state != "CLOSED" {
		return ""
	}
	switch reason {
	case "COMPLETED":
		return "complete"
	case "NOT_PLANNED":
		return "paused"
	}
	return ""
}
func (c *Coord) finishAssignmentGoal(ctx context.Context, j *Job) {
	if c.dry || j.NativeGoal == nil || !j.NativeGoal.Owned {
		return
	}
	var issue struct {
		State  string `json:"state"`
		Reason string `json:"stateReason"`
	}
	if ghJSON(ctx, &issue, "issue", "view", fmt.Sprint(j.Issue), "--repo", c.cfg.Inbox, "--json", "state,stateReason") != nil {
		log.Printf("issue #%d: native goal transition INCONCLUSIVE reason=goal-inbox-unavailable", j.Issue)
		return
	}
	if status := assignmentGoalStatus(issue.State, issue.Reason); status != "" {
		c.transitionGoal(ctx, j, status)
	}
}
