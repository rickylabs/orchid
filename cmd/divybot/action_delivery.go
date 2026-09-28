package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Cockpit publishes one 0600 JSON file by fsyncing tmp/<operationId>.json and
// renaming it into new/. The mount is separate from Matrix.ReceiptRoot.
// Only divybot writes the immutable owner-only action receipts.
var actionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var opaqueIDPattern = regexp.MustCompile(`^(agent|assignment)_[0-9a-f]{64}$`)
var actionKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type actionRequest struct {
	SchemaVersion         int    `json:"schemaVersion"`
	OperationID           string `json:"operationId"`
	IdempotencyKey        string `json:"idempotencyKey"`
	Repository            string `json:"repository"`
	IssueNumber           int    `json:"issueNumber"`
	AgentID               string `json:"agentId"`
	DispatchID            string `json:"dispatchId"`
	ExpectedAgentRevision string `json:"expectedAgentRevision"`
	Action                string `json:"action"`
	Payload               struct {
		Text   string `json:"text,omitempty"`
		Reason string `json:"reason,omitempty"`
	} `json:"payload"`
}

type actionReceipt struct {
	SchemaVersion      int    `json:"schemaVersion"`
	OperationID        string `json:"operationId"`
	RequestDigest      string `json:"requestDigest"`
	IdempotencyKey     string `json:"idempotencyKey"`
	Repository         string `json:"repository,omitempty"`
	IssueNumber        int    `json:"issueNumber,omitempty"`
	AgentID            string `json:"agentId,omitempty"`
	DispatchID         string `json:"dispatchId,omitempty"`
	Action             string `json:"action,omitempty"`
	NativeRunID        string `json:"nativeRunId,omitempty"`
	NativeSessionID    string `json:"nativeSessionId,omitempty"`
	Host               string `json:"host,omitempty"`
	PaneID             string `json:"paneId,omitempty"`
	WorkspaceID        string `json:"workspaceId,omitempty"`
	Outcome            string `json:"outcome"` // accepted|rejected|unknown, never execution
	Reason             string `json:"reason"`
	ObservedAt         string `json:"observedAt"`
	ReplacementAgentID string `json:"replacementAgentId,omitempty"`
	MessageID          string `json:"messageId,omitempty"`
}

type actionCalls struct {
	list          func(context.Context, Host) ([]AgentInfo, error)
	send          func(context.Context, Host, string, string) error
	close         func(context.Context, Host, string) error
	stopProcess   func(context.Context, Host, string, string) (*actionStopProcess, error)
	processGone   func(context.Context, Host, actionStopProcess) (bool, error)
	workspaceGone func(context.Context, Host, string) (bool, error)
}

func (c *Coord) actionList(ctx context.Context, h Host) ([]AgentInfo, error) {
	if c.actions.list != nil {
		return c.actions.list(ctx, h)
	}
	return h.agentList(ctx)
}
func (c *Coord) actionSend(ctx context.Context, h Host, pane, text string) error {
	if c.actions.send != nil {
		return c.actions.send(ctx, h, pane, text)
	}
	return h.send(ctx, pane, text)
}
func (c *Coord) actionClose(ctx context.Context, h Host, workspace string) error {
	if c.actions.close != nil {
		return c.actions.close(ctx, h, workspace)
	}
	return h.closeWorkspace(ctx, workspace)
}

func readPrivateActionJSON(path string, value any) error {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 1048576 {
		return errors.New("private_receipt_invalid")
	}
	body, err := os.ReadFile(path)
	if err != nil || strictJSON(body, value) != nil {
		return errors.New("private_receipt_invalid")
	}
	return nil
}

func actionOpaque(kind, runID string) string { return kind + "_" + shaText([]byte(kind+"\x00"+runID)) }
func actionRoot(root string) string          { return filepath.Join(root, "actions") }

