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
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		wait: func(context.Context) bool { p.now = p.now.Add(2 * time.Second); return p.now.Before(time.Unix(160, 0)) },
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
		// R3: the unchanged screen persists for at least ten seconds of the
		// acceptance clock; the native loop still sends once and never Enter.
		{"unchanged-screen", func(h *accHarness) {
			noTurn(h)
			start := time.Unix(1000, 0)
			var mu sync.Mutex
			now := start
			h.acc.now = func() time.Time {
				mu.Lock()
				defer mu.Unlock()
				now = now.Add(time.Second)
				return now
			}
			h.acc.deadline = start.Add(15 * time.Second)
			h.after = func(t *testing.T) {
				mu.Lock()
				defer mu.Unlock()
				if now.Sub(start) < 10*time.Second {
					t.Fatalf("unchanged screen covered only %v", now.Sub(start))
				}
			}
		}, func(*accPrompt, string) promptSnapshot { return promptFixture() }, false},
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
			if h.after != nil {
				h.after(t)
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

// stageHerdrPromptHost installs a fake Herdr for injectGoal on h's host and
// returns its state dir; a submitted prompt lands in <state>/prompt.
func stageHerdrPromptHost(t *testing.T, h *accHarness) string {
	t.Helper()
	state := t.TempDir()
	h.f.sentFile = filepath.Join(state, "prompt")
	bin := filepath.Join(h.f.home, ".local", "bin")
	if os.MkdirAll(bin, 0700) != nil {
		t.Fatal("fixture")
	}
	agent, _ := json.Marshal(map[string]any{"result": map[string]any{"agent": map[string]any{"agent": "codex", "name": "codex-fixture", "pane_id": "w1:p1", "workspace_id": "w1",
		"agent_status": "idle", "state_change_seq": 1, "interactive_ready": true, "cwd": "/fixture/project"}}})
	screen := "OpenAI Codex (v0.159.3)\n› Anything interesting on the docket?\n" + h.acc.run.NativeSessionID + " fixture-footer\n"
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
	return state
}

// Delivery uses the resume proof stored before the goal: without it, or with
// its pin lost, nothing is typed. It never re-reads the native log.
func TestAcceptanceRefusesWithoutStoredProof(t *testing.T) {
	for _, mode := range []string{"no-proof", "no-pin"} {
		t.Run(mode, func(t *testing.T) {
			h := newAccHarness(t)
			state := stageHerdrPromptHost(t, h)
			if mode == "no-proof" {
				h.host.RemoteRun.Resume = nil
			} else {
				h.host.RemoteRun.ClientVersion = ""
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := h.host.injectGoal(ctx, "w1:p1", runPointer, true); err == nil {
				t.Fatal("goal delivered without the stored resume proof")
			}
			if _, err := os.Stat(filepath.Join(state, "prompt")); err == nil {
				t.Fatal("goal typed into an unproven attachment")
			}
		})
	}
}

// R10: the production branch transports the nonce-wrapped staged pointer
// through injectGoal; the digest is of that exact argument.
func TestAcceptanceStagedPointerThroughInjectGoal(t *testing.T) {
	h := newAccHarness(t)
	state := stageHerdrPromptHost(t, h)
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
	run.ClientVersion, run.Resume = "9.1.0", &codexResumeProof{Producer: strings.Repeat("ab", 32), Connection: "252"} // proven once before the goal
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
	_ = os.WriteFile(filepath.Join(state, "screen"), []byte("OpenAI Codex (v0.159.3)\n› Ask Codex to do anything\n"+run.NativeSessionID+" fixture-footer\n"), 0600)
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
		f := newConsumerFixture(t, true, true)
		_, scope, err := f.c.finalScope(f.j)
		if err != nil {
			t.Fatal("persisted final scope unavailable")
		}
		host := f.c.hosts[f.j.Host]
		// The committed positive passes with the real scope, so only the
		// delivery predicate can refuse the markerless job below.
		if f.c.finalProof(context.Background(), host, f.j, scope) != nil || !retryBoundGoalEligible(f.j) {
			t.Fatal("committed positive refused at a direct seam")
		}
		f.j.GoalDeliveryCommit = ""
		if f.c.finalProof(context.Background(), host, f.j, scope) == nil {
			t.Fatal("finalProof accepted a markerless job")
		}
		if retryBoundGoalEligible(f.j) {
			t.Fatal("goal retry accepted a markerless job")
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

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// operands flattens a chain of one boolean operator, through parentheses.
func operands(e ast.Expr, op token.Token) []ast.Expr {
	e = unparen(e)
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == op {
		return append(operands(b.X, op), operands(b.Y, op)...)
	}
	return []ast.Expr{e}
}

func hasOperand(e ast.Expr, op token.Token, want string) bool {
	for _, x := range operands(e, op) {
		if types.ExprString(unparen(x)) == want {
			return true
		}
	}
	return false
}

func returnsAt(body *ast.BlockStmt) bool {
	return len(body.List) > 0 && func() bool { _, ok := body.List[len(body.List)-1].(*ast.ReturnStmt); return ok }()
}

// predicateUses counts deliveryConfirmed calls in a function: a guarded
// function has exactly one, so a dead or duplicate copy is refused.
func predicateUses(fn *ast.FuncDecl) int {
	n := 0
	ast.Inspect(fn.Body, func(x ast.Node) bool {
		if call, ok := x.(*ast.CallExpr); ok && types.ExprString(call.Fun) == "deliveryConfirmed" {
			n++
		}
		return true
	})
	return n
}

// refusingIf reports whether stmt is a plain top-level if (no init, no else)
// whose body is exactly one return of ret ("" for a bare return).
func refusingIf(stmt ast.Stmt, ret string) (*ast.IfStmt, bool) {
	ifs, ok := stmt.(*ast.IfStmt)
	if !ok || ifs.Init != nil || ifs.Else != nil || len(ifs.Body.List) != 1 {
		return nil, false
	}
	r, ok := ifs.Body.List[0].(*ast.ReturnStmt)
	if !ok {
		return nil, false
	}
	parts := []string{}
	for _, e := range r.Results {
		parts = append(parts, types.ExprString(e))
	}
	return ifs, strings.Join(parts, ", ") == ret
}

// leadingGuard requires the operative delivery condition: the function's
// first statement refuses with ret when want is a direct || operand, and the
// predicate appears nowhere else in the function.
func leadingGuard(fn *ast.FuncDecl, want, ret string) bool {
	if fn == nil || len(fn.Body.List) == 0 || predicateUses(fn) != 1 {
		return false
	}
	ifs, ok := refusingIf(fn.Body.List[0], ret)
	return ok && hasOperand(ifs.Cond, token.LOR, want)
}

// commitSnapshotValid enforces the one rollback-snapshot read (F5-R7-A) in
// its real role: inside the c.st.mu critical section held by a deferred
// unlock, before the set, restored only in the before-rename failure branch.
func commitSnapshotValid(fn *ast.FuncDecl) bool {
	list := fn.Body.List
	lockAt, snapAt, setAt := -1, -1, -1
	snap := ""
	for i, stmt := range list {
		switch x := stmt.(type) {
		case *ast.ExprStmt:
			if types.ExprString(x.X) == "c.st.mu.Lock()" && lockAt < 0 && i+1 < len(list) {
				if d, ok := list[i+1].(*ast.DeferStmt); ok && types.ExprString(d.Call) == "c.st.mu.Unlock()" {
					lockAt = i
				}
			}
		case *ast.AssignStmt:
			if len(x.Lhs) == 1 && len(x.Rhs) == 1 {
				if types.ExprString(x.Rhs[0]) == "j.NativeGoal.PromptConfirmed" {
					if id, ok := x.Lhs[0].(*ast.Ident); ok {
						snap, snapAt = id.Name, i
					}
				}
				if types.ExprString(x.Lhs[0]) == "j.NativeGoal.PromptConfirmed" && types.ExprString(x.Rhs[0]) == "true" && setAt < 0 {
					setAt = i
				}
			}
		}
	}
	if lockAt < 0 || snap == "" || !(lockAt+1 < snapAt && snapAt < setAt) {
		return false
	}
	locks := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if s := types.ExprString(call.Fun); strings.HasSuffix(s, ".Lock") || strings.HasSuffix(s, ".Unlock") {
				locks++
			}
		}
		return true
	})
	if locks != 2 { // exactly the Lock and its deferred Unlock
		return false
	}
	// The before-rename failure branch, recognized exactly: the save, the
	// phase variable, then the top-level if on the before-rename condition.
	branch := -1
	for i := setAt + 1; i+2 < len(list); i++ {
		save, ok := list[i].(*ast.AssignStmt)
		decl, ok2 := list[i+1].(*ast.DeclStmt)
		ifs, ok3 := refusingIfBlock(list[i+2])
		if ok && ok2 && ok3 && types.ExprString(save.Rhs[0]) == "c.st.saveLocked()" &&
			strings.Contains(types.ExprString(decl.Decl.(*ast.GenDecl).Specs[0].(*ast.ValueSpec).Type), "stateSaveError") &&
			types.ExprString(ifs.Cond) == beforeRenameCond {
			branch = i + 2
			break
		}
	}
	if branch < 0 {
		return false
	}
	restores, uses := 0, 0
	for i, stmt := range list {
		ast.Inspect(stmt, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 &&
				types.ExprString(as.Lhs[0]) == "j.NativeGoal.PromptConfirmed" && types.ExprString(as.Rhs[0]) == snap {
				if i != branch || !directIn(list[branch].(*ast.IfStmt).Body, as) {
					restores = -100
				}
				restores++
			}
			if id, ok := n.(*ast.Ident); ok && id.Name == snap {
				uses++
			}
			return true
		})
	}
	return restores == 1 && uses == 2 // the snapshot declaration and its one restore
}

