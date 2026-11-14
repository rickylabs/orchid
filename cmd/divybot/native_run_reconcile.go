package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

type nativeEndEvidence struct {
	ReportAt time.Time
	ClosedAt time.Time
	Idle     bool
}

func provenNativeEnd(e nativeEndEvidence) bool {
	// guard:terminal-positive-business-evidence — idle or a turn ending alone is not an end.
	return !e.ReportAt.IsZero() && !e.ClosedAt.IsZero() && !e.ReportAt.After(time.Now()) && !e.ClosedAt.After(time.Now()) && e.Idle
}

func nativeEndRevision(r nativeRunRecord, j *Job) string {
	r.ObservedAt = "" // active lookups are sampled, not a change of assignment
	b, _ := json.Marshal(struct {
		Record nativeRunRecord
		Job    *Job
	}{r, j})
	return shaText(b)
}

// Read only the canonical source issue and the dispatcher-tracked PR. A final
// marker is authenticated by author, exact assignment and dispatch start time.
// Never call publishFinalReport, close an issue, or stop/unload native work here.
var terminalPRLink = regexp.MustCompile(`https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([1-9][0-9]{0,15})`)

func (c *Coord) readNativeEndEvidence(ctx context.Context, j *Job) (nativeEndEvidence, error) {
	if c.actions.endEvidence != nil {
		return c.actions.endEvidence(ctx, j)
	}
	var source struct {
		State    string `json:"state"`
		ClosedAt string `json:"closedAt"`
		Comments []struct {
			Body      string                 `json:"body"`
			CreatedAt string                 `json:"createdAt"`
			Author    struct{ Login string } `json:"author"`
		} `json:"comments"`
	}
	home := jobHome(c.cfg.Inbox, j)
	if err := ghJSON(ctx, &source, "issue", "view", fmt.Sprint(home.Number), "--repo", home.Repo,
		"--json", "state,closedAt,comments"); err != nil {
		return nativeEndEvidence{}, err
	}
	e := nativeEndEvidence{}
	reportBody := ""
	for _, comment := range source.Comments {
		if markedCompletionComment(comment.Body, comment.Author.Login, c.cfg.BotLogin, comment.CreatedAt, j.SpawnedAt, j.DispatchKey) {
			at, _ := time.Parse(time.RFC3339, comment.CreatedAt)
			if at.After(e.ReportAt) {
				e.ReportAt, reportBody = at, comment.Body
			}
		}
	}
	if e.ReportAt.IsZero() {
		return e, nil
	}
	if source.State == "CLOSED" {
		e.ClosedAt, _ = time.Parse(time.RFC3339, source.ClosedAt)
	}
	// pollPR used to erase a merged PR pointer. Recover only bounded links
	// from this exact authenticated report, never a branch/history scan.
	prs := map[string]int{}
	if j.PR != 0 {
		prs[j.Repo+"/"+strconv.Itoa(j.PR)] = j.PR
	}
	links := terminalPRLink.FindAllStringSubmatch(reportBody, 17)
	if len(links) > 16 {
		return e, goalError("terminal_report_bounds")
	}
	for _, link := range links {
		number, err := strconv.Atoi(link[2])
		if err != nil {
			return e, err
		}
		allowed := link[1] == j.Repo || link[1] == home.Repo
		for _, target := range c.cfg.Targets {
			allowed = allowed || target.Repo == link[1]
		}
		if !allowed {
			return e, goalError("terminal_report_scope_unavailable")
		}
		prs[link[1]+"/"+link[2]] = number
	}
	for key, number := range prs {
		repo := key[:len(key)-len(strconv.Itoa(number))-1]
		var pr struct {
			State    string `json:"state"`
			ClosedAt string `json:"closedAt"`
			MergedAt string `json:"mergedAt"`
		}
		if err := ghJSON(ctx, &pr, "pr", "view", strconv.Itoa(number), "--repo", repo,
			"--json", "state,closedAt,mergedAt"); err != nil {
			return e, err
		}
		if pr.State != "MERGED" && pr.State != "CLOSED" {
			return nativeEndEvidence{}, nil
		}
		at, _ := time.Parse(time.RFC3339, pr.ClosedAt)
		if pr.MergedAt != "" {
			at, _ = time.Parse(time.RFC3339, pr.MergedAt)
		}
		if at.IsZero() || at.After(time.Now()) {
			return e, goalError("terminal_source_clock_unavailable")
		}
		if at.After(e.ClosedAt) {
			e.ClosedAt = at
		}
	}
	if e.ClosedAt.IsZero() {
		return e, nil
	}
	cutoff := e.ClosedAt
	if e.ReportAt.After(cutoff) {
		cutoff = e.ReportAt
	}
	h, ok := c.hosts[j.Host]
	if !ok {
		return e, nil
	}
	h = h.forJob(j)
	if j.Agent == "claude" {
		e.Idle = h.claudeSessionEnded(ctx, j)
		return e, nil
	}
	if j.Agent != "codex" || !privateNativeID(j.NativeSessionID) {
		return e, nil // unsupported/unavailable runtime proof stays unknown
	}
	err := h.withCanonicalConnection(ctx, j.NativeSessionID, func(p *goalRPC) error {
		if err := p.remoteWorkIdle(true); err != nil {
			return err
		}
		// A later turn means the report predates newer work. This is an
		// exclusion fence, never positive completion evidence.
		raw, err := p.request("thread/turns/list", map[string]any{
			"threadId": p.thread, "limit": 1, "sortDirection": "desc", "itemsView": "full",
		})
		var turns struct {
			Data []struct {
				Status      string `json:"status"`
				CompletedAt *int64 `json:"completedAt"`
			} `json:"data"`
		}
		if err != nil || decodeNativeJSON(raw, &turns) != nil || len(turns.Data) != 1 {
			return goalError("terminal_work_unconfirmed")
		}
		if err := p.reconcileNativeLifecycle(); err != nil {
			return err
		}
		t := turns.Data[0]
		e.Idle = t.Status == "completed" && t.CompletedAt != nil && *t.CompletedAt > 0 &&
			!time.Unix(*t.CompletedAt, 0).After(cutoff)
		return nil
	})
	return e, err
}

