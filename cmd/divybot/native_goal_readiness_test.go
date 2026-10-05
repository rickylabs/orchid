package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The native readiness verdict uses the stored attachment proof, Herdr's
// structured occupant and the canonical daemon's thread status only.
func TestCodexGoalReadinessNativeVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, status, value, reason string
		agent                       func(*AgentInfo)
		stable, unproven, broken    bool
		wantRead                    bool
	}{
		{name: "idle-thread-ready", status: "idle", value: "ready", stable: true, wantRead: true},
		{name: "active-turn", status: "active", value: "not-ready", reason: "native-turn-active", stable: true, wantRead: true},
		{name: "not-loaded", status: "notLoaded", value: "unknown", reason: "native-thread-not-loaded", stable: true, wantRead: true},
		{name: "system-error", status: "systemError", value: "unknown", reason: "native-thread-system-error", stable: true, wantRead: true},
		{name: "unknown-status", status: "paused", value: "unknown", reason: "native-thread-unknown", stable: true, wantRead: true},
		{name: "thread-unreadable", status: "idle", value: "unknown", reason: "native-thread-unreadable", stable: true, broken: true, wantRead: true},
		{name: "herdr-working", status: "idle", value: "not-ready", reason: "herdr-not-ready", stable: true, agent: func(a *AgentInfo) { a.AgentStatus = "working" }},
		{name: "herdr-not-interactive", status: "idle", value: "not-ready", reason: "herdr-not-ready", stable: true, agent: func(a *AgentInfo) { a.InteractiveReady = false }},
		{name: "occupant-unstable", status: "idle", value: "unknown", reason: "occupant-unstable"},
		{name: "other-agent", status: "idle", value: "unknown", reason: "occupant-unstable", stable: true, agent: func(a *AgentInfo) { a.Agent = "claude" }},
		{name: "attachment-unproven", status: "idle", value: "unknown", reason: "codex-tui-attachment-unproven", stable: true, unproven: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := provenRun(t)
			if tc.unproven {
				run.Resume = nil
			}
			var mu sync.Mutex
			reads := 0
			h := canonicalFixtureHost(t, "multi", func(method string, params map[string]any) any {
				if method != "thread/read" {
					return map[string]any{}
				}
				mu.Lock()
				reads++
				mu.Unlock()
				th := remoteThreadFixture(run)
				if tc.broken && reads > 1 { // the binding check passes; the readiness read fails
					return map[string]any{"thread": map[string]any{"id": "other"}}
				}
				th["thread"].(map[string]any)["status"] = map[string]string{"type": tc.status}
				return th
			})
			h.RemoteRun = run
			agent := promptFixture().Agent
			if tc.agent != nil {
				tc.agent(&agent)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			v := h.codexGoalReadiness(ctx, agent, tc.stable)
			if v.Fact != shadowReadiness || v.Value != tc.value || v.Reason != tc.reason {
				t.Fatalf("verdict %+v, want %s/%s", v, tc.value, tc.reason)
			}
			if (v.Value == "ready") != (v.Authority == "native" && v.Live && tc.value == "ready") {
				t.Fatal("a ready verdict without native authority")
			}
			mu.Lock()
			read := reads > 0
			mu.Unlock()
			if read != tc.wantRead {
				t.Fatalf("daemon read=%v, want %v", read, tc.wantRead)
			}
		})
	}
}

// The native verdict never reads the pane, and the hook records only through
// the structured snapshot fields.
func TestCodexGoalReadinessNeverReadsScreen(t *testing.T) {
	src := productionSources(t)["native_goal_readiness.go"]
	for _, banned := range []string{".Screen", "visiblePromptScreen", "promptSnapshot(", `"pane"`, "codexReady", "emptyCodexComposer", "codexTrustDialog", "markerVisible"} {
		if strings.Contains(src, banned) {
			t.Fatalf("native readiness reads screen evidence: %s", banned)
		}
	}
	hook := productionSources(t)["native_prompt.go"]
	if !strings.Contains(hook, "h.GoalReadiness.record(today, h.codexGoalReadiness(ctx, s.Agent, s.Stable)) // guard:readiness-structured-only") {
		t.Fatal("readiness hook passes more than the structured occupant")
	}
}

