package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The configured CLI is named opencode: Herdr starts the canonical executable
// from the pane PATH, so only its directory varies. `$HOME/` is expanded by the
// host shell; nothing else in the path is interpreted.
var openCodeBinaryPath = regexp.MustCompile(`^(\$HOME/|/)([A-Za-z0-9._-]+/)*opencode$`)

// openCodeRunDatabasePrefix names each run's own database. It is a contract with the
// Harness reader, whose OPENCODE_RUN_DB_PREFIX (packages/telemetry, opencode-run-store.ts)
// must equal it: the reader finds a run's database only by this name.
const openCodeRunDatabasePrefix = "opencode-v2-"

var openCodeSessionPattern = regexp.MustCompile(`^ses_[A-Za-z0-9_-]{1,128}$`)

func (h Host) openCodeConfig() OpenCodeConfig {
	if h.OpenCodeCLI == nil {
		return OpenCodeConfig{}
	}
	return *h.OpenCodeCLI
}

// The configured CLI as one absolute path on the host: a leading `$HOME/` resolves against
// the host's agent home, so the binary the catalog checks and the directory Herdr's PATH
// lists never depend on the pane's own HOME.
func (h Host) openCodeBinaryPath() (string, error) {
	cfg := h.openCodeConfig()
	if !openCodeBinaryPath.MatchString(cfg.Binary) || strings.Contains(cfg.Binary, "/../") || strings.Contains(cfg.Binary, "/./") {
		return "", matrixReason("opencode-binary-unconfigured")
	}
	if rest, ok := strings.CutPrefix(cfg.Binary, "$HOME/"); ok {
		return path.Join(h.agentHome(), rest), nil
	}
	return cfg.Binary, nil
}

func (h Host) openCodeBinary() (string, error) {
	binary, err := h.openCodeBinaryPath()
	if err != nil {
		return "", err
	}
	return shq(binary), nil
}

func (h Host) openCodeBinaryDir() (string, error) {
	binary, err := h.openCodeBinaryPath()
	if err != nil {
		return "", err
	}
	return shq(path.Dir(binary)), nil
}

// A fresh 2.x session ID chosen before launch. The TUI creates it at the first
// prompt (`--session`), so no session index is read to find it.
func newOpenCodeSessionID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errMatrix
	}
	return "ses_" + hex.EncodeToString(b[:]), nil
}

// openCodeStagedRunFile holds a launch's pre-assigned session and database, written
// beside the receipt before Herdr starts the run (the remote-control-run.json seam).
const openCodeStagedRunFile = "opencode-run.json"

// openCodeRunSessionEnv carries the pre-assigned session into the owner-only
// `opencode run` command, which has no TUI argv to append it to.
const openCodeRunSessionEnv = "DIVYBOT_OPENCODE_SESSION"

type openCodeStagedRun struct {
	SessionID string `json:"sessionID"`
	Database  string `json:"database"`
}

// readOpenCodeStagedRun reads and checks the staged session of the receipt at receiptFile.
func readOpenCodeStagedRun(receiptFile string) (openCodeStagedRun, error) {
	var staged openCodeStagedRun
	if readPrivateActionJSON(filepath.Join(filepath.Dir(receiptFile), openCodeStagedRunFile), &staged) != nil {
		return openCodeStagedRun{}, errMatrix
	}
	database, err := openCodeRunDatabase(staged.SessionID)
	if err != nil || database != staged.Database {
		return openCodeStagedRun{}, errMatrix
	}
	return staged, nil
}

// The run's own database, relative to OpenCode's data directory. A shared 2.x
// database would let a run killed mid-turn hold its location for later hosts.
func openCodeRunDatabase(session string) (string, error) {
	if !openCodeSessionPattern.MatchString(session) {
		return "", matrixReason("opencode-route-invalid")
	}
	return openCodeRunDatabasePrefix + session + ".db", nil
}

