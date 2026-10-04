package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

var shadowT0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func shadowFixtureJob(t *testing.T, agent string) *Job {
	t.Helper()
	run := syntheticRemoteRun(t)
	return &Job{Issue: 7, Agent: agent, Label: "harness", Host: "fixture-host", Pane: "w1:p1", Workspace: "w1",
		DispatchKey: strings.Repeat("a", 64), RemoteControl: run}
}

func shadowFixtureReducer(t *testing.T) (*shadowReducer, string, uint64) {
	t.Helper()
	j := shadowFixtureJob(t, "codex")
	b, ok := shadowBindingFor(j)
	if !ok {
		t.Fatal("eligible binding refused")
	}
	r := newShadowReducer(b, 1)
	return r, j.RemoteControl.NativeSessionID, r.registerSource(shadowCodexCanonical)
}

func shadowRowAt(epoch, seq uint64, fact shadowFact, value, thread, turn string, at time.Time) shadowRow {
	return shadowRow{Source: shadowCodexCanonical, SourceEpoch: epoch, Seq: seq, Fact: fact, Value: value, Thread: thread, Turn: turn, ObservedAt: at}
}

func mustIngest(t *testing.T, r *shadowReducer, row shadowRow) {
	t.Helper()
	if ok, reason := r.ingest(row, row.ObservedAt); !ok {
		t.Fatalf("valid native row refused: %s", reason)
	}
}

func TestShadowOldStopCannotOverwriteNewWorking(t *testing.T) {
	r, root, e := shadowFixtureReducer(t)
	mustIngest(t, r, shadowRowAt(e, 1, shadowActivity, "working", root, "turn-1", shadowT0))
	mustIngest(t, r, shadowRowAt(e, 2, shadowActivity, "working", root, "turn-2", shadowT0.Add(time.Second)))
	// Delayed terminal of the older turn arrives with a HIGHER ingest sequence.
	mustIngest(t, r, shadowRowAt(e, 3, shadowTurnOutcome, "completed", root, "turn-1", shadowT0.Add(2*time.Second)))
	now := shadowT0.Add(3 * time.Second)
	if v := r.decide(shadowActivity, now); v.Value != "working" || v.Authority != "native" {
		t.Fatalf("old Stop overwrote new working: %+v", v)
	}
	if v := r.decide(shadowTurnOutcome, now); v.Value != "unknown" {
		t.Fatalf("older turn outcome credited to current turn: %+v", v)
	}
	// An unassociated terminal cannot clear the current turn either.
	mustIngest(t, r, shadowRowAt(e, 4, shadowTurnOutcome, "completed", root, "", now))
	if v := r.decide(shadowActivity, now); v.Value != "working" {
		t.Fatalf("unassociated Stop cleared current turn: %+v", v)
	}
	if v := r.decide(shadowTurnOutcome, now); v.Value != "unknown" {
		t.Fatalf("unassociated Stop became outcome: %+v", v)
	}
	// Exact terminal for the current turn is native turn outcome; never readiness.
	mustIngest(t, r, shadowRowAt(e, 5, shadowTurnOutcome, "interrupted", root, "turn-2", now))
	if v := r.decide(shadowTurnOutcome, now); v.Value != "interrupted" || v.Live {
		t.Fatalf("exact turn outcome lost or granted live authority: %+v", v)
	}
	if v := r.decide(shadowActivity, now); v.Value == "idle" || v.Value == "ready" || v.Value == "working" {
		t.Fatalf("terminal turn manufactured readiness or stayed working: %+v", v)
	}
}

