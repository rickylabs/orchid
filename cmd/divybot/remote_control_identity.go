package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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
	// The daemon's native resume record proves attachment once: the canonical
	// daemon spawned this prepared thread's session for exactly one pinned
	// codex-tui connection, which then proceeded past a successful resume. No
	// screen is read and nothing is typed into the agent.
	err = h.awaitCodexResumeIdentity(ctx, run) // guard:codex-resume-identity
	if err == nil {
		run.IdentitySource = "codex-native-status"
	}
	current, currentErr := h.remoteTUIProcess(ctx, location.PaneID)
	if err != nil || currentErr != nil || *process != *current || ctx.Err() != nil {
		run.IdentitySource, run.Resume = "", nil
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
	err := verifyCodexAttachment(ctx, label, run, location, func() (AgentInfo, error) { return h.agentInfoOf(ctx, location.PaneID) })
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

// verifyCodexAttachment supervises an attached Codex TUI from native facts
// only: the resume proof established once before the goal (kept in private run
// state with its client pin), and Herdr's structured occupant before and after
// (no change, no conflicting hook). The daemon log is never re-read here: Codex
// prunes per-thread log rows, so the original resume record is not durable.
func verifyCodexAttachment(ctx context.Context, label string, run *remoteControlRun, location *dispatchLocation, observe func() (AgentInfo, error)) error {
	if ctx.Err() != nil || run == nil || location == nil || !codexResumeProven(run) { // guard:attachment-stored-proof
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

// codexResumeProof is the native resume record verified once before the goal:
// the canonical daemon incarnation (a digest of its log producer identity) and
// that daemon's connection id for the attached codex-tui client.
type codexResumeProof struct {
	Producer   string `json:"producer"`
	Connection string `json:"connection"`
}

var resumeConnectionPattern = regexp.MustCompile(`^[0-9]{1,19}$`)

// codexResumeProven reports whether run carries a verified resume proof and its
// client pin. An absent pin (or proof) never widens to a wildcard.
func codexResumeProven(run *remoteControlRun) bool {
	return run != nil && run.Resume != nil && digestPattern.MatchString(run.Resume.Producer) &&
		resumeConnectionPattern.MatchString(run.Resume.Connection) && codexVersionPattern.MatchString(run.ClientVersion)
}

// codexResumeRecordPython reads the daemon's native log (read-only) once for the
// prepared thread, by its structured columns (thread_id, process_uuid, target)
// and start-anchored request spans only:
//   - producer: the one process_uuid that created the thread under Orchid's own
//     thread/start request (the canonical daemon incarnation Orchid prepared it on);
//   - record: that producer spawned the resumed session for a codex-tui
//     thread/resume request, at the pinned client version;
//   - outcome: the same producer then received thread/turns/list on that
//     connection, which the TUI sends only after a successful resume response.
//
// Any codex-tui resume of the thread by another producer or connection refuses.
const codexResumeRecordPython = `import hashlib,json,os,re,sqlite3,sys
tid,ver,creator=sys.argv[1],sys.argv[2],sys.argv[3]
def out(state,**k):
 print(json.dumps(dict(state=state,**k)));sys.exit(0)
def span(m):
 return re.compile(r'app_server\.request\{otel\.kind="server" otel\.name="'+m+r'" rpc\.system="jsonrpc" rpc\.method="'+m+r'" rpc\.transport="unix_socket" rpc\.request_id=[^ {}]+ app_server\.connection_id=([0-9]+) app_server\.api_version="v2" app_server\.client_name="([^"]*)" app_server\.client_version="([^"]*)"\}:')
home=os.environ.get('CODEX_HOME') or os.path.join(os.environ['HOME'],'.codex')
db=os.path.join(home,'logs_2.sqlite')
if not os.path.isfile(db):out('pending')
c=sqlite3.connect('file:'+db+'?mode=ro',uri=True,timeout=5)
start,resume=span('thread/start'),span('thread/resume')
creators,tui,spawned=set(),set(),[]
for i,ts,pu,tgt,b in c.execute("select id,ts,process_uuid,target,feedback_log_body from logs where thread_id=? order by id",(tid,)):
 b=b or ''
 m=start.match(b)
 if m and tgt=='codex_core::shell_snapshot' and m.group(2)==creator and b[m.end():].startswith('app_server.thread_start.create_thread'):creators.add(pu)
 m=resume.match(b)
 if m and m.group(2)=='codex-tui':
  tui.add((pu,m.group(1)))
  if tgt=='codex_core::shell_snapshot' and b[m.end():].startswith('resume_thread_with_history:thread_spawn'):spawned.append((i,ts,pu,m.group(1),m.group(3)))
if len(creators)>1 or len(tui)>1:out('refused')
if not creators or not spawned:out('pending')
p=next(iter(creators))
i,ts,pu,conn,v=spawned[0]
if pu!=p or v!=ver:out('refused')
pfx='app-server request: thread/turns/list connection_id=ConnectionId('+conn+') '
if not c.execute("select 1 from logs where process_uuid=? and ts>=? and id>? and thread_id is null and target='codex_app_server::message_processor' and substr(feedback_log_body,1,?)=? limit 1",(p,ts,i,len(pfx),pfx)).fetchone():out('pending')
out('proven',producer=hashlib.sha256(p.encode()).hexdigest(),connection=conn)
`

// codexResumeRecord reads the native resume record once.
func (h Host) codexResumeRecord(ctx context.Context, run *remoteControlRun) (string, *codexResumeProof, error) {
	if run == nil || !privateNativeID(run.NativeSessionID) || !codexVersionPattern.MatchString(run.ClientVersion) {
		return "", nil, goalError("remote-control-identity-unconfirmed")
	}
	script := fmt.Sprintf("export HOME=%s; exec python3 -c %s %s %s %s", shq(h.agentHome()), shq(codexResumeRecordPython),
		shq(run.NativeSessionID), shq(run.ClientVersion), shq(codexGoalClientName))
	out, err := h.runRemote(ctx, script)
	var v struct {
		State      string `json:"state"`
		Producer   string `json:"producer"`
		Connection string `json:"connection"`
	}
	if err != nil || len(out) > 512 || decodeNativeJSON([]byte(strings.TrimSpace(out)), &v) != nil {
		return "", nil, goalError("remote-control-identity-unconfirmed")
	}
	switch v.State {
	case "pending", "refused":
		return v.State, nil, nil
	case "proven":
		proof := &codexResumeProof{Producer: v.Producer, Connection: v.Connection}
		if !digestPattern.MatchString(proof.Producer) || !resumeConnectionPattern.MatchString(proof.Connection) {
			return "", nil, goalError("remote-control-identity-unconfirmed")
		}
		return v.State, proof, nil
	}
	return "", nil, goalError("remote-control-identity-unconfirmed")
}

// awaitCodexResumeIdentity waits, within the launch budget, for the native
// resume record of the pinned TUI on the prepared thread, and stores it in the
// private run state. An ambiguous or foreign record refuses at once.
func (h Host) awaitCodexResumeIdentity(ctx context.Context, run *remoteControlRun) error {
	if run == nil || !privateNativeID(run.NativeSessionID) || !codexVersionPattern.MatchString(run.ClientVersion) { // guard:identity-version-pinned
		return goalError("remote-control-identity-unconfirmed")
	}
	for ctx.Err() == nil {
		state, proof, err := h.codexResumeRecord(ctx, run)
		if err == nil {
			switch state {
			case "proven": // guard:resume-proven
				run.Resume = proof
				return nil
			case "refused":
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