// Observe-only parity: the hook sees today's verdict at the first observation
// and at the gate, and delivery's decisions, bytes and effects are unchanged,
// even when the hook panics.
func TestDeliverCodexPromptReadinessIsObserveOnly(t *testing.T) {
	run := func(readiness func(context.Context, promptSnapshot, string)) (submits int, sent string, err error) {
		reads := 0
		err = deliverCodexPrompt(context.Background(), promptFixture().Agent, promptFixtureGoal, promptCalls{
			observe: func(context.Context) (promptSnapshot, error) {
				reads++
				if reads > 2 {
					return consumedPrompt(sent), nil
				}
				return promptFixture(), nil
			},
			submit:    func(_ context.Context, text string) error { submits++; sent = text; return nil },
			enter:     func(context.Context) error { return errors.New("no enter expected") },
			wait:      func(context.Context) bool { return true },
			now:       func() time.Time { return time.Unix(100, 0) },
			readiness: readiness,
		})
		return
	}
	baseSubmits, baseSent, baseErr := run(nil)
	type call struct {
		today string
		ready bool
	}
	var calls []call
	submits, sent, err := run(func(_ context.Context, s promptSnapshot, today string) {
		calls = append(calls, call{today, s.Stable && s.Agent.InteractiveReady})
	})
	if (err == nil) != (baseErr == nil) || submits != baseSubmits || submits != 1 || len(sent) != len(baseSent) {
		t.Fatalf("readiness hook changed delivery: err=%v/%v submits=%d/%d", err, baseErr, submits, baseSubmits)
	}
	if len(calls) != 2 || calls[0].today != "ready" || calls[1].today != "ready" {
		t.Fatalf("readiness not recorded at first observation and gate: %+v", calls)
	}
	submits, _, err = run(func(context.Context, promptSnapshot, string) { panic("shadow failure") })
	if (err == nil) != (baseErr == nil) || submits != 1 {
		t.Fatal("a failing readiness hook affected delivery")
	}
	// A not-ready first observation is recorded as such; the gate still decides.
	calls = nil
	reads := 0
	_ = deliverCodexPrompt(context.Background(), promptFixture().Agent, promptFixtureGoal, promptCalls{
		observe: func(context.Context) (promptSnapshot, error) {
			reads++
			s := promptFixture()
			if reads == 1 {
				s.Agent.AgentStatus = "working"
			}
			return s, nil
		},
		submit:    func(context.Context, string) error { return nil },
		enter:     func(context.Context) error { return nil },
		wait:      func(context.Context) bool { return reads < 3 },
		now:       func() time.Time { return time.Unix(100, 0) },
		readiness: func(_ context.Context, _ promptSnapshot, today string) { calls = append(calls, call{today: today}) },
	})
	if len(calls) < 1 || calls[0].today != "not-ready" {
		t.Fatalf("first not-ready observation not recorded: %+v", calls)
	}
}

// The shadow records the native readiness verdict beside today's, with closed
// codes, and its readout keeps only closed values.
func TestShadowGoalReadinessComparison(t *testing.T) {
	s := newNativeEvidenceShadow(func() time.Time { return shadowT0 })
	j := shadowFixtureJob(t, "codex")
	g := &goalReadinessShadow{shadow: s, job: j}
	g.record("ready", readinessVerdict("ready", ""))
	g.record("ready", readinessVerdict("not-ready", "native-turn-active"))
	g.record("not-ready", readinessVerdict("unknown", "native-thread-unreadable"))
	l := s.ledger(j)
	if len(l) != 3 {
		t.Fatalf("ledger %d entries", len(l))
	}
	for i, want := range []string{"agree", "disagree", "shadow-unknown"} {
		c := l[i]
		if c.Site != shadowSiteGoalReadiness || c.Agreement != want || !c.ScreenDerived || len(c.Proposed) != 1 || c.Proposed[0].Fact != shadowReadiness {
			t.Fatalf("comparison %d wrong: %+v", i, c)
		}
	}
	out := s.readout(shadowT0)
	found := 0
	for _, r := range out.Runs {
		for _, c := range r.Comparisons {
			if c.Site == shadowSiteGoalReadiness {
				found++
				if c.Today == shadowInputOther || c.Proposed[0].Value == shadowInputOther || c.Proposed[0].Reason == shadowInputOther || c.Provenance[1] != shadowInputScreenComposer {
					t.Fatalf("readout lost a closed readiness code: %+v", c)
				}
			}
		}
	}
	if found != 3 {
		t.Fatalf("readout has %d readiness comparisons", found)
	}
	var nilShadow *goalReadinessShadow
	nilShadow.record("ready", readinessVerdict("ready", "")) // no recorder: no effect
}

// Every reason the native readiness verdict can emit survives the readout's
// closed vocabulary.
func TestGoalReadinessReasonsAreClosed(t *testing.T) {
	for _, reason := range []string{"", "codex-tui-attachment-unproven", "occupant-unstable", "herdr-not-ready", "native-turn-active",
		"native-thread-unreadable", "native-thread-not-loaded", "native-thread-system-error", "native-thread-unknown"} {
		if shadowClosed(reason, shadowClosedReasons) != reason {
			t.Fatalf("readiness reason %q is not a closed readout code", reason)
		}
	}
	for _, value := range []string{"ready", "not-ready", "unknown"} {
		if shadowClosed(value, shadowClosedValues) != value || (value != "unknown" && !shadowToday[shadowSiteGoalReadiness][value]) {
			t.Fatalf("readiness value %q is not closed", value)
		}
	}
}