func TestShadowRejectsForeignInvocationSourceAndScope(t *testing.T) {
	r, root, e := shadowFixtureReducer(t)
	if ok, reason := r.ingest(shadowRowAt(e+7, 1, shadowConnection, "connected", root, "", shadowT0), shadowT0); ok || reason != "source-epoch-foreign" {
		t.Fatalf("foreign source epoch accepted: %v %s", ok, reason)
	}
	row := shadowRowAt(e, 1, shadowConnection, "connected", root, "", shadowT0)
	row.Source = "herdr-screen"
	if ok, reason := r.ingest(row, shadowT0); ok || reason != "source-unregistered" {
		t.Fatalf("screen/unregistered source accepted as native: %v %s", ok, reason)
	}
	if ok, reason := r.ingest(shadowRowAt(e, 1, shadowConnection, "connected", "foreign-thread", "", shadowT0), shadowT0); ok || reason != "scope-foreign" {
		t.Fatalf("foreign native scope accepted: %v %s", ok, reason)
	}
	if ok, reason := r.ingest(shadowRowAt(e, 1, shadowAttachment, "attached", root, "", shadowT0), shadowT0); ok || reason != "capability-unsupported" {
		t.Fatalf("source claimed a fact it cannot prove: %v %s", ok, reason)
	}
	// A newly registered epoch revokes the old epoch's live facts and rows.
	r2, root2, e2 := shadowFixtureReducer(t)
	mustIngest(t, r2, shadowRowAt(e2, 1, shadowConnection, "connected", root2, "", shadowT0))
	e3 := r2.registerSource(shadowCodexCanonical)
	if v := r2.decide(shadowConnection, shadowT0.Add(time.Second)); v.Value != "unknown" {
		t.Fatalf("old source epoch survived re-registration: %+v", v)
	}
	if ok, _ := r2.ingest(shadowRowAt(e2, 2, shadowConnection, "connected", root2, "", shadowT0), shadowT0); ok {
		t.Fatal("delayed row from revoked epoch accepted")
	}
	mustIngest(t, r2, shadowRowAt(e3, 1, shadowConnection, "connected", root2, "", shadowT0))

	// Invocation binding: any change to dispatch/native/placement/process is a new invocation.
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	j := shadowFixtureJob(t, "codex")
	j.RemoteControl.TUIProcess = &remoteTUIProcess{PID: 41, Fingerprint: "fixture-start"}
	scope := s.scope(j)
	p := scope.open(shadowCodexCanonical, j.RemoteControl.NativeSessionID)
	p.connection(true)
	for name, change := range map[string]func(*Job){
		"pane":    func(j *Job) { j.Pane = "w1:p2" },
		"native":  func(j *Job) { j.RemoteControl.NativeSessionID = strings.Repeat("b", 32) },
		"process": func(j *Job) { j.RemoteControl.TUIProcess = &remoteTUIProcess{PID: 41, Fingerprint: "reused-pid"} },
		"host":    func(j *Job) { j.Host = "other-host" },
	} {
		next := *j
		run := *j.RemoteControl
		next.RemoteControl = &run
		change(&next)
		again := s.scope(&next)
		if again == nil || again.invocation == scope.invocation {
			t.Fatalf("%s change reused the invocation epoch", name)
		}
		if v := s.verdict(&next, shadowConnection); v.Value != "unknown" {
			t.Fatalf("%s change inherited prior native fact: %+v", name, v)
		}
		// The new invocation's first publisher reuses epoch/sequence numbers.
		fresh := again.open(shadowCodexCanonical, next.RemoteControl.NativeSessionID)
		fresh.connection(false)
		p.connection(true) // delayed evidence from the prior invocation
		if v := s.verdict(&next, shadowConnection); v.Value != "not-connected" {
			t.Fatalf("%s: prior invocation publisher wrote into new binding: %+v", name, v)
		}
		fresh.close(nil)
	}
}

