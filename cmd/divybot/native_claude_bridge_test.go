package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// writeBridgeTranscript writes the session's own transcript (owner-only)
// where Claude Code keeps it for cwd.
func writeBridgeTranscript(t *testing.T, home, cwd, sid string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(cwd, "-"))
	path := filepath.Join(dir, sid+".jsonl")
	if os.MkdirAll(dir, 0700) != nil || os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600) != nil || os.Chmod(path, 0600) != nil {
		t.Fatal("fixture transcript")
	}
	return path
}

// launchedSelf records this test process as the Claude process captured at
// launch for j, as the managed launch does.
func launchedSelf(t *testing.T, j *Job) *Job {
	t.Helper()
	j.RemoteControl.ClaudeProcess = &claudeProcess{PID: os.Getpid(), Start: procStart(t, os.Getpid())}
	return j
}

// bridgeStatus is a typed bridge_status transcript entry.
func bridgeStatus(sid, url string) string {
	b, _ := json.Marshal(map[string]any{"type": "system", "subtype": "bridge_status", "sessionId": sid, "url": url, "content": "synthetic status"})
	return string(b)
}

// setPanePIDs makes the fake Herdr report other pane processes.
func setPanePIDs(t *testing.T, h Host, pids ...int) {
	t.Helper()
	procs := []map[string]any{}
	for _, pid := range pids {
		procs = append(procs, map[string]any{"pid": pid, "name": "claude"})
	}
	info, _ := json.Marshal(map[string]any{"result": map[string]any{"process_info": map[string]any{"foreground_processes": procs}}})
	writeFixture(t, filepath.Join(h.Home, ".local", "bin", "herdr"), "#!/bin/sh\nprintf '%s\\n' '"+string(info)+"'\n")
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
	// Claude documents no native connection state: always unconfirmed.
	if without := observationResult(t, false, "slow", budget(4*time.Second)); without != "unconfirmed" {
		t.Fatalf("baseline fixture not unconfirmed: %s", without)
	}
	for _, mode := range []string{"present", "absent", "unknown", "malformed", "slow"} {
		t.Run("decision-"+mode, func(t *testing.T) {
			if with := observationResult(t, true, mode, budget(4*time.Second)); with != "unconfirmed" {
				t.Fatalf("native read changed today's decision: %s", with)
			}
		})
	}
	t.Run("caller-deadline", func(t *testing.T) {
		without := observationResult(t, false, "slow", budget(600*time.Millisecond))
		with := observationResult(t, true, "slow", budget(600*time.Millisecond))
		if without != "unconfirmed" || with != without {
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
	// Terminal text never proves a Claude connection or gates a launch.
	proof := productionSources(t)["remote_control.go"]
	start := strings.Index(proof, "func (h Host) remoteProof(")
	end := strings.Index(proof[start+1:], "\nfunc ")
	if start < 0 || end < 0 {
		t.Fatal("remote proof not found")
	}
	for _, banned := range []string{"visiblePromptScreen", "Screen", "/rc active", "claudeRemoteConnected"} {
		if strings.Contains(proof[start:start+1+end], banned) {
			t.Fatalf("the remote proof reads terminal text: %s", banned)
		}
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
			read, reason := h.claudeBridgeSession(context.Background(), "w1:p1", sid, "", claudeTranscriptCursor{})
			if (reason == "") != tc.ok || (tc.ok && read.Bridge != tc.bridge) || (!tc.ok && read != (claudeBridgeRead{})) {
				t.Fatalf("bridge=%q reason=%q", read.Bridge, reason)
			}
		})
	}
}

func TestClaudeLinkStore(t *testing.T) {
	s := &claudeLinkStore{}
	now := time.Now()
	link := "https://claude.ai/code/" + fixtureBridge
	s.set("k", "native-a", link, now)
	if got := s.fresh("k", "native-a", now.Add(claudeLinkFreshness)); got == nil || *got != link {
		t.Fatal("fresh link not returned")
	}
	if s.fresh("k", "native-b", now) != nil || s.fresh("other", "native-a", now) != nil {
		t.Fatal("link returned for another session or job")
	}
	if s.fresh("k", "native-a", now.Add(claudeLinkFreshness+time.Second)) != nil || s.fresh("k", "native-a", now.Add(-time.Second)) != nil {
		t.Fatal("stale or future link returned")
	}
	// Withholding a published link revokes the published row exactly once; a
	// later publish writes no link.
	revoked := 0
	s.revokeWith(func(key string) {
		if key == "k" {
			revoked++
		}
	})
	var wrote *string
	write := func(l *string) error { wrote = l; return nil }
	live := true
	alive := func() bool { return live }
	if s.publish("k", 7, "native-a", now, alive, write) != nil || wrote == nil || *wrote != link {
		t.Fatal("control: fresh link not published")
	}
	s.withhold("k", 7, linkRecordUnbound)
	if revoked != 1 || s.fresh("k", "native-a", now) != nil {
		t.Fatal("withheld link not revoked from the published row")
	}
	s.withhold("k", 7, linkRecordUnbound)
	if s.publish("k", 7, "native-a", now, alive, write) != nil || wrote != nil {
		t.Fatal("a withheld link was published again")
	}
	s.withhold("k", 7, linkBridgeAbsent)
	if revoked != 1 {
		t.Fatal("a row without a link was revoked")
	}
	s.set("k", "native-a", link, now)
	if s.publish("k", 7, "native-a", now, alive, func(*string) error { return errClaudeBridgeUnknown }) == nil {
		t.Fatal("write error lost")
	}
	s.withhold("k", 7, linkRecordUnbound)
	if revoked != 1 {
		t.Fatal("an unwritten link was revoked")
	}
	// A link is published only while the launched process is alive now; an
	// ended process withholds the cached link.
	s.set("k", "native-a", link, now)
	live = false
	if s.publish("k", 7, "native-a", now, alive, write) != nil || wrote != nil || s.fresh("k", "native-a", now) != nil {
		t.Fatal("a link was published, or kept, for an ended process")
	}
	live = true
	// forget drops the transcript cursor with the link.
	s.advance("k", claudeTranscriptCursor{offset: 9, entry: true, url: link})
	s.forget("k", 7, linkIdentityChanged)
	if s.cursor("k") != (claudeTranscriptCursor{}) {
		t.Fatal("an ended session kept its transcript cursor")
	}
	var none *claudeLinkStore
	none.set("k", "n", "u", now)
	none.withhold("k", 7, linkRecordUnbound)
	none.forget("k", 7, linkIdentityChanged)
	none.revokeWith(nil)
	none.advance("k", claudeTranscriptCursor{offset: 1})
	if none.fresh("k", "n", now) != nil || none.cursor("k") != (claudeTranscriptCursor{}) || none.publish("k", 7, "n", now, alive, write) != nil || wrote != nil {
		t.Fatal("nil store returned or published a link")
	}
}

// A withheld link is logged once per reason change, naming only the reason.
func TestClaudeLinkWithheldReasonLogged(t *testing.T) {
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	s := &claudeLinkStore{}
	s.withhold("k", 7, linkBridgeAbsent)
	s.withhold("k", 7, linkBridgeAbsent)
	s.withhold("k", 7, linkSourcesDisagree)
	s.set("k", "native-a", "https://claude.ai/code/"+fixtureBridge, time.Now())
	s.withhold("k", 7, linkSourcesDisagree)
	got := buf.String()
	if strings.Count(got, "claude session link withheld") != 3 || strings.Count(got, "("+linkBridgeAbsent+")") != 1 || strings.Contains(got, fixtureBridge) || strings.Contains(got, "native-a") {
		t.Fatalf("withheld reasons not logged once per change without private values: %d lines", strings.Count(got, "\n"))
	}
}

// End to end through the production observation: the Claude session link is
// served only when Claude's own record bound to the launched process and the
// latest bridge_status entry of its transcript agree. Terminal text never
// matters: the state is unconfirmed whatever the pane shows, and a session URL
// shown as text never becomes a link.
func TestObservationCarriesNativeClaudeLink(t *testing.T) {
	url := "https://claude.ai/code/" + fixtureBridge
	other := "https://claude.ai/code/session_ZyXwVuTsRqPoNmLkJiHgFeDc"
	entry := func(u string) func(string) []string {
		return func(sid string) []string { return []string{`{"type":"user","sessionId":"x"}`, bridgeStatus(sid, u)} }
	}
	for _, tc := range []struct {
		name, mode, pane string
		transcript       func(sid string) []string
		link             bool
	}{
		{"both-agree", "present", "/rc active", entry(url), true},
		{"both-agree-footer-failed", "present", "Remote Control failed", entry(url), true},
		{"both-agree-no-footer", "present", "idle composer", entry(url), true},
		{"latest-entry-agrees", "present", "", func(sid string) []string { return []string{bridgeStatus(sid, other), bridgeStatus(sid, url)} }, true},
		{"latest-entry-disagrees", "present", "", func(sid string) []string { return []string{bridgeStatus(sid, url), bridgeStatus(sid, other)} }, false},
		{"record-only", "present", "/rc active", nil, false},
		{"transcript-only", "absent", "/rc active", entry(url), false},
		{"sources-disagree", "present", "/rc active", entry(other), false},
		{"foreign-session-entry", "present", "", func(string) []string { return []string{bridgeStatus("another-session", url)} }, false},
		{"duplicate-key-entry", "present", "", func(sid string) []string {
			return []string{fmt.Sprintf(`{"type":"system","subtype":"bridge_status","sessionId":%q,"url":%q,"url":%q}`, sid, url, url)}
		}, false},
		{"url-not-string", "present", "", func(sid string) []string {
			return []string{fmt.Sprintf(`{"type":"system","subtype":"bridge_status","sessionId":%q,"url":7}`, sid)}
		}, false},
		{"no-record-pane-shows-url", "none", "/rc active " + url, entry(url), false},
		{"malformed-record", "malformed", "/rc active", entry(url), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, link := observeClaudeLink(t, tc.mode, tc.pane, tc.transcript)
			if state != "unconfirmed" {
				t.Fatalf("state %q, want unconfirmed", state)
			}
			if tc.link != (link != nil) || (link != nil && *link != url) { // guard:claude-link-e2e
				t.Fatalf("link present=%v, want %v", link != nil, tc.link)
			}
		})
	}
}

