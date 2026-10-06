package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A canonical daemon that records turn/start and turn/steer and answers them as
// told, with one newest turn of the given status. A logging herdr proves that
// nothing reaches the pane.
type nativeInputFixture struct {
	f        *accFixture
	host     Host
	herdrLog string
	mu       sync.Mutex
	calls    []map[string]any // method + params
	answer   func(method string, params map[string]any) *accReply
}

func newNativeInputFixture(t *testing.T, newest map[string]any) *nativeInputFixture {
	t.Helper()
	run := syntheticRemoteRun(t)
	run.ClientVersion, run.Resume = "9.1.0", &codexResumeProof{Producer: strings.Repeat("ab", 32), Connection: "252"}
	f := newAccFixture(t, run)
	f.baseline = []any{newest}
	n := &nativeInputFixture{f: f}
	f.reply = func(_ *accFixture, method string, params map[string]any, _ int) *accReply {
		if method != "turn/start" && method != "turn/steer" {
			return nil
		}
		n.mu.Lock()
		n.calls = append(n.calls, map[string]any{"method": method, "params": params})
		n.mu.Unlock()
		if n.answer != nil {
			return n.answer(method, params)
		}
		return nil
	}
	bin := filepath.Join(f.home, ".local", "bin")
	if os.MkdirAll(bin, 0700) != nil {
		t.Fatal("fixture setup failed")
	}
	n.herdrLog = filepath.Join(f.home, "herdr-calls")
	t.Setenv("NATIVE_INPUT_HERDR", n.herdrLog)
	if os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\necho \"$@\" >> \"$NATIVE_INPUT_HERDR\"\nexit 1\n"), 0700) != nil {
		t.Fatal("fixture executable unavailable")
	}
	n.host = Host{Home: f.home, Name: "fixture-host", CanonicalCodex: true, RemoteRun: run}
	return n
}

func (n *nativeInputFixture) recorded() []map[string]any {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]map[string]any(nil), n.calls...)
}

func (n *nativeInputFixture) noPane(t *testing.T) {
	t.Helper()
	if raw, err := os.ReadFile(n.herdrLog); err == nil && len(raw) > 0 {
		t.Fatalf("a Codex input reached the pane: %s", raw)
	}
}

func answerJSON(id string, result any) *accReply {
	body, _ := json.Marshal(map[string]any{"id": "%ID%", "result": result})
	return &accReply{raw: []byte(strings.Replace(string(body), `"%ID%"`, "%ID%", 1))}
}

const nativeInputText = "PR #8 is NOT mergeable — CI is RED: fix it and push."

func TestCodexInputStartsATurnWhenIdle(t *testing.T) {
	n := newNativeInputFixture(t, accBaseTurn("turn-done"))
	n.answer = func(method string, _ map[string]any) *accReply {
		return answerJSON("", map[string]any{"turn": map[string]any{"id": "turn-next", "items": []any{}, "status": "inProgress"}})
	}
	if err := n.host.submitCodexInput(context.Background(), nativeInputText); err != nil {
		t.Fatalf("idle input refused: %v", err)
	}
	calls := n.recorded()
	if len(calls) != 1 || calls[0]["method"] != "turn/start" {
		t.Fatalf("calls: %+v", calls)
	}
	params := calls[0]["params"].(map[string]any)
	raw, _ := json.Marshal(params)
	want := `{"input":[{"text":"` + nativeInputText + `","text_elements":[],"type":"text"}],"threadId":"` + n.host.RemoteRun.NativeSessionID + `"}`
	if string(raw) != want {
		t.Fatalf("turn/start params\n got %s\nwant %s", raw, want)
	}
	n.noPane(t)
}

func TestCodexInputSteersTheActiveTurn(t *testing.T) {
	n := newNativeInputFixture(t, accTurn("turn-live", "inProgress"))
	n.answer = func(string, map[string]any) *accReply { return answerJSON("", map[string]any{"turnId": "turn-live"}) }
	if err := n.host.submitCodexInput(context.Background(), nativeInputText); err != nil {
		t.Fatalf("steer refused: %v", err)
	}
	calls := n.recorded()
	if len(calls) != 1 || calls[0]["method"] != "turn/steer" || calls[0]["params"].(map[string]any)["expectedTurnId"] != "turn-live" {
		t.Fatalf("calls: %+v", calls)
	}
	n.noPane(t)
	// A steer the daemon reports into another turn is not this delivery.
	other := newNativeInputFixture(t, accTurn("turn-live", "inProgress"))
	other.answer = func(string, map[string]any) *accReply { return answerJSON("", map[string]any{"turnId": "turn-other"}) }
	if !errors.Is(other.host.submitCodexInput(context.Background(), nativeInputText), errPromptUnconfirmed) {
		t.Fatal("a steer into another turn was confirmed")
	}
}

