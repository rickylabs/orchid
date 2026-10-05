package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixedNow = func() time.Time { return time.Date(2026, 10, 5, 1, 2, 3, 0, time.UTC) }

func fixtureAttempt(t *testing.T, publish bool, run bool, retry *retryExpectation) (*launchAttempt, string) {
	t.Helper()
	root := privateTestRoot(t)
	a := newLaunchAttempt(root, nil, publish, "instance-live", strings.Repeat("d", 64), "assignment_"+strings.Repeat("e", 64),
		dispatchIssue{Repo: "fixture/inbox", Number: 7}, outcomeRoute{Harness: "codex", Model: "fixture-model", Effort: "high"}, run, retry, fixedNow)
	return a, root
}

func readOutcome(t *testing.T, root, attemptID string) (launchOutcome, []byte) {
	t.Helper()
	raw, err := os.ReadFile(attemptFile(root, "outcome", attemptID))
	var o launchOutcome
	if err != nil || decodeNativeJSON(raw, &o) != nil {
		t.Fatalf("outcome unavailable: %v", err)
	}
	return o, raw
}

// A1: one reduction from native facts and the last stage.
func TestLaunchOutcomeReduction(t *testing.T) {
	end := func(kind, stage string, shutdown bool) *attemptContextEnd {
		return &attemptContextEnd{Kind: kind, Stage: stage, Shutdown: shutdown}
	}
	for _, tc := range []struct {
		name                                    string
		run                                     bool
		stage                                   string
		facts                                   attemptFacts
		err                                     error
		outcome, effects, phase, reason, signal string
	}{
		{"interactive-success", false, "goal-confirmation", attemptFacts{Registered: true, GoalCommitted: true}, nil, "started", "present", "goal-confirmation", "started", "orchid-commit"},
		{"run-success", true, "environment", attemptFacts{RunStarted: true}, nil, "started", "present", "environment", "started", "orchid-run"},
		{"nil-error-without-commit-is-not-success", false, "goal-confirmation", attemptFacts{Registered: true}, nil, "unconfirmed", "possible", "goal-confirmation", "progress-unconfirmed", "orchid-progress"},
		{"codex-client-unmatched", false, "client-selection", attemptFacts{CodexClientUnmatched: true}, errMatrix, "failed", "none", "client-selection", "codex-client-unmatched", "codex-app-server"},
		{"codex-client-check-failed", false, "client-selection", attemptFacts{CodexClientCheckFailed: true}, errMatrix, "failed", "none", "client-selection", "codex-client-check-failed", "codex-client-check"},
		{"agy-settings", false, "client-selection", attemptFacts{AgySettingsUnreadable: true}, errMatrix, "failed", "none", "client-selection", "agy-settings-unreadable", "agy-settings"},
		{"shutdown", false, "registration", attemptFacts{ContextEnded: end("cancelled", "registration", true)}, errMatrix, "unconfirmed", "possible", "registration", "launch-cancelled", "orchid-shutdown"},
		{"cancel", false, "workspace", attemptFacts{ContextEnded: end("cancelled", "workspace", false)}, errMatrix, "unconfirmed", "possible", "workspace", "launch-cancelled", "orchid-cancel"},
		{"registered-then-shutdown", false, "goal-delivery", attemptFacts{Registered: true, ContextEnded: end("cancelled", "goal-delivery", true)}, errMatrix, "unconfirmed", "possible", "goal-delivery", "launch-cancelled", "orchid-shutdown"},
		{"deadline-registration", false, "registration", attemptFacts{ContextEnded: end("deadline", "registration", false)}, errMatrix, "unconfirmed", "possible", "registration", "startup-timeout", "orchid-deadline"},
		{"deadline-goal-delivery", false, "goal-delivery", attemptFacts{ContextEnded: end("deadline", "goal-delivery", false)}, errMatrix, "unconfirmed", "possible", "goal-delivery", "goal-unconfirmed", "orchid-deadline"},
		{"deadline-goal-confirmation", false, "goal-confirmation", attemptFacts{ContextEnded: end("deadline", "goal-confirmation", false)}, errMatrix, "unconfirmed", "possible", "goal-confirmation", "goal-unconfirmed", "orchid-deadline"},
		{"deadline-preparation", false, "preparation", attemptFacts{ContextEnded: end("deadline", "preparation", false)}, errMatrix, "unconfirmed", "possible", "preparation", "deadline-exceeded", "orchid-deadline"},
		{"deadline-client-selection", false, "client-selection", attemptFacts{ContextEnded: end("deadline", "client-selection", false)}, errMatrix, "unconfirmed", "possible", "client-selection", "deadline-exceeded", "orchid-deadline"},
		{"deadline-workspace", false, "workspace", attemptFacts{ContextEnded: end("deadline", "workspace", false)}, errMatrix, "unconfirmed", "possible", "workspace", "deadline-exceeded", "orchid-deadline"},
		{"deadline-environment", false, "environment", attemptFacts{ContextEnded: end("deadline", "environment", false)}, errMatrix, "unconfirmed", "possible", "environment", "deadline-exceeded", "orchid-deadline"},
		{"deadline-thread-preparation", false, "thread-preparation", attemptFacts{ContextEnded: end("deadline", "thread-preparation", false)}, errMatrix, "unconfirmed", "possible", "thread-preparation", "deadline-exceeded", "orchid-deadline"},
		{"herdr-timeout", false, "registration", attemptFacts{HerdrStartError: "timeout"}, errMatrix, "unconfirmed", "possible", "registration", "startup-timeout", "herdr-structured-error"},
		{"herdr-not-ready", false, "registration", attemptFacts{HerdrStartError: "agent_not_ready"}, errMatrix, "unconfirmed", "possible", "registration", "startup-blocked", "herdr-structured-error"},
		{"herdr-busy", false, "registration", attemptFacts{HerdrStartError: "agent_pane_busy"}, errMatrix, "unconfirmed", "possible", "registration", "startup-busy", "herdr-structured-error"},
		{"context-precedes-structured", false, "registration", attemptFacts{HerdrStartError: "agent_not_ready", ContextEnded: end("deadline", "registration", false)}, errMatrix, "unconfirmed", "possible", "registration", "startup-timeout", "orchid-deadline"},
		{"goal-stage-without-native-end", false, "goal-delivery", attemptFacts{Registered: true}, errMatrix, "unconfirmed", "possible", "goal-delivery", "progress-unconfirmed", "orchid-progress"},
		{"nothing-native", false, "workspace", attemptFacts{}, errMatrix, "unconfirmed", "possible", "workspace", "progress-unconfirmed", "orchid-progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := attemptProgress{SchemaVersion: 1, AttemptID: attemptIDFor(strings.Repeat("d", 64)), Run: tc.run, Stage: tc.stage, Facts: tc.facts}
			o := decideOutcome(p, tc.err, "2026-10-05T01:02:03.000Z")
			if o.Outcome != tc.outcome || o.Effects != tc.effects || o.Phase != tc.phase || o.ReasonCode != tc.reason || o.Signal != tc.signal {
				t.Fatalf("got %s/%s/%s/%s/%s", o.Outcome, o.Effects, o.Phase, o.ReasonCode, o.Signal)
			}
			if !reflect.DeepEqual(o.Evidence, tc.facts.evidence()) {
				t.Fatalf("evidence %v, want %v", o.Evidence, tc.facts.evidence())
			}
			if (o.Outcome == "failed") != (o.Effects == "none") {
				t.Fatal("failed must mean no effect, and only failed")
			}
		})
	}
}

