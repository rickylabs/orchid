package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// procStart is this process's kernel start time, as Claude records it.
func procStart(t *testing.T, pid int) string {
	t.Helper()
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Skip("procfs unavailable")
	}
	fields := strings.Fields(string(raw)[strings.LastIndex(string(raw), ")")+1:])
	return fields[19]
}

// claudeBridgeHost is a local host with a fake Herdr reporting pids and a
// Claude config home holding the given session records.
func claudeBridgeHost(t *testing.T, pids []int, records map[int]any) Host {
	t.Helper()
	home := t.TempDir()
	procs := []map[string]any{}
	for _, pid := range pids {
		procs = append(procs, map[string]any{"pid": pid, "name": "claude"})
	}
	info, _ := json.Marshal(map[string]any{"result": map[string]any{"process_info": map[string]any{"foreground_processes": procs}}})
	writeFixture(t, filepath.Join(home, ".local", "bin", "herdr"), "#!/bin/sh\nprintf '%s\\n' '"+string(info)+"'\n")
	if os.Chmod(filepath.Join(home, ".local", "bin", "herdr"), 0700) != nil {
		t.Fatal("fixture")
	}
	_ = os.MkdirAll(filepath.Join(home, ".claude", "sessions"), 0700)
	for pid, rec := range records {
		path := filepath.Join(home, ".claude", "sessions", fmt.Sprintf("%d.json", pid))
		if raw, ok := rec.(string); ok {
			_ = os.WriteFile(path, []byte(raw), 0600)
			continue
		}
		b, _ := json.Marshal(rec)
		_ = os.WriteFile(path, b, 0600)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return Host{Name: "fixture-host", Home: home}
}

// Claude's own session record, bound to the exact live process by pid, kernel
// start time and native session id, with a strict schema, decides whether a
// bridge identity is recorded; anything else is unknown.
func TestClaudeBridgeIdentityFromNativeSessionRecord(t *testing.T) {
	sid := syntheticRemoteRun(t).NativeSessionID
	self, parent := os.Getpid(), os.Getppid()
	start := procStart(t, self)
	rec := func(pid int, session, start string, bridge any) map[string]any {
		r := map[string]any{"pid": pid, "sessionId": session, "procStart": start, "kind": "interactive"}
		if bridge != nil {
			r["bridgeSessionId"] = bridge
		}
		return r
	}
	raw := func(body string) string { return body }
	exact := fmt.Sprintf(`"pid":%d,"sessionId":%q,"procStart":%q`, self, sid, start)
	for _, tc := range []struct {
		name          string
		pids          []int
		records       map[int]any
		present, isOK bool
	}{
		{"bridge-identity", []int{self}, map[int]any{self: rec(self, sid, start, "bridge_x")}, true, true},
		{"no-bridge-field", []int{self}, map[int]any{self: rec(self, sid, start, nil)}, false, true},
		{"other-session", []int{self}, map[int]any{self: rec(self, syntheticRemoteRun(t).NativeSessionID, start, "bridge_x")}, false, false},
		{"reused-pid", []int{self}, map[int]any{self: rec(self, sid, start+"1", "bridge_x")}, false, false},
		{"record-names-other-pid", []int{self}, map[int]any{self: rec(self+1, sid, start, "bridge_x")}, false, false},
		{"no-record", []int{self}, map[int]any{}, false, false},
		{"garbage-record", []int{self}, map[int]any{self: raw("{not json")}, false, false},
		{"two-matches", []int{self, parent}, map[int]any{self: rec(self, sid, start, "b"), parent: rec(parent, sid, procStart(t, parent), "b")}, false, false},
		{"other-pane-process-irrelevant", []int{parent, self}, map[int]any{self: rec(self, sid, start, "bridge_x")}, true, true},
		{"valid-plus-malformed-pane-record", []int{self, parent}, map[int]any{self: rec(self, sid, start, "bridge_x"), parent: raw("{not json")}, false, false},
		{"no-processes", nil, map[int]any{self: rec(self, sid, start, "bridge_x")}, false, false},
		{"bridge-empty-string", []int{self}, map[int]any{self: rec(self, sid, start, "")}, false, false},
		{"bridge-object", []int{self}, map[int]any{self: rec(self, sid, start, map[string]any{"a": 1})}, false, false},
		{"bridge-array", []int{self}, map[int]any{self: rec(self, sid, start, []int{1})}, false, false},
		{"bridge-number", []int{self}, map[int]any{self: rec(self, sid, start, 7)}, false, false},
		{"bridge-boolean", []int{self}, map[int]any{self: rec(self, sid, start, true)}, false, false},
		{"bridge-null", []int{self}, map[int]any{self: raw("{" + exact + `,"bridgeSessionId":null}`)}, false, false},
		{"duplicate-bridge-key", []int{self}, map[int]any{self: raw("{" + exact + `,"bridgeSessionId":{},"bridgeSessionId":"b"}`)}, false, false},
		{"duplicate-pid-key", []int{self}, map[int]any{self: raw(fmt.Sprintf(`{"pid":1,%s,"bridgeSessionId":"b"}`, exact))}, false, false},
		{"pid-as-string", []int{self}, map[int]any{self: raw(fmt.Sprintf(`{"pid":"%d","sessionId":%q,"procStart":%q,"bridgeSessionId":"b"}`, self, sid, start))}, false, false},
		{"procstart-as-number", []int{self}, map[int]any{self: raw(fmt.Sprintf(`{"pid":%d,"sessionId":%q,"procStart":%s,"bridgeSessionId":"b"}`, self, sid, start))}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := claudeBridgeHost(t, tc.pids, tc.records)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			present, ok := h.claudeBridgeIdentity(ctx, "w1:p1", sid)
			if present != tc.present || ok != tc.isOK {
				t.Fatalf("present=%v ok=%v, want %v/%v", present, ok, tc.present, tc.isOK)
			}
		})
	}
}

