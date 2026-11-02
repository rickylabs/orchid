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
	Reason    string `json:"reason,omitempty"`
	// Failure is Claude's typed StopFailure error, never terminal or message text.
	Failure string `json:"failure,omitempty"`
	Seq     uint64 `json:"seq"`
}

// claudeStopFailures is Claude Code's closed StopFailure error enum (hook input
// field "error"); the hook records only these values.
var claudeStopFailures = map[string]bool{
	"authentication_failed": true, "oauth_org_not_allowed": true, "account_on_hold": true, "verification_required": true,
	"billing_error": true, "rate_limit": true, "overloaded": true, "invalid_request": true, "model_not_found": true,
	"server_error": true, "unknown": true, "max_output_tokens": true, "cloud_credential_error": true,
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
		e.Cwd != a.Cwd || e.Terminal == "" || e.Terminal != a.TerminalID || !privateNativeID(e.Session) || vendorEventState(e.Event) == "" ||
		e.Failure != "" && (e.Agent != "claude" || e.Event != "StopFailure" || !claudeStopFailures[e.Failure]) {
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
except FileNotFoundError:print('{}')
except Exception:print('{"unreadable":true}')
`

func (h Host) vendorLifecycle(ctx context.Context, a AgentInfo) *vendorLifecycleEvent {
	event, _ := h.vendorLifecycleObserved(ctx, a)
	return event
}

// vendorLifecycleObserved also reports whether the receipt source was read.
// An absent, foreign or invalid receipt is a completed read with no event; a
// failed command or an unreadable receipt is not, so a caller can retry it.
func (h Host) vendorLifecycleObserved(ctx context.Context, a AgentInfo) (*vendorLifecycleEvent, bool) {
	if (a.Agent != "claude" && a.Agent != "codex") || a.Cwd == "" || a.Name == "" || a.TerminalID == "" {
		return nil, true
	}
	file := vendorLifecycleFile(a.Agent, a.Name, a.PaneID, a.WorkspaceID, a.Cwd)
	out, err := h.runBounded(ctx, fmt.Sprintf("exec python3 -c %s %s", shq(vendorLifecycleRead), shq(file)))
	var unread struct {
		Unreadable bool `json:"unreadable"`
	}
	if err != nil || len(out) > 4096 || json.Unmarshal([]byte(out), &unread) == nil && unread.Unreadable {
		return nil, false
	}
	var event vendorLifecycleEvent
	if strictJSON([]byte(out), &event) != nil || !event.matches(a) {
		return nil, true
	}
	return &event, true
}
