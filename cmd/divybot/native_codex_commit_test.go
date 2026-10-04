package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- delivery level: R1–R4, R25 ----

type accPrompt struct {
	t                          *testing.T
	reads, submits, enters     int
	afterSubmit, readAfterSend bool
	now                        time.Time
	screen                     func(p *accPrompt, sent string) promptSnapshot
	sent                       string
}

func (p *accPrompt) deliver(h *accHarness, goal string) error {
	p.now = time.Unix(100, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return deliverCodexPrompt(ctx, promptFixture().Agent, goal, promptCalls{
		observe: func(context.Context) (promptSnapshot, error) {
			p.reads++
			if p.afterSubmit {
				p.readAfterSend = true
				return p.screen(p, p.sent), nil
			}
			return promptFixture(), nil
		},
		submit: func(_ context.Context, text string) error {
			p.submits++
			p.afterSubmit, p.sent = true, text
			h.f.setSent(text)
			return nil
		},
		enter: func(context.Context) error { p.enters++; return nil },
		wait:  func(context.Context) bool { p.now = p.now.Add(2 * time.Second); return p.now.Before(time.Unix(160, 0)) },
		now:   func() time.Time { return p.now },
		native: func(ctx context.Context, sent string, submit func(context.Context) error) error {
			return h.host.acceptCodexPrompt(ctx, h.acc, sent, submit)
		},
	})
}

func TestAcceptanceScreenNeverDecides(t *testing.T) {
	noTurn := func(h *accHarness) {
		h.acc.deadline = time.Now().Add(700 * time.Millisecond)
		h.f.page = func(f *accFixture, i int) ([]any, any) {
			return accDefaultPage(&accFixture{baseline: f.baseline, baseCur: f.baseCur}, i)
		}
	}
	placeholder := func(_ *accPrompt, sent string) promptSnapshot {
		s := promptFixture()
		s.Screen = "OpenAI Codex (v0.159.3)\n› [Pasted Content " + itoa(len([]rune(sent))) + " chars]\n"
		return s
	}
	cases := []struct {
		name   string
		setup  func(h *accHarness)
		screen func(p *accPrompt, sent string) promptSnapshot
		accept bool
	}{
		{"marker-and-working-without-native-turn", noTurn, func(_ *accPrompt, sent string) promptSnapshot { return consumedPrompt(sent) }, false},
		{"exact-native-turn-blank-screen", func(*accHarness) {}, func(*accPrompt, string) promptSnapshot { return promptSnapshot{} }, true},
		{"unchanged-screen", noTurn, func(*accPrompt, string) promptSnapshot { return promptFixture() }, false},
		{"collapsed-placeholder", noTurn, placeholder, false},
		{"partial-composer", noTurn, func(_ *accPrompt, sent string) promptSnapshot {
			s := promptFixture()
			s.Screen = "OpenAI Codex (v0.159.3)\n› " + sent[:20] + "\n"
			return s
		}, false},
		{"foreign-composer", noTurn, func(*accPrompt, string) promptSnapshot {
			s := promptFixture()
			s.Screen = "OpenAI Codex (v0.159.3)\n› someone else's text\n"
			return s
		}, false},
		{"empty-composer", noTurn, func(*accPrompt, string) promptSnapshot { return promptFixture() }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newAccHarness(t)
			c.setup(h)
			p := &accPrompt{t: t, screen: c.screen}
			err := p.deliver(h, promptFixtureGoal)
			if p.submits != 1 || p.enters != 0 {
				t.Fatalf("effects: submits=%d enters=%d", p.submits, p.enters)
			}
			if p.readAfterSend {
				t.Fatal("pane read inside the acceptance loop")
			}
			if c.accept != (err == nil) {
				t.Fatalf("decision: err=%v accept=%v", err, c.accept)
			}
		})
	}
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Replace(string(rune('0'+0)), "0", "", 1)) + jsonNumber(n)
}
func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// R10: the production branch transports the nonce-wrapped staged pointer
// through injectGoal; the digest is of that exact argument.
func TestAcceptanceStagedPointerThroughInjectGoal(t *testing.T) {
	h := newAccHarness(t)
	state := t.TempDir()
	h.f.sentFile = filepath.Join(state, "prompt")
	bin := filepath.Join(h.f.home, ".local", "bin")
	if os.MkdirAll(bin, 0700) != nil {
		t.Fatal("fixture")
	}
	agent, _ := json.Marshal(map[string]any{"result": map[string]any{"agent": map[string]any{"agent": "codex", "name": "codex-fixture", "pane_id": "w1:p1", "workspace_id": "w1",
		"agent_status": "idle", "state_change_seq": 1, "interactive_ready": true, "cwd": "/fixture/project"}}})
	screen := "OpenAI Codex (v0.159.3)\n› Anything interesting on the docket?\n" + h.acc.run.NativeSessionID + " gpt · 100% left\n"
	_ = os.WriteFile(filepath.Join(state, "agent"), agent, 0600)
	_ = os.WriteFile(filepath.Join(state, "screen"), []byte(screen), 0600)
	script := `#!/usr/bin/env python3
import json,os,sys
d=os.environ['ACC_HERDR']; a=sys.argv[1:]
if a[:2]==['agent','get']: print(open(d+'/agent').read())
elif a[:2]==['pane','read']: print(json.dumps({'result':{'text':open(d+'/screen').read()}}))
elif a[:2]==['agent','prompt']:
 open(d+'/prompt','w').write(a[3]); print(json.dumps({'result':{}}))
elif a[:2]==['pane','send-keys']:
 open(d+'/enter','a').write('x'); print(json.dumps({'result':{}}))
else: print(json.dumps({'result':{}}))
`
	if os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700) != nil {
		t.Fatal("fixture")
	}
	t.Setenv("ACC_HERDR", state)
	h.host.Acceptance = h.acc
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := h.host.injectGoal(ctx, "w1:p1", runPointer, true); err != nil {
		t.Fatalf("staged pointer not accepted: %v", err)
	}
	sent, _ := os.ReadFile(h.f.sentFile)
	sum := sha256.Sum256(sent)
	r := h.receipt()
	if !strings.Contains(string(sent), runPointer) || !strings.HasPrefix(string(sent), "Assignment delivery marker: ") || r.Digest != hex.EncodeToString(sum[:]) || r.Length != len(sent) || r.State != "native-accepted" {
		t.Fatal("receipt digest is not the exact transported pointer")
	}
	if _, err := os.Stat(filepath.Join(state, "enter")); err == nil {
		t.Fatal("Enter sent on the native path")
	}
	// Without an acceptance configuration an RC Codex host has no screen path.
	h2 := newAccHarness(t)
	if err := h2.host.injectCodexGoal(ctx, "w1:p1", runPointer, promptFixture().Agent); err == nil {
		t.Fatal("RC Codex delivered without native acceptance")
	}
}