// The bridge field is identity metadata, never a connection: the shadow keeps
// it as bridge-identity, Claude's native connection stays unknown with no
// official surface, and unknown evidence revokes the source.
func TestClaudeBridgeIsIdentityNeverConnection(t *testing.T) {
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	j := shadowFixtureJob(t, "claude")
	self := os.Getpid()
	h := claudeBridgeHost(t, []int{self}, map[int]any{self: map[string]any{"pid": self, "sessionId": j.RemoteControl.NativeSessionID, "procStart": procStart(t, self), "bridgeSessionId": "bridge_x"}})
	h.ShadowScope = s.scope(j)
	h.readClaudeBridge(j)
	if v := s.verdict(j, shadowBridgeIdentity); v.Value != "present" || v.Authority != "native" {
		t.Fatalf("bridge identity not recorded: %+v", v)
	}
	if v := s.verdict(j, shadowConnection); v.Value != "unknown" || v.Reason != "no-official-surface" {
		t.Fatalf("bridge identity became a connection verdict: %+v", v)
	}
	for _, footer := range []string{"connected", "unconfirmed"} {
		s.compare(j, shadowSiteConnection, footer, []string{shadowInputHerdrAgent, shadowInputScreenFooter})
	}
	for _, c := range s.ledger(j) {
		if c.Agreement != "shadow-unknown" || c.Proposed[0].Fact != shadowConnection || c.Proposed[2].Fact != shadowBridgeIdentity || c.Proposed[2].Value != "present" {
			t.Fatalf("connection comparison credited the bridge identity: %+v", c)
		}
	}
	out, _ := json.Marshal(s.readout(shadowT0))
	if strings.Contains(string(out), "bridge_x") || strings.Contains(string(out), j.RemoteControl.NativeSessionID) || !strings.Contains(string(out), `"no-official-surface"`) {
		t.Fatal("readout leaked a private value or lost the closed reason")
	}
	_ = os.Remove(filepath.Join(h.Home, ".claude", "sessions", fmt.Sprintf("%d.json", self)))
	h.ShadowScope = s.scope(j)
	h.readClaudeBridge(j)
	if v := s.verdict(j, shadowBridgeIdentity); v.Value != "unknown" || v.Reason != "source-disconnected" {
		t.Fatalf("unknown evidence did not revoke the source: %+v", v)
	}
	writeFixture(t, filepath.Join(h.Home, ".claude", "sessions", fmt.Sprintf("%d.json", self)),
		fmt.Sprintf(`{"pid":%d,"sessionId":%q,"procStart":%q}`, self, j.RemoteControl.NativeSessionID, procStart(t, self)))
	h.ShadowScope = s.scope(j)
	h.readClaudeBridge(j)
	if v := s.verdict(j, shadowBridgeIdentity); v.Value != "absent" {
		t.Fatalf("no bridge field not recorded as absent: %+v", v)
	}
	codex := shadowFixtureJob(t, "codex")
	h.ShadowScope = s.scope(codex)
	h.observeClaudeBridge(codex)
	s.bg.Wait()
	if v := s.verdict(codex, shadowBridgeIdentity); v.Value != "unknown" {
		t.Fatal("claude evidence published for a codex job")
	}
}

