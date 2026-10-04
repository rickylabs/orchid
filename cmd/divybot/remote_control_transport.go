package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// The daemon's owned Unix socket carries RFC6455, not JSONL. This bounded
// standard-library adapter runs as the host's agent user, including over SSH.
// Neither native identifiers nor protocol bodies are command arguments/logs.
const canonicalCodexBridge = `import os,sys,socket,stat,struct,base64,hashlib,select
def run():
 home=os.environ.get('CODEX_HOME') or os.path.join(os.environ['HOME'],'.codex')
 directory=os.path.join(home,'app-server-control'); path=os.path.join(directory,'app-server-control.sock')
 d=os.lstat(directory); before=os.lstat(path); uid=os.getuid()
 if not stat.S_ISDIR(d.st_mode) or d.st_uid!=uid or stat.S_IMODE(d.st_mode)!=0o700: raise ValueError()
 if not stat.S_ISSOCK(before.st_mode) or before.st_uid!=uid or stat.S_IMODE(before.st_mode)!=0o600: raise ValueError()
 s=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);s.settimeout(10);s.connect(path)
 if hasattr(socket,'SO_PEERCRED'):
  if struct.unpack('3i',s.getsockopt(socket.SOL_SOCKET,socket.SO_PEERCRED,12))[1]!=uid: raise ValueError()
 elif hasattr(s,'getpeereid'):
  if s.getpeereid()[0]!=uid: raise ValueError()
 else:
  import ctypes
  peer_uid=ctypes.c_uint();peer_gid=ctypes.c_uint()
  if ctypes.CDLL(None).getpeereid(s.fileno(),ctypes.byref(peer_uid),ctypes.byref(peer_gid))!=0 or peer_uid.value!=uid:raise ValueError()
 after=os.lstat(path)
 if (before.st_dev,before.st_ino,before.st_uid,before.st_mode)!=(after.st_dev,after.st_ino,after.st_uid,after.st_mode): raise ValueError()
 key=base64.b64encode(os.urandom(16)).decode()
 s.sendall(('GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: '+key+'\r\nSec-WebSocket-Version: 13\r\n\r\n').encode())
 data=bytearray()
 while not data.endswith(b'\r\n\r\n'):
  part=s.recv(1)
  if not part or len(data)>=8192: raise ValueError()
  data.extend(part)
 lines=bytes(data).decode('ascii').split('\r\n')
 if lines[0] not in ('HTTP/1.1 101 Switching Protocols','HTTP/1.1 101'): raise ValueError()
 headers={}
 for line in lines[1:]:
  if not line: continue
  k,v=line.split(':',1);k=k.lower().strip()
  if k in headers: raise ValueError()
  headers[k]=v.strip()
 expected=base64.b64encode(hashlib.sha1((key+'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
 if headers.get('sec-websocket-accept')!=expected or headers.get('upgrade','').lower()!='websocket' or headers.get('connection','').lower()!='upgrade': raise ValueError()
 def send(body,opcode=1):
  n=len(body)
  if n>1048576: raise ValueError()
  mask=os.urandom(4);head=bytes([0x80|opcode])
  head+=bytes([0x80|n]) if n<126 else bytes([0xfe])+struct.pack('!H',n) if n<65536 else bytes([0xff])+struct.pack('!Q',n)
  s.sendall(head+mask+bytes(v^mask[i%4] for i,v in enumerate(body)))
 def take(n):
  b=bytearray()
  while len(b)<n:
   p=s.recv(n-len(b))
   if not p: raise ValueError()
   b.extend(p)
  return bytes(b)
 pending=bytearray()
 while True:
  ready,_,_=select.select([s,sys.stdin],[],[],10)
  if not ready: raise ValueError()
  if sys.stdin in ready:
   b=os.read(sys.stdin.fileno(),4096)
   if not b: return
   pending.extend(b)
   if len(pending)>1048576: raise ValueError()
   while b'\n' in pending:
    line,_,rest=pending.partition(b'\n');pending=bytearray(rest);send(bytes(line))
  if s in ready:
   a,b=take(2);opcode=a&15;n=b&127
   if a&0x70 or not a&0x80 or b&0x80: raise ValueError()
   if n==126:n=struct.unpack('!H',take(2))[0]
   elif n==127:n=struct.unpack('!Q',take(8))[0]
   if n>1048576 or opcode not in (1,8,9,10) or (opcode>=8 and n>125):raise ValueError()
   body=take(n)
   if opcode==8:return
   if opcode==9:send(body,10);continue
   if opcode==10:continue
   body.decode('utf-8')
   if b'\n' in body or b'\r' in body:raise ValueError()
   sys.stdout.buffer.write(body+b'\n');sys.stdout.buffer.flush()
try:run()
except Exception:sys.exit(2)
`

