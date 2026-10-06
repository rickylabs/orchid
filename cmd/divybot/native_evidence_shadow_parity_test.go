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

func parityCount(st shadowParityState, k shadowParityKey) int64 {
	for _, row := range st.Counts {
		if (shadowParityKey{row.Site, row.Vendor, row.Today, row.Native, row.Agreement, row.Reason}) == k {
			return row.Count
		}
	}
	return 0
}

var hookUnknown = shadowParityKey{Site: shadowSiteRemoteHook, Vendor: "codex", Today: "pass", Native: "unknown", Agreement: "shadow-unknown", Reason: "codex-tui-attachment-unproven"}

// Every comparison counts in its closed class, past the per-job ledger bound,
// and nothing private or unlisted enters the counts.
func TestShadowParityCountsClosedOutcomesBeyondTheLedger(t *testing.T) {
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	j := shadowFixtureJob(t, "codex")
	j.RemoteControl.Cwd, j.Pane, j.Host = "/canary/private/path", "canary-pane", "canary-host.example"
	for i := 0; i < shadowLedgerLimit+8; i++ {
		s.compare(j, shadowSiteRemoteHook, "pass", []string{shadowInputHerdrAgent})
	}
	s.compare(j, shadowSiteRemoteHook, "canary-today", nil)
	s.compareNative(j, shadowSiteGoalReadiness, "ready", nil, readinessVerdict("ready", ""))
	s.compareNative(j, shadowSiteGoalReadiness, "ready", nil, readinessVerdict("not-ready", "native-turn-active"))
	st := s.parityState()
	if got := parityCount(st, hookUnknown); got != shadowLedgerLimit+8 {
		t.Fatalf("hook count %d, want %d (the ledger keeps only %d)", got, shadowLedgerLimit+8, len(s.ledger(j)))
	}
	if got := parityCount(st, shadowParityKey{shadowSiteRemoteHook, "codex", shadowInputOther, "unknown", "shadow-unknown", "codex-tui-attachment-unproven"}); got != 1 {
		t.Fatalf("unlisted today not closed: %+v", st.Counts)
	}
	if parityCount(st, shadowParityKey{shadowSiteGoalReadiness, "codex", "ready", "ready", "agree", ""}) != 1 ||
		parityCount(st, shadowParityKey{shadowSiteGoalReadiness, "codex", "ready", "not-ready", "disagree", "native-turn-active"}) != 1 {
		t.Fatalf("readiness agreement classes: %+v", st.Counts)
	}
	b, _ := json.Marshal(s.readout(shadowT0))
	for _, canary := range []string{"canary", j.RemoteControl.NativeSessionID, j.DispatchKey, "/canary"} {
		if strings.Contains(string(b), canary) {
			t.Fatalf("private value entered the counts: %s", canary)
		}
	}
	if r := s.readout(shadowT0); len(r.Parity) != len(st.Counts) {
		t.Fatal("readout does not carry the counts")
	}
}

// A job whose scope is gone (evicted, or ended) keeps its counted outcomes.
func TestShadowParitySurvivesTheJobsScope(t *testing.T) {
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	first := shadowFixtureJob(t, "codex")
	for i := 0; i < 3; i++ {
		s.compare(first, shadowSiteRemoteHook, "pass", nil)
	}
	for i := 0; i < shadowMaxScopes; i++ {
		other := shadowFixtureJob(t, "claude")
		other.DispatchKey = fmt.Sprintf("%064x", i+1)
		s.compare(other, shadowSiteConnection, "connected", nil)
	}
	if len(s.ledger(first)) != 0 {
		t.Fatal("control: the first job's scope was not evicted")
	}
	if got := parityCount(s.parityState(), hookUnknown); got != 3 {
		t.Fatalf("evicted job's counts %d, want 3", got)
	}
}

func runParityLoop(t *testing.T, c *Coord, started time.Time) (tick func(), stop func()) {
	t.Helper()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.shadowReadoutLoop(ctx, ticks, started); close(done) }()
	send := func() {
		select {
		case ticks <- started:
		case <-time.After(5 * time.Second):
			t.Fatal("readout loop not receiving ticks")
		}
	}
	return func() { send(); send() }, func() { cancel(); <-done }
}