// ---- T2: the C3 caller commit on a real state file ----

type commitFixture struct {
	c    *Coord
	j    *Job
	path string
}

func newCommitFixture(t *testing.T) *commitFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	st := loadState(path)
	c := &Coord{st: st, cfg: &Config{}}
	j := &Job{Issue: 7, Agent: "codex", Label: "codex-fixture", Host: "fixture-host", Pane: "w1:p1", Workspace: "w1",
		DispatchKey: strings.Repeat("d", 64), GoalDelivery: "pending", RemoteControl: syntheticRemoteRun(t),
		NativeGoal: &dispatchGoal{ReceiptKey: strings.Repeat("d", 64), Reason: "native-session-unavailable"}}
	st.Jobs[7] = j
	st.Jobs[8] = &Job{Issue: 8, Agent: "claude", GoalDelivery: "pending"}
	if st.saveLocked() != nil {
		t.Fatal("fixture save")
	}
	return &commitFixture{c: c, j: j, path: path}
}

func (f *commitFixture) restart() *Job { return loadState(f.path).Jobs[7] }
func (f *commitFixture) bytes() []byte { b, _ := os.ReadFile(f.path); return b }
func tuple(j *Job) [3]any {
	p := false
	if j.NativeGoal != nil {
		p = j.NativeGoal.PromptConfirmed
	}
	return [3]any{j.GoalDelivery, j.GoalDeliveryCommit, p}
}

var committedTuple = [3]any{"confirmed", deliveryCommitMark, true}
var pendingTuple = [3]any{"pending", "", false}

