package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shadowReadoutRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "receipts")
	if err := os.Mkdir(root, 0700); err != nil || !privateReceiptRoot(root) {
		t.Fatal("private receipt root fixture unavailable")
	}
	return root
}

func TestShadowReadoutHoldsClosedCodesOnly(t *testing.T) {
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	j := shadowFixtureJob(t, "codex")
	j.RemoteControl.Cwd, j.RemoteControl.Name = "/canary/private/path", "canary prompt words"
	j.Pane, j.Workspace, j.Host, j.Repo = "canary-pane", "canary-workspace", "canary-host.example", "canary/repo"
	p := s.scope(j).open(shadowCodexCanonical, j.RemoteControl.NativeSessionID)
	p.connection(true)
	p.turn(j.RemoteControl.NativeSessionID, "canary-turn-id", "inProgress")
	s.compare(j, shadowSiteConnection, "connected", []string{shadowInputHerdrAgent, shadowInputScreenFooter, shadowInputCodexStatus})
	s.compare(j, shadowSiteRemoteHook, "canary-today", []string{"canary-input"})
	s.compare(j, "canary-site", "pass", nil)
	p.turn(j.RemoteControl.NativeSessionID, "canary-turn-id", "bogus-canary-status") // malformed: counted, closed reason
	r := s.readout(shadowT0)
	if r.SchemaVersion != 1 || len(r.Runs) != 1 {
		t.Fatalf("readout envelope: %+v", r)
	}
	run := r.Runs[0]
	if run.Issue != j.Issue || run.Vendor != "codex" || len(run.Comparisons) != 2 {
		t.Fatalf("run row: %+v", run)
	}
	if run.Native.Accepted["connection"] != 1 || run.Native.Accepted["activity"] != 1 || run.Native.Rejected["row-invalid"] != 1 {
		t.Fatalf("native observation counts missing: %+v", run.Native)
	}
	if c := run.Comparisons[0]; c.Proposed[0].Authority != "native" || c.Proposed[0].Value != "connected" || !c.ScreenDerived {
		t.Fatalf("native proposal not exported beside screen provenance: %+v", c)
	}
	if c := run.Comparisons[1]; c.Today != shadowInputOther || len(c.Provenance) != 1 || c.Provenance[0] != shadowInputOther {
		t.Fatalf("unlisted value was not closed: %+v", c)
	}
	b, _ := json.Marshal(r)
	for _, canary := range []string{"canary", j.RemoteControl.NativeSessionID, j.DispatchKey, "/"} {
		if canary == "/" {
			if strings.Contains(strings.ReplaceAll(string(b), `/`, "/"), "/") {
				t.Fatal("path-like value entered readout")
			}
			continue
		}
		if strings.Contains(string(b), canary) {
			t.Fatalf("private value entered readout: %s", canary)
		}
	}
	var nilShadow *nativeEvidenceShadow
	if empty := nilShadow.readout(shadowT0); empty.SchemaVersion != 1 || len(empty.Runs) != 0 || empty.Runs == nil {
		t.Fatalf("nil shadow readout not an empty closed envelope: %+v", empty)
	}
}

func TestShadowReadoutWritesOnlyToPrivateRoot(t *testing.T) {
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	j := shadowFixtureJob(t, "claude")
	s.compare(j, shadowSiteConnection, "connected", []string{shadowInputHerdrAgent, shadowInputScreenFooter})
	root := shadowReadoutRoot(t)
	if err := writeShadowReadout(root, nil, s.readout(shadowT0)); err != nil {
		t.Fatalf("private readout refused: %v", err)
	}
	path := filepath.Join(root, shadowReadoutFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("readout is not a private regular file")
	}
	var got shadowReadout
	b, _ := os.ReadFile(path)
	if strictJSON(b, &got) != nil || len(got.Runs) != 1 || got.Runs[0].Comparisons[0].Agreement != "shadow-unknown" {
		t.Fatalf("readout content: %s", b)
	}

	open := filepath.Join(t.TempDir(), "open")
	_ = os.Mkdir(open, 0700)
	_ = os.Chmod(open, 0755) // explicit: independent of the caller's umask
	target := shadowReadoutRoot(t)
	link := filepath.Join(t.TempDir(), "link")
	_ = os.Symlink(target, link)
	for name, bad := range map[string]string{"relative": "receipts", "missing": filepath.Join(t.TempDir(), "absent"), "group-readable": open, "symlink": link, "empty": ""} {
		if writeShadowReadout(bad, nil, s.readout(shadowT0)) == nil {
			t.Fatalf("%s root accepted", name)
		}
		if _, err := os.Stat(filepath.Join(bad, shadowReadoutFile)); bad != "" && err == nil {
			t.Fatalf("%s root received a readout", name)
		}
	}
	if _, err := os.Stat(filepath.Join(target, shadowReadoutFile)); err == nil {
		t.Fatal("readout followed a symlinked root")
	}
}