// The read is bounded by its own budget even when a child holds the output
// pipes open past it.
func TestClaudeBridgeReadIsBounded(t *testing.T) {
	j := shadowFixtureJob(t, "claude")
	h := claudeBridgeHost(t, []int{os.Getpid()}, map[int]any{})
	writeFixture(t, filepath.Join(h.Home, ".local", "bin", "herdr"), "#!/bin/sh\n(sleep 5) &\nsleep 5\n")
	s := newNativeEvidenceShadow(time.Now)
	h.ShadowScope = s.scope(j)
	start := time.Now()
	h.readClaudeBridge(j)
	elapsed := time.Since(start)
	if elapsed > claudeBridgeReadBudget+claudeBridgeWaitDelay+500*time.Millisecond {
		t.Fatalf("read took %s past its %s bound", elapsed, claudeBridgeReadBudget)
	}
	if v := s.verdict(j, shadowBridgeIdentity); v.Value != "unknown" {
		t.Fatalf("a timed-out read produced a verdict: %+v", v)
	}
}

// One background read per job at a time.
func TestClaudeBridgeSingleFlight(t *testing.T) {
	s := newNativeEvidenceShadow(time.Now)
	sc := s.scope(shadowFixtureJob(t, "claude"))
	if !sc.begin("claude-session") || sc.begin("claude-session") {
		t.Fatal("a second concurrent read was allowed")
	}
	sc.end("claude-session")
	if !sc.begin("claude-session") {
		t.Fatal("a finished read did not release its slot")
	}
	sc.end("claude-session")
}

