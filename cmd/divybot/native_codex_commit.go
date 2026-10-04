package main

import (
	"context"
	"errors"
	"log"
	"os"
	"time"
)

// Durability operations for saveLocked; nil uses the operating system.
type stateFS struct {
	syncFile func(*os.File) error
	rename   func(string, string) error
	syncDir  func(*os.File) error
	hold     func(stage string)
}

func (fs *stateFS) fileSync(f *os.File) error {
	if fs != nil && fs.hold != nil {
		fs.hold("state-file")
	}
	if fs != nil && fs.syncFile != nil {
		return fs.syncFile(f)
	}
	return f.Sync()
}
func (fs *stateFS) move(from, to string) error {
	if fs != nil && fs.rename != nil {
		return fs.rename(from, to)
	}
	return os.Rename(from, to)
}
func (fs *stateFS) dirSync(dir *os.File) error {
	if fs != nil && fs.syncDir != nil {
		return fs.syncDir(dir)
	}
	return dir.Sync()
}

// A save error after the rename: the pathname names the new state, only its
// crash durability is uncertain. Callers that only test err != nil are unchanged.
type stateSaveError struct {
	afterRename bool
	err         error
}

func (e stateSaveError) Error() string { return e.err.Error() }

// The RC Codex delivery commit. C3 is the single linearization point: an
// on-time decision recorded by one save of three fields. Nothing after C3 can
// refuse; a failure before the rename restores every field under the lock.
func (c *Coord) commitRemoteCodexDelivery(ctx context.Context, deadline time.Time, j *Job) error {
	if ctx.Err() != nil || !time.Now().Before(deadline) { // guard:c3
		return errPromptUnconfirmed
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	if j == nil || j.NativeGoal == nil { // guard:nil-native-goal
		return errPromptUnconfirmed
	}
	priorDelivery, priorCommit := j.GoalDelivery, j.GoalDeliveryCommit
	priorPrompt := j.NativeGoal.PromptConfirmed
	j.GoalDelivery, j.GoalDeliveryCommit = "confirmed", deliveryCommitMark
	j.NativeGoal.PromptConfirmed = true
	err := c.st.saveLocked()
	var saved stateSaveError
	if err != nil && !(errors.As(err, &saved) && saved.afterRename) { // guard:before-rename
		j.GoalDelivery, j.GoalDeliveryCommit = priorDelivery, priorCommit
		j.NativeGoal.PromptConfirmed = priorPrompt
		return errPromptUnconfirmed
	}
	if err != nil {
		log.Printf("issue #%d: delivery-commit-durability-uncertain", j.Issue)
	}
	return nil
}

// Acceptance configuration for one RC Codex launch. nil fails closed.
func (c *Coord) codexAcceptanceFor(ctx context.Context, h Host, j *Job) *codexAcceptance {
	deadline, ok := ctx.Deadline()
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if !ok || err != nil || !rcCodex(j) {
		return nil
	}
	location := &dispatchLocation{PaneID: j.Pane, WorkspaceID: j.Workspace}
	return &codexAcceptance{root: c.cfg.Matrix.ReceiptRoot, key: j.DispatchKey, owner: owner, run: j.RemoteControl,
		pane: j.Pane, ws: j.Workspace, host: j.Host, deadline: deadline, every: time.Second,
		binding: func(ctx context.Context) error {
			return h.remoteStructuredBinding(ctx, j.Label, j.RemoteControl, location)
		}}
}

// Structured occupant and TUI-process continuity; never a pane read.
func (h Host) remoteStructuredBinding(ctx context.Context, label string, r *remoteControlRun, location *dispatchLocation) error {
	if r == nil || location == nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	out, err := h.herdr(ctx, "agent", "get", location.PaneID)
	if err != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	raw, err := herdrUnwrap(out)
	if err != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	occupant := remoteOccupant
	if r.IdentitySource == "codex-native-status" {
		occupant = remoteStatusOccupant
	}
	if !occupant(raw, "codex", label, r.Cwd, r.NativeSessionID, location) {
		return goalError("remote-control-identity-unconfirmed")
	}
	if r.IdentitySource == "codex-native-status" {
		current, err := h.remoteTUIProcess(ctx, location.PaneID)
		if err != nil || r.TUIProcess == nil || *current != *r.TUIProcess {
			return goalError("remote-control-identity-unconfirmed")
		}
	}
	return nil
}