// Read-only projection of one run database: `mode=ro`, `query_only`, no OpenCode
// host (a second host on a run's store would resume its claimed session). The header,
// size scan and blob fetch share one read transaction, so the bounds hold for the rows
// that are read even while OpenCode writes the store. Prompt
// text never leaves the host; only its digests do. A missing store or session is
// an empty observation, not a failure. Field names are the 2.1.2 SessionMessageInfo.
const openCodeStoreReadScript = `import hashlib,json,os,sqlite3,stat,sys,urllib.parse
def strict(pairs):
 out={}
 for k,v in pairs:
  assert k not in out; out[k]=v
 return out
def digests(t):
 s=[hashlib.sha256(t.encode('utf-8')).hexdigest()]
 if t.endswith(' '): s.append(hashlib.sha256(t[:-1].encode('utf-8')).hexdigest())
 return s
try:
 name,sid=sys.argv[1],sys.argv[2]
 assert '/' not in name and name.endswith('.db') and '/' not in sid
 data=os.environ.get('XDG_DATA_HOME') or os.path.join(os.environ['HOME'],'.local','share')
 p=os.path.join(data,'opencode',name)
 try: st=os.lstat(p)
 except FileNotFoundError: print(json.dumps({'info':None,'messages':[]})); sys.exit(0)
 assert stat.S_ISREG(st.st_mode) and st.st_uid==os.getuid() and not (st.st_mode & 0o022)
 db=sqlite3.connect('file:'+urllib.parse.quote(p)+'?mode=ro',uri=True,timeout=5,isolation_level=None)
 db.execute('PRAGMA query_only=1')
 db.execute('BEGIN')
 row=db.execute('select id,directory,time_created,parent_id from session_v2 where id=?',(sid,)).fetchone()
 if row is None: print(json.dumps({'info':None,'messages':[]})); sys.exit(0)
 sizes=db.execute('select length(cast(data as blob)) from session_message where session_id=? order by seq limit 1025',(sid,)).fetchall()
 assert len(sizes)<=1024 and all(isinstance(n,int) and 0<=n<=1048576 for (n,) in sizes) and sum(n for (n,) in sizes)<=8388608
 rows=db.execute('select id,type,seq,cast(data as blob) from session_message where session_id=? order by seq limit ?',(sid,len(sizes))).fetchall()
 assert [len(r[3]) for r in rows]==[n for (n,) in sizes]
 db.execute('ROLLBACK')
 messages=[]
 for mid,kind,seq,raw in rows:
  d=json.loads(raw.decode('utf-8'),object_pairs_hook=strict); assert isinstance(d,dict)
  m={'id':mid,'type':kind,'seq':seq,'time':d.get('time')}
  if kind=='user':
   assert isinstance(d.get('text'),str); m['textDigests']=digests(d['text'])
  elif kind=='assistant':
   content=d.get('content'); assert isinstance(content,list)
   m.update(agent=d.get('agent'),model=d.get('model'),finish=d.get('finish'),failed=d.get('error') is not None,
    content=[{'type':c.get('type'),'text':isinstance(c.get('text'),str) and c['text'].strip()!='',
     'status':(c.get('state') or {}).get('status')} for c in content])
  elif kind=='idle':
   m.update(outcome=d.get('outcome'),failed=d.get('error') is not None)
  messages.append(m)
 print(json.dumps({'info':{'id':row[0],'directory':row[1],'created':row[2],'parentID':row[3]},'messages':messages}))
except Exception: sys.exit(1)
`

func (h Host) openCodeStore(ctx context.Context, run *openCodeRun) ([]byte, error) {
	if run == nil {
		return nil, matrixReason("opencode-output-unconfirmed")
	}
	database, err := openCodeRunDatabase(run.SessionID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	script := "export HOME=" + shq(h.agentHome()) + "; exec python3 -c " + shq(openCodeStoreReadScript) + " " + shq(database) + " " + shq(run.SessionID) + " 2>/dev/null"
	if h.isLocal() {
		return matrixCommand(ctx, "", "bash", nil, "-c", script)
	}
	return matrixCommand(ctx, "", "ssh", nil, append(h.sshBase(), h.SSH, script)...)
}

type openCodeStoreMessage struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Seq         int64    `json:"seq"`
	TextDigests []string `json:"textDigests"`
	Time        struct {
		Created, Completed int64
	} `json:"time"`
	Agent string `json:"agent"`
	Model struct {
		ID, ProviderID, Variant string
	} `json:"model"`
	Finish  string `json:"finish"`
	Failed  bool   `json:"failed"`
	Outcome string `json:"outcome"`
	Content []struct {
		Type, Status string
		Text         bool
	} `json:"content"`
}

// Every 2.1.2 SessionMessageInfo type. user, assistant and idle carry turn evidence;
// synthetic, shell and compaction start new work; an unknown type confirms nothing.
var openCodeMessageTypes = map[string]bool{"agent-switched": true, "model-switched": true, "location-switched": true,
	"user": true, "synthetic": true, "system": true, "skill": true, "shell": true, "assistant": true, "compaction": true, "idle": true}

