package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// Private provenance for the real vendor hook event. This is not Herdr's
// screen_detection_skipped flag, which covers only its full lifecycle allowlist.
type vendorLifecycleEvent struct {
	Agent     string `json:"agent"`
	Name      string `json:"name"`
	Pane      string `json:"pane"`
	Workspace string `json:"workspace"`
	Cwd       string `json:"cwd"`
	Terminal  string `json:"terminal"`
	Session   string `json:"session"`
	Event     string `json:"event"`
	Seq       uint64 `json:"seq"`
}

func vendorEventState(event string) string {
	switch event {
	case "SessionStart", "Stop", "Interrupt":
		return "idle"
	case "UserPromptSubmit", "PreToolUse", "PostToolUse":
		return "working"
	case "PermissionRequest", "StopFailure":
		return "blocked"
	}
	return ""
}

func (e *vendorLifecycleEvent) matches(a AgentInfo) bool {
	if e == nil || e.Seq == 0 || e.Agent != a.Agent || e.Name != a.Name || e.Pane != a.PaneID || e.Workspace != a.WorkspaceID ||
		e.Cwd != a.Cwd || e.Terminal == "" || e.Terminal != a.TerminalID || !privateNativeID(e.Session) || vendorEventState(e.Event) == "" {
		return false
	}
	var session struct{ Source, Agent, Kind, Value string }
	return json.Unmarshal(a.AgentSession, &session) == nil && session.Source == "herdr:"+a.Agent && session.Agent == a.Agent && session.Kind == "id" && session.Value == e.Session
}

const vendorLifecycleRead = `import json,os,stat,sys
try:
 path=sys.argv[1]
 for directory in (os.path.dirname(path),os.path.dirname(os.path.dirname(path))):
  s=os.lstat(directory)
  assert stat.S_ISDIR(s.st_mode) and s.st_uid==os.getuid() and stat.S_IMODE(s.st_mode)==0o700
 fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
 with os.fdopen(fd) as f:
  s=os.fstat(f.fileno());assert stat.S_ISREG(s.st_mode) and s.st_uid==os.getuid() and stat.S_IMODE(s.st_mode)==0o600
  data=f.read(4097);assert len(data)<=4096
  print(data)
except Exception:print('{}')
`

func (h Host) vendorLifecycle(ctx context.Context, a AgentInfo) *vendorLifecycleEvent {
	if (a.Agent != "claude" && a.Agent != "codex") || a.Cwd == "" || a.Name == "" || a.TerminalID == "" {
		return nil
	}
	file := vendorLifecycleFile(a.Agent, a.Name, a.PaneID, a.WorkspaceID, a.Cwd)
	out, err := h.runBounded(ctx, fmt.Sprintf("exec python3 -c %s %s", shq(vendorLifecycleRead), shq(file)))
	var event vendorLifecycleEvent
	if err != nil || len(out) > 4096 || strictJSON([]byte(out), &event) != nil || !event.matches(a) {
		return nil
	}
	return &event
}
