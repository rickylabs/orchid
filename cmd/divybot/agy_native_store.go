package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// NativeStore belongs to the private reservation. Neither it nor the native ID
// is part of dispatch.json, an operator diagnostic or a public agent identifier.
type nativeStore struct {
	Source    string `json:"source"`
	Directory string `json:"directory"`
}

var agyConversationID = regexp.MustCompile(`^[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}$`)

func agyStoreDirectory(cwd, runID string) (string, error) {
	key := strings.TrimPrefix(runID, "orchid-")
	if !agyTrustScope(cwd) || runID != "orchid-"+key || !digestPattern.MatchString(key) {
		return "", matrixReason("native-session-evidence-invalid")
	}
	return filepath.Join(filepath.Dir(cwd), ".divybot-native", key, "agy"), nil
}

func validAGYStore(store *nativeStore, runID string) bool {
	if store == nil || store.Source != "agy" || !agyTrustScope(store.Directory) || filepath.Base(store.Directory) != "agy" {
		return false
	}
	key := filepath.Base(filepath.Dir(store.Directory))
	return digestPattern.MatchString(key) && runID == "orchid-"+key &&
		filepath.Base(filepath.Dir(filepath.Dir(store.Directory))) == ".divybot-native"
}

func (r *durableMatrixReceipt) writeAGYStore(store *nativeStore) error {
	if r == nil {
		return errMatrix
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatch == nil || r.dispatch.Source != "agy" || r.dispatch.State != "dispatched" || !validAGYStore(store, r.dispatch.RunID) {
		return errMatrix
	}
	path := filepath.Join(filepath.Dir(r.file), "binding.json")
	raw, err := os.ReadFile(path)
	var binding map[string]json.RawMessage
	if err != nil || decodeNativeJSON(raw, &binding) != nil {
		return errMatrix
	}
	if binding == nil {
		binding = map[string]json.RawMessage{}
	}
	if _, present := binding["NativeStore"]; present {
		return errMatrix
	}
	binding["NativeStore"], _ = json.Marshal(store)
	data, err := json.Marshal(binding)
	if err != nil {
		return errMatrix
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".native-store-")
	if err != nil {
		return errMatrix
	}
	defer os.Remove(f.Name())
	_, written := f.Write(data)
	owned, synced, closed := transferReceiptOwner(r.owner, f.Name()), f.Sync(), f.Close()
	if written != nil || owned != nil || synced != nil || closed != nil || os.Rename(f.Name(), path) != nil {
		return errMatrix
	}
	if syncDirectory(filepath.Dir(path)) != nil {
		_ = os.Remove(path)
		return errMatrix
	}
	return nil
}

func readAGYStoreBinding(r *durableMatrixReceipt) (*nativeStore, error) {
	if r == nil || r.dispatch == nil || r.dispatch.Source != "agy" {
		return nil, errMatrix
	}
	var binding map[string]json.RawMessage
	if readPrivateActionJSON(filepath.Join(filepath.Dir(r.file), "binding.json"), &binding) != nil {
		return nil, errMatrix
	}
	var store nativeStore
	if strictJSON(binding["NativeStore"], &store) != nil || !validAGYStore(&store, r.dispatch.RunID) {
		return nil, errMatrix
	}
	return &store, nil
}

// Fixed metadata queries only. No AGY process, API, payload, title, preview,
// credential or setting is read to discover identity. Python's SQLite URI is
// read-only against the native WAL; failures disclose no path or identity.
const agyNativeMetadataQuery = `import json,os,pathlib,re,sqlite3,stat,sys
try:
 root=pathlib.Path(sys.argv[1]); uid=os.getuid()
 for directory in [root,root.parent,root.parent.parent]:
  s=directory.lstat()
  assert stat.S_ISDIR(s.st_mode) and s.st_uid==uid and stat.S_IMODE(s.st_mode)==0o700
 assert str(root.resolve())==str(root)
 summary=root/'conversation_summaries.db'
 def connect(p):
  s=p.lstat(); assert stat.S_ISREG(s.st_mode) and s.st_uid==uid
  db=sqlite3.connect(p.as_uri()+'?mode=ro',uri=True,timeout=0.2)
  db.execute('PRAGMA query_only=ON');return db
 db=connect(summary)
 rows=db.execute('SELECT conversation_id,parent_conversation_id FROM conversation_summaries LIMIT 65').fetchall();db.close()
 assert len(rows)<=64
 roots=[r[0] for r in rows if r[1] in [None,'']]
 if not roots: print('{}');sys.exit(0)
 assert len(roots)==1
 identity=roots[0];assert re.fullmatch(r'[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}',identity)
 directory=root/'conversations';s=directory.lstat()
 assert stat.S_ISDIR(s.st_mode) and s.st_uid==uid
 db=connect(directory/(identity+'.db'))
 meta=db.execute('SELECT trajectory_id,cascade_id FROM trajectory_meta LIMIT 2').fetchall();db.close()
 assert len(meta)==1 and meta[0][1]==identity
 assert re.fullmatch(r'[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}',meta[0][0])
`

const agyNativeQueryErrors = `except (FileNotFoundError,sqlite3.OperationalError):
 print('{}')
except Exception:
 sys.exit(1)
`

const agyIdentityQuery = agyNativeMetadataQuery + " print(json.dumps({'id':identity}))\n" + agyNativeQueryErrors

func (h Host) readAGYIdentity(ctx context.Context, store *nativeStore) (string, nativeIdentityReason) {
	if store == nil || store.Source != "agy" || !agyTrustScope(store.Directory) {
		return "", nativeInvalid
	}
	raw, err := h.agyTrustCommand(ctx, "exec python3 -c "+shq(agyIdentityQuery)+" "+shq(store.Directory)+" 2>/dev/null")
	var row struct {
		ID string `json:"id"`
	}
	if err != nil || len(raw) > 1024 || strictJSON(raw, &row) != nil {
		return "", nativeInvalid
	}
	if row.ID == "" {
		return "", nativeUnavailable
	}
	if !agyConversationID.MatchString(row.ID) {
		return "", nativeInvalid
	}
	return row.ID, ""
}

func agyIdentityOccupant(a AgentInfo, j *Job) bool {
	return j != nil && a.Agent == "agy" && a.Name == j.Label && a.PaneID == j.Pane &&
		a.WorkspaceID == j.Workspace && a.InteractiveReady &&
		(a.AgentStatus == "idle" || a.AgentStatus == "working" || a.AgentStatus == "blocked" || a.AgentStatus == "done")
}

func agyBindingEligible(j *Job) bool {
	return j != nil && j.Agent == "agy" && j.GoalDelivery == "confirmed" && !j.RunMode
}

// The independently selected route and whole-brief reservation remain authority
// while native metadata is read. Extra private authority fields stay pinned too.
func readAGYBindingAuthority(r *durableMatrixReceipt, j *Job) (string, error) {
	uid := os.Getuid()
	if r.owner != nil {
		uid = r.owner.uid
	}
	raw, err := ownerNativePrivateRead(filepath.Join(filepath.Dir(r.file), "binding.json"), uid)
	var binding struct {
		IssueID, Repo, BriefDigest, Host string
		Route                            struct{ Transport, Provider, Model, Effort string }
	}
	if err != nil || decodeNativeJSON(raw, &binding) != nil || binding.IssueID == "" || binding.Repo != j.Repo || binding.Host != j.Host ||
		!digestPattern.MatchString(binding.BriefDigest) || shaText([]byte(binding.IssueID+"\x00"+binding.Repo+"\x00"+binding.BriefDigest)) != j.DispatchKey ||
		binding.Route.Transport != "agy" || binding.Route.Provider != r.dispatch.Provider || binding.Route.Model != r.dispatch.Model ||
		binding.Route.Effort != r.dispatch.Effort || binding.Route.Model != j.Overrides.Model ||
		!(binding.Route.Effort == j.Overrides.Effort || r.dispatch.MatrixSource == ownerNativeSource && binding.Route.Effort == "provider_default" && j.Overrides.Effort == "") {
		return "", errMatrix
	}
	return shaText(raw), nil
}

func (c *Coord) bindAGYLiveIdentity(ctx context.Context, host Host, j *Job) {
	if c.dry || !agyBindingEligible(j) {
		return
	}
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err != nil {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if retryAGYNativeBinding(readCtx, c.cfg.Matrix.ReceiptRoot, c.cfg.Inbox, owner, j,
		host.agentInfoOf, host.readAGYIdentity) {
		log.Printf("issue #%d: AGY native identity bound", j.Issue)
	}
}

// Read around the native metadata lookup, then reopen the durable authority.
// None of these observations substitutes a path/clock/title guess for identity.
func retryAGYNativeBinding(ctx context.Context, root, inbox string, owner *receiptOwner, j *Job,
	readAgent func(context.Context, string) (AgentInfo, error),
	readIdentity func(context.Context, *nativeStore) (string, nativeIdentityReason)) bool {
	if ctx.Err() != nil || !agyBindingEligible(j) {
		return false
	}
	r, existing, err := loadNativeBindingReceipt(root, j.DispatchKey, j, inbox, owner, "agy")
	if err != nil || existing != "" || r.dispatch.Location == nil || r.dispatch.Host != j.Host ||
		r.dispatch.Location.PaneID != j.Pane || r.dispatch.Location.WorkspaceID != j.Workspace {
		return false
	}
	beforeBinding, err := readAGYBindingAuthority(r, j)
	if err != nil {
		return false
	}
	store, err := readAGYStoreBinding(r)
	if err != nil {
		return false
	}
	before, err := readAgent(ctx, j.Pane)
	if err != nil || ctx.Err() != nil || !agyIdentityOccupant(before, j) {
		return false
	}
	directory, err := agyStoreDirectory(before.Cwd, r.dispatch.RunID)
	if err != nil || store.Directory != directory {
		return false
	}
	id, reason := readIdentity(ctx, store)
	if reason != "" || ctx.Err() != nil || !agyBindingEligible(j) || !agyConversationID.MatchString(id) {
		return false
	}
	after, err := readAgent(ctx, j.Pane)
	if err != nil || ctx.Err() != nil || !agyIdentityOccupant(after, j) || before.Cwd != after.Cwd || before.StateChangeSeq != after.StateChangeSeq {
		return false
	}
	verified, existing, err := loadNativeBindingReceipt(root, j.DispatchKey, j, inbox, owner, "agy")
	if err != nil || existing != "" || !reflect.DeepEqual(r.dispatch, verified.dispatch) {
		return false
	}
	currentBinding, err := readAGYBindingAuthority(verified, j)
	if err != nil || currentBinding != beforeBinding || ctx.Err() != nil || !agyBindingEligible(j) {
		return false
	}
	current, err := readAGYStoreBinding(verified)
	if err != nil || !reflect.DeepEqual(current, store) {
		return false
	}
	return publishNativeIdentity(ctx, id, verified.writeNativeIdentity)
}