func TestRemoteCodexCommitTransaction(t *testing.T) {
	future := time.Now().Add(time.Minute)
	t.Run("c3-refuses-without-write", func(t *testing.T) {
		f := newCommitFixture(t)
		before := f.bytes()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if f.c.commitRemoteCodexDelivery(ctx, future, f.j) == nil || !bytes.Equal(before, f.bytes()) || tuple(f.j) != pendingTuple || !goalDeliveryUnconfirmed(f.restart()) {
			t.Fatal("C3 refusal wrote or confirmed")
		}
		if f.c.commitRemoteCodexDelivery(context.Background(), time.Now().Add(-time.Second), f.j) == nil {
			t.Fatal("expired C3 committed")
		}
	})
	t.Run("cancel-during-held-save-commits", func(t *testing.T) {
		f := newCommitFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		f.c.st.fs = &stateFS{hold: func(string) { cancel() }}
		if err := f.c.commitRemoteCodexDelivery(ctx, future, f.j); err != nil {
			t.Fatalf("on-time C3 refused after its write: %v", err)
		}
		if tuple(f.j) != committedTuple || tuple(f.restart()) != committedTuple || !deliveryConfirmed(f.restart()) || goalDeliveryUnconfirmed(f.restart()) {
			t.Fatal("committed tuple not persisted")
		}
	})
	for _, stage := range []string{"file", "rename"} {
		t.Run("before-rename-"+stage, func(t *testing.T) {
			f := newCommitFixture(t)
			before := f.bytes()
			fail := errors.New("injected")
			f.c.st.fs = &stateFS{}
			if stage == "file" {
				f.c.st.fs.syncFile = func(*os.File) error { return fail }
			} else {
				f.c.st.fs.rename = func(string, string) error { return fail }
			}
			if f.c.commitRemoteCodexDelivery(context.Background(), future, f.j) == nil || !bytes.Equal(before, f.bytes()) {
				t.Fatal("pre-rename failure committed")
			}
			if tuple(f.j) != pendingTuple {
				t.Fatalf("memory not restored: %v", tuple(f.j))
			}
			f.c.st.fs = nil
			f.c.st.mu.Lock()
			f.c.st.Jobs[8].Title = "unrelated"
			_ = f.c.st.saveLocked()
			f.c.st.mu.Unlock()
			if r := f.restart(); tuple(r) != pendingTuple || !goalDeliveryUnconfirmed(r) {
				t.Fatalf("a later unrelated save persisted part of the refused commit: %v", tuple(r))
			}
		})
	}
	t.Run("dir-fsync-after-rename", func(t *testing.T) {
		f := newCommitFixture(t)
		before := f.bytes()
		f.c.st.fs = &stateFS{syncDir: func(*os.File) error { return errors.New("injected") }}
		var logs bytes.Buffer
		restore := accCaptureLog(&logs)
		err := f.c.commitRemoteCodexDelivery(context.Background(), future, f.j)
		restore()
		if err != nil || tuple(f.j) != committedTuple || tuple(f.restart()) != committedTuple || !strings.Contains(logs.String(), "delivery-commit-durability-uncertain") {
			t.Fatal("renamed commit with uncertain durability not committed")
		}
		_ = os.WriteFile(f.path, before, 0600) // a crash that lost the rename
		if !goalDeliveryUnconfirmed(f.restart()) {
			t.Fatal("rolled-back state read as delivered")
		}
	})
	t.Run("crash-before-rename", func(t *testing.T) {
		f := newCommitFixture(t)
		f.c.st.fs = &stateFS{hold: func(string) { panic("crash") }}
		func() {
			defer func() { _ = recover() }()
			_ = f.c.commitRemoteCodexDelivery(context.Background(), future, f.j)
		}()
		if _, err := os.Stat(f.path + ".tmp"); err != nil {
			t.Fatal("crash fixture left no temp file")
		}
		if r := f.restart(); tuple(r) != pendingTuple || !goalDeliveryUnconfirmed(r) {
			t.Fatal("staged temp state was read")
		}
	})
	t.Run("markerless-confirmed-is-unconfirmed", func(t *testing.T) {
		f := newCommitFixture(t)
		f.j.GoalDelivery, f.j.NativeGoal.PromptConfirmed = "confirmed", true
		if deliveryConfirmed(f.j) || !goalDeliveryUnconfirmed(f.j) {
			t.Fatal("markerless confirmation accepted")
		}
	})
	t.Run("refused-then-blocking-saves-fail", func(t *testing.T) {
		f := newCommitFixture(t)
		f.c.st.fs = &stateFS{syncFile: func(*os.File) error { return errors.New("injected") }}
		if f.c.commitRemoteCodexDelivery(context.Background(), future, f.j) == nil {
			t.Fatal("refused commit succeeded")
		}
		f.c.blockGoalDelivery(7, f.j)
		if strings.Contains(string(f.bytes()), `"goal_delivery": "confirmed"`) || !goalDeliveryUnconfirmed(f.restart()) {
			t.Fatal("pathname shows confirmed after a refusal")
		}
	})
	t.Run("on-time-positive", func(t *testing.T) {
		f := newCommitFixture(t)
		if f.c.commitRemoteCodexDelivery(context.Background(), future, f.j) != nil || goalDeliveryUnconfirmed(f.j) {
			t.Fatal("on-time commit refused")
		}
		r := f.restart()
		if tuple(r) != committedTuple || goalDeliveryUnconfirmed(r) {
			t.Fatal("committed tuple lost on restart")
		}
		if !retryBoundGoalEligible(r) {
			t.Fatal("qualifying retry state refused")
		}
		r.NativeGoal.Owned = true
		if retryBoundGoalEligible(r) {
			t.Fatal("owned goal became retry-eligible")
		}
	})
	// A later save failing inside the same commit cannot leave confirmed
	// without the marker: the commit is one save.
	t.Run("single-save", func(t *testing.T) {
		f := newCommitFixture(t)
		calls := 0
		f.c.st.fs = &stateFS{syncFile: func(file *os.File) error {
			calls++
			if calls > 1 {
				return errors.New("injected")
			}
			return file.Sync()
		}}
		if err := f.c.commitRemoteCodexDelivery(context.Background(), future, f.j); err != nil || calls != 1 {
			t.Fatalf("commit was not a single save: err=%v saves=%d", err, calls)
		}
		if r := f.restart(); tuple(r) != committedTuple {
			t.Fatalf("pathname tuple: %v", tuple(r))
		}
	})
	t.Run("nil-native-goal", func(t *testing.T) {
		f := newCommitFixture(t)
		before := f.bytes()
		f.j.NativeGoal = nil
		if f.c.commitRemoteCodexDelivery(context.Background(), future, f.j) == nil || !bytes.Equal(before, f.bytes()) || f.j.GoalDelivery != "pending" {
			t.Fatal("nil native goal committed")
		}
	})
}

