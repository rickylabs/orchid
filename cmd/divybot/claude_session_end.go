package main

import (
	"context"
	"fmt"
	"strings"
)

// Direct identity read for the native process captured from Claude's own
// session record. Only ENOENT or a definite exited/replaced identity is ended;
// permission, parsing and transport errors remain unknown. No ancestry join.
const claudeSessionProcessState = `import os,sys
try:
 try:data=open('/proc/%d/stat'%int(sys.argv[1])).read()
 except FileNotFoundError:print('ended');sys.exit(0)
 fields=data.rsplit(')',1)[1].split()
 assert len(fields)>19
 print('ended' if fields[0] in ('Z','X','x') or fields[19]!=sys.argv[2] else 'active')
except Exception:print('unknown')
`

func claudeEndEventMatches(e *vendorLifecycleEvent, j *Job) bool {
	if e == nil || j == nil || j.RemoteControl == nil || e.Event != "SessionEnd" || e.Seq == 0 ||
		e.Agent != "claude" || e.Name != j.Label || e.Pane != j.Pane || e.Workspace != j.Workspace ||
		e.Cwd != j.RemoteControl.Cwd || e.Session != j.RemoteControl.NativeSessionID || e.Terminal == "" ||
		(j.TerminalID != "" && e.Terminal != j.TerminalID) {
		return false
	}
	switch e.Reason {
	case "logout", "prompt_input_exit", "other":
		return true
	}
	return false // clear, resume, legacy and future unknown reasons are not exits
}

func (h Host) claudeSessionEnded(ctx context.Context, j *Job) bool {
	if j == nil || j.RemoteControl == nil {
		return false
	}
	p := j.RemoteControl.ClaudeProcess
	if p == nil || p.PID <= 1 || !claudeProcessStart.MatchString(p.Start) {
		return false
	}
	read := func() bool {
		file := vendorLifecycleFile(j.Agent, j.Label, j.Pane, j.Workspace, j.RemoteControl.Cwd)
		out, err := h.runBounded(ctx, "exec python3 -c "+shq(vendorLifecycleRead)+" "+shq(file))
		var event vendorLifecycleEvent
		return err == nil && len(out) <= 4096 && strictJSON([]byte(out), &event) == nil && claudeEndEventMatches(&event, j)
	}
	if !read() {
		return false
	}
	out, err := h.runBounded(ctx, fmt.Sprintf("exec python3 -c %s %d %s", shq(claudeSessionProcessState), p.PID, shq(p.Start)))
	return err == nil && strings.TrimSpace(out) == "ended" && read() && ctx.Err() == nil
}