// observationResult replays the production observation entry point with
// synthetic native receipts and Herdr: today's decision with and without the
// shadow read must match, under a short caller deadline and cancellation too.
func observationResult(t *testing.T, shadow bool, mode string, ctxFor func() (context.Context, context.CancelFunc)) string {
	r := registrationReceipt(t, "claude", Overrides{})
	r.dispatch.Host = "fixture-host"
	if err := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); err != nil {
		t.Fatal("fixture dispatch")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(r.file)))
	run := syntheticRemoteRun(t)
	j := &Job{Issue: 7, Agent: "claude", Repo: "fixture/repo", Label: "fixture-agent", Host: "fixture-host", Pane: "w1:p1", Workspace: "w1", DispatchKey: strings.Repeat("d", 64), RemoteControl: run}
	binding, _ := json.Marshal(map[string]string{"Repo": j.Repo, "NativeSessionID": run.NativeSessionID})
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.file), "binding.json"), binding, 0600); err != nil {
		t.Fatal("fixture binding")
	}
	records := map[int]any{}
	if mode == "present" || mode == "absent" {
		rec := map[string]any{"pid": os.Getpid(), "sessionId": run.NativeSessionID, "procStart": procStart(t, os.Getpid())}
		if mode == "present" {
			rec["bridgeSessionId"] = "bridge_x"
		}
		records[os.Getpid()] = rec
	} else if mode == "malformed" {
		records[os.Getpid()] = "{not-json"
	}
	h := claudeBridgeHost(t, []int{os.Getpid()}, records)
	agent := claudeAgentInfo(t, run.NativeSessionID)
	a := agent["agent"].(map[string]any)
	a["cwd"], a["agent_status"], a["state_change_seq"] = run.Cwd, "idle", 1
	info, _ := json.Marshal(map[string]any{"result": agent})
	delay := 0.0
	if mode == "slow" {
		delay = 0.8
	}
	processInfo := fmt.Sprintf(`{"result":{"process_info":{"foreground_processes":[{"pid":%d}]}}}`, os.Getpid())
	script := "#!/usr/bin/env python3\nimport sys,time\na=sys.argv[1:]\nif a[:2]==['pane','process-info']:\n time.sleep(" + fmt.Sprint(delay) + ")\n print(" + fmt.Sprintf("%q", processInfo) + ")\nelif a[:2]==['agent','get']:\n print(" + fmt.Sprintf("%q", string(info)) + ")\nelif a[:2]==['pane','read']:\n print('/rc active')\nelse: sys.exit(1)\n"
	writeFixture(t, filepath.Join(h.Home, ".local", "bin", "herdr"), script)
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}}
	if shadow {
		c.shadow = newNativeEvidenceShadow(time.Now)
	}
	ctx, cancel := ctxFor()
	defer cancel()
	c.observeRemoteControl(ctx, h, j)
	if c.shadow != nil {
		c.shadow.bg.Wait()
	}
	b, e := os.ReadFile(filepath.Join(filepath.Dir(r.file), "remote-control.json"))
	if e != nil {
		return "absent"
	}
	var observation remoteControlObservation
	if json.Unmarshal(b, &observation) != nil {
		t.Fatal("observation malformed")
	}
	return observation.State
}

func TestClaudeBridgeObservationParity(t *testing.T) {
	budget := func(d time.Duration) func() (context.Context, context.CancelFunc) {
		return func() (context.Context, context.CancelFunc) { return context.WithTimeout(context.Background(), d) }
	}
	cancelled := func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, cancel
	}
	if without := observationResult(t, false, "slow", budget(4*time.Second)); without != "connected" {
		t.Fatalf("baseline fixture did not prove connection: %s", without)
	}
	for _, mode := range []string{"present", "absent", "unknown", "malformed", "slow"} {
		t.Run("decision-"+mode, func(t *testing.T) {
			if with := observationResult(t, true, mode, budget(4*time.Second)); with != "connected" {
				t.Fatalf("native read changed today's decision: %s", with)
			}
		})
	}
	t.Run("caller-deadline", func(t *testing.T) {
		without := observationResult(t, false, "slow", budget(600*time.Millisecond))
		with := observationResult(t, true, "slow", budget(600*time.Millisecond))
		if without != "connected" || with != without {
			t.Fatalf("caller deadline parity broken: without=%s with=%s", without, with)
		}
	})
	t.Run("caller-cancelled", func(t *testing.T) {
		without := observationResult(t, false, "slow", cancelled)
		with := observationResult(t, true, "slow", cancelled)
		if with != without {
			t.Fatalf("cancellation parity broken: without=%s with=%s", without, with)
		}
	})
}

// The native path never reads the pane, runs off the decision path on its own
// context, and observation starts it without waiting.
func TestClaudeBridgeNeverReadsScreen(t *testing.T) {
	src := productionSources(t)["native_claude_bridge.go"]
	for _, banned := range []string{"visiblePromptScreen", ".Screen", "claudeRemoteConnected", `"read"`, "promptSnapshot(", "connection("} {
		if strings.Contains(src, banned) {
			t.Fatalf("native Claude bridge path has %s", banned)
		}
	}
	for _, required := range []string{"go func() { // guard:bridge-async", "context.WithTimeout(context.Background(), claudeBridgeReadBudget) // guard:bridge-own-context"} {
		if !strings.Contains(src, required) {
			t.Fatalf("native read not off the decision path: %s", required)
		}
	}
	if !strings.Contains(productionSources(t)["remote_control_observation.go"], "h.observeClaudeBridge(j) // guard:claude-bridge-off-path") {
		t.Fatal("observation does not start the read off the decision path")
	}
}