// claudeLinkFixture is a Claude job with synthetic receipts, Herdr and native
// records, observed through the production observation entry point.
type claudeLinkFixture struct {
	c   *Coord
	h   Host
	j   *Job
	row string
}

func newClaudeLinkFixture(t *testing.T, mode, pane string, transcript func(sid string) []string) *claudeLinkFixture {
	t.Helper()
	r := registrationReceipt(t, "claude", Overrides{})
	r.dispatch.Host = "fixture-host"
	if err := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); err != nil {
		t.Fatal("fixture dispatch")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(r.file)))
	run := syntheticRemoteRun(t)
	j := launchedSelf(t, &Job{Issue: 7, Agent: "claude", Repo: "fixture/repo", Label: "fixture-agent", Host: "fixture-host", Pane: "w1:p1", Workspace: "w1", DispatchKey: strings.Repeat("d", 64), RemoteControl: run})
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
	if transcript != nil {
		writeBridgeTranscript(t, h.Home, run.Cwd, run.NativeSessionID, transcript(run.NativeSessionID)...)
	}
	agent := claudeAgentInfo(t, run.NativeSessionID)
	a := agent["agent"].(map[string]any)
	a["cwd"], a["agent_status"], a["state_change_seq"] = run.Cwd, "idle", 1
	info, _ := json.Marshal(map[string]any{"result": agent})
	processInfo := fmt.Sprintf(`{"result":{"process_info":{"foreground_processes":[{"pid":%d}]}}}`, os.Getpid())
	script := "#!/usr/bin/env python3\nimport sys\na=sys.argv[1:]\nif a[:2]==['pane','process-info']:\n print(" + fmt.Sprintf("%q", processInfo) + ")\nelif a[:2]==['agent','get']:\n print(" + fmt.Sprintf("%q", string(info)) + ")\nelif a[:2]==['pane','read']:\n print(" + fmt.Sprintf("%q", pane) + ")\nelse: sys.exit(1)\n"
	writeFixture(t, filepath.Join(h.Home, ".local", "bin", "herdr"), script)
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, shadow: newNativeEvidenceShadow(time.Now), claudeLinks: &claudeLinkStore{}}
	return &claudeLinkFixture{c: c, h: h, j: j, row: filepath.Join(filepath.Dir(r.file), "remote-control.json")}
}