const beforeRenameCond = "err != nil && !(errors.As(err, &saved) && saved.afterRename)"

// refusingIfBlock: a plain top-level if (no init, no else) ending in
// return errPromptUnconfirmed.
func refusingIfBlock(stmt ast.Stmt) (*ast.IfStmt, bool) {
	ifs, ok := stmt.(*ast.IfStmt)
	if !ok || ifs.Init != nil || ifs.Else != nil || !returnsAt(ifs.Body) {
		return nil, false
	}
	r := ifs.Body.List[len(ifs.Body.List)-1].(*ast.ReturnStmt)
	return ifs, len(r.Results) == 1 && types.ExprString(r.Results[0]) == "errPromptUnconfirmed"
}

func directIn(body *ast.BlockStmt, stmt ast.Stmt) bool {
	for _, s := range body.List {
		if s == stmt {
			return true
		}
	}
	return false
}

// confirmCommitValid recognizes the non-RC goal commit exactly: Lock, deferred
// Unlock, the context decision, the two snapshots, the writes, one save, the
// phase variable, and the before-rename branch restoring exactly those
// snapshots. After that branch nothing reads the context or refuses.
func confirmCommitValid(fn *ast.FuncDecl) bool {
	list := fn.Body.List
	if len(list) < 10 {
		return false
	}
	stmt := func(s ast.Stmt) string {
		switch x := s.(type) {
		case *ast.ExprStmt:
			return types.ExprString(x.X)
		case *ast.DeferStmt:
			return "defer " + types.ExprString(x.Call)
		case *ast.AssignStmt:
			if len(x.Lhs) == 1 && len(x.Rhs) == 1 {
				return types.ExprString(x.Lhs[0]) + " " + x.Tok.String() + " " + types.ExprString(x.Rhs[0])
			}
		case *ast.DeclStmt:
			if g, ok := x.Decl.(*ast.GenDecl); ok && len(g.Specs) == 1 {
				if v, ok := g.Specs[0].(*ast.ValueSpec); ok && len(v.Names) == 1 && v.Type != nil && len(v.Values) == 0 {
					return "var " + v.Names[0].Name + " " + types.ExprString(v.Type)
				}
			}
		case *ast.ReturnStmt:
			parts := []string{}
			for _, r := range x.Results {
				parts = append(parts, types.ExprString(r))
			}
			return "return " + strings.Join(parts, ", ")
		}
		return ""
	}
	// The nil-guarded prompt write: `if j.NativeGoal != nil { j.NativeGoal.PromptConfirmed = <v> }`.
	guarded := func(s ast.Stmt, value string) bool {
		ifs, ok := s.(*ast.IfStmt)
		return ok && ifs.Init == nil && ifs.Else == nil && types.ExprString(ifs.Cond) == "j.NativeGoal != nil" &&
			len(ifs.Body.List) == 1 && stmt(ifs.Body.List[0]) == "j.NativeGoal.PromptConfirmed = "+value
	}
	decision, ok := refusingIf(list[2], "errPromptUnconfirmed")
	if stmt(list[0]) != "c.st.mu.Lock()" || stmt(list[1]) != "defer c.st.mu.Unlock()" || !ok || types.ExprString(decision.Cond) != "ctx.Err() != nil" ||
		stmt(list[3]) != "priorDelivery := j.GoalDelivery" || stmt(list[4]) != "priorPrompt := j.NativeGoal != nil && j.NativeGoal.PromptConfirmed" ||
		stmt(list[5]) != `j.GoalDelivery = "confirmed"` || !guarded(list[6], "true") ||
		stmt(list[7]) != "err := c.st.saveLocked()" || stmt(list[8]) != "var saved stateSaveError" {
		return false
	}
	branch, ok := refusingIfBlock(list[9])
	if !ok || types.ExprString(branch.Cond) != beforeRenameCond || len(branch.Body.List) != 3 ||
		stmt(branch.Body.List[0]) != "j.GoalDelivery = priorDelivery" || !guarded(branch.Body.List[1], "priorPrompt") {
		return false
	}
	// After the branch: no context veto and no refusal; the snapshots are used only
	// by their restores; exactly one save and one Lock/Unlock pair.
	idents := map[string]int{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			idents[id.Name]++
		}
		return true
	})
	for _, s := range list[10:] {
		bad := false
		ast.Inspect(s, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "ctx" {
				bad = true
			}
			if r, ok := n.(*ast.ReturnStmt); ok && stmt(r) != "return nil" {
				bad = true
			}
			return true
		})
		if bad {
			return false
		}
	}
	saves, locks := 0, 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			s := types.ExprString(call.Fun)
			if s == "c.st.saveLocked" {
				saves++
			}
			if strings.HasSuffix(s, ".Lock") || strings.HasSuffix(s, ".Unlock") {
				locks++
			}
		}
		return true
	})
	return saves == 1 && locks == 2 && idents["priorDelivery"] == 2 && idents["priorPrompt"] == 2 && idents["ctx"] == 1 // the one decision (the parameter is outside the body)
}

