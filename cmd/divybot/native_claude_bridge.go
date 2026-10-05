package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Native Claude Remote Control connection evidence, observe-only.
//
// Claude Code keeps its own per-process session record, sessions/<pid>.json,
// naming the process (pid and start time), its native session id and, once a
// Remote Control bridge exists, bridgeSessionId. Today's Remote Control proof
// for Claude still matches the visible footer; this records beside it, in the
// private native-evidence shadow, what that native record says for the exact
// process in the run's pane. Nothing reads it back and it never reads the pane.

const (
	shadowClaudeSession    shadowSource = "claude-session"
	claudeBridgeReadBudget              = 2 * time.Second
)

var errClaudeBridgeUnknown = errors.New("claude-bridge-unknown")

// claudeBridgePython matches Claude's session record to a live process by pid,
// its kernel start time and the run's native session id. A record that cannot
// be read or does not match is not a match.
const claudeBridgePython = `import json,os,sys
sid=sys.argv[1]; pids=[int(x) for x in sys.argv[2:]]
home=os.environ.get('CLAUDE_CONFIG_DIR') or os.path.join(os.environ['HOME'],'.claude')
m=[]
for pid in pids:
 try:
  start=open('/proc/%d/stat'%pid).read().rsplit(')',1)[1].split()[19]
  d=json.load(open(os.path.join(home,'sessions','%d.json'%pid)))
 except Exception:
  continue
 if isinstance(d,dict) and d.get('pid')==pid and str(d.get('procStart'))==start and d.get('sessionId')==sid:
  m.append(bool(d.get('bridgeSessionId')))
print(json.dumps({'matches':len(m),'bridge':len(m)==1 and m[0]}))
`

// claudeBridge reports whether Claude's own session record for the exact
// process in the pane names a Remote Control bridge. ok=false is unknown:
// no single matching record, or anything unreadable.
func (h Host) claudeBridge(ctx context.Context, pane, sessionID string) (connected, ok bool) {
	if !privateNativeID(sessionID) {
		return false, false
	}
	out, err := h.herdr(ctx, "pane", "process-info", "--pane", pane)
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
	raw, err := h.runRemote(ctx, script)
	var v struct {
		Matches int  `json:"matches"`
		Bridge  bool `json:"bridge"`
	}
	if err != nil || len(raw) > 256 || decodeNativeJSON([]byte(strings.TrimSpace(raw)), &v) != nil || v.Matches != 1 { // guard:claude-bridge-single-match
		return false, false
	}
	return v.Bridge, true
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

// observeClaudeBridge publishes one native connection snapshot for a Claude
// Remote Control job into its shadow scope. Unknown evidence revokes the
// source instead of leaving an older verdict live.
func (h Host) observeClaudeBridge(ctx context.Context, j *Job) {
	if j == nil || j.Agent != "claude" || j.RemoteControl == nil || h.ShadowScope == nil {
		return
	}
	defer shadowContain()
	rctx, cancel := context.WithTimeout(ctx, claudeBridgeReadBudget)
	defer cancel()
	p := h.ShadowScope.open(shadowClaudeSession, j.RemoteControl.NativeSessionID)
	if p == nil {
		return
	}
	connected, ok := h.claudeBridge(rctx, j.Pane, j.RemoteControl.NativeSessionID)
	if !ok {
		p.close(errClaudeBridgeUnknown)
		return
	}
	p.connection(connected) // guard:claude-bridge-publish
	p.close(nil)
}