// observe runs one observation and waits for its off-path read.
func (f *claudeLinkFixture) observe() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	f.c.observeRemoteControl(ctx, f.h, f.j)
	cancel()
	f.c.shadow.bg.Wait()
}

// published reads the row on disk: whether it exists and its link.
func (f *claudeLinkFixture) published(t *testing.T) (bool, string, *string) {
	t.Helper()
	b, err := os.ReadFile(f.row)
	if os.IsNotExist(err) {
		return false, "", nil
	}
	var o remoteControlObservation
	if err != nil || json.Unmarshal(b, &o) != nil {
		t.Fatal("observation malformed")
	}
	return true, o.State, o.Link
}

func observeClaudeLink(t *testing.T, mode, pane string, transcript func(sid string) []string) (string, *string) {
	t.Helper()
	f := newClaudeLinkFixture(t, mode, pane, transcript)
	f.observe() // the first pass starts the off-path read; the second writes its link
	f.observe()
	for _, c := range f.c.shadow.ledger(f.j) {
		if c.Site == shadowSiteConnection && c.ScreenDerived {
			t.Fatal("the Claude connection verdict is recorded as screen-derived")
		}
	}
	ok, state, link := f.published(t)
	if !ok {
		return "absent", nil
	}
	return state, link
}

// A published link is revoked from the row on disk as soon as a later native
// read no longer supports it (the record ended or lost its bridge), even while
// the occupant itself is unchanged; the next row carries no link.
func TestClaudeLinkRevokedFromPublishedRow(t *testing.T) {
	for _, later := range []string{"record-removed", "bridge-removed", "transcript-disagrees"} {
		t.Run(later, func(t *testing.T) {
			f := newClaudeLinkFixture(t, "present", "", func(sid string) []string { return []string{bridgeStatus(sid, "https://claude.ai/code/"+fixtureBridge)} })
			f.observe()
			f.observe()
			if _, _, link := f.published(t); link == nil {
				t.Fatal("control: agreeing sources did not publish a link")
			}
			run := f.j.RemoteControl
			record := filepath.Join(f.h.Home, ".claude", "sessions", fmt.Sprintf("%d.json", os.Getpid()))
			switch later {
			case "record-removed":
				_ = os.Remove(record)
			case "bridge-removed":
				b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": run.NativeSessionID, "procStart": procStart(t, os.Getpid())})
				writeFixture(t, record, string(b))
			case "transcript-disagrees":
				writeBridgeTranscript(t, f.h.Home, run.Cwd, run.NativeSessionID, bridgeStatus(run.NativeSessionID, "https://claude.ai/code/session_ZyXwVuTsRqPoNmLkJiHgFeDc"))
			}
			// This pass publishes from the still-fresh cache while its off-path
			// read withholds: the withheld link must not stay on disk.
			f.observe()
			if _, _, link := f.published(t); link != nil { // guard:claude-link-revoked-on-disk
				t.Fatal("a withheld link stayed on the published row")
			}
			f.observe()
			if ok, state, link := f.published(t); !ok || state != "unconfirmed" || link != nil {
				t.Fatal("the next row was not unconfirmed without a link")
			}
		})
	}
}