func TestShadowSequenceDuplicateConflictGapAndRegression(t *testing.T) {
	r, root, e := shadowFixtureReducer(t)
	first := shadowRowAt(e, 1, shadowConnection, "connected", root, "", shadowT0)
	mustIngest(t, r, first)
	if ok, reason := r.ingest(first, shadowT0); ok || reason != "duplicate" {
		t.Fatalf("identical duplicate not idempotent: %v %s", ok, reason)
	}
	if v := r.decide(shadowConnection, shadowT0); v.Value != "connected" {
		t.Fatal("identical duplicate invalidated the source")
	}
	conflict := first
	conflict.Value = "not-connected"
	if ok, reason := r.ingest(conflict, shadowT0); ok || reason != "sequence-conflict" {
		t.Fatalf("same sequence with different payload accepted: %v %s", ok, reason)
	}
	if v := r.decide(shadowConnection, shadowT0); v.Value != "unknown" || v.Reason != "sequence-conflict" {
		t.Fatalf("sequence conflict did not fail closed: %+v", v)
	}

	r, root, e = shadowFixtureReducer(t)
	mustIngest(t, r, shadowRowAt(e, 1, shadowConnection, "connected", root, "", shadowT0))
	if ok, reason := r.ingest(shadowRowAt(e, 3, shadowConnection, "connected", root, "", shadowT0), shadowT0); ok || reason != "sequence-gap" {
		t.Fatalf("skipped sequence accepted: %v %s", ok, reason)
	}
	if v := r.decide(shadowConnection, shadowT0); v.Value != "unknown" || v.Reason != "sequence-gap" {
		t.Fatalf("gap left a live positive: %+v", v)
	}
	if ok, _ := r.ingest(shadowRowAt(e, 2, shadowConnection, "connected", root, "", shadowT0), shadowT0); ok {
		t.Fatal("unavailable source recovered without a new registered epoch")
	}

	r, root, e = shadowFixtureReducer(t)
	if ok, reason := r.ingest(shadowRowAt(e, 0, shadowConnection, "connected", root, "", shadowT0), shadowT0); ok || reason != "sequence-gap" {
		t.Fatalf("reset/zero sequence accepted: %v %s", ok, reason)
	}
	r, root, e = shadowFixtureReducer(t)
	mustIngest(t, r, shadowRowAt(e, 1, shadowConnection, "connected", root, "", shadowT0))
	bad := shadowRowAt(e, 2, shadowConnection, "maybe", root, "", shadowT0)
	if ok, reason := r.ingest(bad, shadowT0); ok || reason != "row-invalid" {
		t.Fatalf("malformed value accepted: %v %s", ok, reason)
	}
	if v := r.decide(shadowConnection, shadowT0); v.Value != "unknown" {
		t.Fatalf("malformed row left source live: %+v", v)
	}
}

func TestShadowFreshnessExpiryFutureAndNoHeartbeatRefresh(t *testing.T) {
	r, root, e := shadowFixtureReducer(t)
	mustIngest(t, r, shadowRowAt(e, 1, shadowActivity, "working", root, "turn-1", shadowT0))
	mustIngest(t, r, shadowRowAt(e, 2, shadowConnection, "connected", root, "", shadowT0))
	r.heartbeat(shadowCodexCanonical, e, shadowT0.Add(25*time.Second))
	at := shadowT0.Add(shadowLiveWindow + time.Millisecond)
	for _, fact := range []shadowFact{shadowActivity, shadowConnection} {
		if v := r.decide(fact, at); v.Value != "unknown" || v.Reason != "native-expired" {
			t.Fatalf("%s survived its window via heartbeat/no refresh: %+v", fact, v)
		}
	}
	if v := r.decide(shadowConnection, shadowT0.Add(shadowLiveWindow)); v.Value != "connected" {
		t.Fatalf("fact expired before its window: %+v", v)
	}
	if v := r.decide(shadowConnection, shadowT0.Add(-time.Second)); v.Value != "unknown" {
		t.Fatalf("decision clock before observation credited a fact: %+v", v)
	}
	// Receipt does not refresh an old observation, and a future one is invalid.
	r, root, e = shadowFixtureReducer(t)
	old := shadowRowAt(e, 1, shadowConnection, "connected", root, "", shadowT0)
	if ok, _ := r.ingest(old, shadowT0.Add(40*time.Second)); !ok {
		t.Fatal("late but valid row refused")
	}
	if v := r.decide(shadowConnection, shadowT0.Add(40*time.Second)); v.Value != "unknown" {
		t.Fatalf("late receipt refreshed an old observation: %+v", v)
	}
	r, root, e = shadowFixtureReducer(t)
	if ok, reason := r.ingest(shadowRowAt(e, 1, shadowConnection, "connected", root, "", shadowT0.Add(time.Second)), shadowT0); ok || reason != "row-invalid" {
		t.Fatalf("future observation accepted: %v %s", ok, reason)
	}
}

