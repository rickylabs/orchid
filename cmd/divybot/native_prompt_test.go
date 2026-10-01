package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

const promptFixtureGoal = "Read the staged fixture assignment in full and carry it out."
const promptFixtureEmpty = "OpenAI Codex (v0.159.3)\n› Anything interesting on the docket?\n"

func promptFixture() promptSnapshot {
	return promptSnapshot{Agent: AgentInfo{Agent: "codex", Name: "fixture-agent", PaneID: "w1:p1", WorkspaceID: "w1", Cwd: "/fixture/project", AgentStatus: "done", StateChangeSeq: 7, InteractiveReady: true}, Screen: promptFixtureEmpty, Stable: true}
}

type promptHarness struct {
	reads, submits, enters, waits int
	time                          time.Time
	read                          func(*promptHarness) (promptSnapshot, error)
	limit                         int
}

func (h *promptHarness) run() error {
	h.time = time.Unix(100, 0)
	return deliverCodexPrompt(context.Background(), promptFixture().Agent, promptFixtureGoal, promptCalls{
		observe: func(context.Context) (promptSnapshot, error) { h.reads++; return h.read(h) },
		submit:  func(context.Context, string) error { h.submits++; return nil },
		enter:   func(context.Context) error { h.enters++; return nil },
		wait:    func(context.Context) bool { h.waits++; h.time = h.time.Add(2 * time.Second); return h.waits < h.limit },
		now:     func() time.Time { return h.time },
	})
}
func consumedPrompt() promptSnapshot {
	s := promptFixture()
	s.Agent.AgentStatus = "working"
	s.Agent.StateChangeSeq++
	s.Screen = "› " + promptFixtureGoal + "\n• Reading the fixture assignment\n"
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
			if h.run() == nil || h.submits != 0 || h.enters != 0 {
				t.Fatal("unready/nonempty UI received a goal or an approval answer")
			}
		})
	}
}
func TestCodexPromptBootDoneDoesNotConfirmOrReplay(t *testing.T) {
	h := &promptHarness{limit: 20, read: func(h *promptHarness) (promptSnapshot, error) {
		s := promptFixture()
		if h.submits > 0 {
			s.Agent.StateChangeSeq++
		}
		return s, nil
	}}
	if h.run() == nil || h.submits != 1 || h.enters != 0 {
		t.Fatal("boot done certified the missing prompt or licensed replay")
	}
}
func TestCodexPromptLostFirstSubmissionRetriesOnce(t *testing.T) {
	h := &promptHarness{limit: 20, read: func(h *promptHarness) (promptSnapshot, error) {
		if h.submits == 2 {
			return consumedPrompt(), nil
		}
		return promptFixture(), nil
	}}
	if err := h.run(); err != nil || h.submits != 2 || h.enters != 0 {
		t.Fatal("proved unchanged empty composer was not repaired exactly once")
	}
}
func TestCodexPromptRetainedComposerGetsOnlyEnter(t *testing.T) {
	h := &promptHarness{limit: 20, read: func(h *promptHarness) (promptSnapshot, error) {
		if h.enters > 0 {
			return consumedPrompt(), nil
		}
		s := promptFixture()
		if h.submits > 0 {
			s.Screen = "› " + promptFixtureGoal
		}
		return s, nil
	}}
	if err := h.run(); err != nil || h.submits != 1 || h.enters != 1 {
		t.Fatal("existing composer was double-pasted or not submitted")
	}
}
func TestCodexPromptAmbiguousEffectsNeverLicenseReplay(t *testing.T) {
	for _, kind := range []string{"session", "sequence", "working-without-text", "changed-pane", "changed-name", "read-failed", "changed-screen"} {
		t.Run(kind, func(t *testing.T) {
			h := &promptHarness{limit: 20, read: func(h *promptHarness) (promptSnapshot, error) {
				s := promptFixture()
				if h.submits == 0 {
					return s, nil
				}
				switch kind {
				case "session":
					s.Agent.AgentSession = []byte(`{"source":"herdr:codex","value":"fixture-thread"}`)
				case "sequence":
					s.Agent.StateChangeSeq++
				case "working-without-text":
					s.Agent.AgentStatus = "working"
					s.Agent.StateChangeSeq++
				case "changed-pane":
					s.Agent.PaneID = "w2:p1"
				case "changed-name":
					s.Agent.Name = "different-agent"
				case "read-failed":
					return promptSnapshot{}, errors.New("private fixture error")
				case "changed-screen":
					s.Screen += "• Starting a thread\n"
				}
				return s, nil
			}}
			if h.run() == nil || h.submits != 1 || h.enters != 0 {
				t.Fatal("ambiguous prompt effect was accepted or replayed")
			}
		})
	}
}
func TestCodexPromptExhaustedRepairFailsLoudly(t *testing.T) {
	h := &promptHarness{limit: 20, read: func(*promptHarness) (promptSnapshot, error) { return promptFixture(), nil }}
	if h.run() == nil || h.submits != 2 {
		t.Fatal("lost second submission did not fail after one retry")
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
func TestCodexPromptVersionsAndFastTurn(t *testing.T) {
	for _, screen := range []string{"OpenAI Codex (v0.159.2)\n› \n", promptFixtureEmpty, "OpenAI Codex (v0.159.3)\n› Ask Codex to do anything\n"} {
		h := &promptHarness{limit: 20, read: func(h *promptHarness) (promptSnapshot, error) {
			s := promptFixture()
			s.Screen = screen
			if h.submits > 0 {
				s.Agent.StateChangeSeq++
				s.Screen = "› " + promptFixtureGoal + "\n• Fixture work completed\n› Ask Codex to do anything\n"
			}
			return s, nil
		}}
		if err := h.run(); err != nil || h.submits != 1 || h.enters != 0 {
			t.Fatal("version placeholder/fast consumed turn was not confirmed once")
		}
	}
}
func TestCodexPromptDelayedRealConsumptionNeverDoubleSubmits(t *testing.T) {
	h := &promptHarness{limit: 30, read: func(h *promptHarness) (promptSnapshot, error) {
		s := promptFixture()
		if h.submits > 0 {
			s.Screen = "› " + promptFixtureGoal
			s.Agent.AgentStatus = "unknown"
			if h.waits > 12 {
				return consumedPrompt(), nil
			}
		}
		return s, nil
	}}
	if err := h.run(); err != nil || h.submits != 1 || h.enters != 0 {
		t.Fatal("delayed visible prompt was replayed")
	}
}

func TestCodexPromptFooterCannotCertifyRetainedComposer(t *testing.T) {
	s := promptFixture()
	s.Screen = "› " + promptFixtureGoal + "\n• Startup tip after the input box\n"
	if codexPromptConsumed(s, promptFixtureGoal) {
		t.Fatal("unrelated footer certified a retained composer")
	}
}