// Only the process captured at launch supplies the link: a later process in
// the pane with its own bound record for the same session is refused, also
// when the launched process never had a bridge; without a launch capture there
// is no link at all.
func TestClaudeLinkOnlyFromLaunchedProcess(t *testing.T) {
	self, parent := os.Getpid(), os.Getppid()
	for _, tc := range []struct {
		name         string
		launched     bool
		firstBridged bool
	}{{"launched-bridged-then-replaced", true, true}, {"launched-bridgeless-then-replaced", true, false}, {"no-launch-capture", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			j := shadowFixtureJob(t, "claude")
			if tc.launched {
				launchedSelf(t, j)
			}
			rec := func(pid int, bridge bool) map[string]any {
				r := map[string]any{"pid": pid, "sessionId": j.RemoteControl.NativeSessionID, "procStart": procStart(t, pid)}
				if bridge {
					r["bridgeSessionId"] = fixtureBridge
				}
				return r
			}
			h := claudeBridgeHost(t, []int{self}, map[int]any{self: rec(self, tc.firstBridged), parent: rec(parent, true)})
			writeBridgeTranscript(t, h.Home, j.RemoteControl.Cwd, j.RemoteControl.NativeSessionID, bridgeStatus(j.RemoteControl.NativeSessionID, "https://claude.ai/code/"+fixtureBridge))
			h.ShadowScope = newNativeEvidenceShadow(time.Now).scope(j)
			h.ClaudeLinks = &claudeLinkStore{}
			h.readClaudeBridge(j)
			if got := h.ClaudeLinks.fresh(j.DispatchKey, j.RemoteControl.NativeSessionID, time.Now()) != nil; got != (tc.launched && tc.firstBridged) {
				t.Fatalf("control: launched-process link present=%v", got)
			}
			setPanePIDs(t, h, parent)
			h.readClaudeBridge(j)
			if h.ClaudeLinks.fresh(j.DispatchKey, j.RemoteControl.NativeSessionID, time.Now()) != nil { // guard:claude-link-launched-only
				t.Fatal("a process other than the launched one supplied the link")
			}
		})
	}
}

