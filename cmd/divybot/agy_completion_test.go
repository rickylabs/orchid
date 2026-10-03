package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Native 1.2.14 summary/Step wire numbers, also measured in the retained store.
// IDs and response text are synthetic. This is a native database, not a pane mock.
const agyCompletionFixtureSQL = `import pathlib,sqlite3,sys,time
r=pathlib.Path(sys.argv[1]);identity=sys.argv[2];change=sys.argv[3]
def var(v):
 b=bytearray()
 while v>=128:b.append((v&127)|128);v>>=7
 b.append(v);return bytes(b)
def n(k,v):return var(k*8)+var(v)
def b(k,v):return var(k*8+2)+var(len(v))+v
def ts(k,v):return b(k,n(1,v))
start=int(time.time())-10;trajectory='10000000-0000-4000-8000-000000000001'
summary=n(2,2)+ts(3,start+2)+b(4,trajectory.encode())+n(5,1)+ts(7,start)+n(16,0)
if change=='streaming':summary=summary.replace(n(5,1),n(5,2))+n(21,1)
if change=='killed':summary+=n(23,1)
if change=='child-active':summary+=n(18,1)
if change=='interrupted':summary+=n(25,1)
if change=='user-index':summary=summary.replace(n(16,0),n(16,1))
if change=='summary-count':summary=summary.replace(n(2,2),n(2,3))
if change=='summary-trajectory':summary=summary.replace(trajectory.encode(),identity.encode())
if change=='summary-future':summary=summary.replace(ts(3,start+2),ts(3,start+1000))
user=ts(1,start)+ts(8,start)
ended=start+2
status=3 if change!='pending' else 1
meta=ts(1,start+1)+(ts(8,ended) if change!='no-completion-clock' else b'')
if change=='future':meta=ts(1,start+1)+ts(8,start+1000)
if change=='inverted-clock':meta=ts(1,start+1)+ts(8,start)
if change=='newer-update':meta+=ts(22,start+3)
stop=10 if change=='tool-continuation' else 0 if change=='unknown-stop' else 16 if change=='cancelled' else 2
text=b'' if change=='empty' else b'   ' if change=='whitespace' else b'The fixture is complete.'
response=b(1,text)+n(12,stop)
if change=='duplicate-stop':response+=n(12,2)
frame=n(1,15)+n(4,status)+b(5,meta)+b(20,response)
if change=='step-type':frame=frame.replace(n(1,15),n(1,14),1)
if change=='step-status':frame=frame.replace(n(4,status),n(4,2),1)
if change=='header-disagreement':frame=frame.replace(b(5,meta),b(5,user),1)
if change=='truncated-payload':frame=frame[:-1]
s=sqlite3.connect(r/'conversation_summaries.db')
if change=='resumed-summary':s.execute('PRAGMA journal_mode=WAL')
s.execute('CREATE TABLE conversation_summaries(conversation_id TEXT,parent_conversation_id TEXT,step_count INTEGER,not_fully_idle INTEGER,killed INTEGER,raw_summary BLOB)')
s.execute('INSERT INTO conversation_summaries VALUES(?,NULL,2,?,0,?)',(identity,1 if change=='streaming' else 0,summary));s.commit();s.close()
d=sqlite3.connect(r/'conversations'/(identity+'.db'))
d.execute('CREATE TABLE trajectory_meta(trajectory_id TEXT,cascade_id TEXT)')
d.execute('INSERT INTO trajectory_meta VALUES(?,?)',(trajectory,identity if change!='foreign-cascade' else trajectory))
d.execute('CREATE TABLE steps(idx INTEGER,step_type INTEGER,status INTEGER,metadata BLOB,error_details BLOB,step_payload BLOB,step_format INTEGER)')
d.execute('INSERT INTO steps VALUES(0,14,3,?,NULL,?,0)',(user,n(1,14)+n(4,3)+b(5,user)))
d.execute('INSERT INTO steps VALUES(1,15,?,?,?, ?,?)',(status,meta,b'error' if change=='error' else None,frame,1 if change=='format' else 0))
d.commit();d.close()
if change in ('step-bound','total-byte-bound'):
 count=4097 if change=='step-bound' else 8
 summary=summary.replace(n(2,2),n(2,count))
 s=sqlite3.connect(r/'conversation_summaries.db');s.execute('UPDATE conversation_summaries SET step_count=?,raw_summary=?',(count,summary));s.commit();s.close()
 d=sqlite3.connect(r/'conversations'/(identity+'.db'));d.execute('DELETE FROM steps WHERE idx=1')
 middle=ts(1,start+1)+ts(8,start+1)
 payload=n(1,3)+n(4,3)+b(5,middle)+(b(100,b'x'*800000) if change=='total-byte-bound' else b'')
 d.executemany('INSERT INTO steps VALUES(?,3,3,?,NULL,?,0)',((i,middle,payload) for i in range(1,count-1)))
 d.execute('INSERT INTO steps VALUES(?,15,3,?,NULL,?,0)',(count-1,meta,frame));d.commit();d.close()
if change in ('blob-bound','field-bound','integer-bound','tag-bound'):
 extra=b(100,b'x'*1048576) if change=='blob-bound' else n(100,0)*4097 if change=='field-bound' else n(100,9007199254740992) if change=='integer-bound' else n(0,1)
 s=sqlite3.connect(r/'conversation_summaries.db');s.execute('UPDATE conversation_summaries SET raw_summary=?',(summary+extra,));s.commit();s.close()
if change in ('session-bound','orphan-child'):
 s=sqlite3.connect(r/'conversation_summaries.db')
 for i in range(2,22 if change=='session-bound' else 3):
  child='00000000-0000-4000-8000-%012d'%i
  native='10000000-0000-4000-8000-%012d'%i
  parent=identity if change=='session-bound' else '00000000-0000-4000-8000-000000000099'
  s.execute('INSERT INTO conversation_summaries VALUES(?,?,2,0,0,?)',(child,parent,summary.replace(trajectory.encode(),native.encode())))
  original=sqlite3.connect(r/'conversations'/(identity+'.db'));copy=sqlite3.connect(r/'conversations'/(child+'.db'));original.backup(copy);original.close()
  copy.execute('UPDATE trajectory_meta SET trajectory_id=?,cascade_id=?',(native,child));copy.commit();copy.close()
 s.commit();s.close()
if change=='symlink-db':
 p=r/'conversations'/(identity+'.db');q=r/'moved.db';p.rename(q);p.symlink_to(q)
if change=='public-root':r.chmod(0o755)
`

