package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

func (c *Coord) clearRemoteControl(j *Job) {
	if j == nil || !digestPattern.MatchString(j.DispatchKey) || !privateReceiptRoot(c.cfg.Matrix.ReceiptRoot) {
		return
	}
	dir := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
	if !privateReceiptRoot(filepath.Dir(dir)) || !privateReceiptRoot(dir) {
		return
	}
	_ = os.Remove(filepath.Join(dir, "remote-control.json"))
	_ = syncDirectory(dir)
}

func (c *Coord) observeRemoteControl(ctx context.Context, h Host, j *Job) {
	if j == nil || j.RemoteControl == nil || (j.Agent != "codex" && j.Agent != "claude") {
		return
	}
	h.ShadowScope = c.shadow.scope(j)
	h.ClaudeLinks = c.claudeLinks
	c.claudeLinks.revokeWith(c.revokeRemoteControl)
	// A lost or changed native identity withdraws the row and any session link.
	drop := func() {
		if j.Agent == "claude" {
			c.claudeLinks.forget(j.DispatchKey, j.Issue, linkIdentityChanged) // guard:claude-link-identity-forgets
		}
		c.clearRemoteControl(j)
	}
	// Observe-only native evidence, read off the decision path (asynchronous,
	// own context): it cannot change today's decision or consume its deadline.
	h.observeClaudeBridge(j) // guard:claude-bridge-off-path
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err != nil {
		drop()
		return
	}
	r, id, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, owner, j.Agent)
	if err != nil || id != j.RemoteControl.NativeSessionID || r.dispatch.Location == nil || r.dispatch.Host != j.Host {
		drop()
		return
	}
	proofErr := h.remoteProof(check, j.Agent, j.Label, j.RemoteControl, r.dispatch.Location)
	if proofErr != nil && j.Agent == "claude" {
		// Claude's proof is the native occupant alone: any failure is an
		// ended or replaced session, never a connection-only uncertainty.
		drop() // guard:claude-proof-error-drops
		return
	}
	current, currentID, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, owner, j.Agent)
	if err != nil || currentID != id || !reflect.DeepEqual(current.dispatch, r.dispatch) || check.Err() != nil {
		drop()
		return
	}
	var name *string
	state, reason := "connected", ""
	if proofErr != nil || j.Agent == "claude" { // guard:claude-never-connected
		// Claude documents no native connection state: always unconfirmed.
		state, reason = "unconfirmed", "remote-control-unconfirmed"
	} else {
		name = &j.RemoteControl.Name
	}
	c.shadow.compare(j, shadowSiteConnection, state, remoteProofInputs(j))
	latest, latestID, bindingErr := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, owner, j.Agent)
	if bindingErr != nil || latestID != id || !reflect.DeepEqual(latest.dispatch, r.dispatch) || check.Err() != nil {
		drop()
		return
	}
	write := func(link *string) error {
		if check.Err() != nil {
			return check.Err()
		}
		return writeRemoteObservation(check, r, j.Agent, j.RemoteControl, state, reason, name, link)
	}
	// The Claude session link comes only from Claude's own records agreeing for
	// the launched process (read off this path, still fresh); never from the
	// pane. It is published under the lock that withholding takes.
	if j.Agent == "claude" {
		alive := func() bool { return h.claudeProcessAlive(check, j.RemoteControl.ClaudeProcess) }
		err = c.claudeLinks.publish(j.DispatchKey, j.Issue, j.RemoteControl.NativeSessionID, time.Now(), alive, write) // guard:claude-link-native-only
	} else {
		err = write(nil)
	}
	if err != nil || check.Err() != nil {
		drop()
	}
}

// revokeRemoteControl withdraws a job's published row by its dispatch key.
func (c *Coord) revokeRemoteControl(key string) {
	c.clearRemoteControl(&Job{DispatchKey: key})
}

// The official post-prompt hook remains a second confirmation. Never overwrite
// a prepared identity with a different report or allow continuation input after
// a conflict. The invocation-only attached-thread footer also remains exact;
// an absent daemon-inherited hook is not fabricated into an official report.
func (c *Coord) checkRemoteHook(ctx context.Context, h Host, j *Job) bool {
	if j == nil || j.RemoteControl == nil {
		return true
	}
	h.ShadowScope = c.shadow.scope(j)
	ok, inputs := c.checkRemoteHookToday(ctx, h, j)
	verdict := "refuse"
	if ok {
		verdict = "pass"
	}
	c.shadow.compare(j, shadowSiteRemoteHook, verdict, inputs)
	return ok
}

// Today's decision, unchanged; it only also names the inputs it consumed.
func (c *Coord) checkRemoteHookToday(ctx context.Context, h Host, j *Job) (bool, []string) {
	if j.Agent != "codex" {
		return true, []string{shadowInputUnchecked}
	}
	herdrOnly := []string{shadowInputHerdrAgent}
	attached := []string{shadowInputHerdrAgent, shadowInputScreenFooter}
	if j.RemoteControl.IdentitySource == "codex-native-status" {
		attached = append(attached, shadowInputTUIProcess)
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := h.herdr(check, "agent", "get", j.Pane)
	if err != nil || check.Err() != nil {
		return false, herdrOnly
	}
	raw, err := herdrUnwrap(out)
	if err != nil {
		return false, herdrOnly
	}
	id, reason := nativeSessionFromResponse(raw, "agent_info", j.Agent, j.Label, &dispatchLocation{PaneID: j.Pane, WorkspaceID: j.Workspace})
	if reason == nativeUnavailable {
		if err := h.remoteAttachedStatus(check, j.Label, j.RemoteControl, &dispatchLocation{PaneID: j.Pane, WorkspaceID: j.Workspace}); err != nil {
			if err == goalError("remote-control-hook-mismatch") {
				c.blockRemoteMismatch(ctx, h, j)
			}
			return false, attached
		}
		return true, attached
	}
	if reason != "" || id != j.RemoteControl.NativeSessionID {
		c.blockRemoteMismatch(ctx, h, j)
		return false, herdrOnly
	}
	if !j.RemoteControl.HookConfirmed {
		c.st.mu.Lock()
		j.RemoteControl.HookConfirmed = true
		_ = c.st.saveLocked()
		c.st.mu.Unlock()
	}
	if err := h.remoteAttachedStatus(check, j.Label, j.RemoteControl, &dispatchLocation{PaneID: j.Pane, WorkspaceID: j.Workspace}); err != nil {
		if err == goalError("remote-control-hook-mismatch") {
			c.blockRemoteMismatch(ctx, h, j)
		}
		return false, attached
	}
	return true, attached
}

// Inputs today's Remote Control connection proof consumes for this vendor.
func remoteProofInputs(j *Job) []string {
	if j.Agent != "codex" {
		return []string{shadowInputHerdrAgent} // guard:claude-proof-inputs-native
	}
	inputs := []string{shadowInputHerdrAgent, shadowInputScreenFooter, shadowInputCodexStatus}
	if j.RemoteControl.IdentitySource == "codex-native-status" {
		inputs = append(inputs, shadowInputTUIProcess)
	}
	return inputs
}

func (c *Coord) blockRemoteMismatch(ctx context.Context, h Host, j *Job) {
	c.blockGoalDelivery(j.Issue, j)
	c.clearRemoteControl(j)
	stop, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Stop only the originally prepared native work. A conflicting occupant
	// cannot license closing a pane that may now belong to another session.
	_ = h.stopRemoteRun(stop, j)
}
