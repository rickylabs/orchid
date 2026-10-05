package main

import (
	"context"
	"time"
)

// Native goal-input readiness for the Remote Control Codex TUI, observe-only.
//
// deliverCodexPrompt still decides readiness from today's inputs (Herdr agent
// info plus the visible composer). Beside it, this records in the private
// native-evidence shadow what native evidence alone would decide at the same
// moments: the stored native attachment proof, Herdr's STRUCTURED occupant,
// and the canonical Codex app-server's own status for the prepared thread.
// Nothing reads the record back, and this path never reads the pane.

const (
	shadowReadiness           shadowFact = "goal-input-readiness"
	shadowSiteGoalReadiness              = "goal-readiness"
	shadowInputScreenComposer            = "screen-composer"
	goalReadinessReadBudget              = 5 * time.Second
)

// goalReadinessShadow binds the shadow record to one dispatched job.
type goalReadinessShadow struct {
	shadow *nativeEvidenceShadow
	job    *Job
}

func (g *goalReadinessShadow) record(today string, native shadowVerdict) {
	if g == nil || g.shadow == nil || g.job == nil {
		return
	}
	g.shadow.compareNative(g.job, shadowSiteGoalReadiness, today, []string{shadowInputHerdrAgent, shadowInputScreenComposer}, native)
}

func readinessVerdict(value, reason string) shadowVerdict {
	if value == "unknown" {
		return shadowVerdict{Fact: shadowReadiness, Value: value, Authority: "none", Reason: reason}
	}
	return shadowVerdict{Fact: shadowReadiness, Value: value, Authority: "native", Reason: reason, Live: true}
}

// codexGoalReadiness is the native verdict on whether the attached Codex TUI
// can take the goal input now. Ready needs all three native inputs; anything
// missing, unstable or unreadable is unknown, never ready.
func (h Host) codexGoalReadiness(ctx context.Context, agent AgentInfo, stable bool) shadowVerdict {
	if !codexResumeProven(h.RemoteRun) { // guard:readiness-attachment
		return readinessVerdict("unknown", "codex-tui-attachment-unproven")
	}
	if !stable || agent.Agent != "codex" { // guard:readiness-occupant
		return readinessVerdict("unknown", "occupant-unstable")
	}
	if !agent.InteractiveReady || (agent.AgentStatus != "idle" && agent.AgentStatus != "done") { // guard:readiness-herdr
		return readinessVerdict("not-ready", "herdr-not-ready")
	}
	rctx, cancel := context.WithTimeout(ctx, goalReadinessReadBudget)
	defer cancel()
	status := ""
	err := h.withCanonicalConnection(rctx, h.RemoteRun.NativeSessionID, func(p *goalRPC) error {
		raw, err := p.request("thread/read", map[string]any{"threadId": h.RemoteRun.NativeSessionID, "includeTurns": false})
		var v struct {
			Thread struct {
				ID     string `json:"id"`
				Status struct {
					Type string `json:"type"`
				} `json:"status"`
			} `json:"thread"`
		}
		if err != nil || decodeNativeJSON(raw, &v) != nil || v.Thread.ID != h.RemoteRun.NativeSessionID {
			return goalError("remote-control-binding-invalid")
		}
		status = v.Thread.Status.Type
		return nil
	})
	if err != nil {
		return readinessVerdict("unknown", "native-thread-unreadable")
	}
	switch status { // guard:readiness-thread-status
	case "idle":
		return readinessVerdict("ready", "")
	case "active":
		return readinessVerdict("not-ready", "native-turn-active")
	case "notLoaded":
		return readinessVerdict("unknown", "native-thread-not-loaded")
	case "systemError":
		return readinessVerdict("unknown", "native-thread-system-error")
	}
	return readinessVerdict("unknown", "native-thread-unknown")
}