// Change real WAL-backed native metadata after the first snapshot's rows have
// been read. The unchanged terminal rows must not certify a resumed session.
const agyResumedSummaryHook = `import sqlite3,sys
native_connect=sqlite3.connect;changed=False
class WatchCursor(sqlite3.Cursor):
 def fetchall(self):
  global changed
  rows=super().fetchall()
  if getattr(self,'watch',False) and not changed:
   changed=True
   db=native_connect(sys.argv[1]+'/conversation_summaries.db')
   db.execute('UPDATE conversation_summaries SET not_fully_idle=1');db.commit();db.close()
  return rows
class WatchConnection(sqlite3.Connection):
 def execute(self,sql,*args):
  cursor=self.cursor(factory=WatchCursor);cursor.execute(sql,*args)
  cursor.watch='ORDER BY conversation_id LIMIT 21' in sql
  return cursor
sqlite3.connect=lambda *args,**kwargs:native_connect(*args,**kwargs,factory=WatchConnection)
`

func agyCompletionFixture(t *testing.T, change string) (*Coord, *Job, *bool, *bool, *int, *durableMatrixReceipt) {
	t.Helper()
	c, j, seatGone, processGone, closes := completionFixture(t)
	root, valid, r := agyLiveBindingFixture(t)
	*j = *valid
	j.SpawnedAt, j.Deadline = time.Now().Add(-time.Minute), time.Now().Add(time.Minute)
	cwd := filepath.Join(t.TempDir(), "issue-7")
	directory, _ := agyStoreDirectory(cwd, r.dispatch.RunID)
	if os.MkdirAll(filepath.Join(directory, "conversations"), 0700) != nil {
		t.Fatal("fixture store")
	}
	if _, err := matrixCommand(context.Background(), "", "python3", nil, "-c", agyCompletionFixtureSQL, directory, syntheticAGYID, change); err != nil {
		t.Fatal("fixture native database")
	}
	if r.writeAGYStore(&nativeStore{Source: "agy", Directory: directory}) != nil {
		t.Fatal("fixture store binding")
	}
	id := syntheticAGYID
	if r.writeNativeIdentity(&id) != nil {
		t.Fatal("fixture native binding")
	}
	c.cfg.Inbox, c.cfg.Matrix.ReceiptRoot = "fixture/inbox", root
	a := AgentInfo{Agent: "agy", Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace,
		Cwd: cwd, InteractiveReady: true, AgentStatus: "done", StateChangeSeq: 42}
	c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
		if *seatGone {
			return nil, nil
		}
		return []AgentInfo{a}, nil
	}
	c.actions.completed = nil
	bin := t.TempDir()
	agent, _ := json.Marshal(map[string]any{"result": map[string]any{"type": "agent_info", "agent": a}})
	t.Setenv("AGY_COMPLETION_AGENT", string(agent))
	script := "#!/usr/bin/env python3\nimport os,sys,subprocess\ns=sys.argv[-1]\nif s.startswith('exec python3 -c '):sys.exit(subprocess.run(['bash','-c',s]).returncode)\nelse:print(os.environ['AGY_COMPLETION_AGENT'])\n"
	if change == "resumed-summary" {
		script = "#!/usr/bin/env python3\nimport os,sys,shlex\ns=sys.argv[-1]\nif s.startswith('exec python3 -c '):\n args=shlex.split(s);sys.argv=['-c']+args[4:6]\n exec(" + strconv.Quote(agyResumedSummaryHook) + "+args[3])\nelse:print(os.environ['AGY_COMPLETION_AGENT'])\n"
	}
	if os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700) != nil {
		t.Fatal("fixture ssh")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c.hosts = map[string]Host{j.Host: {Name: j.Host, SSH: "fixture-host"}}
	if c.st.save() != nil {
		t.Fatal("fixture state")
	}
	return c, j, seatGone, processGone, closes, r
}

