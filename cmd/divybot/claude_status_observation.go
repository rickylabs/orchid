package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

// This is a current observation, not a terminal event. It is replaced on every
// verified tick with herdr's status for the bound session: "working" while the
// agent works, "idle" or "done" once it has stopped at its prompt, so a finished
// root reads as not running rather than unknown. It is removed whenever that
// observation cannot be made, including "blocked" and "unknown".
type claudeStatusObservation struct {
	SchemaVersion   int    `json:"schemaVersion"`
	RunID           string `json:"runId"`
	NativeSessionID string `json:"nativeSessionId"`
	Host            string `json:"host"`
	PaneID          string `json:"paneId"`
	WorkspaceID     string `json:"workspaceId"`
	Status          string `json:"status"`
	ObservedAt      string `json:"observedAt"`
}

func writeClaudeStatus(path string, owner *receiptOwner, row claudeStatusObservation) error {
	return writePrivateJSON(path, ".claude-status-", owner, row)
}

// writePrivateJSON replaces path atomically with an owner-only JSON document.
func writePrivateJSON(path, tempPrefix string, owner *receiptOwner, value any) error {
	dir := filepath.Dir(path)
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, tempPrefix)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = transferReceiptOwner(owner, f.Name()); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

// A bound Claude session can use Herdr's exact-pane status as running evidence:
// "working" is running, and "idle" or "done" is stopped at the prompt (not ended:
// the agent can be prompted again). The lookup and receipt are rechecked before
// publication; no screen status is promoted to terminal evidence, and a failed
// read cannot preserve any status. It reports whether a row was written.
func observeClaudeWorking(ctx context.Context, root, inbox string, owner *receiptOwner, j *Job,
	read func(context.Context, string) (json.RawMessage, error), now time.Time) bool {
	if j == nil || j.Agent != "claude" || !digestPattern.MatchString(j.DispatchKey) || !privateReceiptRoot(root) {
		return false
	}
	path := filepath.Join(root, j.DispatchKey, "record", "claude-status.json")
	valid := false
	defer func() {
		if !valid {
			_ = os.Remove(path)
			_ = syncDirectory(filepath.Dir(path))
		}
	}()
	r, id, err := loadNativeBindingReceipt(root, j.DispatchKey, j, inbox, owner, "claude")
	if err != nil || id == "" || r.dispatch.Location == nil ||
		r.dispatch.Location.PaneID != j.Pane || r.dispatch.Location.WorkspaceID != j.Workspace || r.dispatch.Host != j.Host {
		return false
	}
	raw, err := read(ctx, j.Pane)
	if err != nil {
		return false
	}
	observedID, reason := nativeSessionFromResponse(raw, "agent_info", "claude", j.Label, r.dispatch.Location)
	if reason != "" || observedID != id {
		return false
	}
	var response struct {
		Agent struct {
			Status string `json:"agent_status"`
		} `json:"agent"`
	}
	if decodeNativeJSON(raw, &response) != nil {
		return false
	}
	switch response.Agent.Status {
	case "working", "idle", "done":
	default:
		return false
	}
	verified, currentID, err := loadNativeBindingReceipt(root, j.DispatchKey, j, inbox, owner, "claude")
	if err != nil || currentID != id || !reflect.DeepEqual(r.dispatch, verified.dispatch) {
		return false
	}
	row := claudeStatusObservation{SchemaVersion: 1, RunID: r.dispatch.RunID,
		NativeSessionID: id, Host: j.Host, PaneID: j.Pane, WorkspaceID: j.Workspace,
		Status: response.Agent.Status, ObservedAt: now.UTC().Format(time.RFC3339Nano)}
	if writeClaudeStatus(path, owner, row) != nil {
		return false
	}
	valid = true
	return true
}

func (c *Coord) observeClaudeWorking(ctx context.Context, host Host, j *Job) {
	if j.Agent != "claude" || j.Pane == "" {
		return
	}
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err != nil {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	_ = observeClaudeWorking(readCtx, c.cfg.Matrix.ReceiptRoot, c.cfg.Inbox, owner, j,
		func(ctx context.Context, pane string) (json.RawMessage, error) {
			out, err := host.herdr(ctx, "agent", "get", pane)
			if err != nil {
				return nil, err
			}
			return herdrUnwrap(out)
		}, time.Now())
}

func (c *Coord) clearClaudeWorking(j *Job) {
	if j == nil || j.Agent != "claude" || !digestPattern.MatchString(j.DispatchKey) || !privateReceiptRoot(c.cfg.Matrix.ReceiptRoot) {
		return
	}
	dir := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
	_ = os.Remove(filepath.Join(dir, "claude-status.json"))
	_ = syncDirectory(dir)
}