// The managed launch captures the launched process from Claude's own bound
// record, without a bridge, and keeps it in the run's private state (which a
// restarted coordinator reloads); a missing record never fails the launch.
func TestClaudeLaunchCapturesProcess(t *testing.T) {
	for _, withRecord := range []bool{true, false} {
		t.Run(fmt.Sprintf("record-%v", withRecord), func(t *testing.T) {
			root, cwd := t.TempDir(), t.TempDir()
			bin := filepath.Join(root, ".local", "bin")
			if os.MkdirAll(bin, 0700) != nil || os.MkdirAll(filepath.Join(root, ".claude", "sessions"), 0700) != nil {
				t.Fatal("fixture setup failed")
			}
			id := privateTestID(t)
			if withRecord {
				b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "sessionId": id, "procStart": procStart(t, os.Getpid())})
				writeFixture(t, filepath.Join(root, ".claude", "sessions", fmt.Sprintf("%d.json", os.Getpid())), string(b))
			}
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("REMOTE_FIXTURE_CWD", cwd)
			t.Setenv("REMOTE_FIXTURE_ID", id)
			t.Setenv("REMOTE_FIXTURE_PID", fmt.Sprint(os.Getpid()))
			script := `#!/usr/bin/env python3
import sys,json,os
a=sys.argv[1:]
agent={'agent':'claude','name':'fixture-agent','pane_id':'w1:p1','workspace_id':'w1','cwd':os.environ['REMOTE_FIXTURE_CWD'],'agent_status':'idle','interactive_ready':True,'state_change_seq':1,'agent_session':{'source':'herdr:claude','agent':'claude','kind':'id','value':os.environ['REMOTE_FIXTURE_ID']}}
if a[:2]==['workspace','create']:r={'type':'workspace_created','workspace':{'workspace_id':'w1'},'root_pane':{'pane_id':'w1:p1','workspace_id':'w1'}}
elif a[:2]==['agent','start']:r={'type':'agent_started','agent':agent}
elif a[:2]==['agent','get']:r={'type':'agent_info','agent':agent}
elif a[:2]==['pane','process-info']:r={'process_info':{'foreground_processes':[{'pid':int(os.environ['REMOTE_FIXTURE_PID'])}]}}
elif a[:2]==['pane','read']:print('idle composer');sys.exit(0)
else:r={'type':'ok'}
print(json.dumps({'result':r}))
`
			writeFixture(t, filepath.Join(bin, "herdr"), script)
			if os.Chmod(filepath.Join(bin, "herdr"), 0700) != nil {
				t.Fatal("fixture executable unavailable")
			}
			on := true
			host := Host{Name: "fixture-host", Home: root, RemoteControl: &RemoteControlConfig{Claude: &on}}
			receipt := registrationReceipt(t, "claude", Overrides{})
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if _, _, err := host.spawnAgent(ctx, "fixture-agent", cwd, nil, "claude", Overrides{}, receipt, "synthetic task"); err != nil {
				t.Fatalf("launch failed: %v", err)
			}
			var run remoteControlRun
			if readPrivateActionJSON(filepath.Join(filepath.Dir(receipt.file), "remote-control-run.json"), &run) != nil {
				t.Fatal("private run state unreadable")
			}
			want := &claudeProcess{PID: os.Getpid(), Start: procStart(t, os.Getpid())}
			if withRecord && (run.ClaudeProcess == nil || *run.ClaudeProcess != *want) { // guard:claude-launch-captured
				t.Fatal("the launched process was not captured with the run")
			}
			if !withRecord && run.ClaudeProcess != nil {
				t.Fatal("a process was captured without its bound record")
			}
		})
	}
}