func TestAGYNativeFinalStopRetiresBeforeOperatorTimeout(t *testing.T) {
	c, j, seatGone, processGone, closes, _ := agyCompletionFixture(t, "")
	if !c.retireCompleted(context.Background(), j.Issue, j, completionRef(j), true) || *closes != 1 {
		t.Fatal("certified AGY final stop cannot retire before operator timeout")
	}
	if c.st.Jobs[j.Issue] == nil || c.st.CompletedRuns[j.Issue].Phase != "close-sent" {
		t.Fatal("close delivery asserted paired absence")
	}
	c.st = loadState(c.st.path)
	j = c.st.Jobs[j.Issue]
	*seatGone = true
	c.retireCompleted(context.Background(), j.Issue, j, completionRef(j), true)
	if c.st.Jobs[j.Issue] == nil || *closes != 1 {
		t.Fatal("seat-only absence released native process or replayed close")
	}
	*processGone = true
	c.retireCompleted(context.Background(), j.Issue, j, completionRef(j), true)
	if c.st.Jobs[j.Issue] != nil || c.st.CompletedRuns[j.Issue].Phase != "observed" || c.st.reserveLaunch(j.Issue) {
		t.Fatal("paired cleanup lost the no-replay fence")
	}
}

func TestAGYStreamingAndInvalidNativeStopCannotRetire(t *testing.T) {
	for _, change := range []string{"streaming", "pending", "no-completion-clock", "future", "inverted-clock", "newer-update", "tool-continuation", "unknown-stop", "cancelled", "empty", "whitespace", "error", "killed", "child-active", "interrupted", "user-index", "summary-count", "summary-trajectory", "summary-future", "step-type", "step-status", "header-disagreement", "truncated-payload", "duplicate-stop", "format", "foreign-cascade", "symlink-db", "public-root", "step-bound", "total-byte-bound", "blob-bound", "field-bound", "integer-bound", "tag-bound", "session-bound", "orphan-child", "resumed-summary"} {
		t.Run(change, func(t *testing.T) {
			c, j, _, _, closes, _ := agyCompletionFixture(t, change)
			if c.retireCompleted(context.Background(), j.Issue, j, completionRef(j), true) || *closes != 0 || len(c.st.CompletedRuns) != 0 {
				t.Fatal("streaming or invalid native AGY proof became success")
			}
		})
	}
}