// A1/F1: identical final kinds from a native path and a screen path classify
// differently; screen text never selects the producer reason.
func TestLaunchOutcomeNativeNotScreen(t *testing.T) {
	for _, tc := range []struct {
		mode, wantReason string
	}{
		// Herdr's structured timeout, then the inherited screen refinement to startup_blocked.
		{"trust-dialog", "startup-timeout"},
		// Herdr's own structured agent_not_ready.
		{"envelope", "startup-blocked"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			h, _ := registrationHost(t, tc.mode)
			r := registrationReceipt(t, "codex", Overrides{})
			root := filepath.Dir(filepath.Dir(filepath.Dir(r.file)))
			r.attempt = newLaunchAttempt(root, nil, true, "instance-live", strings.Repeat("d", 64), "assignment_"+strings.Repeat("e", 64),
				dispatchIssue{Repo: "fixture/inbox", Number: 7}, outcomeRoute{Harness: "codex"}, false, nil, time.Now)
			r.attempt.begin()
			_, _, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil, "codex", Overrides{}, r)
			if registrationFailureKind(err) != "startup_blocked" {
				t.Fatalf("fixture no longer yields the shared final kind: %s", registrationFailureKind(err))
			}
			r.attempt.finish(context.Background(), err)
			o, _ := readOutcome(t, root, r.attempt.p.AttemptID)
			if o.ReasonCode != tc.wantReason || o.Signal != "herdr-structured-error" || o.Phase != "registration" || o.Effects != "possible" {
				t.Fatalf("screen-refined result chose the producer reason: %+v", o)
			}
		})
	}
}