// confirmCallerValid (confirm-hazard V9): in spawn, the non-RC-Codex arm passes
// the launch's own goal context, declared once in the same block, and the
// refusal takes the existing block-and-return branch.
func confirmCallerValid(fn *ast.FuncDecl) bool {
	calls, gctxDecls, gctxWrites := 0, 0, 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if types.ExprString(x.Fun) == "c.confirmGoalDelivery" {
				calls++
			}
		case *ast.AssignStmt:
			for _, l := range x.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == "gctx" {
					gctxWrites++
					if x.Tok == token.DEFINE && len(x.Rhs) == 1 && types.ExprString(x.Rhs[0]) == "context.WithTimeout(ctx, 120 * time.Second)" {
						gctxDecls++
					}
				}
			}
		}
		return true
	})
	if calls != 1 || gctxDecls != 1 || gctxWrites != 1 {
		return false
	}
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		declared := false
		for i, s := range block.List {
			if as, ok := s.(*ast.AssignStmt); ok && len(as.Lhs) == 2 && types.ExprString(as.Lhs[0]) == "gctx" {
				declared = true
			}
			ifs, ok := s.(*ast.IfStmt)
			if !ok || !declared || types.ExprString(ifs.Cond) != "remoteCodex" || i+1 >= len(block.List) {
				continue
			}
			arm, ok := ifs.Else.(*ast.BlockStmt)
			if !ok || len(arm.List) != 1 {
				continue
			}
			as, ok := arm.List[0].(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 || types.ExprString(as.Lhs[0]) != "confirmErr" || as.Tok != token.ASSIGN ||
				types.ExprString(as.Rhs[0]) != "c.confirmGoalDelivery(gctx, j)" {
				continue
			}
			next, ok := block.List[i+1].(*ast.IfStmt)
			if !ok || next.Init != nil || next.Else != nil || types.ExprString(next.Cond) != "confirmErr != nil" || len(next.Body.List) != 4 {
				continue
			}
			body := []string{}
			for _, b := range next.Body.List {
				switch y := b.(type) {
				case *ast.ExprStmt:
					body = append(body, types.ExprString(y.X))
				case *ast.ReturnStmt:
					if len(y.Results) == 1 {
						body = append(body, "return "+types.ExprString(y.Results[0]))
					}
				}
			}
			// The native context end is captured first, before the cancel (launch outcome).
			if strings.Join(body, "; ") == `attempt.contextEnded(gctx); gcancel(); c.blockGoalDelivery(n, j); return matrixReason("goal-prompt-unconfirmed")` {
				found = true
			}
		}
		return true
	})
	return found
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
		"confirmGoalDelivery":       {0, 1}, // its one snapshot read, validated below
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
	// T3h-1: each required guard is a direct operand in its decisive place.
	// goalDeliveryUnconfirmed is a single return; the RC disjunct is a direct
	// operand of the || group that is a direct operand of its && chain.
	g := funcs["goalDeliveryUnconfirmed"]
	ok := false
	if g != nil && len(g.Body.List) == 1 && predicateUses(g) == 1 {
		if r, isRet := g.Body.List[0].(*ast.ReturnStmt); isRet && len(r.Results) == 1 {
			for _, conj := range operands(r.Results[0], token.LAND) {
				if hasOperand(conj, token.LOR, "rcCodex(j) && !deliveryConfirmed(j)") {
					ok = true
				}
			}
		}
	}
	if !ok {
		return errors.New("required guard missing: goalDeliveryUnconfirmed")
	}
	if !leadingGuard(funcs["observeFinalReport"], "!deliveryConfirmed(j)", "") {
		return errors.New("required guard missing: observeFinalReport")
	}
	if !leadingGuard(funcs["finalProof"], "!deliveryConfirmed(j)", "errFinalPublication") {
		return errors.New("required guard missing: finalProof")
	}
	retire := funcs["retireCompleted"]
	if retire == nil {
		return errors.New("required guard missing: retireCompleted")
	}
	// The unfenced gate is a top-level refusing if: !fenced && (... || !deliveryConfirmed(j) || ...).
	unfenced := false
	for _, stmt := range retire.Body.List {
		ifs, ok := refusingIf(stmt, "false")
		if !ok {
			continue
		}
		conj := operands(ifs.Cond, token.LAND)
		if len(conj) == 2 && types.ExprString(conj[0]) == "!fenced" && hasOperand(conj[1], token.LOR, "!deliveryConfirmed(j)") {
			unfenced = true
		}
	}
	if !unfenced || predicateUses(retire) != 1 {
		return errors.New("required guard missing: retireCompleted unfenced gate")
	}
	if commit := funcs["commitRemoteCodexDelivery"]; commit == nil || !commitSnapshotValid(commit) {
		return errors.New("commit snapshot read is not bookkeeping-only")
	}
	if confirm := funcs["confirmGoalDelivery"]; confirm == nil || !confirmCommitValid(confirm) {
		return errors.New("goal confirmation is not one on-time decision recorded by one save")
	}
	if funcs["confirmGoalDeliveryBeforeDeadline"] != nil {
		return errors.New("post-save deadline refusal reintroduced")
	}
	if spawn := funcs["spawn"]; spawn == nil || !confirmCallerValid(spawn) {
		return errors.New("goal confirmation caller does not forward its launch context to a blocking refusal")
	}
	retry := funcs["retryBoundGoalEligible"]
	vetoed := false
	if retry != nil && predicateUses(retry) == 1 {
		for _, stmt := range retry.Body.List {
			if ifs, ok := refusingIf(stmt, "false"); ok && types.ExprString(unparen(ifs.Cond)) == "rcCodex(j) && !deliveryConfirmed(j)" {
				vetoed = true
			}
		}
	}
	if !vetoed {
		return errors.New("required guard missing: retry veto")
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
	negatives := []struct {
		name, file, old, new string
		more                 [][2]string
	}{
		{"guard-goalDeliveryUnconfirmed", "goal_delivery.go", " ||\n\t\t(rcCodex(j) && !deliveryConfirmed(j)))", ")", nil},
		{"guard-observeFinalReport", "final_report_publication.go", "j.RunMode || !deliveryConfirmed(j) || !known", "j.RunMode || !known", nil},
		{"guard-finalProof", "final_report_publication.go", "ctx.Err() != nil || !deliveryConfirmed(j) {", "ctx.Err() != nil || j.GoalDelivery != \"confirmed\" {", nil},
		{"guard-retireCompleted", "completion.go", "j.Issue != n || !deliveryConfirmed(j) || j.RunMode", "j.Issue != n || j.RunMode", nil},
		{"guard-retry", "native_goal.go", "\tif rcCodex(j) && !deliveryConfirmed(j) {\n\t\treturn false\n\t}\n", "", nil},
		{"raw-in-new-function", "goal_delivery.go", "func rcCodex(j *Job) bool {", "func rawConfirmed(j *Job) bool { return j.GoalDelivery == \"confirmed\" }\n\nfunc rcCodex(j *Job) bool {", nil},
		{"extra-in-goalDeliveryUnconfirmed", "goal_delivery.go", "return j != nil && !j.RunMode && (", "return j != nil && !j.RunMode && (j.GoalDelivery == \"confirmed\" && false ||", nil},
		{"extra-in-retry", "native_goal.go", "\tif rcCodex(j) && !deliveryConfirmed(j) {", "\tif j.NativeGoal.PromptConfirmed && false {\n\t\treturn false\n\t}\n\tif rcCodex(j) && !deliveryConfirmed(j) {", nil},
		{"completion-guard-fenced", "completion.go", "\tif j.RemoteControl != nil {\n\t\tif fenced {", "\tif fenced && !deliveryConfirmed(j) {\n\t\treturn false\n\t}\n\tif j.RemoteControl != nil {\n\t\tif fenced {", nil},
		{"commit-extra-read", "native_codex_commit.go", "\tj.GoalDelivery, j.GoalDeliveryCommit = \"confirmed\", deliveryCommitMark", "\tif j.NativeGoal.PromptConfirmed {\n\t\treturn nil\n\t}\n\tj.GoalDelivery, j.GoalDeliveryCommit = \"confirmed\", deliveryCommitMark", nil},
		{"commit-confirmed-comparison", "native_codex_commit.go", "\tpriorDelivery, priorCommit := j.GoalDelivery, j.GoalDeliveryCommit", "\tif j.GoalDelivery == \"confirmed\" {\n\t\treturn nil\n\t}\n\tpriorDelivery, priorCommit := j.GoalDelivery, j.GoalDeliveryCommit", nil},
		{"commit-snapshot-reused", "native_codex_commit.go", "\tif err != nil {\n\t\tlog.Printf", "\tif priorPrompt {\n\t\treturn nil\n\t}\n\tif err != nil {\n\t\tlog.Printf", nil},
		{"outer-publication-bypass", "final_report_publication.go", "j.RunMode || !deliveryConfirmed(j) || !known", "j.RunMode || (false && !deliveryConfirmed(j)) || !known", nil},
		{"finalproof-bypass", "final_report_publication.go", "ctx.Err() != nil || !deliveryConfirmed(j) {", "ctx.Err() != nil || (false && !deliveryConfirmed(j)) {", nil},
		{"retire-bypass", "completion.go", "j.Issue != n || !deliveryConfirmed(j) || j.RunMode", "j.Issue != n || (false && !deliveryConfirmed(j)) || j.RunMode", nil},
		{"goal-unconfirmed-bypass", "goal_delivery.go", "(rcCodex(j) && !deliveryConfirmed(j)))", "(false && rcCodex(j) && !deliveryConfirmed(j)))", nil},
		{"snapshot-outside-critical-section", "native_codex_commit.go", "\tpriorPrompt := j.NativeGoal.PromptConfirmed", "\tc.st.mu.Unlock()\n\tpriorPrompt := j.NativeGoal.PromptConfirmed\n\tc.st.mu.Lock()", nil},
		{"restore-outside-failure-branch", "native_codex_commit.go", "\tif err != nil {\n\t\tlog.Printf", "\tj.NativeGoal.PromptConfirmed = priorPrompt\n\tif err != nil {\n\t\tlog.Printf", nil},
		// F5a: the predicate moved out of the operative condition into dead code.
		{"outer-guard-in-dead-branch", "final_report_publication.go", "j.RunMode || !deliveryConfirmed(j) || !known", "j.RunMode || !known",
			[][2]string{{"func (c *Coord) observeFinalReport(ctx context.Context, j *Job, ref agentRef, known bool) {\n", "func (c *Coord) observeFinalReport(ctx context.Context, j *Job, ref agentRef, known bool) {\n\tif false {\n\t\tif !deliveryConfirmed(j) {\n\t\t\treturn\n\t\t}\n\t}\n"}}},
		{"finalproof-guard-in-dead-branch", "final_report_publication.go", "if ctx.Err() != nil || !deliveryConfirmed(j) {", "if ctx.Err() != nil {",
			[][2]string{{"func (c *Coord) finalProof(ctx context.Context, h Host, j *Job, s finalReportScope) error {\n", "func (c *Coord) finalProof(ctx context.Context, h Host, j *Job, s finalReportScope) error {\n\tif false {\n\t\tif !deliveryConfirmed(j) {\n\t\t\treturn errFinalPublication\n\t\t}\n\t}\n"}}},
		{"outer-guard-duplicated-dead", "final_report_publication.go", "func (c *Coord) observeFinalReport(ctx context.Context, j *Job, ref agentRef, known bool) {\n", "func (c *Coord) observeFinalReport(ctx context.Context, j *Job, ref agentRef, known bool) {\n\tif false && !deliveryConfirmed(j) {\n\t\treturn\n\t}\n", nil},
		{"outer-guard-not-returning", "final_report_publication.go", "ref.Host != j.Host || ref.Pane != j.Pane || ref.Workspace != j.Workspace || ref.Agent != j.Agent {\n\t\treturn\n\t}", "ref.Host != j.Host || ref.Pane != j.Pane || ref.Workspace != j.Workspace || ref.Agent != j.Agent {\n\t}", nil},
		{"retire-guard-in-dead-branch", "completion.go", "j.Issue != n || !deliveryConfirmed(j) || j.RunMode", "j.Issue != n || j.RunMode",
			[][2]string{{"\thost, ok := c.hosts[j.Host]\n\tif !ok {\n\t\treturn fenced\n\t}\n\tcheck, cancel := context.WithTimeout(ctx, 20*time.Second)", "\tif false {\n\t\tif !fenced && !deliveryConfirmed(j) {\n\t\t\treturn false\n\t\t}\n\t}\n\thost, ok := c.hosts[j.Host]\n\tif !ok {\n\t\treturn fenced\n\t}\n\tcheck, cancel := context.WithTimeout(ctx, 20*time.Second)"}}},
		{"retry-veto-in-dead-branch", "native_goal.go", "\tif rcCodex(j) && !deliveryConfirmed(j) {\n\t\treturn false\n\t}\n", "\tif false {\n\t\tif rcCodex(j) && !deliveryConfirmed(j) {\n\t\t\treturn false\n\t\t}\n\t}\n", nil},
		// F5b: the rollback restored after the rename (phase inverted) or outside the branch.
		{"restore-after-rename", "native_codex_commit.go", "err != nil && !(errors.As(err, &saved) && saved.afterRename)", "err != nil && (errors.As(err, &saved) && saved.afterRename)", nil},
		{"restore-before-phase-check", "native_codex_commit.go", "\t\tj.NativeGoal.PromptConfirmed = priorPrompt\n", "",
			[][2]string{{"\tvar saved stateSaveError", "\tj.NativeGoal.PromptConfirmed = priorPrompt\n\tvar saved stateSaveError"}}},
		// The non-RC goal commit (confirm-hazard F2/F3).
		{"confirm-snapshot-outside-critical-section", "goal_delivery.go", "\tpriorPrompt := j.NativeGoal != nil && j.NativeGoal.PromptConfirmed\n", "",
			[][2]string{{"func (c *Coord) confirmGoalDelivery(ctx context.Context, j *Job) error {\n", "func (c *Coord) confirmGoalDelivery(ctx context.Context, j *Job) error {\n\tpriorPrompt := j.NativeGoal != nil && j.NativeGoal.PromptConfirmed\n"}}},
		{"confirm-second-prompt-read", "goal_delivery.go", "\tj.GoalDelivery = \"confirmed\"\n", "\t_ = j.NativeGoal != nil && j.NativeGoal.PromptConfirmed\n\tj.GoalDelivery = \"confirmed\"\n", nil},
		{"confirm-restore-outside-branch", "goal_delivery.go", "\t\tif j.NativeGoal != nil {\n\t\t\tj.NativeGoal.PromptConfirmed = priorPrompt\n\t\t}\n", "",
			[][2]string{{"\tif err != nil {\n\t\tlog.Printf(", "\tif j.NativeGoal != nil && err != nil {\n\t\tj.NativeGoal.PromptConfirmed = priorPrompt\n\t}\n\tif err != nil {\n\t\tlog.Printf("}}},
		{"confirm-context-veto-after-save", "goal_delivery.go", "\treturn nil\n}", "\tif ctx.Err() != nil {\n\t\treturn errPromptUnconfirmed\n\t}\n\treturn nil\n}", nil},
		{"confirm-decision-dropped", "goal_delivery.go", "\tif ctx.Err() != nil { // guard:confirm-decision\n\t\treturn errPromptUnconfirmed\n\t}\n", "\t_ = ctx\n", nil},
		{"confirm-unconditional-revert", "goal_delivery.go", "if err != nil && !(errors.As(err, &saved) && saved.afterRename) { // guard:confirm-before-rename", "if err != nil { // guard:confirm-before-rename", nil},
		{"confirm-caller-background-context", "main.go", "confirmErr = c.confirmGoalDelivery(gctx, j) // guard:confirm-caller", "confirmErr = c.confirmGoalDelivery(context.Background(), j) // guard:confirm-caller", nil},
		{"confirm-caller-parent-context", "main.go", "confirmErr = c.confirmGoalDelivery(gctx, j) // guard:confirm-caller", "confirmErr = c.confirmGoalDelivery(ctx, j) // guard:confirm-caller", nil},
		{"confirm-caller-refusal-not-blocked", "main.go", "\t\tif confirmErr != nil {\n\t\t\tattempt.contextEnded(gctx)\n\t\t\tgcancel()\n\t\t\tc.blockGoalDelivery(n, j)\n", "\t\tif confirmErr != nil {\n\t\t\tattempt.contextEnded(gctx)\n\t\t\tgcancel()\n\t\t\t_ = j\n", nil},
		{"confirm-wrapper-reintroduced", "goal_delivery.go", "func (c *Coord) confirmGoalDelivery(", "func confirmGoalDeliveryBeforeDeadline(ctx context.Context, confirm func() error) error {\n\treturn confirm()\n}\n\nfunc (c *Coord) confirmGoalDelivery(", nil},
		{"commit-snapshot-after-write", "native_codex_commit.go", "\tpriorPrompt := j.NativeGoal.PromptConfirmed\n\tj.GoalDelivery, j.GoalDeliveryCommit = \"confirmed\", deliveryCommitMark\n\tj.NativeGoal.PromptConfirmed = true", "\tj.GoalDelivery, j.GoalDeliveryCommit = \"confirmed\", deliveryCommitMark\n\tj.NativeGoal.PromptConfirmed = true\n\tpriorPrompt := j.NativeGoal.PromptConfirmed", nil},
	}
	for _, n := range negatives {
		t.Run(n.name, func(t *testing.T) {
			copySrc := map[string]string{}
			for k, v := range src {
				copySrc[k] = v
			}
			for _, edit := range append([][2]string{{n.old, n.new}}, n.more...) {
				if strings.Count(copySrc[n.file], edit[0]) != 1 {
					t.Fatalf("negative anchor drifted: %s", n.name)
				}
				copySrc[n.file] = strings.Replace(copySrc[n.file], edit[0], edit[1], 1)
			}
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