type nativeEndResult struct {
	Key    string `json:"key"`
	State  string `json:"state"` // eligible | recorded | already_recorded | unknown
	Reason string `json:"reason"`
}

// The running dispatcher owns the apply fence. No offline process edits State.
// Both planning and execution use exact bounded keys and the same proof policy.
func (c *Coord) reconcileNativeEnd(ctx context.Context, q nativeRunQuery, revision string, apply bool) nativeEndResult {
	out := nativeEndResult{Key: q.Key, State: "unknown", Reason: "terminal_evidence_unavailable"}
	if !validNativeRunQuery(q) {
		return out
	}
	n, _ := strconv.Atoi(q.Key)
	c.st.mu.Lock()
	j := c.st.Jobs[n]
	ended, reason := c.st.terminalNativeRunLocked(n, j, c.cfg.Inbox)
	if ended != nil && reason == "" && ended.Label == q.Label {
		c.st.mu.Unlock()
		out.State, out.Reason = "already_recorded", "immutable_terminal"
		return out
	}
	record := c.st.nativeRunRecordLocked(n, j, c.cfg.Inbox)
	var frozen *Job
	if j != nil {
		b, _ := json.Marshal(j)
		frozen = &Job{}
		_ = json.Unmarshal(b, frozen)
	}
	var legacy *nativeRunRecord
	if fence, ok := c.st.CompletedRuns[n]; ok && fence.NativeRun != nil &&
		completedNativeRunReason(fence, fence.NativeRun) == "" && validNativeRunRecord(*fence.NativeRun) {
		copy := *fence.NativeRun
		legacy = &copy
	}
	c.st.mu.Unlock()
	if legacy != nil && record == nil {
		record = legacy
	}
	if reason != "" || record == nil || record.Label != q.Label ||
		(revision != "" && revision != nativeEndRevision(*record, frozen)) {
		out.Reason = "assignment_changed_or_unavailable"
		return out
	}
	if legacy != nil && !activeMatchesCompletion(record, legacy, completedRun{DispatchKey: legacy.DispatchKey, NativeSessionID: legacy.NativeSessionID}) {
		out.Reason = "assignment_changed_or_unavailable"
		return out
	}
	expected := nativeEndRevision(*record, frozen)
	if legacy == nil {
		check, cancel := context.WithTimeout(ctx, 20*time.Second)
		evidence, err := c.readNativeEndEvidence(check, frozen)
		expired := check.Err() != nil
		cancel()
		if err != nil || expired || !provenNativeEnd(evidence) || ctx.Err() != nil {
			if !evidence.ReportAt.IsZero() {
				out.Reason = "report_end_unproven"
			}
			return out
		}
	}
	out.State, out.Reason = "eligible", "ended_unobserved"
	if !apply {
		return out
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	current := c.st.nativeRunRecordLocked(n, c.st.Jobs[n], c.cfg.Inbox)
	// guard:terminal-reconcile-identity — proof cannot migrate to a new assignment.
	if c.st.Jobs[n] != j || (current != nil && nativeEndRevision(*current, c.st.Jobs[n]) != expected) ||
		(current == nil && legacy == nil) || ctx.Err() != nil {
		out.State, out.Reason = "unknown", "assignment_changed_or_unavailable"
		return out
	}
	if legacy != nil {
		record = legacy // retain its original completion time, never the replay clock
	}
	if err := c.st.publishNativeTerminalLocked(*record, out.Reason); err != nil {
		out.State, out.Reason = "unknown", "terminal_persistence_unavailable"
		return out
	}
	out.State = "recorded"
	return out
}