// The request spool is shared by two containers. Permit a private 0770 group
// or named-user ACL mask; the divybot-owned receipt root stays strictly 0700.
func privateActionSpoolDir(root string) bool {
	if !filepath.IsAbs(root) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != filepath.Clean(root) {
		return false
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() || (st.Mode().Perm() != 0700 && st.Mode().Perm() != 0770) {
		return false
	}
	for p := root; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil || !os.IsNotExist(err) {
			return false
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return true
}

func actionRequestValid(r actionRequest) string {
	if r.SchemaVersion != 1 || !actionIDPattern.MatchString(r.OperationID) || !actionKeyPattern.MatchString(r.IdempotencyKey) ||
		!repositoryName.MatchString(r.Repository) || r.IssueNumber <= 0 ||
		!opaqueIDPattern.MatchString(r.AgentID) || !strings.HasPrefix(r.AgentID, "agent_") ||
		!opaqueIDPattern.MatchString(r.DispatchID) || !strings.HasPrefix(r.DispatchID, "assignment_") ||
		!digestPattern.MatchString(r.ExpectedAgentRevision) {
		return "request_invalid"
	}
	switch r.Action {
	case "stop":
		if r.Payload.Text != "" || len(r.Payload.Reason) > 512 {
			return "payload_invalid"
		}
	case "steer", "send":
		if len(r.Payload.Text) < 1 || len(r.Payload.Text) > 16000 || r.Payload.Reason != "" {
			return "payload_invalid"
		}
	case "retry":
		if r.Payload.Text != "" || r.Payload.Reason != "" {
			return "payload_invalid"
		}
	default:
		return "action_invalid"
	}
	return ""
}

func actionDirs(spool, receiptRoot string) (string, error) {
	if !privateActionSpoolDir(spool) || !privateReceiptRoot(receiptRoot) || spool == receiptRoot ||
		strings.HasPrefix(spool+string(os.PathSeparator), receiptRoot+string(os.PathSeparator)) ||
		strings.HasPrefix(receiptRoot+string(os.PathSeparator), spool+string(os.PathSeparator)) {
		return "", errors.New("action_root_invalid")
	}
	for _, dir := range []string{"tmp", "new", "done"} {
		if !privateActionSpoolDir(filepath.Join(spool, dir)) {
			return "", errors.New("action_spool_invalid")
		}
	}
	root := actionRoot(receiptRoot)
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if !privateReceiptRoot(root) {
		return "", errors.New("action_receipt_root_invalid")
	}
	return root, nil
}

func (c *Coord) publishActionOwner(paths ...string) error {
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err != nil {
		return err
	}
	return transferReceiptOwner(owner, paths...)
}

func actionSyncFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// Link a synced temporary file to a final name so an existing result is never
// replaced. The directory sync makes the result durable before moving the mail.
func actionImmutableJSON(dir, name string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".action-")
	if err != nil {
		return err
	}
	tmp := temp.Name()
	defer os.Remove(tmp)
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(body)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Link(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func actionIntent(root, id, digest string) (string, bool, bool, error) {
	target := filepath.Join(root, id)
	if b, err := os.ReadFile(filepath.Join(target, "intent.json")); err == nil {
		var prior struct {
			RequestDigest string `json:"requestDigest"`
		}
		if strictJSON(b, &prior) != nil || !digestPattern.MatchString(prior.RequestDigest) {
			return target, false, false, errors.New("intent_invalid")
		}
		return target, false, prior.RequestDigest == digest, nil
	} else if !os.IsNotExist(err) {
		return target, false, false, err
	}
	stage, err := os.MkdirTemp(root, ".intent-")
	if err != nil {
		return target, false, false, err
	}
	defer os.RemoveAll(stage)
	if err = os.Chmod(stage, 0700); err != nil {
		return target, false, false, err
	}
	body, _ := json.Marshal(struct {
		RequestDigest string `json:"requestDigest"`
	}{digest})
	if err = actionSyncFile(filepath.Join(stage, "intent.json"), body); err != nil {
		return target, false, false, err
	}
	if err = syncDirectory(stage); err != nil {
		return target, false, false, err
	}
	if err = os.Rename(stage, target); err != nil {
		return target, false, false, err
	}
	if err = syncDirectory(root); err != nil {
		return target, false, false, err
	}
	return target, true, true, nil
}

func actionReadReceipt(dir string) (*actionReceipt, error) {
	b, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		return nil, err
	}
	var r actionReceipt
	if len(b) > 65536 || strictJSON(b, &r) != nil {
		return nil, errors.New("receipt_invalid")
	}
	return &r, nil
}

func actionMoveDone(spool, file, digest string) error {
	// Preserve each request, including conflicts, without replacing prior mail.
	stem := strings.TrimSuffix(file, ".json")
	for n := 0; n < 1000; n++ {
		name := fmt.Sprintf("%s.%s.%03d.json", stem, digest[:16], n)
		dst := filepath.Join(spool, "done", name)
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			if err = os.Rename(filepath.Join(spool, "new", file), dst); err != nil {
				return err
			}
			if err = syncDirectory(filepath.Join(spool, "done")); err != nil {
				return err
			}
			return syncDirectory(filepath.Join(spool, "new"))
		}
	}
	return errors.New("done_full")
}

func (c *Coord) actionTick(ctx context.Context) {
	if c.cfg.ActionRequestRoot == "" || c.dry {
		return
	}
	root, err := actionDirs(c.cfg.ActionRequestRoot, c.cfg.Matrix.ReceiptRoot)
	if err != nil {
		log.Printf("action spool unavailable: %v", err)
		return
	}
	if err := c.publishActionOwner(root); err != nil {
		log.Printf("action receipt owner unavailable: %v", err)
		return
	}
	names, err := os.ReadDir(filepath.Join(c.cfg.ActionRequestRoot, "new"))
	if err != nil {
		log.Printf("action spool scan unavailable: %v", err)
		return
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
	for _, entry := range names {
		if ctx.Err() != nil {
			return
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !actionIDPattern.MatchString(strings.TrimSuffix(entry.Name(), ".json")) {
			continue
		}
		if err := c.actionOne(ctx, root, entry.Name()); err != nil {
			log.Printf("action %s: %v", entry.Name(), err)
		}
	}
	c.actionObserveStops(ctx, root)
}

func (c *Coord) actionOne(ctx context.Context, receiptRoot, file string) error {
	id := strings.TrimSuffix(file, ".json")
	path := filepath.Join(c.cfg.ActionRequestRoot, "new", file)
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || (st.Mode().Perm() != 0600 && st.Mode().Perm() != 0660) || st.Size() > 20000 {
		return errors.New("request_file_invalid")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	digest := shaText(raw)
	var req actionRequest
	invalid := strictJSON(raw, &req) != nil || req.OperationID != id
	if invalid {
		req = actionRequest{OperationID: id}
	}
	dir, created, matches, err := actionIntent(receiptRoot, id, digest)
	if err != nil {
		// A different digest is a deterministic rejection; the primary receipt
		// remains immutable and the conflicting request is independently visible.
		if err.Error() == "intent_invalid" {
			return err
		}
		return err
	}
	if !matches {
		conflict := actionReceipt{SchemaVersion: 1, OperationID: id, RequestDigest: digest,
			Outcome: "rejected", Reason: "digest_conflict", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if err := actionImmutableJSON(dir, "conflict-"+digest+".json", conflict); err != nil && !os.IsExist(err) {
			return err
		}
		if err := c.publishActionOwner(dir, filepath.Join(dir, "intent.json"), filepath.Join(dir, "conflict-"+digest+".json")); err != nil {
			return err
		}
		return actionMoveDone(c.cfg.ActionRequestRoot, file, digest)
	}
	if prior, err := actionReadReceipt(dir); err == nil {
		if prior.RequestDigest != digest {
			return errors.New("receipt_digest_mismatch")
		}
		if err := c.publishActionOwner(dir, filepath.Join(dir, "intent.json"), filepath.Join(dir, "result.json")); err != nil {
			return err
		}
		if err := c.actionPublishStopIndex(*prior); err != nil {
			return err
		}
		return actionMoveDone(c.cfg.ActionRequestRoot, file, digest)
	} else if !os.IsNotExist(err) {
		return err
	}
	// A prior process could have performed the effect before crashing. Keep the
	// fence and report unknown; never turn a missing receipt into a second send.
	if !created {
		r := actionReceipt{SchemaVersion: 1, OperationID: id, RequestDigest: digest,
			Outcome: "unknown", Reason: "receipt_missing_after_fence", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if err := actionImmutableJSON(dir, "result.json", r); err != nil {
			return err
		}
		if err := c.publishActionOwner(dir, filepath.Join(dir, "intent.json"), filepath.Join(dir, "result.json")); err != nil {
			return err
		}
		return actionMoveDone(c.cfg.ActionRequestRoot, file, digest)
	}
	return c.actionExecuteAndFinish(ctx, dir, file, digest, req, invalid)
}

func (c *Coord) actionExecuteAndFinish(ctx context.Context, dir, file, digest string, req actionRequest, invalid bool) error {
	r := actionReceipt{SchemaVersion: 1, OperationID: req.OperationID, RequestDigest: digest,
		IdempotencyKey: req.IdempotencyKey, Repository: req.Repository, IssueNumber: req.IssueNumber,
		AgentID: req.AgentID, DispatchID: req.DispatchID, Action: req.Action,
		Outcome: "unknown", Reason: "delivery_unobserved", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if invalid {
		r.Outcome, r.Reason = "rejected", "request_invalid"
	} else if reason := actionRequestValid(req); reason != "" {
		r.Outcome, r.Reason = "rejected", reason
	} else {
		c.deliverAction(ctx, dir, req, &r)
	}
	r.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := actionImmutableJSON(dir, "result.json", r); err != nil {
		return err
	}
	ownerPaths := []string{dir, filepath.Join(dir, "intent.json"), filepath.Join(dir, "result.json")}
	if _, err := os.Lstat(filepath.Join(dir, "stop-anchor.json")); err == nil {
		ownerPaths = append(ownerPaths, filepath.Join(dir, "stop-anchor.json"))
	}
	if err := c.publishActionOwner(ownerPaths...); err != nil {
		return err
	}
	if err := c.actionPublishStopIndex(r); err != nil {
		return err
	}
	return actionMoveDone(c.cfg.ActionRequestRoot, file, digest)
}

func (c *Coord) deliverAction(ctx context.Context, dir string, req actionRequest, r *actionReceipt) {
	if req.Repository != c.cfg.Inbox {
		r.Outcome, r.Reason = "rejected", "repository_mismatch"
		return
	}
	c.st.mu.Lock()
	j := c.st.Jobs[req.IssueNumber]
	c.st.mu.Unlock()
	if j == nil {
		r.Reason = "job_not_found"
		return
	}
	if !digestPattern.MatchString(j.DispatchKey) {
		r.Reason = "dispatch_binding_unavailable"
		return
	}
	runID := "orchid-" + j.DispatchKey
	if req.AgentID != actionOpaque("agent", runID) || req.DispatchID != actionOpaque("assignment", runID) {
		r.Outcome, r.Reason = "rejected", "identity_mismatch"
		return
	}
	// Reopen the exact private dispatch receipt. Client-supplied host/pane is
	// never accepted. An unreadable or uncertain launch cannot control a seat.
	record := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
	for _, dir := range []string{filepath.Dir(record), record} {
		st, err := os.Lstat(dir)
		if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
			r.Reason = "dispatch_receipt_unavailable"
			return
		}
	}
	var dispatch dispatchBinding
	if err := readPrivateActionJSON(filepath.Join(record, "dispatch.json"), &dispatch); err != nil ||
		dispatch.SchemaVersion != 1 || dispatch.State != "dispatched" || dispatch.RunID != runID ||
		dispatch.Issue.Repo != c.cfg.Inbox || dispatch.Issue.Number != j.Issue ||
		dispatch.Location == nil || dispatch.Location.PaneID != j.Pane || dispatch.Location.WorkspaceID != j.Workspace ||
		dispatch.Host != j.Host {
		r.Reason = "dispatch_receipt_unavailable"
		return
	}
	var binding struct {
		NativeSessionID string `json:"NativeSessionID"`
		Repo            string `json:"Repo"`
	}
	// The native binding carries additional launch metadata. Keep the strict
	// duplicate-key and private-file checks, then project only the two fields
	// needed to authorize this action.
	var bindingFields map[string]json.RawMessage
	if err := readPrivateActionJSON(filepath.Join(record, "binding.json"), &bindingFields); err != nil ||
		json.Unmarshal(bindingFields["NativeSessionID"], &binding.NativeSessionID) != nil ||
		json.Unmarshal(bindingFields["Repo"], &binding.Repo) != nil {
		r.Reason = "dispatch_receipt_unavailable"
		return
	}
	if binding.Repo != j.Repo {
		r.Reason = "dispatch_binding_mismatch"
		return
	}
	if binding.NativeSessionID != "" {
		if !privateNativeID(binding.NativeSessionID) {
			r.Reason = "native_identity_invalid"
			return
		}
		r.NativeSessionID = binding.NativeSessionID
	}
	if j.Agent == "codex" && r.NativeSessionID == "" {
		r.Reason = "native_identity_unavailable"
		return
	}
	host, ok := c.hosts[j.Host]
	if !ok || j.Pane == "" || j.Workspace == "" {
		r.Reason = "seat_unavailable"
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	agents, err := c.actionList(checkCtx, host)
	cancel()
	if err != nil {
		r.Reason = "seat_observation_unavailable"
		return
	}
	matches := 0
	status := ""
	for _, a := range agents {
		if issueFromCwd(a.Cwd) == j.Issue && a.PaneID == j.Pane && a.WorkspaceID == j.Workspace && accountKey(a.Agent) == accountKey(j.Agent) {
			matches++
			status = a.AgentStatus
		}
	}
	if matches != 1 {
		r.Reason = "seat_identity_unavailable"
		return
	}
	r.NativeRunID, r.Host, r.PaneID, r.WorkspaceID = runID, j.Host, j.Pane, j.Workspace
	switch req.Action {
	case "stop":
		if status == "done" || status == "unknown" {
			r.Outcome, r.Reason = "rejected", "agent_not_stoppable"
			return
		}
		// Capture the exact native process identity before the workspace closes.
		// A failed probe never blocks stop, but cannot create terminal evidence.
		probeCtx, probeDone := context.WithTimeout(ctx, 12*time.Second)
		anchor, probeErr := c.actionStopProcess(probeCtx, host, j.Pane, j.Agent)
		probeDone()
		if probeErr == nil && anchor != nil {
			anchor.OperationID, anchor.RequestDigest = req.OperationID, r.RequestDigest
			if err := actionImmutableJSON(dir, "stop-anchor.json", anchor); err != nil {
				anchor = nil
			}
		}
		// Fence and remove tracking before closing. A close timeout may still
		// have taken effect; the persisted block prevents automatic respawn.
		c.st.mu.Lock()
		if c.st.LaunchBlocks == nil {
			c.st.LaunchBlocks = map[int]launchBlock{}
		}
		c.st.LaunchBlocks[j.Issue] = launchBlock{Reason: "cockpit_stop", Notified: true}
		delete(c.st.Jobs, j.Issue)
		err = c.st.saveLocked()
		c.st.mu.Unlock()
		if err != nil {
			r.Reason = "stop_fence_unavailable"
			return
		}
		effectCtx, done := context.WithTimeout(ctx, 12*time.Second)
		err = c.actionClose(effectCtx, host, j.Workspace)
		done()
		if err != nil {
			r.Reason = "stop_delivery_unconfirmed"
			return
		}
		r.Outcome, r.Reason = "accepted", "workspace_close_delivered"
	case "steer", "send":
		if req.Action == "steer" && status != "working" && status != "blocked" {
			r.Outcome, r.Reason = "rejected", "agent_not_running"
			return
		}
		if status != "working" && status != "blocked" && status != "idle" {
			r.Outcome, r.Reason = "rejected", "agent_not_live"
			return
		}
		effectCtx, done := context.WithTimeout(ctx, 20*time.Second)
		err = c.actionSend(effectCtx, host, j.Pane, req.Payload.Text)
		done()
		if err != nil {
			r.Reason = "prompt_delivery_unconfirmed"
			return
		}
		r.Outcome, r.Reason = "accepted", "prompt_delivered"
	case "retry":
		r.Outcome, r.Reason = "rejected", "retry_requires_terminal_successor_contract"
	}
}
