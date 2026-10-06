package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const promptFixtureGoal = "Read the staged fixture assignment in full and carry it out."
const promptFixtureEmpty = "OpenAI Codex (v0.159.3)\n› Anything interesting on the docket?\n"

func promptFixture() promptSnapshot {
	return promptSnapshot{Agent: AgentInfo{Agent: "codex", Name: "fixture-agent", PaneID: "w1:p1", WorkspaceID: "w1", Cwd: "/fixture/project", AgentStatus: "done", StateChangeSeq: 7, InteractiveReady: true}, Screen: promptFixtureEmpty, Stable: true}
}

// nativeAcceptStub stands in for the native acceptance: it owns the one submit.
func nativeAcceptStub(ctx context.Context, _ string, submit func(context.Context) error) error {
	return submit(ctx)
}

// promptHarness drives the readiness gate and records the effects; the native
// acceptance is a stub that submits once and reports its own verdict.
type promptHarness struct {
	reads, submits, accepts, waits int
	readsAtAccept                  int
	read                           func(*promptHarness) (promptSnapshot, error)
	limit                          int
	goal, sent                     string
	verdict                        error
}

func (h *promptHarness) run() error {
	goal := h.goal
	if goal == "" {
		goal = promptFixtureGoal
	}
	return deliverCodexPrompt(context.Background(), promptFixture().Agent, goal, promptCalls{
		observe: func(context.Context) (promptSnapshot, error) { h.reads++; return h.read(h) },
		submit: func(_ context.Context, text string) error {
			h.submits++
			h.sent = text
			return nil
		},
		wait: func(context.Context) bool { h.waits++; return h.waits < h.limit },
		native: func(ctx context.Context, sent string, submit func(context.Context) error) error {
			h.accepts++
			h.readsAtAccept = h.reads
			if err := submit(ctx); err != nil {
				return err
			}
			return h.verdict
		},
	})
}

// A consumed-looking screen after a submit; the delivery never reads it.
func consumedPrompt(sent string) promptSnapshot {
	s := promptFixture()
	s.Agent.AgentStatus = "working"
	s.Agent.StateChangeSeq++
	s.Screen = "› " + sent + "\n• Reading the fixture assignment\n"
	return s
}

func TestCodexPromptRequiresComposerAndInteractiveReadiness(t *testing.T) {
	for _, kind := range []string{"not-ready", "no-composer", "unstable-read", "nonempty-composer", "blocked-dialog"} {
		t.Run(kind, func(t *testing.T) {
			h := &promptHarness{limit: 5, read: func(*promptHarness) (promptSnapshot, error) {
				s := promptFixture()
				switch kind {
				case "not-ready":
					s.Agent.InteractiveReady = false
				case "no-composer":
					s.Screen = "OpenAI Codex (v0.159.3) starting"
				case "unstable-read":
					s.Stable = false
				case "nonempty-composer":
					s.Screen = "› unrelated pending input"
				case "blocked-dialog":
					s.Agent.AgentStatus = "blocked"
					s.Screen = "Action Required"
				}
				return s, nil
			}}
			if h.run() == nil || h.submits != 0 || h.accepts != 0 {
				t.Fatal("an unready or nonempty UI received the goal")
			}
		})
	}
}

func TestCodexPromptReadinessKeepsOriginalOccupant(t *testing.T) {
	h := &promptHarness{limit: 5, read: func(h *promptHarness) (promptSnapshot, error) {
		s := promptFixture()
		if h.reads > 1 {
			s.Agent.Name = "replacement-agent"
		}
		return s, nil
	}}
	if h.run() == nil || h.submits != 0 {
		t.Fatal("new occupant acquired the old assignment")
	}
}