func TestAGYReportAndMarkerCannotSubstituteNativeStop(t *testing.T) {
	c, j, _, _, _, _ := agyCompletionFixture(t, "streaming")
	j.FinalReportManaged = true
	// Both fallbacks would succeed if the adapter-specific native check were skipped.
	dir, err := c.finalPublicationDir(j.DispatchKey, true)
	if err != nil {
		t.Fatal("fixture publication directory")
	}
	scope := finalReportScope{SchemaVersion: 1, Key: j.DispatchKey, Destination: dispatchIssue{Repo: c.cfg.Inbox, Number: j.Issue}, Host: j.Host, Source: j.Agent, Repo: j.Repo, Cwd: "/fixture/issue-7", BindingDigest: strings.Repeat("b", 64), DispatchDigest: strings.Repeat("e", 64)}
	if actionImmutableJSON(dir, "scope.json", scope) != nil {
		t.Fatal("fixture publication scope")
	}
	c.cfg.BotLogin = "fixture-bot"
	posted := finalPostedComment{ID: 31, IssueURL: "https://api.github.com/repos/" + c.cfg.Inbox + "/issues/7", CreatedAt: time.Now().Truncate(time.Second)}
	posted.User.Login = c.cfg.BotLogin
	c.finalCalls.proof = func(context.Context, Host, *Job, finalReportScope) error { return nil }
	c.finalCalls.report = func(context.Context, Host, finalReportScope) (string, error) { return "Fixture report", nil }
	c.finalCalls.post = func(_ context.Context, _ dispatchIssue, body string) (int64, error) {
		posted.Body = body
		return posted.ID, nil
	}
	c.finalCalls.comment = func(context.Context, dispatchIssue, int64) (finalPostedComment, error) { return posted, nil }
	if _, err := c.publishFinalReport(context.Background(), j); err != nil {
		t.Fatal("fixture marked publication", err)
	}
	bin := t.TempDir()
	comment := map[string]any{"body": "Fixture report\n" + finalCommentMarker(j.DispatchKey), "createdAt": time.Now().UTC().Format(time.RFC3339), "author": map[string]string{"login": "fixture-bot"}}
	raw, _ := json.Marshal(map[string]any{"comments": []any{comment}})
	if os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' "+shq(string(raw))+"\n"), 0700) != nil {
		t.Fatal("fixture gh")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, managed := range []bool{true, false} {
		j.FinalReportManaged = managed
		complete, _ := c.completionEvidence(context.Background(), c.hosts[j.Host], j, syntheticAGYID)
		if complete {
			t.Fatal("report file or marked comment certified a streaming native run")
		}
	}
}

func TestAGYNativeCompletionReadRequiresExactPrivateProof(t *testing.T) {
	for _, change := range []string{"", "foreign-id", "oversize", "unknown-field", "trailing-document", "invalid-id", "wrong-source", "relative-store", "canceled"} {
		t.Run(change, func(t *testing.T) {
			store := &nativeStore{Source: "agy", Directory: "/fixture/.divybot-native/agy"}
			id, reported := syntheticAGYID, syntheticAGYID
			if change == "invalid-id" {
				id, reported = "invalid", "invalid"
			}
			if change == "foreign-id" {
				reported = "00000000-0000-4000-8000-000000000002"
			}
			if change == "wrong-source" {
				store.Source = "foreign"
			}
			if change == "relative-store" {
				store.Directory = "relative/agy"
			}
			raw := `{"id":"` + reported + `","completed":true}`
			if change == "oversize" {
				raw += strings.Repeat(" ", 1025)
			}
			if change == "unknown-field" {
				raw = strings.TrimSuffix(raw, "}") + `,"foreign":true}`
			}
			if change == "trailing-document" {
				raw += `{}`
			}
			bin := t.TempDir()
			if os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nprintf '%s' "+shq(raw)+"\n"), 0700) != nil {
				t.Fatal("fixture ssh")
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if change == "canceled" {
				cancel()
			}
			complete, err := (Host{SSH: "fixture-host"}).readAGYCompletion(ctx, store, id)
			if change == "" {
				if !complete || err != nil {
					t.Fatal("exact private proof rejected", err)
				}
			} else if complete || err == nil {
				t.Fatal("foreign or unbounded native proof became completion")
			}
		})
	}
}