// The counts accumulate across a restart: the stored counts are read back
// before anything is written, and the since time is kept.
func TestShadowParitySurvivesARestart(t *testing.T) {
	root := shadowReadoutRoot(t)
	j := shadowFixtureJob(t, "codex")
	first := &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: root}}, shadow: newNativeEvidenceShadow(func() time.Time { return shadowT0 })}
	tick, stop := runParityLoop(t, first, shadowT0)
	tick()
	for i := 0; i < 3; i++ {
		first.shadow.compare(j, shadowSiteRemoteHook, "pass", nil)
	}
	tick()
	stop()
	later := shadowT0.Add(time.Hour)
	second := &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: root}}, shadow: newNativeEvidenceShadow(func() time.Time { return later })}
	// Counted before the stored counts are read back: both survive.
	second.shadow.compare(j, shadowSiteRemoteHook, "pass", nil)
	second.shadow.compare(j, shadowSiteRemoteHook, "pass", nil)
	tick, stop = runParityLoop(t, second, later)
	tick()
	stop()
	raw, err := os.ReadFile(filepath.Join(root, shadowParityFile))
	if err != nil {
		t.Fatal("no stored counts")
	}
	var st shadowParityState
	if strictJSON(raw, &st) != nil || parityCount(st, hookUnknown) != 5 || st.Since != shadowT0.Format(time.RFC3339Nano) {
		t.Fatalf("stored counts after restart: %s", raw)
	}
	var r shadowReadout
	raw, _ = os.ReadFile(filepath.Join(root, shadowReadoutFile))
	if strictJSON(raw, &r) != nil || r.ParitySince != st.Since || len(r.Parity) != 1 || r.Parity[0].Count != 5 {
		t.Fatalf("readout counts after restart: %s", raw)
	}
}

// Stored counts that cannot be read are never overwritten; invalid ones restart
// the count, and the new since shows it.
func TestShadowParityNeverOverwritesUnreadCounts(t *testing.T) {
	j := shadowFixtureJob(t, "codex")
	root := shadowReadoutRoot(t)
	path := filepath.Join(root, shadowParityFile)
	stored := []byte(`{"schemaVersion":1,"since":"2025-12-01T00:00:00Z","counts":[{"site":"remote-hook","vendor":"codex","today":"pass","native":"unknown","agreement":"shadow-unknown","reason":"codex-tui-attachment-unproven","count":7}]}`)
	if os.WriteFile(path, stored, 0644) != nil { // not owner-only: unreadable as private state
		t.Fatal("fixture")
	}
	c := &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: root}}, shadow: newNativeEvidenceShadow(func() time.Time { return shadowT0 })}
	c.shadow.compare(j, shadowSiteRemoteHook, "pass", nil)
	tick, stop := runParityLoop(t, c, shadowT0)
	tick()
	stop()
	if raw, _ := os.ReadFile(path); string(raw) != string(stored) {
		t.Fatal("unread stored counts were overwritten")
	}
	if _, err := os.Stat(filepath.Join(root, shadowReadoutFile)); err != nil {
		t.Fatal("the readout itself must still be written")
	}

	invalid := shadowReadoutRoot(t)
	if os.WriteFile(filepath.Join(invalid, shadowParityFile), []byte(strings.Replace(string(stored), "remote-hook", "canary-site", 1)), 0600) != nil {
		t.Fatal("fixture")
	}
	c = &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: invalid}}, shadow: newNativeEvidenceShadow(func() time.Time { return shadowT0 })}
	c.shadow.compare(j, shadowSiteRemoteHook, "pass", nil)
	tick, stop = runParityLoop(t, c, shadowT0)
	tick()
	stop()
	var st shadowParityState
	raw, _ := os.ReadFile(filepath.Join(invalid, shadowParityFile))
	if strictJSON(raw, &st) != nil || st.Since != shadowT0.Format(time.RFC3339Nano) || parityCount(st, hookUnknown) != 1 {
		t.Fatalf("invalid stored counts did not restart the count: %s", raw)
	}
}

func TestShadowParityRestoreIsStrict(t *testing.T) {
	good := `{"schemaVersion":1,"since":"2025-12-01T00:00:00Z","counts":[{"site":"remote-hook","vendor":"codex","today":"pass","native":"unknown","agreement":"shadow-unknown","reason":"codex-tui-attachment-unproven","count":7}]}`
	if counts, _, err := restoredParity([]byte(good)); err != nil || counts[hookUnknown] != 7 {
		t.Fatalf("control: valid counts refused: %v", err)
	}
	for name, bad := range map[string]string{
		"schema":    strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":2`, 1),
		"since":     strings.Replace(good, `2025-12-01T00:00:00Z`, `yesterday`, 1),
		"vendor":    strings.Replace(good, `"codex"`, `"canary"`, 1),
		"value":     strings.Replace(good, `"native":"unknown"`, `"native":"maybe"`, 1),
		"reason":    strings.Replace(good, `"codex-tui-attachment-unproven"`, `"canary reason"`, 1),
		"zero":      strings.Replace(good, `"count":7`, `"count":0`, 1),
		"huge":      strings.Replace(good, `"count":7`, `"count":2199023255553`, 1),
		"duplicate": strings.Replace(good, `]}`, `,{"site":"remote-hook","vendor":"codex","today":"pass","native":"unknown","agreement":"shadow-unknown","reason":"codex-tui-attachment-unproven","count":1}]}`, 1),
		"extra key": strings.Replace(good, `"count":7`, `"count":7,"issue":42`, 1),
	} {
		if _, _, err := restoredParity([]byte(bad)); err == nil {
			t.Fatalf("%s: invalid stored counts accepted", name)
		}
	}
}