// ---- T3: delivery-authority consumers ----

type consumerFixture struct {
	c     *Coord
	j     *Job
	posts *int
}

func newConsumerFixture(t *testing.T, marker, prompt bool) *consumerFixture {
	t.Helper()
	c, j, _, posts, _ := finalPublicationFixture(t)
	run := syntheticRemoteRun(t)
	j.RemoteControl = run
	j.GoalDelivery = "confirmed"
	if marker {
		j.GoalDeliveryCommit = deliveryCommitMark
	}
	j.NativeGoal = &dispatchGoal{ReceiptKey: j.DispatchKey, Reason: "native-session-unavailable", PromptConfirmed: prompt}
	host := c.hosts[j.Host]
	host.Home = t.TempDir()
	c.hosts[j.Host] = host
	c.cfg.Hosts = []Host{host}
	state := t.TempDir()
	bin := filepath.Join(host.Home, ".local", "bin")
	_ = os.MkdirAll(bin, 0700)
	row := nativeStartFixture(t, run.NativeSessionID)
	row["type"] = "agent_info"
	a := row["agent"].(map[string]any)
	a["cwd"], a["agent_status"], a["name"], a["pane_id"], a["workspace_id"] = run.Cwd, "idle", j.Label, j.Pane, j.Workspace
	agent, _ := json.Marshal(map[string]any{"result": row})
	_ = os.WriteFile(filepath.Join(state, "agent"), agent, 0600)
	_ = os.WriteFile(filepath.Join(state, "screen"), []byte("OpenAI Codex (v0.159.3)\n› Ask Codex to do anything\n"+run.NativeSessionID+" gpt · 100% left\n"), 0600)
	script := `#!/usr/bin/env python3
import json,os,sys
d=os.environ['ACC_CONSUMER']; a=sys.argv[1:]
if a[:2]==['agent','get']: print(open(d+'/agent').read())
elif a[:2]==['pane','read']: print(json.dumps({'result':{'text':open(d+'/screen').read()}}))
else: print(json.dumps({'result':{}}))
`
	_ = os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700)
	t.Setenv("ACC_CONSUMER", state)
	// RC-shaped binding, occupant and native completion for this exact thread.
	record := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
	binding, _ := json.Marshal(map[string]string{"NativeSessionID": run.NativeSessionID, "Repo": "example/repo"})
	_ = os.WriteFile(filepath.Join(record, "binding.json"), binding, 0600)
	c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
		return []AgentInfo{{Agent: "codex", AgentStatus: "done", InteractiveReady: true, Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace,
			StateChangeSeq: 42, Cwd: run.Cwd, AgentSession: json.RawMessage(`{"agent":"codex","kind":"id","source":"herdr:codex","value":"` + run.NativeSessionID + `"}`)}}, nil
	}
	c.actions.completed = func(_ context.Context, _ Host, _ *Job, native string) (bool, error) {
		return native == run.NativeSessionID, nil
	}
	gh := t.TempDir()
	_ = os.WriteFile(filepath.Join(gh, "gh"), []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0700)
	t.Setenv("PATH", gh+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &consumerFixture{c: c, j: j, posts: posts}
}