// F2/A2: the decision is durable before publication; publication never replaces;
// progress retires only after the outcome is published.
func TestLaunchAttemptLifecycle(t *testing.T) {
	a, root := fixtureAttempt(t, true, false, nil)
	a.begin()
	if _, ok := readAttemptProgress(attemptFile(root, "attempt", a.p.AttemptID)); !ok {
		t.Fatal("progress not durable before launch work")
	}
	admitted, err := os.ReadFile(attemptFile(root, "admitted", a.p.AttemptID))
	var adm attemptAdmission
	if err != nil || decodeNativeJSON(admitted, &adm) != nil || adm.AttemptID != a.p.AttemptID || adm.ActionDispatchID != actionOpaque("assignment", "orchid-"+strings.Repeat("d", 64)) {
		t.Fatal("admission fact missing or wrong identity")
	}
	a.enter(stageWorkspace)
	a.enter(stagePreparation) // never decreases
	if a.p.Stage != "workspace" {
		t.Fatal("stage decreased")
	}
	a.enter(stageRegistration)
	a.registered()
	a.enter(stageGoalDelivery)
	a.enter(stageGoalConfirmation)
	a.goalCommitted()
	a.finish(context.Background(), nil)
	o, raw := readOutcome(t, root, a.p.AttemptID)
	if o.Outcome != "started" || o.AttemptID != adm.AttemptID || !reflect.DeepEqual(o.Evidence, []string{"goalCommitted", "registered"}) {
		t.Fatalf("success not published under the same attempt: %+v", o)
	}
	if _, err := os.Stat(attemptFile(root, "attempt", a.p.AttemptID)); !os.IsNotExist(err) {
		t.Fatal("progress not retired after publication")
	}
	if st, _ := os.Stat(attemptFile(root, "outcome", a.p.AttemptID)); st.Mode().Perm() != 0600 {
		t.Fatal("outcome not private")
	}
	a.finish(context.Background(), errMatrix) // a second finish cannot change the record
	if _, again := readOutcome(t, root, a.p.AttemptID); !bytes.Equal(again, raw) {
		t.Fatal("published outcome changed")
	}
}

func TestLaunchAttemptRunNeverRegisters(t *testing.T) {
	a, root := fixtureAttempt(t, true, true, nil)
	a.begin()
	a.enter(stageEnvironment)
	a.runStarted()
	a.enter(stageRegistration)
	if a.p.Stage != "environment" {
		t.Fatal("a run-mode job entered registration")
	}
	a.finish(context.Background(), nil)
	if o, _ := readOutcome(t, root, a.p.AttemptID); o.Outcome != "started" || o.Phase != "environment" || o.Signal != "orchid-run" {
		t.Fatalf("run-mode success misreported: %+v", o)
	}
}

// Writer off: decisions still recorded and progress retired; no consumer files.
func TestLaunchAttemptWriterOffWritesNoConsumerFiles(t *testing.T) {
	a, root := fixtureAttempt(t, false, false, nil)
	a.begin()
	a.finish(context.Background(), errMatrix)
	for _, prefix := range []string{"admitted", "outcome", "attempt"} {
		if _, err := os.Stat(attemptFile(root, prefix, a.p.AttemptID)); !os.IsNotExist(err) {
			t.Fatalf("%s file present with the writer off", prefix)
		}
	}
}

// A2: a progress write failure leaves the attempt untracked without blocking it.
func TestLaunchAttemptUntrackedNeverBlocks(t *testing.T) {
	a := newLaunchAttempt(filepath.Join(t.TempDir(), "missing"), nil, true, "i", strings.Repeat("d", 64), "assignment_"+strings.Repeat("e", 64),
		dispatchIssue{Repo: "fixture/inbox", Number: 7}, outcomeRoute{}, false, nil, fixedNow)
	a.begin()
	a.enter(stageWorkspace)
	a.finish(context.Background(), errMatrix)
	if a.tracked || a.p.Decision != nil {
		t.Fatal("an untracked attempt claimed a durable decision")
	}
}

// Shutdown is distinguished from other cancellation natively, by the root cause.
func TestLaunchAttemptShutdownCause(t *testing.T) {
	for _, shutdown := range []bool{true, false} {
		a, root := fixtureAttempt(t, true, false, nil)
		a.begin()
		a.enter(stageRegistration)
		a.registered()
		parent, cancel := context.WithCancelCause(context.Background())
		child, childCancel := context.WithTimeout(parent, time.Hour)
		if shutdown {
			cancel(errOrchidShutdown)
		} else {
			cancel(nil)
		}
		a.contextEnded(child)
		childCancel()
		a.finish(parent, errMatrix)
		o, _ := readOutcome(t, root, a.p.AttemptID)
		want := map[bool]string{true: "orchid-shutdown", false: "orchid-cancel"}[shutdown]
		if o.ReasonCode != "launch-cancelled" || o.Signal != want || o.Effects != "possible" || !reflect.DeepEqual(o.Evidence, []string{"contextEnded", "registered"}) {
			t.Fatalf("cancellation misreported: %+v", o)
		}
	}
}

// F5: one winner, identical republication succeeds, a different record never
// replaces the existing bytes, and symlink destinations are refused.
func TestPublishOnceNeverReplaces(t *testing.T) {
	root := privateTestRoot(t)
	path := filepath.Join(root, "outcome-x.json")
	if publishOnce(root, path, []byte("first"), nil) != nil || publishOnce(root, path, []byte("first"), nil) != nil {
		t.Fatal("identical publication refused")
	}
	if !errors.Is(publishOnce(root, path, []byte("second"), nil), errOutcomeConflict) {
		t.Fatal("conflicting publication not reported")
	}
	if b, _ := os.ReadFile(path); string(b) != "first" {
		t.Fatal("existing record replaced")
	}
	link := filepath.Join(root, "outcome-link.json")
	_ = os.Symlink(path, link)
	if !errors.Is(publishOnce(root, link, []byte("first"), nil), errOutcomeConflict) {
		t.Fatal("symlink destination accepted")
	}
	race := filepath.Join(root, "outcome-race.json")
	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := []byte("same")
			if i%2 == 1 {
				body = []byte("other")
			}
			results[i] = publishOnce(root, race, body, nil)
		}(i)
	}
	wg.Wait()
	winner, _ := os.ReadFile(race)
	for i, err := range results {
		body := map[bool]string{true: "other", false: "same"}[i%2 == 1]
		if (err == nil) != (body == string(winner)) {
			t.Fatalf("publisher %d: err=%v winner=%q", i, err, winner)
		}
	}
}

