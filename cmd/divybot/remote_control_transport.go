package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// The daemon's owned Unix socket carries RFC6455, not JSONL. This bounded
// standard-library adapter runs as the host's agent user, including over SSH.
// Neither native identifiers nor protocol bodies are command arguments/logs.
const canonicalCodexBridge = `import os,sys,socket,stat,struct,base64,hashlib,select
class EndpointRefusal(Exception):pass
def refuse(reason):raise EndpointRefusal(reason)
def stamp(s):return (s.st_dev,s.st_ino,s.st_uid,s.st_mode,s.st_mtime_ns,s.st_ctime_ns)
def dir_stamp(s):return (s.st_dev,s.st_ino,s.st_uid,s.st_mode)
def run():
 home=os.environ.get('CODEX_HOME') or os.path.join(os.environ['HOME'],'.codex')
 directory=os.path.join(home,'app-server-control'); entry=os.path.join(directory,'app-server-control.sock');path=entry
 d=os.lstat(directory); entry_before=os.lstat(entry); uid=os.getuid()
 if not stat.S_ISDIR(d.st_mode) or d.st_uid!=uid or stat.S_IMODE(d.st_mode)!=0o700:refuse('remote-control-endpoint-directory')
 if stat.S_ISLNK(entry_before.st_mode):
  if entry_before.st_uid!=uid:refuse('remote-control-endpoint-entry-owner')
  # One readlink, never recursive realpath/stat or dialing the mutable alias.
  link=os.readlink(entry)
  path=os.path.abspath(link if os.path.isabs(link) else os.path.join(directory,link))
 try:before=os.lstat(path)
 except FileNotFoundError:refuse('remote-control-endpoint-dangling')
 if stat.S_ISLNK(before.st_mode):refuse('remote-control-endpoint-link-chain')
 if not stat.S_ISSOCK(before.st_mode):refuse('remote-control-endpoint-not-socket')
 if before.st_uid!=uid:refuse('remote-control-endpoint-socket-owner')
 if stat.S_IMODE(before.st_mode)!=0o600:refuse('remote-control-endpoint-socket-mode')
 # Reject symlinks in target parents too. Pin their identity, not directory
 # timestamps: unrelated native files may legitimately change timestamps.
 parents=[];parent=os.path.dirname(path);current=os.path.sep
 for part in parent.split(os.path.sep)[1:]:
  current=os.path.join(current,part);p=os.lstat(current)
  if stat.S_ISLNK(p.st_mode):refuse('remote-control-endpoint-link-chain')
  if not stat.S_ISDIR(p.st_mode):refuse('remote-control-endpoint-parent-unsafe')
  parents.append((current,dir_stamp(p)))
 p=os.lstat(parent)
 if stat.S_IMODE(p.st_mode)&0o022:refuse('remote-control-endpoint-parent-unsafe')
 parent_fd=os.open(parent,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
 if dir_stamp(os.fstat(parent_fd))!=dir_stamp(p):refuse('remote-control-endpoint-changed')
 s=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);s.settimeout(10);s.connect(path)
 if hasattr(socket,'SO_PEERCRED'):
  if struct.unpack('3i',s.getsockopt(socket.SOL_SOCKET,socket.SO_PEERCRED,12))[1]!=uid:refuse('remote-control-endpoint-peer-owner')
 elif hasattr(s,'getpeereid'):
  if s.getpeereid()[0]!=uid: raise ValueError()
 else:
  import ctypes
  peer_uid=ctypes.c_uint();peer_gid=ctypes.c_uint()
  if ctypes.CDLL(None).getpeereid(s.fileno(),ctypes.byref(peer_uid),ctypes.byref(peer_gid))!=0 or peer_uid.value!=uid:raise ValueError()
 try:
  if dir_stamp(d)!=dir_stamp(os.lstat(directory)):refuse('remote-control-endpoint-changed')
  if stamp(entry_before)!=stamp(os.lstat(entry)):refuse('remote-control-endpoint-changed')
  if stamp(before)!=stamp(os.lstat(path)):refuse('remote-control-endpoint-changed')
  if dir_stamp(p)!=dir_stamp(os.fstat(parent_fd)):refuse('remote-control-endpoint-changed')
  for name,snapshot in parents:
   if snapshot!=dir_stamp(os.lstat(name)):refuse('remote-control-endpoint-changed')
 except OSError:refuse('remote-control-endpoint-changed')
 os.close(parent_fd)
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
except EndpointRefusal as e:sys.stderr.write(str(e)+'\n');sys.exit(2)
except Exception:sys.stderr.write('remote-control-endpoint-unavailable\n');sys.exit(2)
`

// Read only after the helper has been joined. Unknown or excessive native/SSH
// stderr cannot become a public diagnostic; only these exact closed codes can.
type remoteBridgeDiagnostic struct {
	body     []byte
	overflow bool
}

func (d *remoteBridgeDiagnostic) Write(b []byte) (int, error) {
	n := len(b)
	if len(d.body)+n > 128 {
		d.overflow = true
	}
	if left := 128 - len(d.body); left > 0 {
		if len(b) > left {
			b = b[:left]
		}
		d.body = append(d.body, b...)
	}
	return n, nil
}

func (d *remoteBridgeDiagnostic) reason() error {
	if d.overflow {
		return nil
	}
	switch string(d.body) {
	case "remote-control-endpoint-directory\n", "remote-control-endpoint-entry-owner\n",
		"remote-control-endpoint-dangling\n", "remote-control-endpoint-link-chain\n",
		"remote-control-endpoint-not-socket\n", "remote-control-endpoint-socket-owner\n",
		"remote-control-endpoint-socket-mode\n", "remote-control-endpoint-parent-unsafe\n",
		"remote-control-endpoint-changed\n", "remote-control-endpoint-peer-owner\n",
		"remote-control-endpoint-unavailable\n":
		return goalError(strings.TrimSuffix(string(d.body), "\n"))
	}
	return nil
}

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
	var diagnostic remoteBridgeDiagnostic
	cmd.Stderr = &diagnostic
	if cmd.Start() != nil {
		return goalError("goal-transport-unavailable")
	}
	joined := false
	join := func() {
		if !joined {
			_ = in.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			joined = true
		}
	}
	defer join()
	p := newGoalRPC(in, out, thread)
	p.ctx = ctx
	if e := p.initialize(); e != nil {
		join()
		if ctx.Err() == nil {
			if reason := diagnostic.reason(); reason != nil {
				return reason
			}
		}
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
