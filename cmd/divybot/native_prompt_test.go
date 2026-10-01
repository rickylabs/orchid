package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
	goal, sent                    string
}

func (h *promptHarness) run() error {
	h.time = time.Unix(100, 0)
	goal := h.goal
	if goal == "" {
		goal = promptFixtureGoal
	}
	return deliverCodexPrompt(context.Background(), promptFixture().Agent, goal, promptCalls{
		observe: func(context.Context) (promptSnapshot, error) { h.reads++; return h.read(h) },
		submit: func(_ context.Context, text string) error {
			h.submits++
			if h.sent != "" && h.sent != text {
				return errors.New("changed repair nonce")
			}
			h.sent = text
			return nil
		},
		enter: func(context.Context) error { h.enters++; return nil },
		wait:  func(context.Context) bool { h.waits++; h.time = h.time.Add(2 * time.Second); return h.waits < h.limit },
		now:   func() time.Time { return h.time },
	})
}
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
			return consumedPrompt(h.sent), nil
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
			return consumedPrompt(h.sent), nil
		}
		s := promptFixture()
		if h.submits > 0 {
			s.Screen = "› " + h.sent
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
				s.Screen = "› " + h.sent + "\n• Fixture work completed\n› Ask Codex to do anything\n"
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
			s.Screen = "› " + h.sent
			s.Agent.AgentStatus = "unknown"
			if h.waits > 12 {
				return consumedPrompt(h.sent), nil
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
	sent, marker, err := markedCodexPrompt(promptFixtureGoal)
	if err != nil {
		t.Fatal(err)
	}
	s.Screen = "› " + sent + "\n• Startup tip after the input box\n"
	if codexPromptConsumed(s, promptFixture(), marker) {
		t.Fatal("unrelated footer certified a retained composer")
	}
}

// This is the actual worker template with the permitted 4000-character body,
// not the short one-line fixture used by the earlier delivery controls.
func realisticPromptGoal() string {
	body := strings.Repeat("Read the fixture implementation and its evidence before changing behavior.\n", 80)
	return renderGoal("fixture/inbox", "fixture/project", "fixture", "Fixture long goal", body, "/fixture/project", "fixture-branch", "", 42)
}
func wrapPromptAndTakeTail(text string, width, lines int) string {
	var rows []string
	for _, line := range strings.Split(text, "\n") {
		chars := []rune(line)
		for len(chars) > width {
			rows = append(rows, string(chars[:width]))
			chars = chars[width:]
		}
		rows = append(rows, string(chars))
	}
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	return strings.Join(rows, "\n")
}
func TestCodexPromptRealisticLongWrappedGoalAndVisibleTail(t *testing.T) {
	for _, width := range []int{100, 17} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			goal := realisticPromptGoal()
			if len(goal) < 4000 || strings.Count(goal, "\n") < 40 {
				t.Fatal("long-goal control is not realistic")
			}
			h := &promptHarness{goal: goal, limit: 20, read: func(h *promptHarness) (promptSnapshot, error) {
				if h.submits == 0 {
					return promptFixture(), nil
				}
				s := consumedPrompt(h.sent)
				s.Screen = wrapPromptAndTakeTail(s.Screen, width, 60)
				if strings.Contains(s.Screen, goal) {
					t.Fatal("fixture did not cut/wrap the whole goal")
				}
				return s, nil
			}}
			if err := h.run(); err != nil || h.submits != 1 || h.enters != 0 {
				t.Fatal("long wrapped consumed assignment was falsely blocked/replayed", err)
			}
		})
	}
}
func TestCodexPromptCollapsedLongPasteGetsEnterWithoutRepaste(t *testing.T) {
	h := &promptHarness{goal: realisticPromptGoal() + " Unicode fixture é", limit: 20, read: func(h *promptHarness) (promptSnapshot, error) {
		if h.submits == 0 {
			return promptFixture(), nil
		}
		if h.enters > 0 {
			s := consumedPrompt(h.sent)
			s.Screen = wrapPromptAndTakeTail(s.Screen, 100, 60)
			return s, nil
		}
		s := promptFixture()
		s.Screen = fmt.Sprintf("› [Pasted Content %d chars]\n", utf8.RuneCountInString(h.sent))
		return s, nil
	}}
	if err := h.run(); err != nil || h.submits != 1 || h.enters != 1 {
		t.Fatal("retained collapsed paste was blocked or double pasted", err)
	}
}
func TestCodexPromptMarkerAndNewSequenceAreBothRequired(t *testing.T) {
	sent, marker, err := markedCodexPrompt(realisticPromptGoal())
	if err != nil {
		t.Fatal(err)
	}
	before := promptFixture()
	s := consumedPrompt(sent)
	s.Agent.StateChangeSeq = before.Agent.StateChangeSeq
	if codexPromptConsumed(s, before, marker) {
		t.Fatal("unchanged working state certified a prompt")
	}
	s.Agent.StateChangeSeq++
	s.Screen = "› [Pasted Content 5684 chars]\n• Working\n"
	if codexPromptConsumed(s, before, marker) {
		t.Fatal("generic paste placeholder certified unseen assignment")
	}
	s.Screen = "› Assignment delivery marker: orch-goal-wrong\n• Working\n"
	if codexPromptConsumed(s, before, marker) {
		t.Fatal("another marker certified this assignment")
	}
	s.Screen = "› " + sent
	s.Agent.AgentStatus = "done"
	if codexPromptConsumed(s, before, marker) {
		t.Fatal("retained marked composer certified a completed turn")
	}
}
func TestCodexPromptHiddenMarkerNeverCertifiesOrReplays(t *testing.T) {
	h := &promptHarness{goal: realisticPromptGoal(), limit: 20, read: func(h *promptHarness) (promptSnapshot, error) {
		s := promptFixture()
		if h.submits > 0 {
			s.Agent.StateChangeSeq++
			s.Agent.AgentStatus = "working"
			s.Screen = "› [Pasted Content 5684 chars]\n• Working\n"
		}
		return s, nil
	}}
	if h.run() == nil || h.submits != 1 || h.enters != 0 {
		t.Fatal("unseen nonce was accepted or ambiguous active turn was replayed")
	}
}

func TestCodexPromptRetainedBodyComposerGlyphIsNotALaterComposer(t *testing.T) {
	sent, marker, err := markedCodexPrompt(realisticPromptGoal() + "\n› Ask Codex to do anything\n")
	if err != nil {
		t.Fatal(err)
	}
	s := consumedPrompt(sent)
	s.Agent.AgentStatus = "done"
	if codexPromptConsumed(s, promptFixture(), marker) {
		t.Fatal("composer glyph inside unsubmitted text certified delivery")
	}
}