func TestShadowParityCountIsBounded(t *testing.T) {
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	j := shadowFixtureJob(t, "codex")
	s.parity = map[shadowParityKey]int64{hookUnknown: shadowParityMaxCount}
	s.compare(j, shadowSiteRemoteHook, "pass", nil)
	if got := parityCount(s.parityState(), hookUnknown); got != shadowParityMaxCount {
		t.Fatalf("count passed its bound: %d", got)
	}
}

// REVIEW-o92 P2: a receipt root missing at the first load says nothing about the
// stored counts. Loading stays pending, and when the root returns its counts are
// read back and kept, never replaced.
func TestShadowParityWaitsForAMissingRoot(t *testing.T) {
	j := shadowFixtureJob(t, "codex")
	root := shadowReadoutRoot(t)
	stored := `{"schemaVersion":1,"since":"2025-12-01T00:00:00Z","counts":[{"site":"remote-hook","vendor":"codex","today":"pass","native":"unknown","agreement":"shadow-unknown","reason":"codex-tui-attachment-unproven","count":7}]}`
	if os.WriteFile(filepath.Join(root, shadowParityFile), []byte(stored), 0600) != nil {
		t.Fatal("fixture")
	}
	away := root + "-away"
	if os.Rename(root, away) != nil {
		t.Fatal("fixture")
	}
	c := &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: root}}, shadow: newNativeEvidenceShadow(func() time.Time { return shadowT0 })}
	c.shadow.compare(j, shadowSiteRemoteHook, "pass", nil)
	tick, stop := runParityLoop(t, c, shadowT0)
	tick() // the root is missing
	if os.Rename(away, root) != nil {
		t.Fatal("fixture")
	}
	tick() // the root is back
	stop()
	var st shadowParityState
	raw, _ := os.ReadFile(filepath.Join(root, shadowParityFile))
	if strictJSON(raw, &st) != nil || parityCount(st, hookUnknown) != 8 || st.Since != "2025-12-01T00:00:00Z" {
		t.Fatalf("stored counts were not read back after the root returned: %s", raw)
	}
}

// REVIEW-o92 P2: a stored file that was read safely but is not valid UTF-8 is
// invalid state: the count restarts visibly and is persisted again.
func TestShadowParityRestartsInvalidEncoding(t *testing.T) {
	for name, content := range map[string][]byte{"invalid-utf8": {0xff}, "malformed-json": []byte("{")} {
		t.Run(name, func(t *testing.T) {
			j := shadowFixtureJob(t, "codex")
			root := shadowReadoutRoot(t)
			if os.WriteFile(filepath.Join(root, shadowParityFile), content, 0600) != nil {
				t.Fatal("fixture")
			}
			c := &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: root}}, shadow: newNativeEvidenceShadow(func() time.Time { return shadowT0 })}
			c.shadow.compare(j, shadowSiteRemoteHook, "pass", nil)
			tick, stop := runParityLoop(t, c, shadowT0)
			tick()
			stop()
			var st shadowParityState
			raw, _ := os.ReadFile(filepath.Join(root, shadowParityFile))
			if strictJSON(raw, &st) != nil || st.Since != shadowT0.Format(time.RFC3339Nano) || parityCount(st, hookUnknown) != 1 {
				t.Fatalf("invalid stored counts did not restart visibly: %q", raw)
			}
		})
	}
}

// The shared safe reader split keeps the owner-native reader's content rule:
// invalid UTF-8 is still refused there.
func TestOwnerNativePrivateReadStillRefusesInvalidUTF8(t *testing.T) {
	root := shadowReadoutRoot(t)
	path := filepath.Join(root, "binding.json")
	if os.WriteFile(path, []byte{0xff}, 0600) != nil {
		t.Fatal("fixture")
	}
	if _, err := ownerNativePrivateRead(path, os.Getuid()); err == nil {
		t.Fatal("owner-native reader accepted invalid UTF-8")
	}
	if raw, err := privateFileBytes(path, os.Getuid(), ownerNativeOpen); err != nil || len(raw) != 1 {
		t.Fatalf("control: the safe reader refused a safe file: %v", err)
	}
}