// Sent but not answered: unconfirmed, and never sent again.
func TestCodexInputUncertainIsNeverResent(t *testing.T) {
	n := newNativeInputFixture(t, accBaseTurn("turn-done"))
	n.answer = func(string, map[string]any) *accReply {
		return &accReply{raw: []byte(`{"id":%ID%,"error":{"code":-32603,"message":"fixture failure"}}`)}
	}
	if !errors.Is(n.host.submitCodexInput(context.Background(), nativeInputText), errPromptUnconfirmed) {
		t.Fatal("a refused turn/start was confirmed")
	}
	silent := newNativeInputFixture(t, accBaseTurn("turn-done"))
	silent.answer = func(string, map[string]any) *accReply {
		return &accReply{after: func(c net.Conn) {}, raw: []byte(`{"id":%ID%,"result":{"turn":{"id":"","items":[],"status":"inProgress"}}}`)}
	}
	if !errors.Is(silent.host.submitCodexInput(context.Background(), nativeInputText), errPromptUnconfirmed) {
		t.Fatal("an unidentified turn was confirmed")
	}
	if len(n.recorded()) != 1 || len(silent.recorded()) != 1 {
		t.Fatalf("an uncertain input was resent: %d / %d", len(n.recorded()), len(silent.recorded()))
	}
	n.noPane(t)
	silent.noPane(t)
}

func TestCodexInputNeedsTheNativeBinding(t *testing.T) {
	for name, mutate := range map[string]func(*Host){
		"no remote run":   func(h *Host) { h.RemoteRun = nil },
		"not canonical":   func(h *Host) { h.CanonicalCodex = false },
		"no resume proof": func(h *Host) { r := *h.RemoteRun; r.Resume = nil; h.RemoteRun = &r },
		"empty text":      func(*Host) {},
	} {
		n := newNativeInputFixture(t, accBaseTurn("turn-done"))
		h := n.host
		mutate(&h)
		text := nativeInputText
		if name == "empty text" {
			text = "  "
		}
		if !errors.Is(h.submitCodexInput(context.Background(), text), errPromptUnconfirmed) || n.f.connections() != 0 {
			t.Fatalf("%s: input attempted without a native binding (connections %d)", name, n.f.connections())
		}
		n.noPane(t)
	}
}

// The allowlist admits exactly the two native input methods, after initialize.
func TestCodexInputMethodsAllowed(t *testing.T) {
	for _, m := range []string{"turn/start", "turn/steer"} {
		if !goalMethodAllowed(m, 1) || goalMethodAllowed(m, 0) {
			t.Fatalf("%s allowlist", m)
		}
	}
	for _, m := range []string{"turn/settings/update", "thread/fork", "thread/rollback", "command/exec"} {
		if goalMethodAllowed(m, 1) {
			t.Fatalf("%s must not be allowed", m)
		}
	}
}

// ---- real callers: every Codex input goes through the native owner ----

func codexInputSeam(c *Coord) *[]string {
	var got []string
	c.actions.codexInput = func(_ context.Context, _ Host, j *Job, text string) error {
		got = append(got, j.Agent+":"+text)
		return nil
	}
	return &got
}

func TestCodexActionSendUsesNativeInput(t *testing.T) {
	for _, fail := range []bool{false, true} {
		c, spool, receipts := actionFixture(t)
		req := boundActionFixture(t, c, receipts)
		j := c.st.Jobs[7]
		run := syntheticRemoteRun(t)
		run.ClientVersion, run.Resume = "9.1.0", &codexResumeProof{Producer: strings.Repeat("ab", 32), Connection: "252"}
		j.Agent, j.Label, j.Pane, j.Workspace, j.RemoteControl = "codex", "harness", "w1:p1", "w1", run
		j.GoalDelivery, j.NativeGoal = "confirmed", &dispatchGoal{PromptConfirmed: true}
		record := filepath.Join(receipts, j.DispatchKey, "record")
		d := dispatchBinding{SchemaVersion: 1, RunID: "orchid-" + j.DispatchKey, Issue: dispatchIssue{Repo: "example/repo", Number: 7},
			Source: "codex", State: "dispatched", Host: j.Host, Location: &dispatchLocation{PaneID: j.Pane, WorkspaceID: j.Workspace}}
		b, _ := json.Marshal(d)
		if os.WriteFile(filepath.Join(record, "dispatch.json"), b, 0600) != nil ||
			os.WriteFile(filepath.Join(record, "binding.json"), []byte(`{"Repo":"example/repo","NativeSessionID":"`+run.NativeSessionID+`"}`), 0600) != nil {
			t.Fatal("fixture")
		}
		c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
			return []AgentInfo{{Agent: "codex", AgentStatus: "idle", Cwd: "/fixture/issue-7", PaneID: j.Pane, WorkspaceID: j.Workspace}}, nil
		}
		h, herdrCalls := shadowInputsHost(t, j) // serves the job's Codex occupant; logs every herdr call
		c.hosts[j.Host] = h
		var got []string
		c.actions.codexInput = func(_ context.Context, _ Host, _ *Job, text string) error {
			got = append(got, text)
			if fail {
				return errPromptUnconfirmed
			}
			return nil
		}
		req.Action, req.Payload.Text = "send", "Read the fixture assignment."
		actionDrop(t, spool, req)
		c.actionTick(context.Background())
		r := actionResult(t, receipts)
		raw, _ := os.ReadFile(herdrCalls)
		if len(got) != 1 || strings.Contains(string(raw), "agent prompt") || strings.Contains(string(raw), "send-keys") {
			t.Fatalf("fail=%v: send not through native input: native=%v herdr=%q outcome=%s reason=%s", fail, got, raw, r.Outcome, r.Reason)
		}
		if fail && (r.Outcome == "accepted" || r.Reason != "prompt_delivery_unconfirmed") || !fail && (r.Outcome != "accepted" || r.Reason != "prompt_delivered") {
			t.Fatalf("fail=%v: outcome=%s reason=%s", fail, r.Outcome, r.Reason)
		}
	}
}