func writeProgress(t *testing.T, root string, p attemptProgress) {
	t.Helper()
	if writePrivateJSON(attemptFile(root, "attempt", p.AttemptID), ".attempt-", nil, p) != nil {
		t.Fatal(err0)
	}
}

var err0 = errors.New("fixture write failed")

// A2: publication-only recovery; never relaunches; frozen bytes; conflicts kept.
func TestRecoverLaunchAttempts(t *testing.T) {
	base := func(key string) attemptProgress {
		return attemptProgress{SchemaVersion: 1, AttemptID: attemptIDFor(key), ActionDispatchID: actionOpaque("assignment", "orchid-"+key),
			DispatchID: "assignment_" + strings.Repeat("e", 64), Issue: dispatchIssue{Repo: "fixture/inbox", Number: 7},
			Stage: "registration", Facts: attemptFacts{Registered: true}, Instance: "instance-dead", StartedAt: "2026-10-05T00:00:00.000Z"}
	}
	t.Run("dead-instance-interrupted", func(t *testing.T) {
		root := privateTestRoot(t)
		p := base(strings.Repeat("1", 64))
		writeProgress(t, root, p)
		recoverLaunchAttempts(root, nil, true, "instance-live", fixedNow())
		o, raw := readOutcome(t, root, p.AttemptID)
		if o.ReasonCode != "launch-interrupted" || o.Signal != "orchid-recovery" || o.Phase != "registration" || o.Effects != "possible" || !reflect.DeepEqual(o.Evidence, []string{"registered"}) {
			t.Fatalf("interrupted attempt misreported: %+v", o)
		}
		recoverLaunchAttempts(root, nil, true, "instance-live", fixedNow().Add(time.Hour))
		if _, again := readOutcome(t, root, p.AttemptID); !bytes.Equal(again, raw) {
			t.Fatal("recovery rewrote a published outcome")
		}
	})
	t.Run("live-instance-untouched", func(t *testing.T) {
		root := privateTestRoot(t)
		p := base(strings.Repeat("2", 64))
		p.Instance = "instance-live"
		writeProgress(t, root, p)
		recoverLaunchAttempts(root, nil, true, "instance-live", fixedNow())
		if _, err := os.Stat(attemptFile(root, "outcome", p.AttemptID)); !os.IsNotExist(err) {
			t.Fatal("recovery decided live work")
		}
	})
	t.Run("decided-backlog-publishes-frozen-bytes", func(t *testing.T) {
		root := privateTestRoot(t)
		p := base(strings.Repeat("3", 64))
		p.Instance = "instance-live"
		frozen, _ := json.Marshal(decideOutcome(p, errMatrix, "2026-10-05T00:00:01.000Z"))
		p.Decision = &attemptDecision{Bytes: string(frozen), ObservedAt: "2026-10-05T00:00:01.000Z"}
		writeProgress(t, root, p)
		recoverLaunchAttempts(root, nil, true, "instance-live", fixedNow())
		if _, raw := readOutcome(t, root, p.AttemptID); !bytes.Equal(raw, frozen) {
			t.Fatal("backlog publication changed the decided bytes")
		}
		if _, err := os.Stat(attemptFile(root, "attempt", p.AttemptID)); !os.IsNotExist(err) {
			t.Fatal("backlog not retired")
		}
	})
	t.Run("visible-outcome-identical-completes", func(t *testing.T) {
		root := privateTestRoot(t)
		p := base(strings.Repeat("4", 64))
		frozen, _ := json.Marshal(decideOutcome(p, errMatrix, "2026-10-05T00:00:01.000Z"))
		p.Decision = &attemptDecision{Bytes: string(frozen), ObservedAt: "2026-10-05T00:00:01.000Z"}
		writeProgress(t, root, p)
		_ = writePrivateJSON(attemptFile(root, "outcome", p.AttemptID), ".o-", nil, json.RawMessage(frozen))
		recoverLaunchAttempts(root, nil, true, "instance-live", fixedNow())
		if _, err := os.Stat(attemptFile(root, "attempt", p.AttemptID)); !os.IsNotExist(err) {
			t.Fatal("identical visible outcome did not complete")
		}
	})
	t.Run("conflicting-outcome-kept", func(t *testing.T) {
		root := privateTestRoot(t)
		p := base(strings.Repeat("5", 64))
		frozen, _ := json.Marshal(decideOutcome(p, errMatrix, "2026-10-05T00:00:01.000Z"))
		p.Decision = &attemptDecision{Bytes: string(frozen), ObservedAt: "2026-10-05T00:00:01.000Z"}
		writeProgress(t, root, p)
		other := []byte(`{"conflict":true}`)
		_ = os.WriteFile(attemptFile(root, "outcome", p.AttemptID), other, 0600)
		recoverLaunchAttempts(root, nil, true, "instance-live", fixedNow())
		if b, _ := os.ReadFile(attemptFile(root, "outcome", p.AttemptID)); !bytes.Equal(b, other) {
			t.Fatal("conflicting outcome replaced")
		}
		if _, ok := readAttemptProgress(attemptFile(root, "attempt", p.AttemptID)); !ok {
			t.Fatal("conflict hid the progress record")
		}
	})
}

