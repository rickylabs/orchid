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
// start time and native session id, decides the bridge; anything else is unknown.
func TestClaudeBridgeFromNativeSessionRecord(t *testing.T) {
	sid := syntheticRemoteRun(t).NativeSessionID
	self, parent := os.Getpid(), os.Getppid()
	rec := func(pid int, session, start string, bridge bool) map[string]any {
		r := map[string]any{"pid": pid, "sessionId": session, "procStart": start, "kind": "interactive"}
		if bridge {
			r["bridgeSessionId"] = "bridge_" + strings.Repeat("a", 24)
		}
		return r
	}
	start := procStart(t, self)
	for _, tc := range []struct {
		name            string
		pids            []int
		records         map[int]any
		connected, isOK bool
	}{
		{"bridge", []int{self}, map[int]any{self: rec(self, sid, start, true)}, true, true},
		{"no-bridge", []int{self}, map[int]any{self: rec(self, sid, start, false)}, false, true},
		{"other-session", []int{self}, map[int]any{self: rec(self, syntheticRemoteRun(t).NativeSessionID, start, true)}, false, false},
		{"reused-pid", []int{self}, map[int]any{self: rec(self, sid, start+"1", true)}, false, false},
		{"record-names-other-pid", []int{self}, map[int]any{self: rec(self+1, sid, start, true)}, false, false},
		{"no-record", []int{self}, map[int]any{}, false, false},
		{"garbage-record", []int{self}, map[int]any{self: "{not json"}, false, false},
		{"two-matches", []int{self, parent}, map[int]any{self: rec(self, sid, start, true), parent: rec(parent, sid, procStart(t, parent), true)}, false, false},
		{"other-pane-process-irrelevant", []int{parent, self}, map[int]any{self: rec(self, sid, start, true)}, true, true},
		{"no-processes", nil, map[int]any{self: rec(self, sid, start, true)}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := claudeBridgeHost(t, tc.pids, tc.records)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			connected, ok := h.claudeBridge(ctx, "w1:p1", sid)
			if connected != tc.connected || ok != tc.isOK {
				t.Fatalf("bridge=%v ok=%v, want %v/%v", connected, ok, tc.connected, tc.isOK)
			}
		})
	}
}

// The native snapshot feeds the existing shadow reducer: a known verdict is a
// live native connection fact; unknown evidence revokes it. Today's comparison
// at the Remote Control connection site gets a native verdict for Claude.
func TestObserveClaudeBridgeFeedsShadow(t *testing.T) {
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	j := shadowFixtureJob(t, "claude")
	self := os.Getpid()
	h := claudeBridgeHost(t, []int{self}, map[int]any{self: map[string]any{"pid": self, "sessionId": j.RemoteControl.NativeSessionID, "procStart": procStart(t, self), "bridgeSessionId": "bridge_x"}})
	h.ShadowScope = s.scope(j)
	h.observeClaudeBridge(context.Background(), j)
	if v := s.verdict(j, shadowConnection); v.Value != "connected" || v.Authority != "native" || !v.Live {
		t.Fatalf("native bridge not a live connection fact: %+v", v)
	}
	s.compare(j, shadowSiteConnection, "connected", []string{shadowInputHerdrAgent, shadowInputScreenFooter})
	s.compare(j, shadowSiteConnection, "unconfirmed", []string{shadowInputHerdrAgent, shadowInputScreenFooter})
	l := s.ledger(j)
	if len(l) != 2 || l[0].Agreement != "agree" || l[1].Agreement != "disagree" {
		t.Fatalf("connection parity not recorded: %+v", l)
	}
	// The record disappears: unknown, never the earlier connected verdict.
	_ = os.Remove(filepath.Join(h.Home, ".claude", "sessions", fmt.Sprintf("%d.json", self)))
	h.ShadowScope = s.scope(j)
	h.observeClaudeBridge(context.Background(), j)
	if v := s.verdict(j, shadowConnection); v.Value != "unknown" || v.Reason != "source-disconnected" {
		t.Fatalf("unknown evidence did not revoke the native source: %+v", v)
	}
	// A matching record without a bridge is a native not-connected fact.
	writeFixture(t, filepath.Join(h.Home, ".claude", "sessions", fmt.Sprintf("%d.json", self)),
		fmt.Sprintf(`{"pid":%d,"sessionId":%q,"procStart":%q}`, self, j.RemoteControl.NativeSessionID, procStart(t, self)))
	h.ShadowScope = s.scope(j)
	h.observeClaudeBridge(context.Background(), j)
	if v := s.verdict(j, shadowConnection); v.Value != "not-connected" || v.Authority != "native" {
		t.Fatalf("native no-bridge record not a not-connected fact: %+v", v)
	}
	// Only Claude jobs, only with a shadow scope.
	codex := shadowFixtureJob(t, "codex")
	h.ShadowScope = s.scope(codex)
	h.observeClaudeBridge(context.Background(), codex)
	if v := s.verdict(codex, shadowConnection); v.Value != "unknown" {
		t.Fatal("claude evidence published for a codex job")
	}
}

// The native path never reads the pane, and observation reads it before the
// proof's own budget starts.
func TestClaudeBridgeNeverReadsScreen(t *testing.T) {
	src := productionSources(t)["native_claude_bridge.go"]
	for _, banned := range []string{"visiblePromptScreen", ".Screen", "claudeRemoteConnected", `"read"`, "promptSnapshot("} {
		if strings.Contains(src, banned) {
			t.Fatalf("native Claude bridge path reads screen evidence: %s", banned)
		}
	}
	obs := productionSources(t)["remote_control_observation.go"]
	i, k := strings.Index(obs, "h.observeClaudeBridge(ctx, j) // guard:claude-bridge-before-check"), strings.Index(obs, "check, cancel := context.WithTimeout(ctx, 5*time.Second)")
	if i < 0 || k < 0 || i > k {
		t.Fatal("native read is not before the proof's own budget")
	}
}