func TestShadowReadoutLoopIsOffPathAndWritesOnChange(t *testing.T) {
	root := shadowReadoutRoot(t)
	path := filepath.Join(root, shadowReadoutFile)
	ticks := make(chan time.Time)
	now := shadowT0
	c := &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: root}}, shadow: newNativeEvidenceShadow(func() time.Time { return now })}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.shadowReadoutLoop(ctx, ticks, shadowT0); close(done) }()
	send := func(ch chan time.Time, at time.Time) {
		t.Helper()
		select {
		case ch <- at:
		case <-time.After(5 * time.Second):
			t.Fatal("readout loop not receiving ticks")
		}
	}
	tick := func() { send(ticks, now); send(ticks, now) } // the second send proves the first iteration finished

	tick()
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("startup readout not written")
	}
	now = now.Add(time.Minute)
	tick()
	if again, _ := os.ReadFile(path); string(again) != string(first) {
		t.Fatal("unchanged ledger rewritten")
	}
	watched := shadowFixtureJob(t, "codex")
	c.shadow.compare(watched, shadowSiteRemoteHook, "pass", []string{shadowInputHerdrAgent, shadowInputScreenFooter})
	tick()
	var got shadowReadout
	b, _ := os.ReadFile(path)
	if strictJSON(b, &got) != nil || len(got.Runs) != 1 {
		t.Fatalf("changed ledger not written: %s", b)
	}
	// A new comparison on an existing binding is a change too.
	c.shadow.compare(watched, shadowSiteRemoteHook, "refuse", []string{shadowInputHerdrAgent})
	tick()
	b, _ = os.ReadFile(path)
	if strictJSON(b, &got) != nil || len(got.Runs) != 1 || len(got.Runs[0].Comparisons) != 2 {
		t.Fatalf("new comparison on an existing run not written: %s", b)
	}
	// A failing write never stops the loop or reaches a caller.
	_ = os.Chmod(root, 0755)
	other := shadowFixtureJob(t, "codex")
	other.DispatchKey, other.Issue = strings.Repeat("b", 64), 8
	c.shadow.compare(other, shadowSiteRemoteHook, "refuse", nil)
	tick()
	_ = os.Chmod(root, 0700)
	tick()
	b, _ = os.ReadFile(path)
	if strictJSON(b, &got) != nil || len(got.Runs) != 2 {
		t.Fatalf("loop did not recover after refused root: %s", b)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("readout loop ignored cancellation")
	}

	// No shadow or no private root: the loop does nothing and returns.
	for _, off := range []*Coord{{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: shadowReadoutRoot(t)}}}, {cfg: &Config{}, shadow: newNativeEvidenceShadow(time.Now)}} {
		returned := make(chan struct{})
		go func() { off.shadowReadoutLoop(context.Background(), make(chan time.Time), shadowT0); close(returned) }()
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Fatal("disabled readout loop kept running")
		}
		if off.cfg.Matrix.ReceiptRoot != "" {
			if _, err := os.Stat(filepath.Join(off.cfg.Matrix.ReceiptRoot, shadowReadoutFile)); err == nil {
				t.Fatal("disabled readout wrote a file")
			}
		}
	}
	// A panicking shadow clock is contained inside the loop.
	broken := &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: shadowReadoutRoot(t)}}, shadow: newNativeEvidenceShadow(func() time.Time { panic("clock") })}
	bctx, bcancel := context.WithCancel(context.Background())
	bticks := make(chan time.Time)
	bdone := make(chan struct{})
	go func() { broken.shadowReadoutLoop(bctx, bticks, shadowT0); close(bdone) }()
	send(bticks, shadowT0)
	send(bticks, shadowT0)
	bcancel()
	<-bdone
}