// A3: snapshot from always-on records; stale or stopping producer is unknown.
func TestLaunchSnapshot(t *testing.T) {
	root := privateTestRoot(t)
	now := fixedNow()
	hb := func(at time.Time, stopping bool) {
		_ = writePrivateJSON(filepath.Join(root, "producer.json"), ".p-", nil, producerHeartbeat{Instance: "instance-live", StartedAt: stamp(now), ObservedAt: stamp(at), Stopping: stopping})
	}
	live := attemptProgress{SchemaVersion: 1, AttemptID: attemptIDFor(strings.Repeat("6", 64)), ActionDispatchID: actionOpaque("assignment", "orchid-"+strings.Repeat("6", 64)),
		Issue: dispatchIssue{Repo: "fixture/inbox", Number: 594}, Stage: "registration", Instance: "instance-live", StartedAt: stamp(now.Add(-90 * time.Second))}
	writeProgress(t, root, live)
	dead := live
	dead.AttemptID, dead.Instance = attemptIDFor(strings.Repeat("7", 64)), "instance-dead"
	writeProgress(t, root, dead)
	backlog := live
	backlog.AttemptID, backlog.Decision = attemptIDFor(strings.Repeat("8", 64)), &attemptDecision{Bytes: "{}", ObservedAt: stamp(now)}
	writeProgress(t, root, backlog)
	r := registrationReceipt(t, "codex", Overrides{}) // an untracked reserved dispatch, in its own root
	untrackedRoot := filepath.Dir(filepath.Dir(filepath.Dir(r.file)))
	hb(now.Add(-5*time.Second), false)
	s := readLaunchSnapshot(root, now)
	if s.Producer != "live" || len(s.InFlight) != 1 || s.InFlight[0].Issue != 594 || s.InFlight[0].Stage != "registration" || s.InFlight[0].AgeSeconds != 90 || s.Backlog != 1 || s.Unresolved != 1 {
		t.Fatalf("live snapshot wrong: %+v", s)
	}
	for _, stale := range []struct {
		at       time.Time
		stopping bool
	}{{now.Add(-time.Minute), false}, {now, true}} {
		hb(stale.at, stale.stopping)
		if s := readLaunchSnapshot(root, now); s.Producer != "unknown" || len(s.InFlight) != 0 {
			t.Fatalf("stale/stopping producer reported as live: %+v", s)
		}
	}
	if u := readLaunchSnapshot(untrackedRoot, now); len(u.Untracked) != 1 || u.Untracked[0] != 7 {
		t.Fatalf("untracked reserved dispatch missing: %+v", u)
	}
}

// A3: a registering launch counts as active once, with or without its job.
func TestGovernorCountsInFlightAttemptsOnce(t *testing.T) {
	c := &Coord{st: &State{Jobs: map[int]*Job{1: {Agent: "codex", DispatchKey: strings.Repeat("a", 64)}}}}
	a := &launchAttempt{key: strings.Repeat("b", 64)}
	c.trackAttempt(a, "codex")
	if got := c.activeByAccount()["codex"]; got != 2 {
		t.Fatalf("in-flight launch not counted: %d", got)
	}
	c.st.Jobs[2] = &Job{Agent: "codex", DispatchKey: strings.Repeat("b", 64)}
	if got := c.activeByAccount()["codex"]; got != 2 {
		t.Fatalf("a registered in-flight launch counted twice: %d", got)
	}
	c.untrackAttempt(a)
	if got := c.activeByAccount()["codex"]; got != 2 {
		t.Fatalf("jobs miscounted after the attempt ended: %d", got)
	}
}

// F3: retries carry the predecessor and operation; attempts are distinct.
func TestLaunchAttemptRetryIdentity(t *testing.T) {
	first := attemptIDFor(strings.Repeat("d", 64))
	retry := &retryExpectation{OperationID: "op-1", Dispatch: dispatchBinding{RunID: "orchid-" + strings.Repeat("d", 64)}}
	retryKey := shaText([]byte("retry\x00" + strings.Repeat("d", 64) + "\x00op-1"))
	a := newLaunchAttempt(privateTestRoot(t), nil, true, "i", retryKey, "assignment_"+strings.Repeat("e", 64),
		dispatchIssue{Repo: "fixture/inbox", Number: 7}, outcomeRoute{}, false, retry, fixedNow)
	if a.p.AttemptID == first || a.p.PredecessorAttemptID == nil || *a.p.PredecessorAttemptID != first ||
		a.p.RetryOperationID == nil || *a.p.RetryOperationID != "op-1" || a.p.ActionDispatchID != actionOpaque("assignment", "orchid-"+retryKey) {
		t.Fatalf("retry identity wrong: %+v", a.p)
	}
}

