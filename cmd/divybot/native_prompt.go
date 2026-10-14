package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var errPromptUnconfirmed = errors.New("goal-prompt-unconfirmed")

type promptSnapshot struct {
	Agent  AgentInfo
	Screen string
	Stable bool
}

type promptCalls struct {
	noConsent bool
	observe   func(context.Context) (promptSnapshot, error)
	submit    func(context.Context, string) error
	enter     func(context.Context) error
	wait      func(context.Context) bool
	now       func() time.Time
	// RC Codex: native acceptance replaces everything after the marker. It
	// owns the single submit; no Enter, replay or screen read follows.
	native func(ctx context.Context, sent string, submit func(context.Context) error) error
	// Observe-only: records today's readiness verdict beside the native one.
	// It gets an immutable snapshot and no delivery context, must return at
	// once, and never changes the decision, the submit or any other effect.
	readiness func(s promptSnapshot, today string, at time.Time)
}

func samePromptOccupant(a, b AgentInfo) bool {
	return a.Agent != "" && a.Name != "" && a.PaneID != "" && a.WorkspaceID != "" &&
		a.Agent == b.Agent && a.Name == b.Name && a.PaneID == b.PaneID && a.WorkspaceID == b.WorkspaceID && a.Cwd == b.Cwd && a.TerminalID == b.TerminalID && sameHerdrScope(a.HerdrScope, b.HerdrScope) && sameNativeSession(a.AgentSession, b.AgentSession)
}

func codexReady(s promptSnapshot) bool {
	return s.Stable && s.Agent.Agent == "codex" && s.Agent.InteractiveReady &&
		(s.Agent.AgentStatus == "idle" || s.Agent.AgentStatus == "done")
}

// The nonce is confined to the submitted prompt and native acceptance proof. Put it
// at both ends so a long wrapped message can lose its head from the visible tail.
func markedCodexPrompt(goal string) (string, string, error) {
	if strings.TrimSpace(goal) == "" || len(goal) > 32*1024 {
		return "", "", errPromptUnconfirmed
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", "", errPromptUnconfirmed
	}
	marker := "orch-goal-" + hex.EncodeToString(nonce[:])
	line := "Assignment delivery marker: " + marker
	return line + "\n" + goal + "\n" + line, marker, nil
}

// Only native acceptance can confirm a Codex prompt. Readiness uses structured
// occupant state; no composer, marker rendering, Enter repair or replay is read.
func deliverCodexPrompt(ctx context.Context, expected AgentInfo, goal string, d promptCalls) error {
	record := func(s promptSnapshot, today string) {
		if d.readiness != nil {
			func() { defer shadowContain(); d.readiness(s, today, time.Now()) }()
		}
	}
	observed := false
	for ctx.Err() == nil {
		s, err := d.observe(ctx)
		if err != nil || !samePromptOccupant(expected, s.Agent) {
			return errPromptUnconfirmed
		}
		today := "not-ready"
		if codexReady(s) {
			today = "ready"
		}
		if !observed {
			record(s, today)
			observed = true
		}
		if codexReady(s) {
			record(s, "ready")
			if d.native == nil || ctx.Err() != nil {
				return errPromptUnconfirmed
			}
			sent, _, err := markedCodexPrompt(goal)
			if err != nil {
				return errPromptUnconfirmed
			}
			return d.native(ctx, sent, func(ctx context.Context) error { return d.submit(ctx, sent) })
		}
		if s.Agent.AgentStatus == "blocked" || !d.wait(ctx) {
			return errPromptUnconfirmed
		}
	}
	return errPromptUnconfirmed
}

// Herdr0.9.1 formats read output as plain text. Older adapters/fixtures may
// expose result.text. Failed/malformed JSON envelopes never become screen proof.
func (h Host) visiblePromptScreen(ctx context.Context, target string) (string, error) {
	out, err := h.herdr(ctx, "pane", "read", target, "--source", "visible", "--lines", "60", "--format", "text")
	if err != nil || len(out) > 64*1024 {
		return "", errPromptUnconfirmed
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		raw, err := herdrUnwrap(out)
		if err != nil {
			return "", errPromptUnconfirmed
		}
		var r struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Text == nil {
			return "", errPromptUnconfirmed
		}
		return *r.Text, nil
	}
	return out, nil
}

func (h Host) promptSnapshot(ctx context.Context, target string) (promptSnapshot, error) {
	before, err := h.agentInfoOf(ctx, target)
	if err != nil {
		return promptSnapshot{}, errPromptUnconfirmed
	}
	return promptSnapshot{Agent: before, Stable: true}, nil
}

func (h Host) injectCodexGoal(ctx context.Context, target, goal string, expected AgentInfo) error {
	return h.withPromptEvents(ctx, expected, func(ctx context.Context, p *promptEvents) error {
		ready, err := h.waitAgent(ctx, expected)
		if err != nil {
			return err
		}
		native := func(ctx context.Context, _ string, submit func(context.Context) error) error {
			before, err := h.agentInfoOf(ctx, target)
			if err != nil || !samePromptOccupant(expected, before) {
				return errPromptUnconfirmed
			}
			if _, err = p.observe(ctx); err != nil {
				return err
			}
			if err = submit(ctx); err != nil {
				return errPromptUnconfirmed
			}
			return awaitNativePrompt(ctx, before, p.observe, p.wait)
		}
		var readiness func(promptSnapshot, string, time.Time)
		if h.CanonicalCodex && h.RemoteRun != nil {
			if h.Acceptance == nil || !codexResumeProven(h.RemoteRun) {
				return errPromptUnconfirmed
			}
			if h.codexGoalReadiness(ctx, ready, true).Value != "ready" {
				return errPromptUnconfirmed
			}
			native = func(ctx context.Context, sent string, submit func(context.Context) error) error {
				if _, err := p.observe(ctx); err != nil {
					return err
				}
				return h.acceptCodexPrompt(ctx, h.Acceptance, sent, submit)
			}
			if h.GoalReadiness != nil {
				readiness = func(s promptSnapshot, today string, at time.Time) {
					h.observeGoalReadiness(s.Agent, s.Stable, today, at) // guard:readiness-structured-only
				}
			}
		}
		return deliverCodexPrompt(ctx, expected, goal, promptCalls{
			native: native, readiness: readiness,
			observe: func(ctx context.Context) (promptSnapshot, error) {
				a, err := h.agentInfoOf(ctx, target)
				return promptSnapshot{Agent: a, Stable: err == nil}, err
			},
			submit: func(ctx context.Context, goal string) error { return h.send(ctx, expected.Name, goal) },
			wait:   p.wait,
		})
	})
}