func TestShadowDisconnectAndStickyContradiction(t *testing.T) {
	r, root, e := shadowFixtureReducer(t)
	mustIngest(t, r, shadowRowAt(e, 1, shadowConnection, "connected", root, "", shadowT0))
	r.close(shadowCodexCanonical, e, true)
	if v := r.decide(shadowConnection, shadowT0.Add(time.Second)); v.Value != "connected" {
		t.Fatalf("completed snapshot read lost its bounded window: %+v", v)
	}
	if ok, _ := r.ingest(shadowRowAt(e, 2, shadowConnection, "connected", root, "", shadowT0), shadowT0); ok {
		t.Fatal("closed source accepted more rows")
	}
	r, root, e = shadowFixtureReducer(t)
	mustIngest(t, r, shadowRowAt(e, 1, shadowActivity, "working", root, "turn-1", shadowT0))
	r.close(shadowCodexCanonical, e, false)
	if v := r.decide(shadowActivity, shadowT0.Add(time.Second)); v.Value != "unknown" || v.Reason != "source-disconnected" {
		t.Fatalf("disconnect left a live positive: %+v", v)
	}

	r, root, e = shadowFixtureReducer(t)
	mustIngest(t, r, shadowRowAt(e, 1, shadowActivity, "working", root, "turn-1", shadowT0))
	mustIngest(t, r, shadowRowAt(e, 2, shadowTurnOutcome, "completed", root, "turn-1", shadowT0))
	mustIngest(t, r, shadowRowAt(e, 3, shadowActivity, "working", root, "turn-1", shadowT0)) // restarted after terminal
	for _, fact := range []shadowFact{shadowActivity, shadowTurnOutcome} {
		if v := r.decide(fact, shadowT0); v.Value != "unknown" || v.Reason != "conflict-unreconciled" {
			t.Fatalf("%s contradiction not surfaced: %+v", fact, v)
		}
	}
	mustIngest(t, r, shadowRowAt(e, 4, shadowActivity, "working", root, "turn-2", shadowT0))
	mustIngest(t, r, shadowRowAt(e, 5, shadowTurnOutcome, "completed", root, "turn-2", shadowT0))
	if v := r.decide(shadowTurnOutcome, shadowT0); v.Value != "unknown" || v.Reason != "conflict-unreconciled" {
		t.Fatalf("later matching notices erased a sticky contradiction: %+v", v)
	}
	// Re-registration cannot launder a contradiction either.
	e2 := r.registerSource(shadowCodexCanonical)
	mustIngest(t, r, shadowRowAt(e2, 1, shadowActivity, "working", root, "turn-3", shadowT0))
	if v := r.decide(shadowActivity, shadowT0); v.Reason != "conflict-unreconciled" {
		t.Fatalf("new epoch erased contradiction: %+v", v)
	}
}

func TestShadowVendorCapabilitiesStayUnknown(t *testing.T) {
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	claude := shadowFixtureJob(t, "claude")
	if s.scope(claude) == nil {
		t.Fatal("claude Remote Control run not observed")
	}
	if p := s.scope(claude).open(shadowCodexCanonical, claude.RemoteControl.NativeSessionID); p != nil {
		t.Fatal("codex canonical source registered for a claude invocation")
	}
	s.compare(claude, shadowSiteConnection, "connected", []string{shadowInputHerdrAgent, shadowInputScreenFooter})
	got := s.ledger(claude)
	if len(got) != 1 {
		t.Fatalf("comparison not recorded: %d", len(got))
	}
	c := got[0]
	if c.Today != "connected" || !c.ScreenDerived || c.Agreement != "shadow-unknown" || len(c.Proposed) == 0 || c.Proposed[0].Value != "unknown" || c.Proposed[0].Reason != "native-current-connection-unsupported" {
		t.Fatalf("claude screen footer credited as native connection: %+v", c)
	}
	codex := shadowFixtureJob(t, "codex")
	s.compare(codex, shadowSiteRemoteHook, "pass", []string{shadowInputHerdrAgent, shadowInputScreenFooter})
	h := s.ledger(codex)
	if len(h) != 1 || h[0].Agreement != "shadow-unknown" || h[0].Proposed[0].Fact != shadowAttachment || h[0].Proposed[0].Reason != "codex-tui-attachment-unproven" {
		t.Fatalf("footer attachment credited as native: %+v", h)
	}
	for _, j := range []*Job{nil, {Agent: "codex"}, {Agent: "opencode", DispatchKey: strings.Repeat("a", 64), RemoteControl: syntheticRemoteRun(t)}, {Agent: "codex", DispatchKey: "short", RemoteControl: syntheticRemoteRun(t)}} {
		if s.scope(j) != nil {
			t.Fatalf("ineligible job registered: %+v", j)
		}
	}
}