// Production matrixAttempt: the tracker exists before the launch and the
// outcome is published after it, without changing the existing refusal path.
func TestMatrixAttemptPublishesOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		launch func(r *durableMatrixReceipt) error
		reason string
	}{
		{"started", func(r *durableMatrixReceipt) error {
			r.attempt.enter(stageRegistration)
			r.attempt.registered()
			r.attempt.enter(stageGoalConfirmation)
			r.attempt.goalCommitted()
			return nil
		}, "started"},
		{"codex-client-unmatched", func(r *durableMatrixReceipt) error {
			r.attempt.enter(stageClientSelection)
			r.attempt.codexClientUnmatched()
			return matrixSite("launch.registration", matrixSite("spawn.codex-client", errCodexClientUnmatched))
		}, "codex-client-unmatched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTestRoot(t)
			cfg := &Config{Inbox: "example/inbox", Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: strings.Repeat("b", 40), LaunchOutcomes: true,
				TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)}}, Governor: Gov{WeeklyCeiling: 92}}
			c := &Coord{cfg: cfg, st: loadState(filepath.Join(root, "state.json")), instance: "instance-live"}
			now := time.Now()
			c.gov.q = map[string]quota{"claude": {ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()}}}
			is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic", Body: "/swarm\ntier: feature\nrole: implementation\n\nSynthetic task"}
			var seen *launchAttempt
			deps := matrixAttemptDeps{
				report: func(matrixRefusal) {},
				read: func(context.Context, string, string, string) (string, error) {
					return "| `routing` | matrix `implementation` row |", nil
				},
				resolve: func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) {
					return syntheticRoute(), nil
				},
				host:    func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, true },
				persist: persistMatrixReceipt,
				launch: func(_ context.Context, _ int, _ Issue, _ Host, _ string, _ Overrides, r *durableMatrixReceipt) error {
					seen = r.attempt
					if _, ok := readAttemptProgress(attemptFile(root, "attempt", r.attempt.p.AttemptID)); !ok {
						t.Fatal("progress not durable before the launch")
					}
					if c.activeByAccount()[accountKey(syntheticRoute().Transport)] != 1 {
						t.Fatal("governor does not count the launch in flight")
					}
					return tc.launch(r)
				},
			}
			c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, map[string]int{"claude": 1}, deps)
			if seen == nil {
				t.Fatal("launch never received an attempt")
			}
			o, _ := readOutcome(t, root, seen.p.AttemptID)
			files, _ := filepath.Glob(filepath.Join(root, "*", "record", "dispatch.json"))
			var d dispatchBinding
			raw, _ := os.ReadFile(files[0])
			_ = json.Unmarshal(raw, &d)
			if o.ReasonCode != tc.reason || o.ActionDispatchID != actionOpaque("assignment", d.RunID) || o.DispatchID != launchStateDispatchID(cfg.Inbox, is) {
				t.Fatalf("outcome identity or reason wrong: %+v", o)
			}
			if len(c.attempts) != 0 {
				t.Fatal("attempt still counted after the launch returned")
			}
		})
	}
}

// ---- acceptance: REVIEW-lo3 requirements 1-3 ----

// failPublish makes publishRecord fail for records with prefix until restored.
func failPublish(t *testing.T, prefix string) func() {
	t.Helper()
	failing := true
	var mu sync.Mutex
	orig := publishRecord
	publishRecord = func(root, path string, body []byte, owner *receiptOwner) error {
		mu.Lock()
		f := failing
		mu.Unlock()
		if f && strings.HasPrefix(filepath.Base(path), prefix+"-") {
			return errMatrix
		}
		return orig(root, path, body, owner)
	}
	t.Cleanup(func() { publishRecord = orig })
	return func() { mu.Lock(); failing = false; mu.Unlock() }
}

// R1: an Orchid shutdown while the Codex client is being checked is the
// context's fact, never an early client verdict, through the production spawn.
func TestLaunchAttemptClientSelectionShutdownIsContextFact(t *testing.T) {
	cwd := t.TempDir()
	o := Overrides{Model: "fixture-model", Effort: "low"}
	r := registrationReceipt(t, "codex", o)
	r.attempt, _ = fixtureAttempt(t, true, false, nil)
	r.attempt.begin()
	r.attempt.enter(stageClientSelection)
	h := canonicalFixtureHost(t, "multi", func(string, map[string]any) any { return map[string]any{} })
	on := true
	h.RemoteControl = &RemoteControlConfig{Codex: &on}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errOrchidShutdown)
	_, _, err := h.spawnAgent(ctx, "fixture-agent", cwd, nil, "codex", o, r, "Synthetic task")
	f := r.attempt.p.Facts
	if err == nil || f.CodexClientCheckFailed || f.CodexClientUnmatched || f.ContextEnded == nil || !f.ContextEnded.Shutdown {
		t.Fatalf("a cut-short client check became a client verdict: err=%v facts=%+v", err, f)
	}
	if o := decideOutcome(r.attempt.p, err, "t"); o.ReasonCode != "launch-cancelled" || o.Signal != "orchid-shutdown" || o.Phase != "client-selection" {
		t.Fatalf("shutdown at client selection misreported: %+v", o)
	}
}

