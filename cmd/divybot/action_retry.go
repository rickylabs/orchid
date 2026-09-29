package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A retry survives a dispatcher restart. Automatic polling must not race an
// action whose external launch effect may have occurred before its receipt.
type retryFlight struct {
	OperationID string `json:"operationId"`
	DispatchKey string `json:"dispatchKey"`
}

type retrySource struct {
	Expectation     retryExpectation
	NativeSessionID string
	IssueID         string
	BriefDigest     string
	TargetRepo      string
	Record          string
}

func retryReservationKey(is Issue, repo, operationID string) string {
	original := shaText([]byte(is.ID + "\x00" + repo + "\x00" + briefDigest(is)))
	return shaText([]byte("retry\x00" + original + "\x00" + operationID))
}

// The opaque phone ID has no reversible key. This action-only lookup is
// bounded; the high-frequency feed still uses direct dispatch indexes.
func findRetrySource(root, inbox string, req actionRequest) (*retrySource, string) {
	if !privateReceiptRoot(root) {
		return nil, "dispatch_receipt_unavailable"
	}
	f, err := os.Open(root)
	if err != nil {
		return nil, "dispatch_receipt_unavailable"
	}
	defer f.Close()
	entries, err := f.ReadDir(4097)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, "dispatch_receipt_unavailable"
	}
	if len(entries) > 4096 {
		return nil, "dispatch_lookup_limit"
	}
	for _, entry := range entries {
		key := entry.Name()
		if !entry.IsDir() || !digestPattern.MatchString(key) {
			continue
		}
		runID := "orchid-" + key
		if req.DispatchID != actionOpaque("assignment", runID) {
			continue
		}
		if req.AgentID != actionOpaque("agent", runID) {
			return nil, "identity_mismatch"
		}
		record := filepath.Join(root, key, "record")
		if !privateReceiptRoot(filepath.Join(root, key)) || !privateReceiptRoot(record) {
			return nil, "dispatch_receipt_unavailable"
		}
		var dispatch dispatchBinding
		if readPrivateActionJSON(filepath.Join(record, "dispatch.json"), &dispatch) != nil ||
			dispatch.SchemaVersion != 1 || dispatch.State != "dispatched" || dispatch.RunID != runID ||
			dispatch.Issue.Repo != inbox || dispatch.Issue.Number != req.IssueNumber ||
			dispatch.Location == nil || dispatch.Source != "codex" ||
			!sourceRevision.MatchString(dispatch.ProfileRevision) || dispatch.MatrixSource != matrixSourceRepository || !sourceRevision.MatchString(dispatch.MatrixRevision) {
			return nil, "retry_pins_unavailable"
		}
		var fields map[string]json.RawMessage
		if readPrivateActionJSON(filepath.Join(record, "binding.json"), &fields) != nil {
			return nil, "retry_pins_unavailable"
		}
		var binding struct{ IssueID, BriefDigest, Repo, NativeSessionID string }
		for _, field := range []struct {
			name string
			dest *string
		}{
			{"IssueID", &binding.IssueID}, {"BriefDigest", &binding.BriefDigest}, {"Repo", &binding.Repo}, {"NativeSessionID", &binding.NativeSessionID},
		} {
			if json.Unmarshal(fields[field.name], field.dest) != nil {
				return nil, "retry_pins_unavailable"
			}
		}
		if binding.IssueID == "" || !digestPattern.MatchString(binding.BriefDigest) || !repositoryName.MatchString(binding.Repo) || !privateNativeID(binding.NativeSessionID) {
			return nil, "retry_pins_unavailable"
		}
		var policy matrixReceipt
		if readPrivateActionJSON(filepath.Join(record, "receipt.json"), &policy) != nil ||
			policy.SchemaVersion != 1 || policy.Resolution.SourceRepository != matrixSourceRepository ||
			policy.Resolution.SourceRevision != dispatch.MatrixRevision ||
			policy.Requested["transport"] != dispatch.Source || policy.Requested["model"] != dispatch.Model ||
			policy.Requested["tier"] == "" || policy.Requested["role"] == "" {
			return nil, "retry_pins_unavailable"
		}
		return &retrySource{Expectation: retryExpectation{OperationID: req.OperationID, Dispatch: dispatch,
			Tier: policy.Requested["tier"], Role: policy.Requested["role"]},
			NativeSessionID: binding.NativeSessionID, IssueID: binding.IssueID,
			BriefDigest: binding.BriefDigest, TargetRepo: binding.Repo, Record: record}, ""
	}
	return nil, "retry_pins_unavailable"
}

