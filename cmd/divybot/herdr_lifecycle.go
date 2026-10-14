package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Vendor hooks report semantic lifecycle through Herdr; they never infer state
// from the terminal or turn a stopped turn into assignment completion. The
// command is scoped to the launch's pane, workspace, label and checkout. Herdr
// supplies the socket scope in its hook environment.
const nativeLifecycleHook = `import fcntl,json,os,stat,subprocess,sys,tempfile,time
seq=time.time_ns()
def unique(pairs):
 d={}
 for k,v in pairs:
  if k in d: raise ValueError()
  d[k]=v
 return d
def run():
 agent,label,pane,workspace,cwd,event_file=sys.argv[1:]
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
 source='herdr:'+agent
 args=[herdr,'pane','release-agent' if kind=='SessionEnd' else 'report-agent',pane,'--source',source,'--agent',agent,'--seq',str(seq)]
 if kind!='SessionEnd':args+=['--state',states[kind],'--agent-session-id',sid]
 # The file records the vendor event, never a detected screen state. Publish it
 # before Herdr's state event so subscribers can read it on that invalidation.
 directory=os.path.dirname(event_file);parent=os.path.dirname(directory)
 for path in (parent,directory):
  try:os.mkdir(path,0o700)
  except FileExistsError:pass
  st=os.lstat(path)
  if not stat.S_ISDIR(st.st_mode) or st.st_uid!=os.getuid() or stat.S_IMODE(st.st_mode)!=0o700:return
 lock=os.open(event_file+'.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
 try:
  fcntl.flock(lock,fcntl.LOCK_EX)
  try:
   old=os.open(event_file,os.O_RDONLY|os.O_NOFOLLOW)
   with os.fdopen(old) as f:previous=json.load(f,object_pairs_hook=unique)
   if previous['seq']>=seq:return
  except FileNotFoundError:pass
  record={'agent':agent,'name':label,'pane':pane,'workspace':workspace,'cwd':cwd,'terminal':a.get('terminal_id'),'session':sid,'event':kind,'seq':seq}
  fd,temp=tempfile.mkstemp(dir=directory)
  try:
   with os.fdopen(fd,'w') as f:json.dump(record,f);f.flush();os.fsync(f.fileno())
   os.replace(temp,event_file)
  finally:
   if os.path.exists(temp):os.unlink(temp)
  subprocess.run(args,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=1,check=True)
  if kind!='SessionEnd':
   applied=json.loads(subprocess.run([herdr,'agent','get',pane],capture_output=True,timeout=1,check=True).stdout,object_pairs_hook=unique).get('result',{}).get('agent',{})
   bound=applied.get('agent_session',{})
   status=applied.get('agent_status')
   state_matches=status==states[kind] or states[kind]=='idle' and status=='done'
   if not state_matches or (applied.get('terminal_id'),applied.get('name'),bound.get('source'),bound.get('agent'),bound.get('kind'),bound.get('value'))!=(a.get('terminal_id'),label,source,agent,'id',sid):
    os.unlink(event_file)
 finally:os.close(lock)
try:run()
except Exception:pass
print('{}')
`

func nativeLifecycleHooks(agent, label, pane, workspace, cwd string) map[string]any {
	command := "python3 -c " + shq(nativeLifecycleHook)
	for _, value := range []string{agent, label, pane, workspace, cwd, vendorLifecycleFile(agent, label, pane, workspace, cwd)} {
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
	return source == "herdr:"+agent || (agent == "agy" && source == "herdr:antigravity_cli")
}

// Keep sequence comparisons meaningful across native identity publication.
func sameNativeSession(a, b json.RawMessage) bool {
	if len(a) == 0 || strings.TrimSpace(string(a)) == "null" {
		return true // the original occupant may publish its first session
	}
	var first, second struct{ Source, Agent, Kind, Value string }
	if json.Unmarshal(a, &first) != nil || json.Unmarshal(b, &second) != nil {
		return false
	}
	return first.Agent == second.Agent && first.Kind == second.Kind && first.Value == second.Value &&
		lifecycleSource(first.Source, first.Agent) && lifecycleSource(second.Source, second.Agent)
}

func vendorLifecycleFile(agent, label, pane, workspace, cwd string) string {
	key := shaText([]byte(agent + "\x00" + label + "\x00" + pane + "\x00" + workspace + "\x00" + cwd))
	return filepath.Join(filepath.Dir(cwd), ".divybot-native", key, "lifecycle.json")
}