// R2: a decision whose first durable write fails is kept in memory and retried
// with its frozen bytes; it is never re-decided or lost to recovery.
func TestLaunchAttemptDecisionRetainedAndRetried(t *testing.T) {
	a, root := fixtureAttempt(t, true, false, nil)
	a.begin()
	a.enter(stageRegistration)
	a.registered()
	if os.Chmod(root, 0500) != nil {
		t.Fatal("fixture")
	}
	a.finish(context.Background(), errMatrix)
	if !a.decided || a.durable || !a.pending() || a.p.Decision == nil {
		t.Fatal("a non-durable decision was dropped")
	}
	frozen := a.p.Decision.Bytes
	a.retry() // still failing: no change
	if os.Chmod(root, 0700) != nil {
		t.Fatal("fixture")
	}
	a.finish(context.Background(), nil) // a later finish can never re-decide
	a.retry()
	o, raw := readOutcome(t, root, a.p.AttemptID)
	if string(raw) != frozen || o.ReasonCode != "progress-unconfirmed" || a.pending() {
		t.Fatalf("retried decision changed or not published: %+v", o)
	}
	if _, err := os.Stat(attemptFile(root, "attempt", a.p.AttemptID)); !os.IsNotExist(err) {
		t.Fatal("progress not retired after the retried publication")
	}
}

// R2: nothing is written after the decision; a late stage or fact cannot
// recreate or rewrite progress.
func TestLaunchAttemptNoWriteAfterDecision(t *testing.T) {
	a, root := fixtureAttempt(t, true, false, nil)
	a.begin()
	a.finish(context.Background(), errMatrix)
	a.enter(stageGoalConfirmation)
	a.goalCommitted()
	if _, err := os.Stat(attemptFile(root, "attempt", a.p.AttemptID)); !os.IsNotExist(err) {
		t.Fatal("a late update recreated retired progress")
	}
	if a.p.Facts.GoalCommitted || a.p.Stage == "goal-confirmation" {
		t.Fatal("a late update changed the decided attempt")
	}
}

// R2: a failed admission never blocks the launch, is retried with identical
// bytes, and an outcome still publishes while admission is withheld.
func TestLaunchAttemptAdmissionRetryAndOutcomeOnly(t *testing.T) {
	restore := failPublish(t, "admitted")
	a, root := fixtureAttempt(t, true, false, nil)
	a.begin()
	if !a.tracked || a.admitted || !a.pending() {
		t.Fatal("admission failure untracked the attempt or was not kept for retry")
	}
	a.enter(stageRegistration) // the launch goes on
	a.registered()
	a.retry() // still withheld
	if _, err := os.Stat(attemptFile(root, "admitted", a.p.AttemptID)); !os.IsNotExist(err) {
		t.Fatal("withheld admission present")
	}
	restore()
	a.retry()
	raw, err := os.ReadFile(attemptFile(root, "admitted", a.p.AttemptID))
	want, _ := admissionBytes(a.p)
	if err != nil || !bytes.Equal(raw, want) || a.pending() {
		t.Fatal("admission retry missing or changed")
	}
	b, root2 := fixtureAttempt(t, true, false, nil)
	failPublish(t, "admitted")
	b.begin()
	b.finish(context.Background(), errMatrix)
	if o, _ := readOutcome(t, root2, b.p.AttemptID); o.AttemptID != b.p.AttemptID {
		t.Fatal("outcome-only publication blocked by a withheld admission")
	}
}

// R2: recovery republishes a missing admission from the durable progress
// record with identical bytes, then the frozen outcome.
func TestRecoverRepublishesAdmission(t *testing.T) {
	a, root := fixtureAttempt(t, true, false, nil)
	restore := failPublish(t, "admitted")
	a.begin()
	restore()
	a.p.Instance = "instance-dead"
	writeProgress(t, root, a.p)
	recoverLaunchAttempts(root, nil, true, "instance-live", fixedNow())
	raw, err := os.ReadFile(attemptFile(root, "admitted", a.p.AttemptID))
	want, _ := admissionBytes(a.p)
	if err != nil || !bytes.Equal(raw, want) {
		t.Fatal("recovery did not republish the admission")
	}
	if o, _ := readOutcome(t, root, a.p.AttemptID); o.ReasonCode != "launch-interrupted" {
		t.Fatal("recovery did not decide the dead attempt")
	}
}

