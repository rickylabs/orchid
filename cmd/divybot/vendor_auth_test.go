package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// authFixture is a Claude job bound to an exact occupant, a fake Herdr that
// logs every call, and the real managed lifecycle hook writing its receipt.
type authFixture struct {
	c     *Coord
	j     *Job
	host  Host
	agent AgentInfo
	ref   agentRef
	calls string
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	c, j, _, _, _ := completionFixture(t)
	root := t.TempDir()
	j.Agent, j.Label, j.NativeSessionID, j.TerminalID = "claude", "claude-fixture", "thread-fixture", "fixture-terminal"
	j.Workdir = filepath.Join(root, "issue-7")
	a := AgentInfo{Agent: "claude", Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, Cwd: j.Workdir,
		TerminalID: j.TerminalID, AgentStatus: "blocked", InteractiveReady: true, StateChangeSeq: 5,
		AgentSession: json.RawMessage(`{"agent":"claude","kind":"id","source":"herdr:claude","value":"thread-fixture"}`)}
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	f := &authFixture{c: c, j: j, host: Host{Name: j.Host, Home: root}, agent: a, calls: filepath.Join(root, "calls.jsonl"),
		ref: agentRef{Host: j.Host, Agent: "claude", Status: "blocked", Pane: j.Pane, Workspace: j.Workspace, StateChangeSeq: 5, TerminalID: j.TerminalID}}
	t.Setenv("AUTH_FIXTURE_CALLS", f.calls)
	t.Setenv("AUTH_FIXTURE_AGENT", filepath.Join(root, "agent.json"))
	t.Setenv("HERDR_BIN_PATH", filepath.Join(bin, "herdr"))
	script := `#!/usr/bin/env python3
import json,os,sys
args=[a for a in sys.argv[1:]]
with open(os.environ['AUTH_FIXTURE_CALLS'],'a') as f:f.write(json.dumps(args)+'\n')
if args[:2]==['agent','get']:print(open(os.environ['AUTH_FIXTURE_AGENT']).read())
else:print('{"result":{"type":"ok"}}')
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f.publish(t)
	return f
}

func (f *authFixture) publish(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(os.Getenv("AUTH_FIXTURE_AGENT"), fixtureJSON(t, map[string]any{"result": map[string]any{"type": "agent_info", "agent": f.agent}}), 0600); err != nil {
		t.Fatal(err)
	}
}

// hook runs the real managed lifecycle hook with one vendor event.
func (f *authFixture) hook(t *testing.T, event map[string]any) {
	t.Helper()
	a := f.agent
	event["session_id"], event["cwd"] = "thread-fixture", a.Cwd
	cmd := exec.Command("python3", "-c", nativeLifecycleHook, a.Agent, a.Name, a.PaneID, a.WorkspaceID, a.Cwd,
		vendorLifecycleFile(a.Agent, a.Name, a.PaneID, a.WorkspaceID, a.Cwd))
	cmd.Stdin = bytes.NewReader(fixtureJSON(t, event))
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "{}\n" {
		t.Fatalf("hook changed vendor behavior: %v %q", err, out)
	}
}

func (f *authFixture) herdrCalls(t *testing.T) [][]string {
	t.Helper()
	raw, err := os.ReadFile(f.calls)
	if os.IsNotExist(err) {
		return nil
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var call []string
		if json.Unmarshal([]byte(line), &call) != nil {
			t.Fatal("unreadable fixture call")
		}
		calls = append(calls, call)
	}
	return calls
}

func (f *authFixture) observation(t *testing.T) vendorAuthObservation {
	t.Helper()
	a, err := f.host.agentInfoOf(context.Background(), f.j.Pane)
	if err != nil {
		t.Fatal(err)
	}
	return claudeAuthObservation(a, f.j)
}

// Only Claude's typed StopFailure error can say "unauthenticated". Quoted or
// localized text in prompts, assistant messages or error details never can.
func TestVendorAuthClaudeOnlyTypedStopFailureIsUnauthenticated(t *testing.T) {
	cases := []struct {
		name  string
		event map[string]any
		want  vendorAuthState
	}{
		{"typed-auth-failure", map[string]any{"hook_event_name": "StopFailure", "error": "authentication_failed"}, authUnauthenticated},
		{"typed-other-failure", map[string]any{"hook_event_name": "StopFailure", "error": "rate_limit", "error_details": "authentication_failed"}, authUnknown},
		{"untyped-failure-text", map[string]any{"hook_event_name": "StopFailure", "error": "Invalid bearer token", "last_assistant_message": "Please run /login"}, authUnknown},
		{"localized-failure-text", map[string]any{"hook_event_name": "StopFailure", "error": "Authentifizierung fehlgeschlagen"}, authUnknown},
		{"quoted-in-prompt", map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "The log said Invalid bearer; Please run /login; authentication_failed"}, authUnknown},
		{"quoted-in-stop", map[string]any{"hook_event_name": "Stop", "stop_hook_active": false, "last_assistant_message": "authentication_failed Invalid bearer"}, authUnknown},
		{"permission", map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "Bash"}, authUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t)
			f.hook(t, tc.event)
			a := f.agent
			raw, err := os.ReadFile(vendorLifecycleFile(a.Agent, a.Name, a.PaneID, a.WorkspaceID, a.Cwd))
			var receipt map[string]any
			if err != nil || json.Unmarshal(raw, &receipt) != nil {
				t.Fatal("hook did not record the vendor event")
			}
			if failure, recorded := receipt["failure"]; recorded && !claudeStopFailures[failure.(string)] || !recorded && tc.event["hook_event_name"] == "StopFailure" && claudeStopFailures[tc.event["error"].(string)] {
				t.Fatalf("hook recorded failure %v for vendor error %v", receipt["failure"], tc.event["error"])
			}
			got := f.observation(t)
			if got.State != tc.want {
				t.Fatalf("auth state = %q, want %q", got.State, tc.want)
			}
			if tc.want == authUnauthenticated && (got.Source != authSourceClaudeStopFailure || !digestPattern.MatchString(got.Key)) {
				t.Fatal("typed outcome lost its provenance or effect fence")
			}
		})
	}
}

// A receipt from an earlier generation of the same pane and label cannot bind
// to the current occupant, so stale native errors cannot trigger effects.
func TestVendorAuthStaleGenerationStaysUnknown(t *testing.T) {
	for _, mode := range []string{"terminal", "session", "label", "cwd"} {
		t.Run(mode, func(t *testing.T) {
			f := newAuthFixture(t)
			f.hook(t, map[string]any{"hook_event_name": "StopFailure", "error": "authentication_failed"})
			switch mode {
			case "terminal":
				f.agent.TerminalID, f.j.TerminalID = "fixture-terminal-2", "fixture-terminal-2"
			case "session":
				f.agent.AgentSession = json.RawMessage(`{"agent":"claude","kind":"id","source":"herdr:claude","value":"thread-next"}`)
				f.j.NativeSessionID = "thread-next"
			case "label":
				f.j.Label = "claude-other"
			case "cwd":
				f.j.Workdir = "/fixture/other"
			}
			f.publish(t)
			if got := f.observation(t); got.State != authUnknown {
				t.Fatal("stale-generation native error produced an auth outcome")
			}
		})
	}
}

func (f *authFixture) acted(t *testing.T, o vendorAuthObservation) bool {
	t.Helper()
	acted, err := f.c.applyVendorAuth(context.Background(), f.host, f.j, o)
	if err != nil {
		t.Fatal("fence save failed unexpectedly", err)
	}
	return acted
}

// The effect is reserved durably before it runs: repeated reads, a restart and
// a failed save cannot repeat it; a new native event is a new outcome.
func TestVendorAuthEffectRunsOncePerNativeEvent(t *testing.T) {
	f := newAuthFixture(t)
	f.hook(t, map[string]any{"hook_event_name": "StopFailure", "error": "authentication_failed"})
	first := f.observation(t)
	if !f.acted(t, first) {
		t.Fatal("typed native auth failure had no effect")
	}
	if f.acted(t, first) || f.acted(t, f.observation(t)) {
		t.Fatal("repeated native event repeated its effect")
	}
	f.c.st = loadState(f.c.st.path)
	f.j = f.c.st.Jobs[7]
	if f.j == nil || f.acted(t, first) {
		t.Fatal("restart repeated the effect or lost the live job")
	}
	if f.acted(t, unknownAuth) || f.acted(t, vendorAuthObservation{State: authUnauthenticated, Source: authSourceClaudeStopFailure, Key: "not-a-digest"}) {
		t.Fatal("unknown or malformed observation had an effect")
	}
	f.hook(t, map[string]any{"hook_event_name": "UserPromptSubmit"})
	f.hook(t, map[string]any{"hook_event_name": "StopFailure", "error": "authentication_failed"})
	second := f.observation(t)
	if second.Key == first.Key || !f.acted(t, second) {
		t.Fatal("a new native auth event was not its own outcome")
	}
	if f.c.st.Jobs[7] != f.j {
		t.Fatal("a suspected auth failure removed the live session")
	}
	if os.Geteuid() == 0 {
		return // root bypasses the read-only state directory
	}
	third := vendorAuthObservation{State: authUnauthenticated, Source: authSourceCodexTurnError, Key: shaText([]byte("third"))}
	if err := os.Chmod(filepath.Dir(f.c.st.path), 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Dir(f.c.st.path), 0700)
	if acted, err := f.c.applyVendorAuth(context.Background(), f.host, f.j, third); acted || err == nil || f.j.AuthSignal != second.Key {
		t.Fatal("an undurable fence still ran the effect or hid the failure")
	}
}

// A fence that could not be saved leaves the native change pending: once
// persistence recovers, the same change is read again and acted on once.
func TestVendorAuthUndurableFenceIsRetried(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the read-only state directory")
	}
	f := newAuthFixture(t)
	f.hook(t, map[string]any{"hook_event_name": "StopFailure", "error": "authentication_failed"})
	dir := filepath.Dir(f.c.st.path)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	f.c.superviseVendorAuth(context.Background(), f.host, f.j, f.ref, true, false)
	_ = os.Chmod(dir, 0700)
	if f.j.AuthSignal != "" || !authObservationDue(f.j, f.ref, time.Now()) {
		t.Fatal("an undurable fence consumed the native change")
	}
	f.c.superviseVendorAuth(context.Background(), f.host, f.j, f.ref, true, false)
	if !digestPattern.MatchString(f.j.AuthSignal) || authObservationDue(f.j, f.ref, time.Now()) {
		t.Fatal("the pending native change was not retried once persistence recovered")
	}
}

// An unreadable receipt is not "no event": the change stays pending until the
// receipt can be read, then its typed failure has its one outcome.
func TestVendorAuthUnreadableReceiptIsRetried(t *testing.T) {
	f := newAuthFixture(t)
	f.hook(t, map[string]any{"hook_event_name": "StopFailure", "error": "authentication_failed"})
	a := f.agent
	receiptDir := filepath.Dir(vendorLifecycleFile(a.Agent, a.Name, a.PaneID, a.WorkspaceID, a.Cwd))
	if err := os.Chmod(receiptDir, 0755); err != nil { // fails the reader's private-mode check
		t.Fatal(err)
	}
	f.c.superviseVendorAuth(context.Background(), f.host, f.j, f.ref, true, false)
	if f.j.AuthSignal != "" || !authObservationDue(f.j, f.ref, time.Now()) {
		t.Fatal("an unreadable receipt was taken as an observed absence")
	}
	if err := os.Chmod(receiptDir, 0700); err != nil {
		t.Fatal(err)
	}
	f.c.superviseVendorAuth(context.Background(), f.host, f.j, f.ref, true, false)
	if !digestPattern.MatchString(f.j.AuthSignal) {
		t.Fatal("the receipt's typed failure was lost after it became readable")
	}
	// An absent receipt is a completed observation with no outcome.
	g := newAuthFixture(t)
	g.c.superviseVendorAuth(context.Background(), g.host, g.j, g.ref, true, false)
	if g.j.AuthSignal != "" || authObservationDue(g.j, g.ref, time.Now()) {
		t.Fatal("an absent receipt was retried or acted on")
	}
}

// Herdr's displayed status never gates the Claude source, and an unmatched or
// recovering occupant is never read.
func TestVendorAuthSupervisionIgnoresDisplayedStatus(t *testing.T) {
	for _, status := range []string{"idle", "blocked", "done", "working", "unknown"} {
		t.Run(status, func(t *testing.T) {
			f := newAuthFixture(t)
			f.hook(t, map[string]any{"hook_event_name": "StopFailure", "error": "authentication_failed"})
			f.ref.Status = status
			_ = os.Remove(f.calls)
			f.c.superviseVendorAuth(context.Background(), f.host, f.j, f.ref, false, false)
			f.c.superviseVendorAuth(context.Background(), f.host, f.j, f.ref, true, true)
			if len(f.herdrCalls(t)) != 0 || f.j.AuthSignal != "" {
				t.Fatal("an unmatched or recovering occupant was read")
			}
			f.c.superviseVendorAuth(context.Background(), f.host, f.j, f.ref, true, false)
			if !digestPattern.MatchString(f.j.AuthSignal) {
				t.Fatalf("typed failure ignored while Herdr displayed %s", status)
			}
		})
	}
}

// Remote Control Codex is read through the real goal RPC reader whatever
// Herdr displays, including a stale "working", and is acted on once.
func TestVendorAuthCodexThroughNativeReader(t *testing.T) {
	c, j := capacityFixture(t)
	j.RemoteControl = &remoteControlRun{}
	j.Pane = "w1:p1"
	connects := 0
	code := "unauthorized"
	prior := codexAuthConnect
	codexAuthConnect = func(_ context.Context, _ Host, thread string, use func(*goalRPC) error) error {
		connects++
		p, _ := goalPort(response(2, capacityTurnResult("auth-turn", "failed", code)))
		p.canonical = true
		if thread != p.thread {
			return errors.New("unbound thread")
		}
		return use(p)
	}
	t.Cleanup(func() { codexAuthConnect = prior })
	ref := agentRef{Host: j.Host, Agent: "codex", Pane: j.Pane, Status: "working", StateChangeSeq: 1}
	c.superviseVendorAuth(context.Background(), Host{Name: j.Host}, j, ref, true, false)
	first := j.AuthSignal
	if connects != 1 || !digestPattern.MatchString(first) {
		t.Fatal("a failed unauthorized turn was not read while Herdr displayed working")
	}
	ref.Status, ref.StateChangeSeq = "done", 2
	c.superviseVendorAuth(context.Background(), Host{Name: j.Host}, j, ref, true, false)
	c.superviseVendorAuth(context.Background(), Host{Name: j.Host}, j, ref, true, false)
	if connects != 2 || j.AuthSignal != first {
		t.Fatal("reads were not debounced or the same failed turn acted twice")
	}
	j.authObservedAt = time.Now().Add(-authObservationBackstop)
	c.superviseVendorAuth(context.Background(), Host{Name: j.Host}, j, ref, true, false)
	if connects != 3 || j.AuthSignal != first {
		t.Fatal("the backstop did not re-read an unchanged cursor, or repeated the effect")
	}
	code, ref.StateChangeSeq = "serverOverloaded", 4
	j.AuthSignal = ""
	c.superviseVendorAuth(context.Background(), Host{Name: j.Host}, j, ref, true, false)
	if j.AuthSignal != "" {
		t.Fatal("a non-auth native failure produced an auth outcome")
	}
	j.RemoteControl = nil
	ref.StateChangeSeq = 5
	c.superviseVendorAuth(context.Background(), Host{Name: j.Host}, j, ref, true, false)
	if connects != 4 {
		t.Fatal("plain Codex, which has no typed auth source, was read")
	}
}

// Supervision reads one exact occupant record per native state change and
// never the terminal; transports without a typed source perform no read.
func TestVendorAuthReadsAreNativeAndEventDriven(t *testing.T) {
	f := newAuthFixture(t)
	f.hook(t, map[string]any{"hook_event_name": "StopFailure", "error": "authentication_failed"})
	_ = os.Remove(f.calls)
	f.c.observeVendorAuth(context.Background(), f.host, f.j, f.ref)
	f.c.observeVendorAuth(context.Background(), f.host, f.j, f.ref)
	calls := f.herdrCalls(t)
	if len(calls) != 1 || calls[0][0] != "agent" || calls[0][1] != "get" || calls[0][2] != f.j.Pane {
		t.Fatalf("auth observation was not one exact occupant read: %q", calls)
	}
	first := f.j.AuthSignal
	if !digestPattern.MatchString(first) {
		t.Fatal("typed native auth failure was not acted on")
	}
	f.ref.StateChangeSeq++
	f.c.observeVendorAuth(context.Background(), f.host, f.j, f.ref)
	if len(f.herdrCalls(t)) != 2 || f.j.AuthSignal != first {
		t.Fatal("a native state change was not observed, or the same event acted twice")
	}
	for _, agent := range []string{"agy", "opencode", "codex"} {
		_ = os.Remove(f.calls)
		f.j.Agent, f.j.RemoteControl, f.j.authObserved = agent, nil, false
		f.ref.StateChangeSeq++
		f.c.observeVendorAuth(context.Background(), f.host, f.j, f.ref)
		if len(f.herdrCalls(t)) != 0 {
			t.Fatalf("%s has no typed native auth source but was read", agent)
		}
	}
}

func TestVendorAuthCodexTypedTurnError(t *testing.T) {
	cases := []struct {
		thread, turn, status, code string
		want                       vendorAuthState
	}{
		{"thread-fixture", "turn-fixture", "failed", "unauthorized", authUnauthenticated},
		{"thread-fixture", "turn-fixture", "failed", "serverOverloaded", authUnknown},
		{"thread-fixture", "turn-fixture", "failed", "usageLimitExceeded", authUnknown},
		{"thread-fixture", "turn-fixture", "completed", "unauthorized", authUnknown},
		{"thread-fixture", "turn-fixture", "inProgress", "unauthorized", authUnknown},
		{"thread-fixture", "turn-fixture", "failed", "Unauthorized", authUnknown},
		{"thread-fixture", "turn-fixture", "failed", "", authUnknown},
		{"", "turn-fixture", "failed", "unauthorized", authUnknown},
		{"thread-fixture", "turn with space", "failed", "unauthorized", authUnknown},
	}
	for _, tc := range cases {
		got := codexAuthObservation(tc.thread, tc.turn, tc.status, tc.code)
		if got.State != tc.want {
			t.Fatalf("%+v: auth state = %q, want %q", tc, got.State, tc.want)
		}
	}
	a := codexAuthObservation("thread-fixture", "turn-a", "failed", "unauthorized")
	b := codexAuthObservation("thread-fixture", "turn-b", "failed", "unauthorized")
	if a.Key == b.Key || a.Source != authSourceCodexTurnError || a.Key != codexAuthObservation("thread-fixture", "turn-a", "failed", "unauthorized").Key {
		t.Fatal("native turn identity does not fence the effect")
	}
}

// The managed hook records only Claude's closed StopFailure enum, and a
// receipt carrying any other failure value never matches an occupant.
func TestVendorAuthReceiptFailureIsClosed(t *testing.T) {
	a := AgentInfo{Agent: "claude", Name: "n", PaneID: "w1:p1", WorkspaceID: "w1", Cwd: "/fixture/x", TerminalID: "t1",
		AgentSession: json.RawMessage(`{"agent":"claude","kind":"id","source":"herdr:claude","value":"thread-fixture"}`)}
	base := vendorLifecycleEvent{Agent: "claude", Name: "n", Pane: "w1:p1", Workspace: "w1", Cwd: "/fixture/x", Terminal: "t1", Session: "thread-fixture", Event: "StopFailure", Seq: 1}
	for failure, ok := range map[string]bool{"": true, "authentication_failed": true, "rate_limit": true, "Invalid bearer": false, "AUTHENTICATION_FAILED": false} {
		e := base
		e.Failure = failure
		if e.matches(a) != ok {
			t.Fatalf("failure %q match = %v", failure, !ok)
		}
	}
	e := base
	e.Event, e.Failure = "Stop", "authentication_failed"
	if e.matches(a) {
		t.Fatal("a failure outside StopFailure matched")
	}
}

// Startup and auth decisions never consult terminal text: the AGY startup
// adapter, the auth adapter and supervision carry no screen read or marker.
func TestStartupAndAuthBoundaryNeverReadsScreen(t *testing.T) {
	sources := productionSources(t)
	for _, file := range []string{"agy_startup.go", "agy_trust.go", "registration_trust.go", "vendor_auth.go"} {
		for _, banned := range []string{"visiblePromptScreen", ".read(", "Screen(", `"pane", "read"`, "for shortcuts", "Invalid bearer", "Please run /login", "not signed in", "Do you trust"} {
			if strings.Contains(sources[file], banned) {
				t.Fatalf("%s decides startup or auth from screen evidence: %s", file, banned)
			}
		}
	}
	for _, banned := range []string{"Invalid bearer", "Please run /login", "possible auth-dead"} {
		if strings.Contains(sources["main.go"], banned) {
			t.Fatalf("supervision still matches terminal auth text: %s", banned)
		}
	}
	if !strings.Contains(sources["main.go"], "c.superviseVendorAuth(ctx, host, j, ref, matched, recovering)") {
		t.Fatal("supervision lost its typed native auth observation")
	}
}

// The adapter itself refuses a receipt from another generation, even when a
// caller hands it one: terminal, session, sequence and enum must all bind.
func TestVendorAuthAdapterRefusesUnboundReceipt(t *testing.T) {
	j := &Job{Agent: "claude", Label: "n", Pane: "w1:p1", Workspace: "w1", TerminalID: "t2", Workdir: "/fixture/x", NativeSessionID: "thread-fixture"}
	a := AgentInfo{Agent: "claude", Name: "n", PaneID: "w1:p1", WorkspaceID: "w1", Cwd: "/fixture/x", TerminalID: "t2",
		AgentSession: json.RawMessage(`{"agent":"claude","kind":"id","source":"herdr:claude","value":"thread-fixture"}`)}
	bound := vendorLifecycleEvent{Agent: "claude", Name: "n", Pane: "w1:p1", Workspace: "w1", Cwd: "/fixture/x", Terminal: "t2",
		Session: "thread-fixture", Event: "StopFailure", Failure: "authentication_failed", Seq: 9}
	a.VendorEvent = &bound
	if claudeAuthObservation(a, j).State != authUnauthenticated {
		t.Fatal("bound typed receipt was refused")
	}
	for name, change := range map[string]func(*vendorLifecycleEvent){
		"old-terminal": func(e *vendorLifecycleEvent) { e.Terminal = "t1" },
		"old-session":  func(e *vendorLifecycleEvent) { e.Session = "thread-old" },
		"no-sequence":  func(e *vendorLifecycleEvent) { e.Seq = 0 },
		"other-pane":   func(e *vendorLifecycleEvent) { e.Pane = "w9:p1" },
	} {
		stale := bound
		change(&stale)
		a.VendorEvent = &stale
		if claudeAuthObservation(a, j).State != authUnknown {
			t.Fatalf("%s receipt produced an auth outcome", name)
		}
	}
}
