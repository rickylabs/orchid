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
		{"bridge-identity", []int{self}, map[int]any{self: rec(self, sid, start, fixtureBridge)}, true, true},
		{"no-bridge-field", []int{self}, map[int]any{self: rec(self, sid, start, nil)}, false, true},
		{"other-session", []int{self}, map[int]any{self: rec(self, syntheticRemoteRun(t).NativeSessionID, start, fixtureBridge)}, false, false},
		{"reused-pid", []int{self}, map[int]any{self: rec(self, sid, start+"1", fixtureBridge)}, false, false},
		{"record-names-other-pid", []int{self}, map[int]any{self: rec(self+1, sid, start, fixtureBridge)}, false, false},
		{"no-record", []int{self}, map[int]any{}, false, false},
		{"garbage-record", []int{self}, map[int]any{self: raw("{not json")}, false, false},
		{"two-matches", []int{self, parent}, map[int]any{self: rec(self, sid, start, "b"), parent: rec(parent, sid, procStart(t, parent), "b")}, false, false},
		{"other-pane-process-irrelevant", []int{parent, self}, map[int]any{self: rec(self, sid, start, fixtureBridge)}, true, true},
		{"valid-plus-malformed-pane-record", []int{self, parent}, map[int]any{self: rec(self, sid, start, fixtureBridge), parent: raw("{not json")}, false, false},
		{"no-processes", nil, map[int]any{self: rec(self, sid, start, fixtureBridge)}, false, false},
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
	h := claudeBridgeHost(t, []int{self}, map[int]any{self: map[string]any{"pid": self, "sessionId": j.RemoteControl.NativeSessionID, "procStart": procStart(t, self), "bridgeSessionId": fixtureBridge}})
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
	if strings.Contains(string(out), fixtureBridge) || strings.Contains(string(out), j.RemoteControl.NativeSessionID) || !strings.Contains(string(out), `"no-official-surface"`) {
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
			rec["bridgeSessionId"] = fixtureBridge
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

// fixtureBridge is a bridge session id in Claude's documented session id form.
const fixtureBridge = "session_AbCdEfGhIjKlMnOpQrStUvWx"

func TestClaudeBridgeSessionIDForm(t *testing.T) {
	sid := syntheticRemoteRun(t).NativeSessionID
	self := os.Getpid()
	start := procStart(t, self)
	for _, tc := range []struct {
		name, bridge string
		ok           bool
	}{
		{"documented-form", fixtureBridge, true},
		{"other-prefix", "bridge_AbCdEfGhIjKl", false},
		{"bare-prefix", "session_", false},
		{"punctuation", "session_Ab-Cd", false},
		{"url", "https://claude.ai/code/" + fixtureBridge, false},
		{"too-long", "session_" + strings.Repeat("a", 129), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := claudeBridgeHost(t, []int{self}, map[int]any{self: map[string]any{"pid": self, "sessionId": sid, "procStart": start, "bridgeSessionId": tc.bridge}})
			bridge, present, ok := h.claudeBridgeSession(context.Background(), "w1:p1", sid)
			if ok != tc.ok || (tc.ok && (bridge != tc.bridge || !present)) || (!tc.ok && bridge != "") {
				t.Fatalf("bridge=%q present=%v ok=%v", bridge, present, ok)
			}
		})
	}
}

func TestClaudeLinkStore(t *testing.T) {
	s := &claudeLinkStore{}
	now := time.Now()
	s.set("k", "native-a", "https://claude.ai/code/"+fixtureBridge, now)
	if got := s.fresh("k", "native-a", now.Add(claudeLinkFreshness)); got == nil || *got != "https://claude.ai/code/"+fixtureBridge {
		t.Fatal("fresh link not returned")
	}
	if s.fresh("k", "native-b", now) != nil || s.fresh("other", "native-a", now) != nil {
		t.Fatal("link returned for another session or job")
	}
	if s.fresh("k", "native-a", now.Add(claudeLinkFreshness+time.Second)) != nil || s.fresh("k", "native-a", now.Add(-time.Second)) != nil {
		t.Fatal("stale or future link returned")
	}
	s.clear("k")
	if s.fresh("k", "native-a", now) != nil {
		t.Fatal("cleared link returned")
	}
	var none *claudeLinkStore
	none.set("k", "n", "u", now)
	if none.fresh("k", "n", now) != nil {
		t.Fatal("nil store returned a link")
	}
}

// End to end through the production observation: the Claude session link
// comes only from Claude's own record bound to the exact process, on both a
// connected and an unconfirmed (footer failed) observation; without a bound
// record it is null even when the pane shows a session URL as text.
func TestObservationCarriesNativeClaudeLink(t *testing.T) {
	for _, tc := range []struct {
		name, mode, pane string
		state            string
		link             bool
	}{
		{"connected-with-bridge", "present", "/rc active", "connected", true},
		{"footer-failed-with-bridge", "present", "no indicator here", "unconfirmed", true},
		{"no-bridge-field", "absent", "/rc active", "connected", false},
		{"no-record-pane-shows-url", "none", "/rc active https://claude.ai/code/" + fixtureBridge, "connected", false},
		{"malformed-record", "malformed", "/rc active", "connected", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, link := observeClaudeLink(t, tc.mode, tc.pane)
			if state != tc.state {
				t.Fatalf("state %q, want %q", state, tc.state)
			}
			if tc.link != (link != nil) || (link != nil && *link != "https://claude.ai/code/"+fixtureBridge) {
				t.Fatalf("link %v, want present=%v", link, tc.link)
			}
		})
	}
}