func TestShadowNativeWorkingVersusScreenDecisionIsRecordedNotActed(t *testing.T) {
	now := shadowT0
	s := newNativeEvidenceShadow(func() time.Time { return now })
	j := shadowFixtureJob(t, "codex")
	p := s.scope(j).open(shadowCodexCanonical, j.RemoteControl.NativeSessionID)
	p.connection(false)
	p.turn(j.RemoteControl.NativeSessionID, "turn-1", "inProgress")
	p.close(nil)
	s.compare(j, shadowSiteConnection, "connected", []string{shadowInputHerdrAgent, shadowInputScreenFooter, shadowInputCodexStatus})
	got := s.ledger(j)
	if len(got) != 1 || got[0].Agreement != "disagree" || got[0].Proposed[0].Value != "not-connected" || got[0].Proposed[1].Fact != shadowActivity || got[0].Proposed[1].Value != "working" {
		t.Fatalf("native disagreement with screen decision not recorded: %+v", got)
	}
	now = now.Add(time.Second)
	s.compare(j, shadowSiteConnection, "unconfirmed", []string{shadowInputHerdrAgent, shadowInputScreenFooter})
	if c := s.ledger(j)[1]; c.Agreement != "agree" {
		t.Fatalf("native agreement not recorded: %+v", c)
	}
	now = now.Add(shadowLiveWindow)
	s.compare(j, shadowSiteConnection, "connected", []string{shadowInputScreenFooter})
	if c := s.ledger(j)[2]; c.Agreement != "shadow-unknown" || c.Proposed[0].Reason != "native-expired" {
		t.Fatalf("expired native evidence still compared: %+v", c)
	}
}

func TestShadowLedgerIsBoundedAndCarriesNoPrivateValues(t *testing.T) {
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	j := shadowFixtureJob(t, "codex")
	j.RemoteControl.Cwd = "/canary/private/path"
	j.Pane, j.Workspace, j.Host = "canary-pane", "canary-workspace", "canary-host.example"
	p := s.scope(j).open(shadowCodexCanonical, j.RemoteControl.NativeSessionID)
	p.turn(j.RemoteControl.NativeSessionID, "canary-turn-id", "inProgress")
	for i := 0; i < shadowLedgerLimit+5; i++ {
		s.compare(j, shadowSiteRemoteHook, "pass", []string{shadowInputHerdrAgent, "canary-unlisted-input", shadowInputScreenFooter})
	}
	got := s.ledger(j)
	if len(got) != shadowLedgerLimit {
		t.Fatalf("ledger unbounded: %d", len(got))
	}
	b, _ := json.Marshal(got)
	for _, canary := range []string{j.RemoteControl.NativeSessionID, j.DispatchKey, "canary", j.RemoteControl.Name} {
		if strings.Contains(string(b), canary) {
			t.Fatalf("private value entered shadow ledger: %s", canary)
		}
	}
	if !strings.Contains(string(b), shadowInputOther) {
		t.Fatal("unlisted provenance input was not closed")
	}
	for i := 0; i < shadowMaxScopes+3; i++ {
		other := shadowFixtureJob(t, "codex")
		other.DispatchKey = strings.Repeat(string("0123456789abcdef"[i%16]), 63) + string("0123456789abcdef"[(i/16)%16])
		s.compare(other, shadowSiteRemoteHook, "pass", nil)
	}
	if len(s.scopes) > shadowMaxScopes {
		t.Fatalf("scope map unbounded: %d", len(s.scopes))
	}
	r, root, e := shadowFixtureReducer(t)
	for i := uint64(1); i <= shadowRowLimit; i++ {
		mustIngest(t, r, shadowRowAt(e, i, shadowConnection, "connected", root, "", shadowT0))
	}
	if ok, reason := r.ingest(shadowRowAt(e, shadowRowLimit+1, shadowConnection, "connected", root, "", shadowT0), shadowT0); ok || reason != "row-limit" {
		t.Fatalf("row journal unbounded: %v %s", ok, reason)
	}
}

