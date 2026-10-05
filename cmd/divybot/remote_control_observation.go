package main

import (
	"context"
	"encoding/json"
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

// A requested title is not an observed native name. This read is optional;
// absent metadata never manufactures a name or invalidates actual connection.
const claudeRemoteNamePython = `import os,sys,json,re,stat
try:
 request=json.loads(sys.stdin.read(8193))
 if set(request)!=set(['cwd','id','name']):raise ValueError()
 directory=re.sub('[^a-zA-Z0-9]','-',request['cwd'])
 path=os.path.join(os.environ.get('CLAUDE_CONFIG_DIR') or os.path.join(os.environ['HOME'],'.claude'),'projects',directory,request['id']+'.jsonl')
 fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK);s=os.fstat(fd)
 if not stat.S_ISREG(s.st_mode) or s.st_uid!=os.getuid() or stat.S_IMODE(s.st_mode)!=0o600 or s.st_size>1048576:raise ValueError()
 data=os.read(fd,1048577);os.close(fd)
 if len(data)>1048576:raise ValueError()
 title=None
 for line in data.splitlines():
  row=json.loads(line)
  if row.get('type')=='custom-title' and row.get('sessionId')==request['id']:title=row.get('customTitle')
 print(json.dumps({'matched':isinstance(title,str) and title==request['name']}))
except Exception:print('{"matched":false}')
`

func (h Host) observedClaudeName(ctx context.Context, run *remoteControlRun) *string {
	if run == nil || !privateNativeID(run.NativeSessionID) || !validRemoteCwd(run.Cwd) {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"cwd": run.Cwd, "id": run.NativeSessionID, "name": run.Name})
	// Input is a private here-document, not argv; never report command errors.
	script := "export HOME=" + shq(h.agentHome()) + "; python3 -c " + shq(claudeRemoteNamePython) + " <<'ORCHID_NATIVE_NAME'\n" + string(body) + "\nORCHID_NATIVE_NAME"
	out, err := h.runRemote(ctx, script)
	var v struct {
		Matched bool `json:"matched"`
	}
	if err != nil || len(out) > 1024 || decodeNativeJSON([]byte(out), &v) != nil || !v.Matched || ctx.Err() != nil {
		return nil
	}
	name := run.Name
	return &name
}

func (c *Coord) observeRemoteControl(ctx context.Context, h Host, j *Job) {
	if j == nil || j.RemoteControl == nil || (j.Agent != "codex" && j.Agent != "claude") {
		return
	}
	h.ShadowScope = c.shadow.scope(j)
	// Observe-only native evidence, read before the proof's own budget starts
	// so it cannot change today's decision.
	h.observeClaudeBridge(ctx, j) // guard:claude-bridge-before-check
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err != nil {
		c.clearRemoteControl(j)
		return
	}
	r, id, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, owner, j.Agent)
	if err != nil || id != j.RemoteControl.NativeSessionID || r.dispatch.Location == nil || r.dispatch.Host != j.Host {
		c.clearRemoteControl(j)
		return
	}
	proofErr := h.remoteProof(check, j.Agent, j.Label, j.RemoteControl, r.dispatch.Location)
	current, currentID, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, owner, j.Agent)
	if err != nil || currentID != id || !reflect.DeepEqual(current.dispatch, r.dispatch) || check.Err() != nil {
		c.clearRemoteControl(j)
		return
	}
	var name *string
	state, reason := "connected", ""
	if proofErr != nil {
		state, reason = "unconfirmed", "remote-control-unconfirmed"
	} else if j.Agent == "codex" {
		name = &j.RemoteControl.Name
	} else {
		name = h.observedClaudeName(check, j.RemoteControl)
	}
	c.shadow.compare(j, shadowSiteConnection, state, remoteProofInputs(j))
	latest, latestID, bindingErr := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, owner, j.Agent)
	if bindingErr != nil || latestID != id || !reflect.DeepEqual(latest.dispatch, r.dispatch) || check.Err() != nil {
		c.clearRemoteControl(j)
		return
	}
	if check.Err() != nil || writeRemoteObservation(check, r, j.Agent, j.RemoteControl, state, reason, name) != nil || check.Err() != nil {
		c.clearRemoteControl(j)
	}
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
		return []string{shadowInputHerdrAgent, shadowInputScreenFooter}
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