func (f *consumerFixture) fenced() bool {
	f.c.st.mu.Lock()
	defer f.c.st.mu.Unlock()
	_, ok := f.c.st.CompletedRuns[f.j.Issue]
	return ok
}

func TestDeliveryAuthorityConsumers(t *testing.T) {
	entries := map[string]func(f *consumerFixture){
		"tick": func(f *consumerFixture) {
			f.c.completionPass(context.Background(), map[int]agentRef{7: completionRef(f.j)})
		},
		"supervise": func(f *consumerFixture) {
			f.c.supervise(context.Background(), 7, f.j, map[int]agentRef{7: completionRef(f.j)}, Issue{Number: 7})
		},
	}
	for entry, run := range entries {
		t.Run("markerless-"+entry, func(t *testing.T) {
			f := newConsumerFixture(t, false, true)
			run(f)
			if *f.posts != 0 || f.fenced() {
				t.Fatalf("markerless confirmation authorized effects: posts=%d fenced=%v", *f.posts, f.fenced())
			}
		})
		t.Run("missing-prompt-bit-"+entry, func(t *testing.T) {
			f := newConsumerFixture(t, true, false)
			run(f)
			if *f.posts != 0 || f.fenced() {
				t.Fatal("marker without the prompt bit authorized effects")
			}
		})
	}
	t.Run("committed-positive", func(t *testing.T) {
		f := newConsumerFixture(t, true, true)
		entries["tick"](f)
		if *f.posts != 1 || !f.fenced() {
			t.Fatalf("committed delivery lost its effects: posts=%d fenced=%v", *f.posts, f.fenced())
		}
	})
	t.Run("already-fenced-unchanged", func(t *testing.T) {
		results := map[bool]bool{}
		for _, marker := range []bool{false, true} {
			f := newConsumerFixture(t, marker, true)
			f.c.st.mu.Lock()
			f.c.st.CompletedRuns = map[int]completedRun{7: {DispatchKey: f.j.DispatchKey, NativeSessionID: "thread-fixture", StateChangeSeq: 42, Phase: "retiring"}}
			f.c.st.mu.Unlock()
			results[marker] = f.c.retireCompleted(context.Background(), 7, f.j, completionRef(f.j), true)
		}
		if results[false] != results[true] {
			t.Fatal("durable completion-fence recovery now depends on the delivery marker")
		}
	})
	t.Run("agent-scoping", func(t *testing.T) {
		for _, marker := range []bool{false, true} {
			f := newConsumerFixture(t, marker, true)
			if agyBindingEligible(f.j) || openCodeBindingEligible(f.j) {
				t.Fatal("agent-scoped reader admitted an RC Codex job")
			}
		}
	})
	t.Run("direct-seams", func(t *testing.T) {
		f := newConsumerFixture(t, false, true)
		if f.c.finalProof(context.Background(), f.c.hosts[f.j.Host], f.j, finalReportScope{}) == nil {
			t.Fatal("finalProof accepted a markerless job")
		}
		if retryBoundGoalEligible(f.j) {
			t.Fatal("goal retry accepted a markerless job")
		}
		f.j.GoalDeliveryCommit = deliveryCommitMark
		if !retryBoundGoalEligible(f.j) {
			t.Fatal("goal retry refused a committed job")
		}
	})
}