func TestShadowIsEffectFreeBesideTodaysChecks(t *testing.T) {
	// Panicking internals and nil receivers never reach the caller.
	var nilShadow *nativeEvidenceShadow
	var nilScope *nativeShadowScope
	var nilPub *shadowPublisher
	nilShadow.compare(nil, shadowSiteRemoteHook, "pass", nil)
	_ = nilShadow.scope(nil)
	_ = nilScope.open(shadowCodexCanonical, "x")
	nilPub.turn("x", "y", "inProgress")
	nilPub.connection(true)
	nilPub.close(errors.New("x"))
	broken := newNativeEvidenceShadow(func() time.Time { panic("shadow clock failure") })
	j := shadowFixtureJob(t, "codex")
	broken.compare(j, shadowSiteRemoteHook, "pass", nil)
	if p := broken.scope(j).open(shadowCodexCanonical, j.RemoteControl.NativeSessionID); p != nil {
		p.turn(j.RemoteControl.NativeSessionID, "turn-1", "inProgress")
		p.close(nil)
	}

	// Today's decision is unchanged with the shadow on, off or broken.
	h := Host{Name: "local", SSH: "localhost", Home: t.TempDir()}
	for _, mk := range []func() *nativeEvidenceShadow{
		func() *nativeEvidenceShadow { return nil },
		func() *nativeEvidenceShadow { return newNativeEvidenceShadow(time.Now) },
		func() *nativeEvidenceShadow { return newNativeEvidenceShadow(func() time.Time { panic("broken") }) },
	} {
		for _, agent := range []string{"claude", "codex"} {
			c := &Coord{st: &State{}, cfg: &Config{}, shadow: mk()}
			j := shadowFixtureJob(t, agent)
			t.Setenv("PATH", t.TempDir()) // no herdr: today's codex check refuses on transport
			want := agent == "claude"
			if got := c.checkRemoteHook(context.Background(), h, j); got != want {
				t.Fatalf("%s shadow changed today's hook decision: %v", agent, got)
			}
			if c.shadow != nil && agent == "codex" {
				if l := c.shadow.ledger(j); len(l) > 0 && (l[0].Today != "refuse" || l[0].ScreenDerived) {
					t.Fatalf("today's transport refusal misattributed: %+v", l[0])
				}
			}
		}
	}

	// Canonical notifications: identical RPC results and buffered events with or without the tap.
	j = shadowFixtureJob(t, "codex")
	thread := j.RemoteControl.NativeSessionID
	params := []json.RawMessage{
		json.RawMessage(`{"threadId":"` + thread + `","turn":{"id":"turn-1","status":"inProgress"}}`),
		json.RawMessage(`{"threadId":"foreign-thread","turn":{"id":"turn-9","status":"inProgress"}}`),
		json.RawMessage(`{"threadId":"` + thread + `","turn":{"id":"turn-1","status":"completed"}}`),
		json.RawMessage(`{"threadId":"` + thread + `","turn":{"id":"turn-1","status":"bogus"}}`),
	}
	methods := []string{"turn/started", "turn/started", "turn/completed", "turn/completed"}
	plain := newGoalRPC(io.Discard, strings.NewReader(""), thread)
	tapped := newGoalRPC(io.Discard, strings.NewReader(""), thread)
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	tapped.shadow = s.scope(j).open(shadowCodexCanonical, thread)
	for i := range params {
		e1 := plain.nativeTurnNotification(methods[i], params[i])
		e2 := tapped.nativeTurnNotification(methods[i], params[i])
		if (e1 == nil) != (e2 == nil) || (e1 != nil && e1.Error() != e2.Error()) {
			t.Fatalf("tap changed notification result at %d", i)
		}
	}
	pb, _ := json.Marshal(plain.turnEvents)
	tb, _ := json.Marshal(tapped.turnEvents)
	if string(pb) != string(tb) {
		t.Fatal("tap changed buffered native lifecycle evidence")
	}
	if v := s.verdict(j, shadowTurnOutcome); v.Value != "completed" {
		t.Fatalf("exact canonical notification not observed by shadow: %+v", v)
	}
}