// Liveness is read natively, now: pid with the captured kernel start time.
func TestClaudeProcessAlive(t *testing.T) {
	h := claudeBridgeHost(t, nil, nil)
	self := os.Getpid()
	if !h.claudeProcessAlive(context.Background(), &claudeProcess{PID: self, Start: procStart(t, self)}) {
		t.Fatal("control: this live process reads as ended")
	}
	child := exec.Command("sleep", "60")
	if child.Start() != nil {
		t.Fatal("fixture child")
	}
	p := &claudeProcess{PID: child.Process.Pid, Start: procStart(t, child.Process.Pid)}
	_ = child.Process.Kill()
	_ = child.Wait()
	for _, ended := range []*claudeProcess{nil, p, {PID: self, Start: procStart(t, self) + "0"}, {PID: self, Start: "1;x"}} {
		if h.claudeProcessAlive(context.Background(), ended) { // guard:claude-process-alive
			t.Fatalf("an ended or reused process reads as alive: %+v", ended != nil)
		}
	}
}

// An exited process that its parent has not reaped yet (procfs state Z) keeps
// its pid and start time, yet it is ended: neither the liveness read nor the
// bound-record read accepts it.
func TestClaudeZombieProcessIsEnded(t *testing.T) {
	child := exec.Command("sleep", "60")
	if child.Start() != nil {
		t.Fatal("fixture child")
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	p := &claudeProcess{PID: pid, Start: procStart(t, pid)}
	sid := syntheticRemoteRun(t).NativeSessionID
	h := claudeBridgeHost(t, []int{pid}, map[int]any{pid: map[string]any{"pid": pid, "sessionId": sid, "procStart": p.Start}})
	if !h.claudeProcessAlive(context.Background(), p) {
		t.Fatal("control: running child reads as ended")
	}
	if _, reason := h.claudeBridgeSession(context.Background(), "w1:p1", sid, "", claudeTranscriptCursor{}); reason != "" {
		t.Fatalf("control: running child record not bound: %s", reason)
	}
	_ = child.Process.Kill()
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			t.Fatal("control: unreaped child vanished")
		}
		if fields := strings.Fields(string(raw)[strings.LastIndex(string(raw), ")")+1:]); len(fields) > 0 && fields[0] == "Z" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control: child never reached zombie state")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h.claudeProcessAlive(context.Background(), p) { // guard:claude-zombie-not-alive
		t.Fatal("a zombie process reads as alive")
	}
	if _, reason := h.claudeBridgeSession(context.Background(), "w1:p1", sid, "", claudeTranscriptCursor{}); reason != linkRecordUnbound { // guard:claude-zombie-not-bound
		t.Fatalf("a zombie process record was bound: %q", reason)
	}
}