// ---- T3h: required-guard AST inventory ----

type inventoryCounts struct{ confirmed, prompt int }

func inventoryFile(src map[string]string) (map[string]inventoryCounts, map[string]*ast.FuncDecl, error) {
	fset := token.NewFileSet()
	counts := map[string]inventoryCounts{}
	funcs := map[string]*ast.FuncDecl{}
	for name, body := range src {
		file, err := parser.ParseFile(fset, name, body, 0)
		if err != nil {
			return nil, nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			funcs[fn.Name.Name] = fn
			writes := map[ast.Expr]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok {
					for _, l := range as.Lhs {
						writes[l] = true
					}
				}
				return true
			})
			c := counts[fn.Name.Name]
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BinaryExpr:
					if x.Op == token.EQL || x.Op == token.NEQ {
						for _, pair := range [][2]ast.Expr{{x.X, x.Y}, {x.Y, x.X}} {
							sel, ok := pair[0].(*ast.SelectorExpr)
							lit, lok := pair[1].(*ast.BasicLit)
							if ok && lok && sel.Sel.Name == "GoalDelivery" && lit.Value == `"confirmed"` {
								c.confirmed++
							}
						}
					}
				case *ast.SelectorExpr:
					if x.Sel.Name == "PromptConfirmed" && !writes[x] {
						c.prompt++
					}
				}
				return true
			})
			counts[fn.Name.Name] = c
		}
	}
	return counts, funcs, nil
}

func exprText(e ast.Node) string {
	var b strings.Builder
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			b.WriteString(x.Name + " ")
		case *ast.UnaryExpr:
			b.WriteString(x.Op.String())
		case *ast.BinaryExpr:
			b.WriteString(x.Op.String() + " ")
		}
		return true
	})
	return b.String()
}

// commitSnapshotValid enforces the one rollback-snapshot read (F5-R7-A).
func commitSnapshotValid(fn *ast.FuncDecl) bool {
	var lockAt, setAt token.Pos
	snap := ""
	var snapAt token.Pos
	uses := 0
	restores := 0
	for _, stmt := range fn.Body.List {
		ast.Inspect(stmt, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Lock" && lockAt == token.NoPos {
					lockAt = call.Pos()
				}
			}
			if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
				if sel, ok := as.Rhs[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "PromptConfirmed" {
					if id, ok := as.Lhs[0].(*ast.Ident); ok {
						snap, snapAt = id.Name, as.Pos()
					}
				}
				if sel, ok := as.Lhs[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "PromptConfirmed" {
					if id, ok := as.Rhs[0].(*ast.Ident); ok && id.Name == "true" && setAt == token.NoPos {
						setAt = as.Pos()
					}
				}
			}
			return true
		})
	}
	if snap == "" || lockAt == token.NoPos || setAt == token.NoPos || !(lockAt < snapAt && snapAt < setAt) {
		return false
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == snap && id.Pos() != snapAt {
			uses++
		}
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if sel, ok := as.Lhs[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "PromptConfirmed" {
				if id, ok := as.Rhs[0].(*ast.Ident); ok && id.Name == snap {
					restores++
				}
			}
		}
		return true
	})
	return uses == restores && restores >= 1
}

