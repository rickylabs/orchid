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
//	readiness  unknown: Herdr's agent-start contract and status are
//	           display-derived for AGY. They may withhold input, never decide
//	           a startup outcome or become a native fact.
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

// agyRegistrationCheck confirms only that the registered pane is still this
// launch's exact occupant: one Herdr agent record, never the screen. AGY's
// Herdr status is display-derived, so it decides no startup outcome here.
// Goal delivery waits on it only to withhold input (waitAgent), and only the
// new native user step confirms the goal.
func (h Host) agyRegistrationCheck(ctx context.Context, label, cwd, pane, ws string) error {
	unknown := func() error { return registrationFailure("", ctx.Err()) }
	if ctx.Err() != nil {
		return unknown()
	}
	a, err := h.agentInfoOf(ctx, pane)
	if err != nil || ctx.Err() != nil || !agyStartupOccupant(a, label, cwd, pane, ws) {
		return unknown()
	}
	return nil
}

func agyStartupOccupant(a AgentInfo, label, cwd, pane, ws string) bool {
	return a.Agent == "agy" && a.PaneID == pane && a.WorkspaceID == ws && a.Cwd == cwd && a.Name == label
}
