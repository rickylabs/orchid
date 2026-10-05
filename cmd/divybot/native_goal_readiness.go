package main

import (
	"context"
	"sync"
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
	wg     sync.WaitGroup // background reads (tests wait on it)
}

func (g *goalReadinessShadow) record(today string, native shadowVerdict, at time.Time) {
	if g == nil || g.shadow == nil || g.job == nil {
		return
	}
	g.shadow.compareNativeAt(g.job, shadowSiteGoalReadiness, today, []string{shadowInputHerdrAgent, shadowInputScreenComposer}, native, at)
}

// observe starts the native read for one recording moment off the launch
// path: it returns at once, and the read runs on its own bounded context,
// never the delivery's, from an immutable copy of the structured occupant.
func (h Host) observeGoalReadiness(agent AgentInfo, stable bool, today string, at time.Time) {
	g := h.GoalReadiness
	if g == nil {
		return
	}
	g.wg.Add(1)
	go func() { // guard:readiness-async
		defer g.wg.Done()
		defer shadowContain()
		ctx, cancel := context.WithTimeout(context.Background(), goalReadinessReadBudget) // guard:readiness-own-context
		defer cancel()
		g.record(today, h.codexGoalReadiness(ctx, agent, stable), at)
	}()
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
	switch agent.AgentStatus { // guard:readiness-herdr
	case "idle", "done":
		if !agent.InteractiveReady {
			return readinessVerdict("not-ready", "herdr-not-ready")
		}
	case "working", "blocked":
		return readinessVerdict("not-ready", "herdr-not-ready")
	default: // missing or unknown is not a definite negative
		return readinessVerdict("unknown", "herdr-status-unknown")
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
		// Turn notices received on this connection outrank a stale idle reply:
		// a turn of this thread that started and has not completed is active.
		pending := map[string]bool{}
		for _, e := range p.turnEvents { // guard:readiness-turn-reconcile
			if e.ThreadID != h.RemoteRun.NativeSessionID {
				continue
			}
			if e.Status == "inProgress" {
				pending[e.ID] = true
			} else {
				delete(pending, e.ID)
			}
		}
		if len(pending) > 0 {
			status = "active"
		}
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