// REVIEW-o94b (the reviewer's probe): a Remote Control Codex PR relay never
// types into the pane or sends Enter; without a provable native owner it is
// not delivered at all.
func TestRCCodexPRRelayUsesNativeSubmission(t *testing.T) {
	c, j, _, _, _, _ := agyCompletionFixture(t, "")
	j.Agent, j.PR = "codex", 8
	j.RemoteControl = syntheticRemoteRun(t)
	callLog := relayLogFixture(t, "#!/bin/sh\nprintf '%s\\n' '{\"number\":8,\"state\":\"OPEN\",\"mergeable\":\"MERGEABLE\",\"statusCheckRollup\":[{\"name\":\"check\",\"conclusion\":\"FAILURE\"}]}'\n")
	c.pollPR(context.Background(), j.Issue, j, c.hosts[j.Host], "idle")
	raw, _ := os.ReadFile(callLog)
	if strings.Contains(string(raw), "'agent' 'prompt'") || strings.Contains(string(raw), "'send-keys'") {
		t.Fatalf("RC Codex PR relay used the pane: %s", raw)
	}
	// With the native owner, the same relay is delivered through it.
	got := codexInputSeam(c)
	j.LastPoke = time.Time{}
	c.pollPR(context.Background(), j.Issue, j, c.hosts[j.Host], "idle")
	if len(*got) != 1 || !strings.Contains((*got)[0], "CI is RED") {
		t.Fatalf("relay not through native input: %v", *got)
	}
}

func TestRCCodexPokeAndFanoutUseNativeSubmission(t *testing.T) {
	c, j, _, _, _, _ := agyCompletionFixture(t, "")
	j.Agent, j.PR, j.RemoteControl = "codex", 0, syntheticRemoteRun(t)
	j.SpawnedAt, j.LastPoke = time.Now().Add(-24*time.Hour), time.Time{}
	callLog := relayLogFixture(t, "#!/bin/sh\necho '[]'\n")
	got := codexInputSeam(c)
	if poke := strandedPoke(j, "idle", time.Now()); poke == "" {
		t.Fatal("control: the job is stranded")
	}
	// The supervisor's stranded poke, called the way superviseActive does.
	if c.jobInput(context.Background(), c.hosts[j.Host], j, strandedPoke(j, "idle", time.Now())) != nil || len(*got) != 1 {
		t.Fatalf("poke not through native input: %v", *got)
	}
	j.PR, j.Home, j.FanoutNudgedAt = 8, nil, time.Time{}
	j.Repo, j.Title = "example/repo", "[example/repo#5] fixture subtask"
	relayLogFixture(t, "#!/bin/sh\ncase \"$1\" in pr) echo '{\"state\":\"MERGED\"}';; *) echo '{\"state\":\"OPEN\"}';; esac\n")
	if !c.fanoutGrace(context.Background(), j.Issue, j) || len(*got) != 2 || !strings.Contains((*got)[1], "split the") {
		t.Fatalf("fan-out nudge not through native input: %v", *got)
	}
	raw, _ := os.ReadFile(callLog)
	if strings.Contains(string(raw), "'agent' 'prompt'") || strings.Contains(string(raw), "'send-keys'") {
		t.Fatalf("a Codex input used the pane: %s", raw)
	}
}

// Claude delivery is unchanged: still the pane sender, never the native owner.
func TestClaudeInputStillUsesThePane(t *testing.T) {
	c := &Coord{}
	native := 0
	c.actions.codexInput = func(context.Context, Host, *Job, string) error { native++; return nil }
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	_ = os.MkdirAll(bin, 0700)
	log := filepath.Join(root, "calls")
	t.Setenv("CLAUDE_INPUT_HERDR", log)
	_ = os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\necho \"$@\" >> \"$CLAUDE_INPUT_HERDR\"\necho '{\"result\":{}}'\n"), 0700)
	if err := c.jobInput(context.Background(), Host{Home: root, Name: "fixture-host"}, &Job{Agent: "claude", Pane: "w1:p1"}, "hello"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(log)
	if native != 0 || !strings.Contains(string(raw), "agent prompt w1:p1 hello") || !strings.Contains(string(raw), "pane send-keys w1:p1 Enter") {
		t.Fatalf("Claude delivery changed: native=%d calls=%q", native, raw)
	}
}