// Every complete transcript line is parsed as JSON, from a per-job cursor:
// a JSON-escaped typed entry counts, a partial last line waits until it is
// complete, a replaced file restarts, and a long transcript is read in bounded
// chunks with the link withheld until the parse has caught up.
func TestClaudeTranscriptCursor(t *testing.T) {
	url := "https://claude.ai/code/" + fixtureBridge
	other := "https://claude.ai/code/session_ZyXwVuTsRqPoNmLkJiHgFeDc"
	setup := func(t *testing.T) (*Job, Host, string) {
		j := launchedSelf(t, shadowFixtureJob(t, "claude"))
		self := os.Getpid()
		h := claudeBridgeHost(t, []int{self}, map[int]any{self: map[string]any{"pid": self, "sessionId": j.RemoteControl.NativeSessionID, "procStart": procStart(t, self), "bridgeSessionId": fixtureBridge}})
		h.ShadowScope = newNativeEvidenceShadow(time.Now).scope(j)
		h.ClaudeLinks = &claudeLinkStore{}
		return j, h, writeBridgeTranscript(t, h.Home, j.RemoteControl.Cwd, j.RemoteControl.NativeSessionID, bridgeStatus(j.RemoteControl.NativeSessionID, url))
	}
	has := func(h Host, j *Job) bool {
		return h.ClaudeLinks.fresh(j.DispatchKey, j.RemoteControl.NativeSessionID, time.Now()) != nil
	}
	appendLine := func(t *testing.T, path, text string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil || func() error { _, e := f.WriteString(text); return e }() != nil || f.Close() != nil {
			t.Fatal("append")
		}
	}
	t.Run("appended-escaped-disagreement", func(t *testing.T) {
		j, h, path := setup(t)
		h.readClaudeBridge(j)
		if !has(h, j) {
			t.Fatal("control: agreeing transcript gave no link")
		}
		appendLine(t, path, strings.Replace(bridgeStatus(j.RemoteControl.NativeSessionID, other), "bridge_status", `bridge\u005fstatus`, 1)+"\n")
		h.readClaudeBridge(j)
		if has(h, j) { // guard:claude-transcript-parse-all
			t.Fatal("a JSON-escaped latest entry was skipped")
		}
	})
	t.Run("partial-line-waits", func(t *testing.T) {
		j, h, path := setup(t)
		h.readClaudeBridge(j)
		line := bridgeStatus(j.RemoteControl.NativeSessionID, other)
		appendLine(t, path, line[:len(line)/2])
		h.readClaudeBridge(j)
		if !has(h, j) {
			t.Fatal("a line still being written was parsed")
		}
		appendLine(t, path, line[len(line)/2:]+"\n")
		h.readClaudeBridge(j)
		if has(h, j) {
			t.Fatal("the completed latest entry was not read")
		}
	})
	t.Run("replaced-file-restarts", func(t *testing.T) {
		j, h, path := setup(t)
		h.readClaudeBridge(j)
		real := path + ".new"
		if os.WriteFile(real, []byte(bridgeStatus(j.RemoteControl.NativeSessionID, other)+"\n"+`{"type":"user"}`+"\n"+`{"type":"user"}`+"\n"), 0600) != nil || os.Rename(real, path) != nil {
			t.Fatal("replace")
		}
		h.readClaudeBridge(j)
		if has(h, j) {
			t.Fatal("a replaced transcript kept the old cursor")
		}
	})
	t.Run("long-transcript-chunks", func(t *testing.T) {
		j, h, path := setup(t)
		filler := `{"type":"user","text":"` + strings.Repeat("x", 1<<20) + `"}` + "\n"
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal("open")
		}
		for i := 0; i < 20; i++ {
			_, _ = f.WriteString(filler)
		}
		_ = f.Close()
		h.readClaudeBridge(j)
		if has(h, j) || h.ClaudeLinks.cursor(j.DispatchKey).offset == 0 { // guard:claude-transcript-chunked
			t.Fatal("a partly parsed transcript served a link, or made no progress")
		}
		h.readClaudeBridge(j)
		if !has(h, j) {
			t.Fatal("the caught-up transcript gave no link")
		}
	})
}

