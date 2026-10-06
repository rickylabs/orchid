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
	observe func(context.Context) (promptSnapshot, error)
	submit  func(context.Context, string) error
	wait    func(context.Context) bool
	// Codex runs only under Remote Control: its native acceptance owns the
	// single submit and decides delivery; no Enter, replay or screen read follows.
	native func(ctx context.Context, sent string, submit func(context.Context) error) error
	// Observe-only: records today's readiness verdict beside the native one.
	// It gets an immutable snapshot and no delivery context, must return at
	// once, and never changes the decision, the submit or any other effect.
	readiness func(s promptSnapshot, today string, at time.Time)
}

func samePromptOccupant(a, b AgentInfo) bool {
	return a.Agent != "" && a.Name != "" && a.PaneID != "" && a.WorkspaceID != "" &&
		a.Agent == b.Agent && a.Name == b.Name && a.PaneID == b.PaneID && a.WorkspaceID == b.WorkspaceID && a.Cwd == b.Cwd
}

// These are UI placeholders, never route/model choices. Unknown/nonempty input refuses.
func emptyCodexComposer(screen string) bool {
	at := strings.LastIndex(screen, "›")
	if at < 0 || codexTrustDialog(screen) {
		return false
	}
	line := strings.TrimSpace(strings.SplitN(screen[at+len("›"):], "\n", 2)[0])
	switch line {
	case "", "Ask Codex to do anything", "Anything interesting on the docket?":
		return true
	}
	return false
}

func codexReady(s promptSnapshot) bool {
	return s.Stable && s.Agent.Agent == "codex" && s.Agent.InteractiveReady &&
		(s.Agent.AgentStatus == "idle" || s.Agent.AgentStatus == "done") && emptyCodexComposer(s.Screen)
}

// The nonce is confined to the submitted prompt and private screen proof. Put it
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
func markerVisible(screen, marker string) bool {
	// Terminal wrapping can split the nonce mid-word. Only this fresh nonce, not
	// arbitrary goal prose, is matched after stripping display whitespace.
	return marker != "" && strings.Contains(strings.Join(strings.Fields(screen), ""), marker)
}

// The readiness gate (until the native verdict replaces it), then the single
// native-accepted submission. Nothing is ever replayed or confirmed from the screen.
func deliverCodexPrompt(ctx context.Context, expected AgentInfo, goal string, d promptCalls) error {
	if d.native == nil { // guard:delivery-native-only
		return errPromptUnconfirmed
	}
	var before, prior promptSnapshot
	stable, observed := false, false
	recordReadiness := func(s promptSnapshot, today string) {
		if d.readiness != nil {
			func() {
				defer shadowContain()
				d.readiness(s, today, time.Now())
			}()
		}
	}
	for {
		if ctx.Err() != nil {
			return errPromptUnconfirmed
		}
		s, err := d.observe(ctx)
		if err != nil || !samePromptOccupant(expected, s.Agent) {
			return errPromptUnconfirmed
		}
		if !observed { // guard:readiness-first-observation
			observed = true
			today := "not-ready"
			if codexReady(s) {
				today = "ready"
			}
			recordReadiness(s, today)
		}
		if codexTrustDialog(s.Screen) {
			// Remote Control never answers a trust dialog on the operator's behalf.
			return errPromptUnconfirmed // guard:trust-dialog-refused
		} else if codexReady(s) {
			if stable && samePromptOccupant(prior.Agent, s.Agent) && prior.Agent.StateChangeSeq == s.Agent.StateChangeSeq && prior.Screen == s.Screen {
				before = s
				break
			}
			prior, stable = s, true
		} else {
			stable = false
			if s.Agent.AgentStatus == "blocked" {
				return errPromptUnconfirmed
			}
		}
		if !d.wait(ctx) {
			return errPromptUnconfirmed
		}
	}
	recordReadiness(before, "ready") // guard:readiness-at-gate
	if ctx.Err() != nil {
		return errPromptUnconfirmed
	}
	sent, marker, err := markedCodexPrompt(goal)
	if err != nil || markerVisible(before.Screen, marker) {
		return errPromptUnconfirmed
	}
	return d.native(ctx, sent, func(ctx context.Context) error { return d.submit(ctx, sent) }) // guard:native-branch
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
	screen, err := h.visiblePromptScreen(ctx, target)
	if err != nil {
		return promptSnapshot{}, errPromptUnconfirmed
	}
	after, err := h.agentInfoOf(ctx, target)
	if err != nil || !samePromptOccupant(before, after) {
		return promptSnapshot{}, errPromptUnconfirmed
	}
	return promptSnapshot{Agent: after, Screen: screen, Stable: before.StateChangeSeq == after.StateChangeSeq}, nil
}

func (h Host) injectCodexGoal(ctx context.Context, target, goal string, expected AgentInfo) error {
	var native func(context.Context, string, func(context.Context) error) error
	var readiness func(promptSnapshot, string, time.Time)
	// Codex runs only under Remote Control; there is no screen-confirmed delivery.
	if !h.CanonicalCodex || h.RemoteRun == nil { // guard:codex-rc-only
		return errPromptUnconfirmed
	}
	if h.Acceptance == nil {
		return errPromptUnconfirmed
	}
	// The attached TUI was proven natively once before delivery (stored with
	// its pin); the native acceptance then binds the goal to exactly this thread.
	if !codexResumeProven(h.RemoteRun) { // guard:delivery-stored-proof
		return errPromptUnconfirmed
	}
	native = func(ctx context.Context, sent string, submit func(context.Context) error) error {
		return h.acceptCodexPrompt(ctx, h.Acceptance, sent, submit)
	}
	if h.GoalReadiness != nil {
		readiness = func(s promptSnapshot, today string, at time.Time) {
			h.observeGoalReadiness(s.Agent, s.Stable, today, at) // guard:readiness-structured-only
		}
	}
	return deliverCodexPrompt(ctx, expected, goal, promptCalls{
		native:    native,
		readiness: readiness,
		observe: func(ctx context.Context) (promptSnapshot, error) {
			s, err := h.promptSnapshot(ctx, target)
			if h.RemoteRun != nil && ctx.Err() != nil {
				return promptSnapshot{}, errPromptUnconfirmed
			}
			return s, err
		},
		submit: func(ctx context.Context, goal string) error {
			_, err := h.herdr(ctx, "agent", "prompt", target, goal)
			return err
		},
		wait: nativePromptWait,
	})
}