// Inspect the run's stored messages, never the requested argv, a footer, an exit
// status or Herdr idle/done. Output after a different prompt cannot satisfy a turn;
// completion needs a stopped answer and the session's own succeeded idle record.
func inspectOpenCodeStore(raw []byte, run *openCodeRun) (confirmed bool, completed bool, created int64, err error) {
	bad := matrixReason("opencode-output-unconfirmed")
	if run == nil || run.NotBefore < 1 || !openCodeSessionPattern.MatchString(run.SessionID) {
		return false, false, 0, bad
	}
	var record struct {
		Info *struct {
			ID, Directory, ParentID string
			Created                 int64
		} `json:"info"`
		Messages []openCodeStoreMessage `json:"messages"`
	}
	if len(raw) > 1024*1024 || !utf8.Valid(raw) || decodeNativeJSON(raw, &record) != nil || len(record.Messages) > 1024 {
		return false, false, 0, bad
	}
	if record.Info == nil {
		return false, false, 0, nil // the first prompt has not created the session yet
	}
	info := record.Info
	if info.ID != run.SessionID || info.Directory != run.Cwd || info.ParentID != "" || info.Created < run.NotBefore ||
		(run.CreatedAt != 0 && info.Created != run.CreatedAt) {
		return false, false, 0, bad
	}
	// answeredAt: completion clock of this turn's stopped, non-empty, tool-settled answer; 0 = none.
	first, answeredAt := true, int64(0)
	seen := map[string]bool{}
	var seq int64
	for _, m := range record.Messages {
		if m.ID == "" || seen[m.ID] || m.Seq < seq || !openCodeMessageTypes[m.Type] || m.Time.Created < info.Created {
			return false, false, 0, bad
		}
		seen[m.ID], seq = true, m.Seq
		switch m.Type {
		case "synthetic", "shell", "compaction":
			// Work that starts after a turn's end, without a new prompt: that end no longer
			// describes the session until a fresh stopped answer and its own succeeded idle.
			completed, answeredAt = false, 0
		case "user":
			confirmed, completed, answeredAt = false, false, 0
			if first && !containsString(m.TextDigests, run.ExpectedPromptDigest) {
				return false, false, 0, bad
			}
			first = false
		case "assistant":
			variant := m.Model.Variant
			if variant == "default" {
				variant = ""
			}
			if first || m.Agent != "build" || m.Model.ProviderID != run.Route.Provider || m.Model.ID != run.Route.Model || variant != run.Route.Variant {
				return false, false, 0, matrixReason("opencode-route-mismatch")
			}
			if m.Failed || m.Finish == "error" {
				return false, false, 0, matrixReason("opencode-provider-error")
			}
			confirmed, completed, answeredAt = true, false, 0
			if m.Time.Completed > 0 && (m.Time.Completed < m.Time.Created || m.Time.Completed > time.Now().UnixMilli()) {
				return false, false, 0, bad
			}
			text, pending := false, false
			for _, c := range m.Content {
				text = text || (c.Type == "text" && c.Text)
				pending = pending || (c.Type == "tool" && c.Status != "completed" && c.Status != "error")
			}
			if m.Time.Completed > 0 && m.Finish == "stop" && !pending {
				if !text {
					return false, false, 0, matrixReason("opencode-empty-answer")
				}
				answeredAt = m.Time.Completed
			}
		case "idle":
			if m.Failed || m.Outcome == "failed" {
				return false, false, 0, matrixReason("opencode-provider-error")
			}
			// The native end must follow the answer it closes; an earlier idle clock contradicts it.
			completed = confirmed && answeredAt > 0 && m.Time.Created >= answeredAt && m.Outcome == "succeeded"
		}
	}
	return confirmed, completed, info.Created, nil
}

func decodeOpenCodeModels(raw []byte) ([]openCodeCatalogModel, error) {
	var catalog struct {
		Data []openCodeCatalogModel `json:"data"`
	}
	if len(raw) == 0 || len(raw) > 1024*1024 || !utf8.Valid(raw) || decodeNativeJSON(raw, &catalog) != nil || len(catalog.Data) == 0 || len(catalog.Data) > 4096 {
		return nil, matrixReason("opencode-catalog-unavailable")
	}
	return catalog.Data, nil
}

type openCodeCatalogModel struct {
	ID         string          `json:"id"`
	ProviderID string          `json:"providerID"`
	Variants   json.RawMessage `json:"variants"`
}