// The transcript must be Claude's own private regular file.
func TestClaudeLinkTranscriptMustBePrivate(t *testing.T) {
	j := shadowFixtureJob(t, "claude")
	self := os.Getpid()
	for _, mode := range []string{"world-readable", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			h := claudeBridgeHost(t, []int{self}, map[int]any{self: map[string]any{"pid": self, "sessionId": j.RemoteControl.NativeSessionID, "procStart": procStart(t, self), "bridgeSessionId": fixtureBridge}})
			path := writeBridgeTranscript(t, h.Home, j.RemoteControl.Cwd, j.RemoteControl.NativeSessionID, bridgeStatus(j.RemoteControl.NativeSessionID, "https://claude.ai/code/"+fixtureBridge))
			if mode == "world-readable" {
				_ = os.Chmod(path, 0644)
			} else {
				real := path + ".real"
				if os.Rename(path, real) != nil || os.Symlink(real, path) != nil {
					t.Fatal("fixture symlink")
				}
			}
			read, reason := h.claudeBridgeSession(context.Background(), j.Pane, j.RemoteControl.NativeSessionID, j.RemoteControl.Cwd, claudeTranscriptCursor{})
			if reason != "" || read.Transcript != "unreadable" || read.URL != "" {
				t.Fatalf("transcript accepted: reason=%q transcript=%q", reason, read.Transcript)
			}
		})
	}
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
	// Claude documents no native connection state: a connected Claude row is refused.
	if writeRemoteObservation(context.Background(), r, "claude", run, "connected", "", nil, nil) == nil { // guard:claude-never-connected-writer
		t.Fatal("a connected Claude row was written")
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
	j := launchedSelf(t, shadowFixtureJob(t, "claude"))
	self := os.Getpid()
	rec := func(bridge bool) map[string]any {
		r := map[string]any{"pid": self, "sessionId": j.RemoteControl.NativeSessionID, "procStart": procStart(t, self)}
		if bridge {
			r["bridgeSessionId"] = fixtureBridge
		}
		return r
	}
	for _, later := range []string{"malformed", "no-bridge", "no-transcript-entry"} {
		t.Run(later, func(t *testing.T) {
			h := claudeBridgeHost(t, []int{self}, map[int]any{self: rec(true)})
			transcript := writeBridgeTranscript(t, h.Home, j.RemoteControl.Cwd, j.RemoteControl.NativeSessionID, bridgeStatus(j.RemoteControl.NativeSessionID, "https://claude.ai/code/"+fixtureBridge))
			h.ShadowScope = newNativeEvidenceShadow(time.Now).scope(j)
			h.ClaudeLinks = &claudeLinkStore{}
			h.readClaudeBridge(j)
			if h.ClaudeLinks.fresh(j.DispatchKey, j.RemoteControl.NativeSessionID, time.Now()) == nil {
				t.Fatal("bound bridge did not yield a link")
			}
			path := filepath.Join(h.Home, ".claude", "sessions", fmt.Sprintf("%d.json", self))
			if later == "malformed" {
				writeFixture(t, path, "{not json")
			} else if later == "no-transcript-entry" {
				writeBridgeTranscript(t, h.Home, j.RemoteControl.Cwd, j.RemoteControl.NativeSessionID, `{"type":"user"}`)
				_ = transcript
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