func observeClaudeLink(t *testing.T, mode, pane string) (string, *string) {
	t.Helper()
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
	switch mode {
	case "present", "absent":
		rec := map[string]any{"pid": os.Getpid(), "sessionId": run.NativeSessionID, "procStart": procStart(t, os.Getpid())}
		if mode == "present" {
			rec["bridgeSessionId"] = fixtureBridge
		}
		records[os.Getpid()] = rec
	case "malformed":
		records[os.Getpid()] = "{not-json"
	}
	h := claudeBridgeHost(t, []int{os.Getpid()}, records)
	agent := claudeAgentInfo(t, run.NativeSessionID)
	a := agent["agent"].(map[string]any)
	a["cwd"], a["agent_status"], a["state_change_seq"] = run.Cwd, "idle", 1
	info, _ := json.Marshal(map[string]any{"result": agent})
	processInfo := fmt.Sprintf(`{"result":{"process_info":{"foreground_processes":[{"pid":%d}]}}}`, os.Getpid())
	script := "#!/usr/bin/env python3\nimport sys\na=sys.argv[1:]\nif a[:2]==['pane','process-info']:\n print(" + fmt.Sprintf("%q", processInfo) + ")\nelif a[:2]==['agent','get']:\n print(" + fmt.Sprintf("%q", string(info)) + ")\nelif a[:2]==['pane','read']:\n print(" + fmt.Sprintf("%q", pane) + ")\nelse: sys.exit(1)\n"
	writeFixture(t, filepath.Join(h.Home, ".local", "bin", "herdr"), script)
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, shadow: newNativeEvidenceShadow(time.Now), claudeLinks: &claudeLinkStore{}}
	for pass := 0; pass < 2; pass++ { // the first pass starts the off-path read; the second writes its link
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c.observeRemoteControl(ctx, h, j)
		cancel()
		c.shadow.bg.Wait()
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(r.file), "remote-control.json"))
	if err != nil {
		return "absent", nil
	}
	var o remoteControlObservation
	if json.Unmarshal(b, &o) != nil {
		t.Fatal("observation malformed")
	}
	return o.State, o.Link
}

// The observation writer accepts only a Claude link in the documented form.
func TestObservationLinkForm(t *testing.T) {
	r := registrationReceipt(t, "claude", Overrides{})
	r.dispatch.Host = "fixture-host"
	if err := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); err != nil {
		t.Fatal("fixture dispatch")
	}
	run := syntheticRemoteRun(t)
	codex := registrationReceipt(t, "codex", Overrides{})
	codex.dispatch.Host = "fixture-host"
	if err := codex.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); err != nil {
		t.Fatal("fixture dispatch")
	}
	// A Codex observation is otherwise valid, so only the link can refuse it.
	if err := writeRemoteObservation(context.Background(), codex, "codex", run, "unconfirmed", "remote-control-unconfirmed", nil, nil); err != nil {
		t.Fatalf("control: codex observation without a link refused: %v", err)
	}
	link := "https://claude.ai/code/" + fixtureBridge
	if writeRemoteObservation(context.Background(), codex, "codex", run, "unconfirmed", "remote-control-unconfirmed", nil, &link) == nil {
		t.Fatal("a Claude link was written on a Codex observation")
	}
	for _, tc := range []struct {
		kind, link string
		ok         bool
	}{
		{"claude", "https://claude.ai/code/" + fixtureBridge, true},
		{"claude", "https://claude.ai/code/" + fixtureBridge + "?x=1", false},
		{"claude", "http://claude.ai/code/" + fixtureBridge, false},
		{"claude", "https://example.com/code/" + fixtureBridge, false},
	} {
		link := tc.link
		err := writeRemoteObservation(context.Background(), r, tc.kind, run, "unconfirmed", "remote-control-unconfirmed", nil, &link)
		if tc.ok != (err == nil) && !(tc.kind == "codex" && err != nil) {
			t.Fatalf("%s %q accepted=%v", tc.kind, tc.link, err == nil)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s %q accepted", tc.kind, tc.link)
		}
	}
}

// A later unknown or bridge-less read clears the cached link: the link never
// outlives the native record it came from.
func TestClaudeLinkClearedByLaterRead(t *testing.T) {
	j := shadowFixtureJob(t, "claude")
	self := os.Getpid()
	rec := func(bridge bool) map[string]any {
		r := map[string]any{"pid": self, "sessionId": j.RemoteControl.NativeSessionID, "procStart": procStart(t, self)}
		if bridge {
			r["bridgeSessionId"] = fixtureBridge
		}
		return r
	}
	for _, later := range []string{"malformed", "no-bridge"} {
		t.Run(later, func(t *testing.T) {
			h := claudeBridgeHost(t, []int{self}, map[int]any{self: rec(true)})
			h.ShadowScope = newNativeEvidenceShadow(time.Now).scope(j)
			h.ClaudeLinks = &claudeLinkStore{}
			h.readClaudeBridge(j)
			if h.ClaudeLinks.fresh(j.DispatchKey, j.RemoteControl.NativeSessionID, time.Now()) == nil {
				t.Fatal("bound bridge did not yield a link")
			}
			path := filepath.Join(h.Home, ".claude", "sessions", fmt.Sprintf("%d.json", self))
			if later == "malformed" {
				writeFixture(t, path, "{not json")
			} else {
				b, _ := json.Marshal(rec(false))
				writeFixture(t, path, string(b))
			}
			h.readClaudeBridge(j)
			if h.ClaudeLinks.fresh(j.DispatchKey, j.RemoteControl.NativeSessionID, time.Now()) != nil {
				t.Fatal("a later read did not clear the link")
			}
		})
	}
}