func retryTeardownComplete(source *retrySource) bool {
	if source == nil {
		return false
	}
	var intent teardownIntent
	if readPrivateActionJSON(filepath.Join(source.Record, "teardown-intent.json"), &intent) != nil ||
		intent.SchemaVersion != 1 || intent.NativeRunID != source.Expectation.Dispatch.RunID ||
		intent.DispatchKey != strings.TrimPrefix(intent.NativeRunID, "orchid-") ||
		intent.NativeSessionID != source.NativeSessionID || intent.Issue != source.Expectation.Dispatch.Issue.Number ||
		intent.Host != source.Expectation.Dispatch.Host || intent.WorkspaceID != source.Expectation.Dispatch.Location.WorkspaceID ||
		intent.PaneID != source.Expectation.Dispatch.Location.PaneID {
		return false
	}
	for name, kind := range map[string]string{"teardown-seat-observed.json": "seat_absent", "teardown-process-observed.json": "process_absent"} {
		var observation teardownObservation
		if readPrivateActionJSON(filepath.Join(source.Record, name), &observation) != nil ||
			observation.SchemaVersion != 1 || observation.NativeRunID != intent.NativeRunID || observation.Kind != kind || observation.ObservedAt == "" {
			return false
		}
	}
	return true
}

func (c *Coord) retryIssue(ctx context.Context, n int) (Issue, string, error) {
	if c.actions.retryIssue != nil {
		return c.actions.retryIssue(ctx, n)
	}
	var raw struct {
		ID     string `json:"id"`
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	check, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := ghJSON(check, &raw, "issue", "view", strconv.Itoa(n), "--repo", c.cfg.Inbox, "--json", "id,number,title,body,state,labels"); err != nil {
		return Issue{}, "", err
	}
	is := Issue{ID: raw.ID, Number: raw.Number, Title: raw.Title, Body: raw.Body}
	for _, label := range raw.Labels {
		is.Labels = append(is.Labels, label.Name)
	}
	return is, raw.State, nil
}

func (c *Coord) retryReopen(ctx context.Context, n int) error {
	if c.actions.retryReopen != nil {
		return c.actions.retryReopen(ctx, n)
	}
	check, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := run(check, "gh", "issue", "reopen", strconv.Itoa(n), "--repo", c.cfg.Inbox)
	return err
}

func (c *Coord) retryNativeFailed(ctx context.Context, host Host, thread string) (bool, error) {
	if c.actions.retryNativeFailed != nil {
		return c.actions.retryNativeFailed(ctx, host, thread)
	}
	var failed bool
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err := host.withGoalConnection(check, thread, func(p *goalRPC) error {
		var readErr error
		failed, readErr = p.lastTurnFailed()
		return readErr
	})
	return failed, err
}

func (c *Coord) retryAttempt(ctx context.Context, is Issue, target Target, expected retryExpectation, preflight bool) (bool, matrixRefusal) {
	if c.actions.retryAttempt != nil {
		return c.actions.retryAttempt(ctx, is, target, expected, preflight)
	}
	budget := c.curCaps()
	status, _ := c.fleetStatus(ctx)
	c.st.mu.Lock()
	for n, job := range c.st.Jobs {
		if ref, known := status[n]; occupiesAdmissionSlot(job, ref, known) {
			budget[accountKey(job.Agent)]--
		}
	}
	c.st.mu.Unlock()
	var refusal matrixRefusal
	_, launched := c.matrixAttempt(ctx, is.Number, is, target, budget, matrixAttemptDeps{
		report: func(r matrixRefusal) { refusal = r }, read: readRoutingFile, resolve: resolveMatrix,
		persist: persistMatrixReceipt, host: c.pickHost, launch: c.spawn,
		retry: &expected, preflight: preflight,
	})
	return launched, refusal
}

func (c *Coord) deliverRetryAction(ctx context.Context, req actionRequest, r *actionReceipt) {
	if c.dry {
		r.Outcome, r.Reason = "rejected", "retry_unavailable"
		return
	}
	is, state, err := c.retryIssue(ctx, req.IssueNumber)
	if err != nil || is.Number != req.IssueNumber || is.ID == "" || (state != "OPEN" && state != "CLOSED") {
		r.Outcome, r.Reason = "rejected", "retry_issue_unavailable"
		return
	}
	target, ok := c.targetFor(is)
	if !ok || target.Disabled {
		r.Outcome, r.Reason = "rejected", "retry_target_unavailable"
		return
	}
	source, reason := findRetrySource(c.cfg.Matrix.ReceiptRoot, c.cfg.Inbox, req)
	if reason != "" {
		r.Outcome, r.Reason = "rejected", reason
		return
	}
	if source.IssueID != is.ID || source.BriefDigest != briefDigest(is) || source.TargetRepo != target.Repo || source.Expectation.Dispatch.Issue.Number != is.Number {
		r.Outcome, r.Reason = "rejected", "retry_pins_unavailable"
		return
	}
	c.st.mu.Lock()
	_, live := c.st.Jobs[is.Number]
	_, retrying := c.st.RetryFlights[is.Number]
	c.st.mu.Unlock()
	if live || retrying {
		r.Outcome, r.Reason = "rejected", "retry_in_flight"
		return
	}
	if !retryTeardownComplete(source) {
		r.Outcome, r.Reason = "rejected", "retry_terminal_unproven"
		return
	}
	host, ok := c.hosts[source.Expectation.Dispatch.Host]
	if !ok {
		r.Outcome, r.Reason = "rejected", "retry_pins_unavailable"
		return
	}
	failed, err := c.retryNativeFailed(ctx, host, source.NativeSessionID)
	if err != nil || !failed {
		r.Outcome, r.Reason = "rejected", "retry_terminal_unproven"
		return
	}
	if launched, refusal := c.retryAttempt(ctx, is, target, source.Expectation, true); !launched {
		r.Outcome, r.Reason = "rejected", "retry_pins_unavailable"
		if refusal.ReasonCode != "" && refusal.ReasonCode != "retry-pins-unavailable" {
			r.Reason = "retry_preflight_unavailable"
		}
		return
	}
	key := retryReservationKey(is, target.Repo, req.OperationID)
	c.st.mu.Lock()
	if c.st.RetryFlights == nil {
		c.st.RetryFlights = map[int]retryFlight{}
	}
	if c.st.RetryFlights[is.Number].OperationID != "" || c.st.Jobs[is.Number] != nil {
		c.st.mu.Unlock()
		r.Outcome, r.Reason = "rejected", "retry_in_flight"
		return
	}
	prior, blocked := c.st.LaunchBlocks[is.Number]
	c.st.RetryFlights[is.Number] = retryFlight{OperationID: req.OperationID, DispatchKey: key}
	delete(c.st.LaunchBlocks, is.Number)
	err = c.st.saveLocked()
	if err != nil {
		delete(c.st.RetryFlights, is.Number)
		if blocked {
			c.st.LaunchBlocks[is.Number] = prior
		}
	}
	c.st.mu.Unlock()
	if err != nil {
		r.Reason = "retry_fence_unavailable"
		return
	}
	if state == "CLOSED" {
		if err := c.retryReopen(ctx, is.Number); err != nil {
			r.Reason = "retry_reopen_unconfirmed"
			return
		}
	}
	// The issue could change while a closed issue is reopened. The action's
	// original brief and target still have to match before a second effect.
	latest, currentState, err := c.retryIssue(ctx, is.Number)
	if err != nil || currentState != "OPEN" || latest.ID != is.ID || briefDigest(latest) != briefDigest(is) {
		r.Reason = "retry_issue_changed"
		return
	}
	launched, refusal := c.retryAttempt(ctx, latest, target, source.Expectation, false)
	if !launched {
		r.Reason = "retry_launch_unconfirmed"
		if refusal.Status == "refused" && refusal.ReasonCode == "retry-pins-unavailable" {
			r.Outcome, r.Reason = "rejected", "retry_pins_unavailable"
		}
		return
	}
	r.NativeRunID = "orchid-" + key
	r.ReplacementAgentID = actionOpaque("agent", r.NativeRunID)
	r.ReplacementDispatchID = actionOpaque("assignment", r.NativeRunID)
	r.Outcome, r.Reason = "accepted", "retry_dispatched"
}
