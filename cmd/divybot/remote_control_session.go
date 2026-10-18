package main

import (
	"context"
	"strconv"
)

// A Remote Control Codex TUI is a client of the shared Codex app-server, which
// runs vendor hooks without the pane's Herdr environment. Neither the bundled
// Herdr Codex integration nor the lifecycle hook can then report the thread, so
// the pane never carries a native session and observers cannot join the seat to
// its dispatch (no terminal, no seat binding). Once the native status proof
// holds, publish exactly that proven thread in the job's Herdr session, as the
// bundled integration would: identity only, never a lifecycle state.
func publishCodexSession(ctx context.Context, label string, run *remoteControlRun, location *dispatchLocation,
	herdr func(context.Context, ...string) (string, error), seq int64) error {
	if run == nil || location == nil || run.IdentitySource != "codex-native-status" || !privateNativeID(run.NativeSessionID) || seq <= 0 {
		return goalError("remote-control-identity-unconfirmed")
	}
	if _, err := herdr(ctx, "pane", "report-agent-session", location.PaneID, "--source", "herdr:codex", "--agent", "codex",
		"--seq", strconv.FormatInt(seq, 10), "--agent-session-id", run.NativeSessionID); err != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	out, err := herdr(ctx, "agent", "get", location.PaneID)
	if err != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	raw, err := herdrUnwrap(out)
	if err != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	if id, reason := nativeSessionFromResponse(raw, "agent_info", "codex", label, location); reason != "" || id != run.NativeSessionID {
		return goalError("remote-control-hook-mismatch")
	}
	return nil
}