func (h Host) withCanonicalConnection(ctx context.Context, thread string, use func(*goalRPC) error) error {
	if ctx.Err() != nil || (thread != "" && !privateNativeID(thread)) {
		return goalError("remote-control-binding-invalid")
	}
	if h.RemoteRun != nil && thread != h.RemoteRun.NativeSessionID {
		return goalError("remote-control-binding-invalid")
	}
	script := fmt.Sprintf("export HOME=%s; exec python3 -c %s", shq(h.agentHome()), shq(canonicalCodexBridge))
	return h.withGoalScript(ctx, script, thread, func(p *goalRPC) error {
		if h.RemoteRun != nil {
			if err := p.verifyRemoteThread(h.RemoteRun, false); err != nil {
				return err
			}
		}
		return use(p)
	})
}

func (h Host) withGoalScript(ctx context.Context, script, thread string, use func(*goalRPC) error) error {
	defer children.hold()()
	var cmd *exec.Cmd
	if h.isLocal() {
		cmd = exec.CommandContext(ctx, "bash", "-c", script)
	} else {
		cmd = exec.CommandContext(ctx, "ssh", append(h.sshBase(), h.SSH, script)...)
	}
	in, e := cmd.StdinPipe()
	if e != nil {
		return goalError("goal-transport-unavailable")
	}
	defer in.Close()
	out, e := cmd.StdoutPipe()
	if e != nil {
		return goalError("goal-transport-unavailable")
	}
	if cmd.Start() != nil {
		return goalError("goal-transport-unavailable")
	}
	defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	p := newGoalRPC(in, out, thread)
	p.ctx = ctx
	if e := p.initialize(); e != nil {
		return e
	}
	if ctx.Err() != nil {
		return goalError("goal-transport-unavailable")
	}
	e = use(p)
	if ctx.Err() != nil {
		return goalError("goal-transport-unavailable")
	}
	return e
}

func (p *goalRPC) remoteConnected() error {
	raw, err := p.request("remoteControl/status/read", map[string]any{})
	var status struct {
		Status string `json:"status"`
	}
	if err != nil || decodeNativeJSON(raw, &status) != nil || status.Status != "connected" {
		return goalError("remote-control-unconfirmed")
	}
	return nil
}

func remoteThreadParams(r *remoteControlRun, env map[string]string) (map[string]any, error) {
	if r == nil || !validRemoteCwd(r.Cwd) || r.Name == "" || !validCodexEffort(r.Effort) {
		return nil, goalError("remote-control-binding-invalid")
	}
	config := map[string]any{"projects": map[string]any{r.Cwd: map[string]string{"trust_level": "trusted"}}}
	if r.Effort != "" {
		config["model_reasoning_effort"] = r.Effort
	}
	if len(env) > 0 {
		config["shell_environment_policy"] = map[string]any{"set": env}
	}
	params := map[string]any{"cwd": r.Cwd, "approvalPolicy": "never", "sandbox": "danger-full-access", "config": config, "ephemeral": false, "historyMode": "paginated"}
	if r.Model != "" {
		params["model"] = r.Model
	}
	return params, nil
}

