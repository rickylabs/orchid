package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type remoteTUIProcess struct {
	PID         int    `json:"pid"`
	Fingerprint string `json:"fingerprint"`
}

// Retain a portable process-start/argv fingerprint alongside the one-shot TUI
// status proof. Pane/cwd/name reuse cannot reuse an earlier attachment proof.
const remoteTUIProcessPython = `import os,sys,subprocess,hashlib,json
try:
 pid=int(sys.argv[1])
 owner=subprocess.check_output(['ps','-p',str(pid),'-o','uid='],timeout=2).decode().strip()
 info=subprocess.check_output(['ps','-p',str(pid),'-o','lstart=','-o','command='],timeout=2)
 if int(owner)!=os.getuid() or not info.strip() or len(info)>16384:raise ValueError()
 print(json.dumps({'fingerprint':hashlib.sha256(info.strip()).hexdigest()}))
except Exception:sys.exit(2)
`

func (h Host) remoteTUIProcess(ctx context.Context, pane string) (*remoteTUIProcess, error) {
	out, err := h.herdr(ctx, "pane", "process-info", "--pane", pane)
	if err != nil {
		return nil, goalError("remote-control-identity-unconfirmed")
	}
	raw, err := herdrUnwrap(out)
	var row struct {
		ProcessInfo struct {
			Processes []struct {
				PID  int    `json:"pid"`
				Name string `json:"name"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	}
	if err != nil || decodeNativeJSON(raw, &row) != nil {
		return nil, goalError("remote-control-identity-unconfirmed")
	}
	pid := 0
	for _, p := range row.ProcessInfo.Processes {
		if p.Name == "codex" {
			if pid != 0 || p.PID <= 1 {
				return nil, goalError("remote-control-identity-unconfirmed")
			}
			pid = p.PID
		}
	}
	if pid == 0 {
		return nil, goalError("remote-control-identity-unconfirmed")
	}
	out, err = h.runRemote(ctx, "python3 -c "+shq(remoteTUIProcessPython)+" "+strconv.Itoa(pid))
	var result struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err != nil || len(out) > 256 || decodeNativeJSON([]byte(out), &result) != nil || !digestPattern.MatchString(result.Fingerprint) || ctx.Err() != nil {
		return nil, goalError("remote-control-identity-unconfirmed")
	}
	return &remoteTUIProcess{PID: pid, Fingerprint: result.Fingerprint}, nil
}

func (h Host) preGoalRemoteIdentity(ctx context.Context, kind, label string, run *remoteControlRun, location *dispatchLocation) error {
	if kind != "codex" {
		id, err := h.awaitRemoteIdentity(ctx, kind, label, run.Cwd, run.NativeSessionID, location)
		if err != nil {
			return err
		}
		run.NativeSessionID, run.IdentitySource, run.HookConfirmed = id, "herdr-session-start", true
		return nil
	}
	// The native status read proves TUI attachment even when an official hook
	// already exists. Keep the hook as a separate confirmation, never replace it.
	out, err := h.herdr(ctx, "agent", "get", location.PaneID)
	if err != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	raw, err := herdrUnwrap(out)
	if err != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	id, reason := nativeSessionFromResponse(raw, "agent_info", kind, label, location)
	if reason == "" && remoteOccupant(raw, kind, label, run.Cwd, run.NativeSessionID, location) && id == run.NativeSessionID {
		run.HookConfirmed = true
	} else if reason != nativeUnavailable {
		return goalError("remote-control-identity-unconfirmed")
	}
	var before struct {
		Agent AgentInfo `json:"agent"`
	}
	if decodeNativeJSON(raw, &before) != nil || !remoteStatusOccupant(raw, kind, label, run.Cwd, run.NativeSessionID, location) {
		return goalError("remote-control-identity-unconfirmed")
	}
	process, err := h.remoteTUIProcess(ctx, location.PaneID)
	if err != nil {
		return err
	}
	// The daemon's own trace proves attachment: exactly one codex-tui connection
	// at the pinned client version ran thread/resume on this freshly prepared
	// thread. No screen is read and nothing is typed into the agent.
	err = h.awaitCodexResumeIdentity(ctx, run) // guard:codex-resume-identity
	if err == nil {
		run.IdentitySource = "codex-native-status"
	}
	current, currentErr := h.remoteTUIProcess(ctx, location.PaneID)
	if err != nil || currentErr != nil || *process != *current || ctx.Err() != nil {
		run.IdentitySource = ""
		return goalError("remote-control-identity-unconfirmed")
	}
	run.TUIProcess = process
	return nil
}

func (h Host) remoteAttachedStatus(ctx context.Context, label string, run *remoteControlRun, location *dispatchLocation) error {
	if run == nil || location == nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	if run.IdentitySource == "codex-native-status" {
		current, err := h.remoteTUIProcess(ctx, location.PaneID)
		if err != nil || run.TUIProcess == nil || *current != *run.TUIProcess {
			return goalError("remote-control-identity-unconfirmed")
		}
	}
	err := verifyCodexAttachment(ctx, label, run, location, func() (AgentInfo, error) { return h.agentInfoOf(ctx, location.PaneID) },
		func() (int, error) { return h.codexResumeConnections(ctx, run) })
	if err != nil {
		return err
	}
	if run.IdentitySource == "codex-native-status" {
		current, err := h.remoteTUIProcess(ctx, location.PaneID)
		if err != nil || run.TUIProcess == nil || *current != *run.TUIProcess {
			return goalError("remote-control-identity-unconfirmed")
		}
	}
	if ctx.Err() != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	return nil
}

// verifyCodexAttachment proves the attached Codex TUI from native facts only:
// Herdr's structured occupant before and after (no change, no conflicting hook),
// and the daemon trace of exactly one pinned codex-tui resume of this thread.
func verifyCodexAttachment(ctx context.Context, label string, run *remoteControlRun, location *dispatchLocation,
	observe func() (AgentInfo, error), resumes func() (int, error)) error {
	if ctx.Err() != nil || run == nil || location == nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	before, err := observe()
	if err != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	raw, _ := json.Marshal(map[string]any{"type": "agent_info", "agent": before})
	if !remoteStatusOccupant(raw, "codex", label, run.Cwd, run.NativeSessionID, location) {
		return goalError("remote-control-identity-unconfirmed")
	}
	if n, err := resumes(); err != nil || n != 1 { // guard:attachment-resume-trace
		return goalError("remote-control-identity-unconfirmed")
	}
	after, err := observe()
	if err != nil || !samePromptOccupant(before, after) || before.StateChangeSeq != after.StateChangeSeq || ctx.Err() != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	raw, _ = json.Marshal(map[string]any{"type": "agent_info", "agent": after})
	if !remoteStatusOccupant(raw, "codex", label, run.Cwd, run.NativeSessionID, location) {
		return goalError("remote-control-identity-unconfirmed")
	}
	return nil
}

// codexResumeTracePython counts the distinct codex-tui connections that ran
// thread/resume on one thread, from the canonical daemon's own structured trace
// (read-only). Only the thread id, connection id and client fields are used.
const codexResumeTracePython = `import json,os,re,sqlite3,sys
tid,ver=sys.argv[1],sys.argv[2]
home=os.environ.get('CODEX_HOME') or os.path.join(os.environ['HOME'],'.codex')
c=sqlite3.connect('file:'+os.path.join(home,'logs_2.sqlite')+'?mode=ro',uri=True,timeout=5)
span=re.compile(r'rpc\.method="thread/resume" .*?app_server\.connection_id=(\d+) .*?app_server\.client_name="([^"]+)" app_server\.client_version="([^"]+)"')
conns=set()
for (b,) in c.execute("select feedback_log_body from logs where thread_id=?",(tid,)):
 m=span.search(b or '')
 if m and m.group(2)=='codex-tui' and (not ver or m.group(3)==ver):conns.add(m.group(1))
print(json.dumps({'connections':len(conns)}))
`

// codexResumeConnections reads the daemon trace once. The pinned client version
// is matched when known (pre-goal); a run reloaded from its private record no
// longer carries it, and the launch-time pin already enforced it.
func (h Host) codexResumeConnections(ctx context.Context, run *remoteControlRun) (int, error) {
	if run == nil || !privateNativeID(run.NativeSessionID) || (run.ClientVersion != "" && !codexVersionPattern.MatchString(run.ClientVersion)) {
		return 0, goalError("remote-control-identity-unconfirmed")
	}
	script := fmt.Sprintf("export HOME=%s; exec python3 -c %s %s %s", shq(h.agentHome()), shq(codexResumeTracePython), shq(run.NativeSessionID), shq(run.ClientVersion))
	out, err := h.runRemote(ctx, script)
	var v struct {
		Connections int `json:"connections"`
	}
	if err != nil || len(out) > 256 || decodeNativeJSON([]byte(strings.TrimSpace(out)), &v) != nil || v.Connections < 0 {
		return 0, goalError("remote-control-identity-unconfirmed")
	}
	return v.Connections, nil
}

// awaitCodexResumeIdentity waits, within the launch budget, for the daemon to
// record exactly one pinned codex-tui thread/resume on the prepared thread. Two
// or more connections are ambiguous and refuse.
func (h Host) awaitCodexResumeIdentity(ctx context.Context, run *remoteControlRun) error {
	if run == nil || !privateNativeID(run.NativeSessionID) || !codexVersionPattern.MatchString(run.ClientVersion) { // guard:identity-version-pinned
		return goalError("remote-control-identity-unconfirmed")
	}
	for ctx.Err() == nil {
		n, err := h.codexResumeConnections(ctx, run)
		if err == nil {
			switch {
			case n == 1: // guard:resume-exactly-one
				return nil
			case n > 1:
				return goalError("remote-control-identity-unconfirmed")
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return goalError("remote-control-identity-unconfirmed")
}