// Past the gate the goal goes once, through the native acceptance, and the
// screen is not read again: the native verdict alone decides delivery.
func TestCodexPromptSubmitsOnceThroughNativeAcceptanceAndReadsNoMoreScreen(t *testing.T) {
	for _, screen := range []string{"OpenAI Codex (v0.159.2)\n› \n", promptFixtureEmpty, "OpenAI Codex (v0.159.3)\n› Ask Codex to do anything\n"} {
		h := &promptHarness{limit: 20, read: func(h *promptHarness) (promptSnapshot, error) {
			s := promptFixture()
			s.Screen = screen
			return s, nil
		}}
		if err := h.run(); err != nil || h.submits != 1 || h.accepts != 1 {
			t.Fatalf("known empty composer %q: err=%v submits=%d accepts=%d", screen, err, h.submits, h.accepts)
		}
		if h.reads != h.readsAtAccept {
			t.Fatal("the screen was read after the native acceptance took over")
		}
		if !strings.Contains(h.sent, promptFixtureGoal) || !strings.HasPrefix(h.sent, "Assignment delivery marker: orch-goal-") {
			t.Fatal("the marked goal was not the submitted text")
		}
	}
	// The native verdict is the delivery's verdict: a refusal is never retried.
	refused := &promptHarness{limit: 20, verdict: errors.New("native-refused"), read: func(*promptHarness) (promptSnapshot, error) { return promptFixture(), nil }}
	if refused.run() == nil || refused.submits != 1 || refused.reads != refused.readsAtAccept {
		t.Fatal("a native refusal was replayed or confirmed from the screen")
	}
}

// With no native acceptance there is no delivery at all: Codex goals are never
// confirmed from the screen.
func TestCodexPromptNeedsNativeAcceptance(t *testing.T) {
	reads, submits := 0, 0
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = errors.New("panicked")
			}
		}()
		err = deliverCodexPrompt(context.Background(), promptFixture().Agent, promptFixtureGoal, promptCalls{
			observe: func(context.Context) (promptSnapshot, error) { reads++; return promptFixture(), nil },
			submit:  func(context.Context, string) error { submits++; return nil },
			wait:    func(context.Context) bool { return true },
		})
	}()
	if err == nil || reads != 0 || submits != 0 {
		t.Fatalf("delivery without native acceptance: err=%v reads=%d submits=%d", err, reads, submits)
	}
}

// A Codex host without Remote Control has no delivery path: it is refused
// before herdr is asked anything, even with an acceptance and a proven resume.
func TestInjectCodexGoalRefusesWithoutRemoteControl(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if os.MkdirAll(bin, 0700) != nil {
		t.Fatal("fixture setup failed")
	}
	calls := filepath.Join(root, "calls")
	t.Setenv("INJECT_FIXTURE_CALLS", calls)
	script := "#!/bin/sh\necho \"$@\" >> \"$INJECT_FIXTURE_CALLS\"\nexit 1\n"
	if os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700) != nil {
		t.Fatal("fixture executable unavailable")
	}
	run := syntheticRemoteRun(t)
	run.ClientVersion, run.Resume = "9.1.0", &codexResumeProof{Producer: strings.Repeat("ab", 32), Connection: "252"}
	h := Host{Name: "fixture-host", Home: root, RemoteRun: run, Acceptance: &codexAcceptance{}}
	if err := h.injectCodexGoal(context.Background(), "w1:p1", promptFixtureGoal, promptFixture().Agent); !errors.Is(err, errPromptUnconfirmed) {
		t.Fatalf("non-Remote-Control Codex delivery was attempted: %v", err)
	}
	if raw, err := os.ReadFile(calls); err == nil && len(raw) > 0 {
		t.Fatalf("herdr was asked for a non-Remote-Control Codex delivery: %s", raw)
	}
	// Control: the same host under Remote Control does reach herdr.
	h.CanonicalCodex = true
	_ = h.injectCodexGoal(context.Background(), "w1:p1", promptFixtureGoal, promptFixture().Agent)
	if raw, _ := os.ReadFile(calls); !strings.Contains(string(raw), "agent get") {
		t.Fatalf("control: the Remote Control delivery did not observe the pane: %q", raw)
	}
}

func TestCodexPromptContextEndsTheGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()
	if err := deliverCodexPrompt(ctx, promptFixture().Agent, promptFixtureGoal, promptCalls{
		observe: func(context.Context) (promptSnapshot, error) { return promptFixture(), nil },
		submit:  func(context.Context, string) error { return nil },
		wait:    func(context.Context) bool { return true },
		native:  nativeAcceptStub,
	}); err == nil {
		t.Fatal("an ended context delivered")
	}
}
