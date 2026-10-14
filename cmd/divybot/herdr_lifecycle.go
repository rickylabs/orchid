package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Vendor hooks report semantic lifecycle through Herdr; they never infer state
// from the terminal or turn a stopped turn into assignment completion. The
// command is scoped to the launch's pane, workspace, label and checkout. Herdr
// supplies the socket scope in its hook environment.
const nativeLifecycleHook = `import json,os,subprocess,sys,time
seq=time.time_ns()
def unique(pairs):
 d={}
 for k,v in pairs:
  if k in d: raise ValueError()
  d[k]=v
 return d
def run():
 agent,label,pane,workspace,cwd=sys.argv[1:]
 event=json.loads(sys.stdin.read(1048577),object_pairs_hook=unique)
 sid=event.get('session_id');kind=event.get('hook_event_name')
 if not isinstance(sid,str) or not sid or len(sid)>256 or any(c.isspace() or c in '/\\' for c in sid):return
 if event.get('agent_id') or event.get('cwd')!=cwd:return
 states={'SessionStart':'idle','UserPromptSubmit':'working','PreToolUse':'working','PostToolUse':'working','PermissionRequest':'blocked','Stop':'idle','Interrupt':'idle','StopFailure':'blocked'}
 if kind not in states and kind!='SessionEnd':return
 if kind=='SessionStart' and event.get('source') not in ('startup','resume'):return
 herdr=os.environ.get('HERDR_BIN_PATH') or 'herdr'
 output=subprocess.run([herdr,'agent','get',pane],capture_output=True,timeout=1,check=True).stdout
 result=json.loads(output,object_pairs_hook=unique).get('result',{})
 a=result.get('agent',{})
 if (a.get('agent'),a.get('name'),a.get('pane_id'),a.get('workspace_id'),a.get('cwd'))!=(agent,label,pane,workspace,cwd):return
 session=a.get('agent_session')
 if session:
  if (session.get('agent'),session.get('kind'),session.get('value'))!=(agent,'id',sid):return
 elif kind!='SessionStart':return
 source='orchid:'+agent
 args=[herdr,'pane','release-agent' if kind=='SessionEnd' else 'report-agent',pane,'--source',source,'--agent',agent,'--seq',str(seq)]
 if kind!='SessionEnd':args+=['--state',states[kind],'--agent-session-id',sid]
 subprocess.run(args,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=1,check=True)
try:run()
except Exception:pass
print('{}')
`

func nativeLifecycleHooks(agent, label, pane, workspace, cwd string) map[string]any {
	command := "python3 -c " + shq(nativeLifecycleHook)
	for _, value := range []string{agent, label, pane, workspace, cwd} {
		command += " " + shq(value)
	}
	hooks := map[string]any{}
	events := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Stop", "SessionEnd"}
	if agent == "claude" {
		events = append(events, "StopFailure")
	} else {
		events = append(events, "Interrupt")
	}
	for _, event := range events {
		timeout := 5
		if event == "SessionEnd" || event == "Interrupt" {
			timeout = 3
		}
		hooks[event] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": timeout}}}}
	}
	return hooks
}

// CLI config overrides use TOML inline tables. Commands are JSON basic strings,
// whose escapes are valid TOML; no hook or user config file is overwritten.
func nativeCodexHookArgs(hooks map[string]any) []string {
	args := []string{"-c", "features.hooks=true"}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Stop", "SessionEnd", "Interrupt"} {
		entry := hooks[event].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
		command, _ := json.Marshal(entry["command"])
		args = append(args, "-c", fmt.Sprintf("hooks.%s=[{hooks=[{type=\"command\",command=%s,timeout=%d}]}]", event, command, entry["timeout"]))
	}
	return args
}

func lifecycleArgs(agent, label, pane, workspace, cwd string) []string {
	if agent != "claude" && agent != "codex" {
		return nil
	}
	hooks := nativeLifecycleHooks(agent, label, pane, workspace, cwd)
	if agent == "codex" {
		return nativeCodexHookArgs(hooks)
	}
	settings, _ := json.Marshal(map[string]any{"hooks": hooks})
	return []string{"--settings", string(settings)}
}

func lifecycleSource(source, agent string) bool {
	return source == "herdr:"+agent || source == "orchid:"+agent
}

// Keep sequence comparisons meaningful across native identity publication.
func sameNativeSession(a, b json.RawMessage) bool {
	if len(a) == 0 || strings.TrimSpace(string(a)) == "null" {
		return true // the original occupant may publish its first session
	}
	var first, second struct{ Agent, Kind, Value string }
	return json.Unmarshal(a, &first) == nil && json.Unmarshal(b, &second) == nil && first == second
}