func checkDeliveryInventory(src map[string]string) error {
	counts, funcs, err := inventoryFile(src)
	if err != nil {
		return err
	}
	allowed := map[string]inventoryCounts{
		"goalDeliveryUnconfirmed":   {2, 1},
		"retryBoundGoalEligible":    {0, 1},
		"commitRemoteCodexDelivery": {0, 1},
		"blockGoalDelivery":         {0, 0},
		"agyBindingEligible":        {1, 0},
		"openCodeBindingEligible":   {1, 0},
		"superviseActive":           {1, 0},
	}
	for name, c := range counts {
		if name == "deliveryConfirmed" {
			continue
		}
		if c != allowed[name] && (c.confirmed > 0 || c.prompt > 0) {
			return errors.New("raw delivery read outside its allowance: " + name)
		}
	}
	// The required guards (T3h-1), located structurally.
	guard := func(expr string) string {
		e, err := parser.ParseExpr(expr)
		if err != nil {
			return "\x00"
		}
		return exprText(e)
	}
	rcGuard, plainGuard := guard("rcCodex(j) && !deliveryConfirmed(j)"), guard("!deliveryConfirmed(j)")
	want := map[string]string{
		"goalDeliveryUnconfirmed": rcGuard,
		"observeFinalReport":      plainGuard,
		"finalProof":              plainGuard,
		"retryBoundGoalEligible":  rcGuard,
	}
	for name, guard := range want {
		fn := funcs[name]
		if fn == nil || !strings.Contains(exprText(fn.Body), guard) {
			return errors.New("required guard missing: " + name)
		}
	}
	retire := funcs["retireCompleted"]
	if retire == nil {
		return errors.New("required guard missing: retireCompleted")
	}
	unfenced, fencedUse := false, false
	ast.Inspect(retire.Body, func(n ast.Node) bool {
		if ifs, ok := n.(*ast.IfStmt); ok {
			text := exprText(ifs.Cond)
			if strings.HasPrefix(text, guard("!fenced && x")[:len("&& !fenced ")]) && strings.Contains(text, plainGuard) {
				unfenced = true
			} else if strings.Contains(text, "deliveryConfirmed") {
				fencedUse = true
			}
		}
		return true
	})
	if !unfenced || fencedUse {
		return errors.New("required guard missing: retireCompleted unfenced gate")
	}
	if commit := funcs["commitRemoteCodexDelivery"]; commit == nil || !commitSnapshotValid(commit) {
		return errors.New("commit snapshot read is not bookkeeping-only")
	}
	if a := funcs["retryBoundGoalEligible"]; a != nil {
		vetoed := false
		ast.Inspect(a.Body, func(n ast.Node) bool {
			if ifs, ok := n.(*ast.IfStmt); ok && exprText(ifs.Cond) == rcGuard {
				for _, s := range ifs.Body.List {
					if r, ok := s.(*ast.ReturnStmt); ok && len(r.Results) == 1 && exprText(r.Results[0]) == "false " {
						vetoed = true
					}
				}
			}
			return true
		})
		if !vetoed {
			return errors.New("required guard missing: retry veto")
		}
	}
	return nil
}

func productionSources(t *testing.T) map[string]string {
	t.Helper()
	src := map[string]string{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, string(filepath.Separator)) {
			return err
		}
		b, err := os.ReadFile(path)
		src[path] = string(b)
		return err
	})
	if err != nil {
		t.Fatal("production sources unavailable")
	}
	return src
}