// R3: the heartbeat is independent of launches: it stays fresh while a launch
// is held past the start budget, and the snapshot shows that launch in flight.
// Stopping is sticky and turns the snapshot unknown.
func TestHeartbeatFreshDuringLongLaunchAndStoppingSticky(t *testing.T) {
	root := privateTestRoot(t)
	c := &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: root}}, instance: "instance-live"}
	held := time.Now().Add(-3 * time.Minute) // longer than the two-minute start budget
	a := newLaunchAttempt(root, nil, false, c.instance, strings.Repeat("d", 64), "assignment_"+strings.Repeat("e", 64),
		dispatchIssue{Repo: "fixture/inbox", Number: 7}, outcomeRoute{Harness: "codex"}, false, nil, func() time.Time { return held })
	a.begin()
	a.enter(stageRegistration)
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.heartbeatLoop(ctx, ticks); close(done) }()
	ticks <- time.Now()
	ticks <- time.Now()
	s := readLaunchSnapshot(root, time.Now())
	if s.Producer != "live" || len(s.InFlight) != 1 || s.InFlight[0].AgeSeconds < 170 {
		t.Fatalf("heartbeat not fresh during a long launch: %+v", s)
	}
	c.markStopping()
	c.beat(false) // a late regular beat
	if s := readLaunchSnapshot(root, time.Now()); s.Producer != "unknown" {
		t.Fatal("a regular beat made a stopping producer live again")
	}
	cancel()
	<-done
}

// R3: Orchid marks stopping before it cancels any launch on a signal.
func TestShutdownMarksStoppingBeforeCancel(t *testing.T) {
	src := productionSources(t)["main.go"]
	i := strings.Index(src, "case <-signals:")
	if i < 0 {
		t.Fatal("signal handler not found")
	}
	body := src[i : i+200]
	m, c := strings.Index(body, "c.markStopping()"), strings.Index(body, "cancelRoot(errOrchidShutdown)")
	if m < 0 || c < 0 || m > c {
		t.Fatal("launches are cancelled before the producer is marked stopping")
	}
}

// R2/R3: a finished attempt is never active work while its publication is
// pending; the heartbeat retry settles it.
func TestFinishedAttemptPendingIsNotActive(t *testing.T) {
	a, root := fixtureAttempt(t, true, false, nil)
	c := &Coord{st: &State{Jobs: map[int]*Job{}}}
	a.begin()
	c.trackAttempt(a, "codex")
	if os.Chmod(root, 0500) != nil {
		t.Fatal("fixture")
	}
	a.finish(context.Background(), errMatrix)
	c.untrackAttempt(a)
	if got := c.activeByAccount()["codex"]; got != 0 || !c.unsettled[a] {
		t.Fatalf("finished attempt counted active or not kept for retry: %d", got)
	}
	_ = os.Chmod(root, 0700)
	c.retryAttempts()
	if len(c.unsettled) != 0 {
		t.Fatal("settled attempt still pending")
	}
	if _, err := os.Stat(attemptFile(root, "outcome", a.p.AttemptID)); err != nil {
		t.Fatal("retried outcome not published")
	}
}

// A1: facts are immutable; the first context end (and its stage) wins over a
// later one.
func TestLaunchAttemptFirstContextEndWins(t *testing.T) {
	a, _ := fixtureAttempt(t, false, false, nil)
	a.begin()
	a.enter(stageRegistration)
	dl, cancelDL := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDL()
	a.contextEnded(dl)
	a.enter(stageGoalDelivery)
	later, cancelLater := context.WithCancelCause(context.Background())
	cancelLater(errOrchidShutdown)
	a.contextEnded(later)
	if ce := a.p.Facts.ContextEnded; ce == nil || ce.Kind != "deadline" || ce.Stage != "registration" || ce.Shutdown {
		t.Fatalf("a later context end replaced the first: %+v", ce)
	}
}

// A2: recovery never publishes an interrupted decision it could not first make
// durable; the next pass decides it instead.
func TestRecoverPublishesOnlyDurableDecisions(t *testing.T) {
	root := privateTestRoot(t)
	p := attemptProgress{SchemaVersion: 1, AttemptID: attemptIDFor(strings.Repeat("5", 64)), ActionDispatchID: actionOpaque("assignment", "orchid-"+strings.Repeat("5", 64)),
		DispatchID: "assignment_" + strings.Repeat("e", 64), Issue: dispatchIssue{Repo: "fixture/inbox", Number: 7}, Stage: "registration", Instance: "instance-dead", StartedAt: "2026-10-05T00:00:00.000Z"}
	writeProgress(t, root, p)
	orig := writeProgressRecord
	writeProgressRecord = func(string, *receiptOwner, attemptProgress) error { return errMatrix }
	recoverLaunchAttempts(root, nil, true, "instance-live", fixedNow())
	writeProgressRecord = orig
	if _, err := os.Stat(attemptFile(root, "outcome", p.AttemptID)); !os.IsNotExist(err) {
		t.Fatal("recovery published a decision that was not durable")
	}
	recoverLaunchAttempts(root, nil, true, "instance-live", fixedNow().Add(time.Minute))
	if o, _ := readOutcome(t, root, p.AttemptID); o.ReasonCode != "launch-interrupted" || o.ObservedAt != stamp(fixedNow().Add(time.Minute)) {
		t.Fatalf("recovery did not decide on the next pass: %+v", o)
	}
}
