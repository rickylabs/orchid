package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Native Claude Remote Control evidence.
//
// Claude Code keeps its own per-process session record, sessions/<pid>.json,
// naming the process (pid and kernel start time) and its native session id.
// It may also carry bridgeSessionId, and the session's own transcript records
// a typed system entry (subtype bridge_status) with the session URL. Neither
// field is documented: the session link is an OBSERVED association, served
// only when both sources agree for the launched process. The URL form itself
// is documented: the session id is the part of the claude.ai/code session URL
// between /code/ and any '?'. Neither field has disconnect semantics, so they
// are identity only, never a connection verdict: Claude's native connection
// stays unknown (no-official-surface) and no terminal text decides it.

const (
	shadowClaudeSession    shadowSource = "claude-session"
	shadowBridgeIdentity   shadowFact   = "bridge-identity"
	claudeBridgeReadBudget              = 2 * time.Second
	claudeBridgeWaitDelay               = 200 * time.Millisecond
)

var errClaudeBridgeUnknown = errors.New("claude-bridge-unknown")

// claudeBridgePython matches Claude's session record to a live process by pid,
// its kernel start time and the run's native session id, with a strict schema:
// duplicate keys or a wrong type make the evidence malformed (unknown). For a
// single bound record with a bridge id it also reads the latest typed
// bridge_status entry of that session's own transcript (private, regular,
// owner-only, bounded). It prints structure only.
const claudeBridgePython = `import json,os,re,stat,sys
sid=sys.argv[1]; cwd=sys.argv[2]; pids=[int(x) for x in sys.argv[3:]]
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
  m.append((pid,start,''))
 elif type(d['bridgeSessionId']) is str and d['bridgeSessionId']:
  m.append((pid,start,d['bridgeSessionId']))
 else:
  bad+=1
out={'matches':len(m),'malformed':bad>0,'pid':0,'start':'','bridge':'','transcript':'none','url':''}
if len(m)==1 and bad==0:
 out['pid'],out['start'],out['bridge']=m[0]
 if out['bridge'] and cwd:
  path=os.path.join(home,'projects',re.sub('[^a-zA-Z0-9]','-',cwd),sid+'.jsonl')
  url=None
  try:
   fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
  except FileNotFoundError:
   fd=None
  except Exception:
   fd=-1
  if fd is None:
   pass
  elif fd<0:
   out['transcript']='unreadable'
  else:
   s=os.fstat(fd)
   if not stat.S_ISREG(s.st_mode) or s.st_uid!=os.getuid() or stat.S_IMODE(s.st_mode)!=0o600 or s.st_size>67108864:
    os.close(fd); out['transcript']='unreadable'
   else:
    try:
     with os.fdopen(fd,'rb') as f:
      for line in f:
       if b'"bridge_status"' not in line: continue
       e=json.loads(line,object_pairs_hook=pairs)
       if not isinstance(e,dict) or e.get('type')!='system' or e.get('subtype')!='bridge_status' or e.get('sessionId')!=sid: continue
       if type(e.get('url')) is not str: raise ValueError()
       url=e['url']
     if url is not None:
      out['transcript'],out['url']='entry',url
    except Exception:
     out['transcript']='malformed'
print(json.dumps(out))
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
	read, reason := h.claudeBridgeSession(ctx, pane, sessionID, "")
	return read.Bridge != "", reason == ""
}

// claudeBridgeSessionID is the session id in the documented claude.ai/code URL
// form (the part after /code/), with a strict local grammar.
var claudeBridgeSessionID = regexp.MustCompile(`^session_[A-Za-z0-9]{1,128}$`)

// claudeBridgeRead is one structured read of Claude's own records: the bound
// process, its bridge id, and its transcript's latest bridge_status URL.
type claudeBridgeRead struct {
	Matches    int    `json:"matches"`
	Malformed  bool   `json:"malformed"`
	PID        int    `json:"pid"`
	Start      string `json:"start"`
	Bridge     string `json:"bridge"`
	Transcript string `json:"transcript"`
	URL        string `json:"url"`
}

// Named reasons a Claude session link is withheld (closed vocabulary, private).
const (
	linkProcessUnavailable   = "process-unavailable"
	linkRecordUnbound        = "record-unbound"
	linkRecordAmbiguous      = "record-ambiguous"
	linkRecordMalformed      = "record-malformed"
	linkReadFailed           = "read-failed"
	linkBridgeAbsent         = "bridge-absent"
	linkProcessReplaced      = "process-replaced"
	linkTranscriptAbsent     = "transcript-entry-absent"
	linkTranscriptUnreadable = "transcript-unreadable"
	linkSourcesDisagree      = "sources-disagree"
	linkIdentityChanged      = "identity-changed"
)

// claudeBridgeSession reads the record bound to a live pane process by pid,
// kernel start time and native session id, plus (when cwd is given) that
// session's transcript entry. A non-empty reason means the read is unknown.
func (h Host) claudeBridgeSession(ctx context.Context, pane, sessionID, cwd string) (claudeBridgeRead, string) {
	var none claudeBridgeRead
	if !privateNativeID(sessionID) || (cwd != "" && !validRemoteCwd(cwd)) {
		return none, linkReadFailed
	}
	herdr := fmt.Sprintf(`export HOME=%s; export PATH="$HOME/.local/bin:/usr/local/bin:$PATH"; herdr %s %s %s %s`,
		shq(h.agentHome()), shq("pane"), shq("process-info"), shq("--pane"), shq(pane))
	out, err := h.runBounded(ctx, herdr)
	if err != nil {
		return none, linkProcessUnavailable
	}
	pids, ok := paneProcessPIDs(out)
	if !ok {
		return none, linkProcessUnavailable
	}
	args := make([]string, 0, len(pids))
	for _, pid := range pids {
		args = append(args, strconv.Itoa(pid))
	}
	script := fmt.Sprintf("export HOME=%s; exec python3 -c %s %s %s %s", shq(h.agentHome()), shq(claudeBridgePython), shq(sessionID), shq(cwd), strings.Join(args, " "))
	raw, err := h.runBounded(ctx, script)
	var v claudeBridgeRead
	if err != nil || len(raw) > 1024 || strictJSON([]byte(strings.TrimSpace(raw)), &v) != nil { // guard:claude-bridge-closed-response
		return none, linkReadFailed
	}
	switch {
	case v.Malformed:
		return none, linkRecordMalformed
	case v.Matches == 0:
		return none, linkRecordUnbound
	case v.Matches != 1: // guard:claude-bridge-single-match
		return none, linkRecordAmbiguous
	}
	// The structured response must be self-consistent.
	entry := v.Transcript == "entry"
	if v.PID <= 1 || v.Start == "" || entry != (v.URL != "") || (v.Bridge == "" && v.Transcript != "none") ||
		(v.Transcript != "none" && v.Transcript != "entry" && v.Transcript != "unreadable" && v.Transcript != "malformed") ||
		(v.Bridge != "" && !claudeBridgeSessionID.MatchString(v.Bridge)) { // guard:claude-bridge-session-form
		return none, linkRecordMalformed
	}
	return v, ""
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
// shadow scope, bounded by its own budget, and sets or withholds the job's
// session link. Unknown evidence revokes the source and the link.
func (h Host) readClaudeBridge(j *Job) {
	defer shadowContain()
	ctx, cancel := context.WithTimeout(context.Background(), claudeBridgeReadBudget) // guard:bridge-own-context
	defer cancel()
	p := h.ShadowScope.open(shadowClaudeSession, j.RemoteControl.NativeSessionID)
	if p == nil {
		return
	}
	read, reason := h.claudeBridgeSession(ctx, j.Pane, j.RemoteControl.NativeSessionID, j.RemoteControl.Cwd)
	if reason != "" {
		h.ClaudeLinks.withhold(j.DispatchKey, j.Issue, reason) // guard:claude-link-unknown-clears
		p.close(errClaudeBridgeUnknown)
		return
	}
	url := "https://claude.ai/code/" + read.Bridge
	switch {
	case read.Bridge == "":
		reason = linkBridgeAbsent
	case !h.ClaudeLinks.pinned(j.DispatchKey, claudeProcess{read.PID, read.Start}): // guard:claude-link-launched-process
		reason = linkProcessReplaced
	case read.Transcript == "none":
		reason = linkTranscriptAbsent
	case read.Transcript != "entry":
		reason = linkTranscriptUnreadable
	case read.URL != url: // guard:claude-link-sources-agree
		reason = linkSourcesDisagree
	}
	if reason == "" {
		h.ClaudeLinks.set(j.DispatchKey, j.RemoteControl.NativeSessionID, url, time.Now())
	} else {
		h.ClaudeLinks.withhold(j.DispatchKey, j.Issue, reason)
	}
	p.bridgeIdentity(read.Bridge != "") // guard:claude-bridge-publish
	p.close(nil)
}

// claudeLinkFreshness bounds how long a bound bridge read may supply the
// session link: longer than one poll interval, so the next tick's
// observation can use the previous tick's off-path read.
const claudeLinkFreshness = 3 * remoteControlFreshness

// claudeLinkStore keeps, per job, the Claude Remote Control session link last
// agreed by Claude's own bound records, the launched process it is pinned to,
// and why a link is withheld. Publishing and withholding hold one lock, so a
// withheld link is never left on, or written back to, the published row.
type claudeLinkStore struct {
	mu        sync.Mutex
	rows      map[string]claudeLink
	pins      map[string]claudeProcess
	reasons   map[string]string
	published map[string]bool
	revoke    func(key string)
}

type claudeLink struct {
	native, url string
	at          time.Time
}

// claudeProcess is the launched Claude process: pid and kernel start time.
type claudeProcess struct {
	pid   int
	start string
}

// revokeWith sets how a published row is withdrawn (the coordinator's clear).
func (s *claudeLinkStore) revokeWith(revoke func(key string)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoke = revoke
}

// pinned pins the job to the first bound process it sees and reports whether
// p is that process: a later process in the pane never supplies the link.
func (s *claudeLinkStore) pinned(key string, p claudeProcess) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pins == nil {
		s.pins = map[string]claudeProcess{}
	}
	if pin, ok := s.pins[key]; ok {
		return pin == p
	}
	s.pins[key] = p
	return true
}

func (s *claudeLinkStore) set(key, native, url string, at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string]claudeLink{}
	}
	s.rows[key] = claudeLink{native: native, url: url, at: at}
	delete(s.reasons, key)
}

// withhold drops the job's link for a named reason and revokes a published
// row that carries it. The reason is logged once per change.
func (s *claudeLinkStore) withhold(key string, issue int, reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.withholdLocked(key, issue, reason)
}

func (s *claudeLinkStore) withholdLocked(key string, issue int, reason string) {
	delete(s.rows, key)
	if s.published[key] { // guard:claude-link-revoke-published
		if s.revoke != nil {
			s.revoke(key)
		}
		delete(s.published, key)
	}
	if s.reasons == nil {
		s.reasons = map[string]string{}
	}
	if s.reasons[key] != reason {
		s.reasons[key] = reason
		log.Printf("issue #%d: claude session link withheld (%s)", issue, reason)
	}
}

// forget withholds the link and drops the process pin: the job's native
// occupant ended or changed.
func (s *claudeLinkStore) forget(key string, issue int, reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.withholdLocked(key, issue, reason)
	delete(s.pins, key)
}

// fresh returns the job's link when it was read for exactly this native
// session within the freshness bound; otherwise nil.
func (s *claudeLinkStore) fresh(key, native string, now time.Time) *string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.freshLocked(key, native, now)
}

func (s *claudeLinkStore) freshLocked(key, native string, now time.Time) *string {
	row, ok := s.rows[key]
	if !ok || row.native != native || now.Before(row.at) || now.Sub(row.at) > claudeLinkFreshness { // guard:claude-link-fresh
		return nil
	}
	url := row.url
	return &url
}

// publish writes the job's row with its fresh link (or none) under the lock
// that withholding takes, and records whether the written row carries a link.
func (s *claudeLinkStore) publish(key, native string, now time.Time, write func(link *string) error) error {
	if s == nil {
		return write(nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	link := s.freshLocked(key, native, now)
	err := write(link)
	if s.published == nil {
		s.published = map[string]bool{}
	}
	s.published[key] = err == nil && link != nil
	return err
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
