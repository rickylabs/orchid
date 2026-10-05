package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Native Claude Remote Control evidence, observe-only.
//
// Claude Code keeps its own per-process session record, sessions/<pid>.json,
// naming the process (pid and kernel start time) and its native session id.
// It may also carry bridgeSessionId. That field is undocumented and has no
// disconnect semantics, so it is identity metadata only ("a Remote Control
// bridge identity is recorded"), never a connection verdict: Claude's native
// connection stays unknown (no-official-surface) until an official state
// surface exists. Today's footer proof is unchanged and nothing reads this back.

const (
	shadowClaudeSession    shadowSource = "claude-session"
	shadowBridgeIdentity   shadowFact   = "bridge-identity"
	claudeBridgeReadBudget              = 2 * time.Second
	claudeBridgeWaitDelay               = 200 * time.Millisecond
)

var errClaudeBridgeUnknown = errors.New("claude-bridge-unknown")

// claudeBridgePython matches Claude's session record to a live process by pid,
// its kernel start time and the run's native session id, with a strict schema:
// duplicate keys or a wrong type make the evidence malformed (unknown).
const claudeBridgePython = `import json,os,sys
sid=sys.argv[1]; pids=[int(x) for x in sys.argv[2:]]
home=os.environ.get('CLAUDE_CONFIG_DIR') or os.path.join(os.environ['HOME'],'.claude')
def pairs(kv):
 d={}
 for k,v in kv:
  if k in d: raise ValueError()
  d[k]=v
 return d
m=[]; bad=0
for pid in pids:
 try:
  start=open('/proc/%d/stat'%pid).read().rsplit(')',1)[1].split()[19]
  raw=open(os.path.join(home,'sessions','%d.json'%pid)).read()
 except Exception:
  continue
 try:
  d=json.loads(raw,object_pairs_hook=pairs)
 except Exception:
  bad+=1; continue
 if not isinstance(d,dict) or type(d.get('pid')) is not int or type(d.get('sessionId')) is not str or type(d.get('procStart')) is not str:
  bad+=1; continue
 if d['pid']!=pid or d['procStart']!=start or d['sessionId']!=sid:
  continue
 if 'bridgeSessionId' not in d:
  m.append(False)
 elif type(d['bridgeSessionId']) is str and d['bridgeSessionId']:
  m.append(True)
 else:
  bad+=1
print(json.dumps({'matches':len(m),'present':len(m)==1 and m[0],'malformed':bad>0}))
`

// runBounded runs a host script whose whole process group, and the wait for
// its output pipes, ends with ctx: no inherited pipe outlives the bound.
func (h Host) runBounded(ctx context.Context, script string) (string, error) {
	defer children.hold()()
	var cmd *exec.Cmd
	if h.isLocal() {
		cmd = exec.CommandContext(ctx, "bash", "-c", script)
	} else {
		cmd = exec.CommandContext(ctx, "ssh", append(h.sshBase(), h.SSH, script)...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) } // guard:bridge-kill-group
	cmd.WaitDelay = claudeBridgeWaitDelay                                                // guard:bridge-wait-delay
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// claudeBridgeIdentity reports whether Claude's own, strictly valid session
// record for the exact process in the pane records a bridge identity.
// ok=false is unknown: no single match, or any malformed record.
func (h Host) claudeBridgeIdentity(ctx context.Context, pane, sessionID string) (present, ok bool) {
	if !privateNativeID(sessionID) {
		return false, false
	}
	herdr := fmt.Sprintf(`export HOME=%s; export PATH="$HOME/.local/bin:/usr/local/bin:$PATH"; herdr %s %s %s %s`,
		shq(h.agentHome()), shq("pane"), shq("process-info"), shq("--pane"), shq(pane))
	out, err := h.runBounded(ctx, herdr)
	if err != nil {
		return false, false
	}
	pids, ok := paneProcessPIDs(out)
	if !ok {
		return false, false
	}
	args := make([]string, 0, len(pids))
	for _, pid := range pids {
		args = append(args, strconv.Itoa(pid))
	}
	script := fmt.Sprintf("export HOME=%s; exec python3 -c %s %s %s", shq(h.agentHome()), shq(claudeBridgePython), shq(sessionID), strings.Join(args, " "))
	raw, err := h.runBounded(ctx, script)
	var v struct {
		Matches   int  `json:"matches"`
		Present   bool `json:"present"`
		Malformed bool `json:"malformed"`
	}
	if err != nil || len(raw) > 256 || decodeNativeJSON([]byte(strings.TrimSpace(raw)), &v) != nil || v.Malformed || v.Matches != 1 { // guard:claude-bridge-single-match
		return false, false
	}
	return v.Present, true
}

// paneProcessPIDs lists the pane's foreground pids from Herdr's structured
// process info. The names are not trusted: the session record binds the pid.
func paneProcessPIDs(out string) ([]int, bool) {
	raw, err := herdrUnwrap(out)
	var row struct {
		ProcessInfo struct {
			Processes []struct {
				PID int `json:"pid"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	}
	if err != nil || decodeNativeJSON(raw, &row) != nil || len(row.ProcessInfo.Processes) == 0 || len(row.ProcessInfo.Processes) > 16 {
		return nil, false
	}
	pids := []int{}
	for _, p := range row.ProcessInfo.Processes {
		if p.PID <= 1 {
			return nil, false
		}
		pids = append(pids, p.PID)
	}
	return pids, true
}

// readClaudeBridge publishes one bridge-identity snapshot into the job's
// shadow scope, bounded by its own budget. Unknown evidence revokes the source.
func (h Host) readClaudeBridge(j *Job) {
	defer shadowContain()
	ctx, cancel := context.WithTimeout(context.Background(), claudeBridgeReadBudget) // guard:bridge-own-context
	defer cancel()
	p := h.ShadowScope.open(shadowClaudeSession, j.RemoteControl.NativeSessionID)
	if p == nil {
		return
	}
	present, ok := h.claudeBridgeIdentity(ctx, j.Pane, j.RemoteControl.NativeSessionID)
	if !ok {
		p.close(errClaudeBridgeUnknown)
		return
	}
	p.bridgeIdentity(present) // guard:claude-bridge-publish
	p.close(nil)
}

// observeClaudeBridge starts the read off the decision path: asynchronous,
// one in flight per job, on its own context, never the caller's deadline.
func (h Host) observeClaudeBridge(j *Job) {
	if j == nil || j.Agent != "claude" || j.RemoteControl == nil || h.ShadowScope == nil {
		return
	}
	if !h.ShadowScope.begin(string(shadowClaudeSession)) { // guard:bridge-single-flight
		return
	}
	go func() { // guard:bridge-async
		defer h.ShadowScope.end(string(shadowClaudeSession))
		h.readClaudeBridge(j)
	}()
}
