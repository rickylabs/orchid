package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

func finalDispatchDigest(d dispatchBinding) string {
	d.State = ""
	d.Location = nil
	return finalJSONDigest(d)
}

func finalBindingDigest(record string, uid int) (string, error) {
	raw, err := ownerNativePrivateRead(filepath.Join(record, "binding.json"), uid)
	var binding map[string]json.RawMessage
	if err != nil || strictJSON(raw, &binding) != nil || len(binding) == 0 {
		return "", errFinalPublication
	}
	// These two fields are certified by the native adapters after registration.
	// Every other private dispatch/full-brief field remains pinned.
	delete(binding, "NativeSessionID")
	delete(binding, "NativeStore")
	return finalJSONDigest(binding), nil
}

func (c *Coord) stageFinalReport(ctx context.Context, h Host, workdir, key string, r *durableMatrixReceipt) error {
	if ctx.Err() != nil || r == nil || r.dispatch == nil {
		return errFinalPublication
	}
	uid := os.Getuid()
	if r.owner != nil {
		uid = r.owner.uid
	}
	digest, err := finalBindingDigest(filepath.Dir(r.file), uid)
	if err != nil {
		return err
	}
	s := finalReportScope{SchemaVersion: 1, Key: key, Destination: r.dispatch.Issue, Host: r.dispatch.Host, Source: r.dispatch.Source, Repo: "", Cwd: workdir, BindingDigest: digest, DispatchDigest: finalDispatchDigest(*r.dispatch)}
	var binding struct{ Repo string }
	raw, readErr := ownerNativePrivateRead(filepath.Join(filepath.Dir(r.file), "binding.json"), uid)
	if readErr != nil || decodeNativeJSON(raw, &binding) != nil {
		return errFinalPublication
	}
	s.Repo = binding.Repo
	if !repositoryName.MatchString(s.Destination.Repo) || s.Destination.Number < 1 || !repositoryName.MatchString(s.Repo) || s.Host != h.Name ||
		(s.Source != "codex" && s.Source != "claude" && s.Source != "opencode" && s.Source != "agy") || !filepath.IsAbs(workdir) || filepath.Clean(workdir) != workdir {
		return errFinalPublication
	}
	dir, err := c.finalPublicationDir(key, true)
	if err != nil || ctx.Err() != nil {
		return errFinalPublication
	}
	if actionImmutableJSON(dir, "scope.json", s) != nil || ctx.Err() != nil {
		return errFinalPublication
	}
	return h.stageFinalReportFiles(ctx, workdir)
}

func (h Host) stageFinalReportFiles(ctx context.Context, workdir string) error {
	// git rev-parse resolves both ordinary clones and worktree .git pointers.
	cmd := fmt.Sprintf("set -e\ncd %s\ntest \"$(pwd -P)\" = %s\nreport_exclude=$(git rev-parse --git-path info/exclude)\npython3 -c %s \"$report_exclude\"\nmkdir -p \"$(dirname \"$report_exclude\")\"\ntest ! -L \"$report_exclude\"\n", shq(workdir), shq(workdir), shq("import os,sys; p=os.path.abspath(sys.argv[1]); assert os.path.realpath(os.path.dirname(p))==os.path.dirname(p)"))
	for _, name := range []string{finalReportFile, finalReportTemp} {
		cmd += fmt.Sprintf("test ! -e %s\ntest ! -L %s\nreport_tracked=$(git ls-files -- %s)\ntest -z \"$report_tracked\"\ngrep -qxF %s \"$report_exclude\" || printf '%%s\\n' %s >> \"$report_exclude\"\n", shq(name), shq(name), shq(name), shq(name), shq(name))
	}
	if _, err := h.runRemote(ctx, cmd); err != nil || ctx.Err() != nil {
		return errFinalPublication
	}
	return nil
}

// Fixed read-only handoff, no symlinks/FIFOs/tracked input or mutable read.
const finalReportReadScript = `import json,os,stat,sys,subprocess
try:
 cwd=sys.argv[1]; name='.divybot-final-report.md'
 assert os.path.realpath(cwd)==cwd
 os.chdir(cwd); parent=os.stat('.')
 assert not subprocess.check_output(['git','ls-files','--',name])
 assert subprocess.run(['git','check-ignore','-q','--',name]).returncode==0
 before=os.lstat(name)
 assert stat.S_ISREG(before.st_mode) and before.st_uid==parent.st_uid and before.st_nlink==1
 assert not (before.st_mode & 0o022) and before.st_size<=60000
 fd=os.open(name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
 try:
  opened=os.fstat(fd); assert (opened.st_dev,opened.st_ino)==(before.st_dev,before.st_ino)
  data=os.read(fd,60001); after=os.fstat(fd); path=os.lstat(name)
  assert len(data)<=60000 and (after.st_size,after.st_mtime_ns,after.st_ctime_ns)==(before.st_size,before.st_mtime_ns,before.st_ctime_ns)
  assert (path.st_dev,path.st_ino,path.st_size,path.st_mtime_ns,path.st_ctime_ns)==(before.st_dev,before.st_ino,before.st_size,before.st_mtime_ns,before.st_ctime_ns)
 finally: os.close(fd)
 print(json.dumps({'body':data.decode('utf-8')}))
except Exception: sys.exit(1)
`

