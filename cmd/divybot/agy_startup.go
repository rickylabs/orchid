package main

import (
	"context"
	"encoding/json"
	"unicode/utf8"
)

// AGY startup observation. Orchid never reads the AGY terminal. The installed
// CLI (1.4.12) has no typed signal between process start and its first
// conversation: its official hooks.json events begin at SessionStart, which
// fires when the first prompt starts a conversation, and Herdr's official
// antigravity_cli integration reports session identity only, deriving the
// pane's lifecycle from screen detection. The startup facts are therefore:
//
//	trust      prepared: the per-launch settings overlay names only the exact
//	           canonical checkout in trustedWorkspaces (agy_trust.go).
//	onboarding the CLI's own typed cache field onboardingComplete, read before
//	           any workspace exists: false is blocked, anything else unproven.
//	auth       unknown: the CLI exposes no typed pre-conversation auth state.
//	readiness  Herdr's agent-start contract for the exact occupant. For AGY it
//	           is display-derived, so it may fence input but is never recorded
//	           as a native fact.
//
// Native acceptance stays the new user step in the isolated store
// (agy_prompt.go); a launch without it remains unconfirmed, never retried.
const (
	agyGapReadyEvent = "agy-cli-no-pre-conversation-ready-event"
	agyGapAuthState  = "agy-cli-no-typed-pre-conversation-auth-state"
	agyGapStopError  = "agy-cli-stop-hook-error-untyped"
)

// agyOnboardingMax bounds the one read of the native onboarding cache.
const agyOnboardingMax = 64 * 1024

// Typed refusals before any workspace or agent exists.
var (
	errAGYOnboardingIncomplete = &agentRegistrationFailure{kind: "startup_blocked"}
	errAGYOnboardingUnproven   = matrixReason("agy-onboarding-unproven")
)

// agyOnboardingComplete reads only the CLI's typed onboardingComplete field
// (json:"onboardingComplete" in its cache struct). Other fields are ignored;
// a missing, non-boolean, duplicated or oversized value is unproven.
func agyOnboardingComplete(raw []byte) error {
	var cache map[string]json.RawMessage
	if len(raw) == 0 || len(raw) > agyOnboardingMax || !utf8.Valid(raw) || strictJSON(raw, &cache) != nil || cache == nil {
		return errAGYOnboardingUnproven
	}
	var complete *bool
	if json.Unmarshal(cache["onboardingComplete"], &complete) != nil || complete == nil {
		return errAGYOnboardingUnproven
	}
	if !*complete {
		return errAGYOnboardingIncomplete
	}
	return nil
}

// agyRegistrationCheck confirms that the registered pane is still this launch's
// exact occupant. It reads Herdr's agent record once and never the screen.
// Herdr's blocked status may only withhold the goal; idle/ready is Herdr's
// display-derived start contract, and goal acceptance still needs native proof.
func (h Host) agyRegistrationCheck(ctx context.Context, label, cwd, pane, ws string) error {
	unknown := func() error { return registrationFailure("", ctx.Err()) }
	if ctx.Err() != nil {
		return unknown()
	}
	a, err := h.agentInfoOf(ctx, pane)
	if err != nil || ctx.Err() != nil || !agyStartupOccupant(a, label, cwd, pane, ws) {
		return unknown()
	}
	switch {
	case a.AgentStatus == "blocked":
		return &agentRegistrationFailure{kind: "startup_blocked"}
	case a.AgentStatus == "idle" && a.InteractiveReady:
		return nil
	}
	return unknown()
}

func agyStartupOccupant(a AgentInfo, label, cwd, pane, ws string) bool {
	return a.Agent == "agy" && a.PaneID == pane && a.WorkspaceID == ws && a.Cwd == cwd && a.Name == label
}