func TestDeliveryAuthorityInventory(t *testing.T) {
	src := productionSources(t)
	if err := checkDeliveryInventory(src); err != nil {
		t.Fatalf("baseline inventory: %v", err)
	}
	negatives := []struct{ name, file, old, new string }{
		{"guard-goalDeliveryUnconfirmed", "goal_delivery.go", " ||\n\t\t(rcCodex(j) && !deliveryConfirmed(j)))", ")"},
		{"guard-observeFinalReport", "final_report_publication.go", "j.RunMode || !deliveryConfirmed(j) || !known", "j.RunMode || !known"},
		{"guard-finalProof", "final_report_publication.go", "ctx.Err() != nil || !deliveryConfirmed(j) {", "ctx.Err() != nil || j.GoalDelivery != \"confirmed\" {"},
		{"guard-retireCompleted", "completion.go", "j.Issue != n || !deliveryConfirmed(j) || j.RunMode", "j.Issue != n || j.RunMode"},
		{"guard-retry", "native_goal.go", "\tif rcCodex(j) && !deliveryConfirmed(j) {\n\t\treturn false\n\t}\n", ""},
		{"raw-in-new-function", "goal_delivery.go", "func rcCodex(j *Job) bool {", "func rawConfirmed(j *Job) bool { return j.GoalDelivery == \"confirmed\" }\n\nfunc rcCodex(j *Job) bool {"},
		{"extra-in-goalDeliveryUnconfirmed", "goal_delivery.go", "return j != nil && !j.RunMode && (", "return j != nil && !j.RunMode && (j.GoalDelivery == \"confirmed\" && false ||"},
		{"extra-in-retry", "native_goal.go", "\tif rcCodex(j) && !deliveryConfirmed(j) {", "\tif j.NativeGoal.PromptConfirmed && false {\n\t\treturn false\n\t}\n\tif rcCodex(j) && !deliveryConfirmed(j) {"},
		{"completion-guard-fenced", "completion.go", "\tif j.RemoteControl != nil {\n\t\tif fenced {", "\tif fenced && !deliveryConfirmed(j) {\n\t\treturn false\n\t}\n\tif j.RemoteControl != nil {\n\t\tif fenced {"},
		{"commit-extra-read", "native_codex_commit.go", "\tj.GoalDelivery, j.GoalDeliveryCommit = \"confirmed\", deliveryCommitMark", "\tif j.NativeGoal.PromptConfirmed {\n\t\treturn nil\n\t}\n\tj.GoalDelivery, j.GoalDeliveryCommit = \"confirmed\", deliveryCommitMark"},
		{"commit-confirmed-comparison", "native_codex_commit.go", "\tpriorDelivery, priorCommit := j.GoalDelivery, j.GoalDeliveryCommit", "\tif j.GoalDelivery == \"confirmed\" {\n\t\treturn nil\n\t}\n\tpriorDelivery, priorCommit := j.GoalDelivery, j.GoalDeliveryCommit"},
		{"commit-snapshot-reused", "native_codex_commit.go", "\tif err != nil {\n\t\tlog.Printf", "\tif priorPrompt {\n\t\treturn nil\n\t}\n\tif err != nil {\n\t\tlog.Printf"},
		{"commit-snapshot-after-write", "native_codex_commit.go", "\tpriorPrompt := j.NativeGoal.PromptConfirmed\n\tj.GoalDelivery, j.GoalDeliveryCommit = \"confirmed\", deliveryCommitMark\n\tj.NativeGoal.PromptConfirmed = true", "\tj.GoalDelivery, j.GoalDeliveryCommit = \"confirmed\", deliveryCommitMark\n\tj.NativeGoal.PromptConfirmed = true\n\tpriorPrompt := j.NativeGoal.PromptConfirmed"},
	}
	for _, n := range negatives {
		t.Run(n.name, func(t *testing.T) {
			copySrc := map[string]string{}
			for k, v := range src {
				copySrc[k] = v
			}
			if strings.Count(copySrc[n.file], n.old) != 1 {
				t.Fatalf("negative anchor drifted: %s", n.name)
			}
			copySrc[n.file] = strings.Replace(copySrc[n.file], n.old, n.new, 1)
			if checkDeliveryInventory(copySrc) == nil {
				t.Fatal("inventory accepted a removed or extra delivery read")
			}
		})
	}
}

// Compatibility: older state, other SSH callers and non-RC confirmation.
func TestAcceptanceCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if os.WriteFile(path, []byte(`{"jobs":{"9":{"issue":9,"agent":"claude","goal_delivery":"confirmed"}}}`), 0600) != nil {
		t.Fatal("fixture")
	}
	if j := loadState(path).Jobs[9]; j == nil || j.GoalDeliveryCommit != "" || !deliveryConfirmed(j) || goalDeliveryUnconfirmed(j) {
		t.Fatal("state without the commit field changed for a non-RC job")
	}
	plain := &Job{Agent: "codex", GoalDelivery: "confirmed", NativeGoal: &dispatchGoal{PromptConfirmed: true}}
	if !deliveryConfirmed(plain) || goalDeliveryUnconfirmed(plain) {
		t.Fatal("non-RC Codex confirmation semantics changed")
	}
	var fail stateSaveError
	if !errors.As(error(stateSaveError{afterRename: true, err: errors.New("x")}), &fail) || fail.Error() != "x" {
		t.Fatal("phase-aware save error lost its message")
	}
}