func decodeRemoteThread(raw []byte, r *remoteControlRun, newThread bool) error {
	var v struct {
		Thread struct {
			ID   string  `json:"id"`
			Cwd  string  `json:"cwd"`
			Name *string `json:"name"`
		} `json:"thread"`
		Model          string `json:"model"`
		Cwd            string `json:"cwd"`
		ApprovalPolicy string `json:"approvalPolicy"`
		Sandbox        struct {
			Type string `json:"type"`
		} `json:"sandbox"`
		Effort string `json:"reasoningEffort"`
	}
	if decodeNativeJSON(raw, &v) != nil || !privateNativeID(v.Thread.ID) || v.Cwd != r.Cwd || v.Thread.Cwd != r.Cwd ||
		v.ApprovalPolicy != "never" || v.Sandbox.Type != "dangerFullAccess" || (r.Model != "" && v.Model != r.Model) || (r.Effort != "" && v.Effort != r.Effort) || (!newThread && v.Thread.ID != r.NativeSessionID) {
		return goalError("remote-control-binding-invalid")
	}
	if newThread {
		r.NativeSessionID = v.Thread.ID
		if v.Model == "" {
			return goalError("remote-control-binding-invalid")
		}
		r.Model = v.Model
		if r.Effort == "" {
			r.Effort = v.Effort
		}
	}
	return nil
}

func (p *goalRPC) verifyRemoteThread(r *remoteControlRun, resume bool) error {
	if r.NativeSessionID != p.thread {
		return goalError("remote-control-binding-invalid")
	}
	if resume {
		raw, err := p.request("thread/resume", map[string]any{"threadId": p.thread})
		if err != nil {
			return err
		}
		if err = decodeRemoteThread(raw, r, false); err != nil {
			return err
		}
	}
	raw, err := p.request("thread/read", map[string]any{"threadId": p.thread, "includeTurns": false})
	var v struct {
		Thread struct {
			ID     string          `json:"id"`
			Cwd    string          `json:"cwd"`
			Name   *string         `json:"name"`
			Model  string          `json:"model"`
			Effort json.RawMessage `json:"reasoningEffort"`
		} `json:"thread"`
	}
	if err != nil || decodeNativeJSON(raw, &v) != nil || v.Thread.ID != p.thread || v.Thread.Cwd != r.Cwd || v.Thread.Name == nil || *v.Thread.Name != r.Name {
		return goalError("remote-control-binding-invalid")
	}
	if v.Thread.Model != "" && v.Thread.Model != r.Model {
		return goalError("remote-control-binding-invalid")
	}
	if len(v.Thread.Effort) > 0 && string(v.Thread.Effort) != "null" {
		var effort string
		if json.Unmarshal(v.Thread.Effort, &effort) != nil || effort != r.Effort {
			return goalError("remote-control-binding-invalid")
		}
	}
	return nil
}

func (h Host) prepareRemoteCodex(ctx context.Context, run *remoteControlRun, env map[string]string) error {
	params, err := remoteThreadParams(run, env)
	if err != nil {
		return err
	}
	return h.withCanonicalConnection(ctx, "", func(p *goalRPC) error {
		return p.prepareRemoteThread(run, params)
	})
}

func (p *goalRPC) prepareRemoteThread(run *remoteControlRun, params map[string]any) error {
	if err := p.remoteConnected(); err != nil {
		return err
	}
	raw, err := p.request("thread/start", params)
	if err != nil {
		return err
	}
	if err = decodeRemoteThread(raw, run, true); err != nil {
		return err
	}
	p.thread = run.NativeSessionID
	if _, err = p.request("thread/name/set", map[string]any{"threadId": p.thread, "name": run.Name}); err != nil {
		return err
	}
	return p.verifyRemoteThread(run, false)
}