func (h Host) readFinalReport(ctx context.Context, cwd string) (string, error) {
	raw, err := h.runRemote(ctx, "exec python3 -c "+shq(finalReportReadScript)+" "+shq(cwd)+" 2>/dev/null")
	var report struct {
		Body string `json:"body"`
	}
	if err != nil || len(raw) > 400000 || strictJSON([]byte(raw), &report) != nil || ctx.Err() != nil {
		return "", errFinalPublication
	}
	return report.Body, nil
}

type finalSourceCalls struct {
	agent    func(context.Context, string) (AgentInfo, error)
	openCode func(context.Context, *Job) (bool, bool, error)
	agy      func(context.Context, *nativeStore) (string, nativeIdentityReason)
}

func (c *Coord) proveFinalReportSource(ctx context.Context, h Host, j *Job, s finalReportScope) error {
	return c.finalSourceProof(ctx, j, s, finalSourceCalls{h.agentInfoOf, h.observeOpenCode, h.readAGYIdentity})
}

func (c *Coord) finalSourceProof(ctx context.Context, j *Job, s finalReportScope, calls finalSourceCalls) error {
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err != nil || ctx.Err() != nil {
		return errFinalPublication
	}
	uid := os.Getuid()
	if owner != nil {
		uid = owner.uid
	}
	read := func() (*durableMatrixReceipt, string, error) {
		r, id, e := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, owner, j.Agent)
		if e != nil || id == "" || r.dispatch.Host != s.Host || r.dispatch.Issue != s.Destination || r.dispatch.Location == nil ||
			r.dispatch.Location.PaneID != j.Pane || r.dispatch.Location.WorkspaceID != j.Workspace || finalDispatchDigest(*r.dispatch) != s.DispatchDigest {
			return nil, "", errFinalPublication
		}
		digest, e := finalBindingDigest(filepath.Dir(r.file), uid)
		if e != nil || digest != s.BindingDigest {
			return nil, "", errFinalPublication
		}
		return r, id, nil
	}
	r, id, err := read()
	if err != nil {
		return err
	}
	before, err := calls.agent(ctx, j.Pane)
	valid := func(a AgentInfo) bool {
		return a.Agent == j.Agent && a.Name == j.Label && a.PaneID == j.Pane && a.WorkspaceID == j.Workspace && a.Cwd == s.Cwd && a.InteractiveReady
	}
	if err != nil || !valid(before) {
		return errFinalPublication
	}
	switch j.Agent {
	case "codex", "claude":
		raw, _ := json.Marshal(map[string]any{"type": "agent_info", "agent": before})
		observed, reason := nativeSessionFromResponse(raw, "agent_info", j.Agent, j.Label, r.dispatch.Location)
		if reason != "" || observed != id {
			return errFinalPublication
		}
	case "opencode":
		if j.OpenCode == nil || j.OpenCode.SessionID != id || j.OpenCode.Cwd != s.Cwd {
			return errFinalPublication
		}
		proof := *j.OpenCode
		confirmed, _, e := calls.openCode(ctx, j)
		if e != nil || !confirmed || !reflect.DeepEqual(proof, *j.OpenCode) {
			return errFinalPublication
		}
	case "agy":
		store, e := readAGYStoreBinding(r)
		if e != nil {
			return errFinalPublication
		}
		directory, e := agyStoreDirectory(s.Cwd, r.dispatch.RunID)
		if e != nil || store.Directory != directory {
			return errFinalPublication
		}
		observed, reason := calls.agy(ctx, store)
		if reason != "" || observed != id {
			return errFinalPublication
		}
	default:
		return errFinalPublication
	}
	after, err := calls.agent(ctx, j.Pane)
	if err != nil || !valid(after) || !samePromptOccupant(before, after) || before.StateChangeSeq != after.StateChangeSeq || ctx.Err() != nil {
		return errFinalPublication
	}
	if j.Agent == "codex" || j.Agent == "claude" {
		raw, _ := json.Marshal(map[string]any{"type": "agent_info", "agent": after})
		observed, reason := nativeSessionFromResponse(raw, "agent_info", j.Agent, j.Label, r.dispatch.Location)
		if reason != "" || observed != id {
			return errFinalPublication
		}
	}
	if j.Agent == "opencode" && j.OpenCode.SessionID != id {
		return errFinalPublication
	}
	_, second, err := read()
	if err != nil || second != id || ctx.Err() != nil {
		return errFinalPublication
	}
	if j.Agent == "agy" {
		current, _, e := read()
		if e != nil {
			return errFinalPublication
		}
		store, e := readAGYStoreBinding(current)
		directory, scopeErr := agyStoreDirectory(s.Cwd, current.dispatch.RunID)
		if e != nil || scopeErr != nil || store.Directory != directory {
			return errFinalPublication
		}
	}
	return nil
}