func TestAGYCompletionKeepsExactOccupantAndPR(t *testing.T) {
	for _, change := range []string{"foreign-native", "wrong-name", "wrong-cwd", "wrong-pane", "wrong-workspace", "extra-seat", "sequence", "working", "open-pr", "pending", "run-mode", "canceled"} {
		t.Run(change, func(t *testing.T) {
			c, j, _, _, closes, r := agyCompletionFixture(t, "")
			if change == "foreign-native" {
				id := "00000000-0000-4000-8000-000000000002"
				_ = r.writeNativeIdentity(&id)
			}
			if change == "open-pr" {
				c.actions.completionPR = func(context.Context, *Job) (bool, error) { return true, nil }
			}
			if change == "pending" {
				j.GoalDelivery = "pending"
			}
			if change == "run-mode" {
				j.RunMode = true
			}
			original, reads := c.actions.list, 0
			c.actions.list = func(ctx context.Context, h Host) ([]AgentInfo, error) {
				a, err := original(ctx, h)
				reads++
				switch change {
				case "wrong-name":
					a[0].Name = "foreign"
				case "wrong-cwd":
					a[0].Cwd = filepath.Dir(a[0].Cwd)
				case "wrong-pane":
					a[0].PaneID = "foreign"
				case "wrong-workspace":
					a[0].WorkspaceID = "foreign"
				case "extra-seat":
					a = append(a, a[0])
				case "sequence":
					a[0].StateChangeSeq += uint64(reads)
				case "working":
					a[0].AgentStatus = "working"
				}
				return a, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if change == "canceled" {
				cancel()
			}
			if c.retireCompleted(ctx, j.Issue, j, completionRef(j), true) || *closes != 0 || len(c.st.CompletedRuns) != 0 {
				t.Fatal("AGY completion bypassed exact occupant, authority or PR supervision")
			}
		})
	}
}

func TestAGYCompletionProofKeepsAuthorityAndDeadline(t *testing.T) {
	for _, change := range []string{"", "streaming", "error", "changed-binding", "changed-dispatch", "changed-store", "changed-goal", "cancel-before", "cancel-native", "changed-occupant", "changed-cwd", "changed-sequence"} {
		t.Run(change, func(t *testing.T) {
			c, j, _, _, _, r := agyCompletionFixture(t, "")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if change == "cancel-before" {
				cancel()
			}
			reads := 0
			agent := func(context.Context, string) (AgentInfo, error) {
				reads++
				a, _ := c.actions.list(ctx, c.hosts[j.Host])
				if reads == 2 {
					switch change {
					case "changed-occupant":
						a[0].Name = "foreign"
					case "changed-cwd":
						a[0].Cwd += "/other"
					case "changed-sequence":
						a[0].StateChangeSeq++
					}
				}
				return a[0], nil
			}
			observe := func(context.Context, *nativeStore, string) (bool, error) {
				switch change {
				case "streaming":
					return false, nil
				case "error":
					return true, errMatrix
				case "cancel-native":
					cancel()
				case "changed-goal":
					j.GoalDelivery = "pending"
				case "changed-dispatch":
					r.dispatch.Profile = "foreign"
					_ = r.writeDispatch("dispatched", r.dispatch.Location)
				case "changed-binding", "changed-store":
					path := filepath.Join(filepath.Dir(r.file), "binding.json")
					var b map[string]any
					raw, _ := os.ReadFile(path)
					_ = json.Unmarshal(raw, &b)
					if change == "changed-store" {
						b["NativeStore"] = map[string]string{"source": "agy", "directory": filepath.Join(t.TempDir(), ".divybot-native", j.DispatchKey, "agy")}
					} else {
						b["FutureProof"] = "changed"
					}
					raw, _ = json.Marshal(b)
					_ = os.WriteFile(path, raw, 0600)
				}
				return true, nil
			}
			complete, err := c.agyCompletionWithObserver(ctx, j, syntheticAGYID, agent, observe)
			if change == "" {
				if !complete || err != nil {
					t.Fatal("valid native proof rejected", err)
				}
			} else if complete || err == nil {
				t.Fatal("changed or late native evidence certified completion")
			}
		})
	}
}

func TestAGYPositiveBindingLogFollowsDurableProofOnly(t *testing.T) {
	for _, change := range []string{"", "foreign-cascade", "canceled", "dry"} {
		t.Run(change, func(t *testing.T) {
			c, j, _, _, _, r := agyCompletionFixture(t, change)
			store, err := readAGYStoreBinding(r)
			if err != nil || r.writeNativeIdentity(nil) != nil || r.writeAGYStore(store) != nil {
				t.Fatal("unbound fixture")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if change == "canceled" {
				cancel()
			}
			c.dry = change == "dry"
			var output bytes.Buffer
			prior := log.Writer()
			log.SetOutput(&output)
			defer log.SetOutput(prior)
			c.bindAGYLiveIdentity(ctx, c.hosts[j.Host], j)
			_, id, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, "agy")
			if err != nil {
				t.Fatal("binding readback")
			}
			if change == "" {
				if id != syntheticAGYID || strings.Count(output.String(), "AGY native identity bound") != 1 {
					t.Fatal("durable binding has no positive log")
				}
				c.bindAGYLiveIdentity(ctx, c.hosts[j.Host], j)
				if strings.Count(output.String(), "AGY native identity bound") != 1 {
					t.Fatal("existing binding emitted another write claim")
				}
			} else if id != "" || strings.Contains(output.String(), "AGY native identity bound") {
				t.Fatal("unproven binding emitted a positive log")
			}
			if strings.Contains(output.String(), syntheticAGYID) || strings.Contains(output.String(), store.Directory) {
				t.Fatal("native identity or store escaped into a log")
			}
		})
	}
}

func TestAGYPrivateIdentityWriteDeadline(t *testing.T) {
	for _, change := range []string{"", "entry", "write", "error"} {
		t.Run(change, func(t *testing.T) {
			_, j, r := agyLiveBindingFixture(t)
			directory, _ := agyStoreDirectory(filepath.Join(t.TempDir(), "checkout"), r.dispatch.RunID)
			if r.writeAGYStore(&nativeStore{Source: "agy", Directory: directory}) != nil {
				t.Fatal("fixture store")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if change == "entry" {
				cancel()
			}
			writes := 0
			bound := publishNativeIdentity(ctx, syntheticAGYID, func(id *string) error {
				writes++
				if change == "error" {
					return errMatrix
				}
				err := r.writeNativeIdentity(id)
				if id != nil && change == "write" {
					cancel()
				}
				return err
			})
			_, id, err := loadNativeBindingReceipt(filepath.Dir(filepath.Dir(filepath.Dir(r.file))), j.DispatchKey, j, "fixture/inbox", nil, "agy")
			if err != nil || bound != (change == "") || change == "" && id != syntheticAGYID || change != "" && id != "" || change == "entry" && writes != 0 || change == "write" && writes != 2 {
				t.Fatal("expired or failed publication retained identity or certified success")
			}
			if change == "write" {
				if _, err := readAGYStoreBinding(r); err == nil {
					t.Fatal("late publication retained usable native store authority")
				}
			}
		})
	}
}

func TestAGYDonePRWorkerKeepsEventRelayWithoutGoalReplay(t *testing.T) {
	c, j, _, _, closes, _ := agyCompletionFixture(t, "")
	j.PR, j.LastPoke = 8, time.Now()
	c.actions.completionPR = func(context.Context, *Job) (bool, error) { return true, nil }
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal("fixture ssh")
	}
	callLog := filepath.Join(filepath.Dir(ssh), "relay-log")
	t.Setenv("AGY_RELAY_LOG", callLog)
	script, _ := os.ReadFile(ssh)
	script = []byte(strings.Replace(string(script), "s=sys.argv[-1]\n", "s=sys.argv[-1]\nwith open(os.environ['AGY_RELAY_LOG'],'a') as f:f.write(s+'\\n')\n", 1))
	if os.WriteFile(ssh, script, 0700) != nil {
		t.Fatal("fixture log")
	}
	gh := "#!/bin/sh\nprintf '%s\\n' '{\"number\":8,\"state\":\"OPEN\",\"mergeable\":\"MERGEABLE\",\"statusCheckRollup\":[{\"name\":\"check\",\"conclusion\":\"FAILURE\"}]}'\n"
	if os.WriteFile(filepath.Join(filepath.Dir(ssh), "gh"), []byte(gh), 0700) != nil {
		t.Fatal("fixture gh")
	}
	c.supervise(context.Background(), j.Issue, j, map[int]agentRef{j.Issue: completionRef(j)}, Issue{Number: j.Issue})
	commands, err := os.ReadFile(callLog)
	if err != nil || strings.Count(string(commands), "'agent' 'prompt'") != 1 || !strings.Contains(string(commands), "New activity on your PR") || strings.Contains(string(commands), "continue — work the assigned issue") || *closes != 0 || len(c.st.CompletedRuns) != 0 {
		t.Fatal("AGY completion intercepted PR relay or replayed the goal", err)
	}
}
