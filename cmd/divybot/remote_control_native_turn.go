package main

import "encoding/json"

type remoteNativeTurn struct {
	ThreadID string
	ID       string
	Status   string
}

type remoteNativeTurnProof struct {
	remoteNativeTurn
	Events int
}

// Canonical notifications are preferred live evidence. They are scoped to the
// exact native thread; foreign daemon tenants never affect its lifecycle. They
// cannot replace full native turn/tool/queue/descendant readback for Done.
func (p *goalRPC) nativeTurnNotification(method string, params json.RawMessage) error {
	var n struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if decodeNativeJSON(params, &n) != nil || !privateNativeID(n.ThreadID) {
		return goalError("goal-response-invalid")
	}
	if n.ThreadID != p.thread && p.turnProofs[n.ThreadID].ThreadID != n.ThreadID {
		return nil
	}
	if !privateNativeID(n.Turn.ID) {
		return goalError("goal-response-invalid")
	}
	if method == "turn/started" && n.Turn.Status != "inProgress" || method == "turn/completed" && n.Turn.Status != "completed" && n.Turn.Status != "failed" && n.Turn.Status != "interrupted" {
		return goalError("goal-response-invalid")
	}
	if len(p.turnEvents) >= 128 {
		return goalError("goal-notification-limit")
	}
	p.turnEvents = append(p.turnEvents, remoteNativeTurn{n.ThreadID, n.Turn.ID, n.Turn.Status})
	return nil
}

func (p *goalRPC) nativeTurnAgrees(id, status string) bool {
	return p.scopedNativeTurnAgrees(p.thread, id, status)
}

func (p *goalRPC) scopedNativeTurnAgrees(thread, id, status string) bool {
	for i := len(p.turnEvents) - 1; i >= 0; i-- {
		if p.turnEvents[i].ThreadID == thread {
			return p.turnEvents[i].ID == id && p.turnEvents[i].Status == status
		}
	}
	// A late/restarted observer can miss a transient notification. The exact
	// canonical persisted full-turn read remains the structured native source;
	// absence of both is unconfirmed. Never fall back to the terminal footer.
	return true
}

// Only validated full native turn reads enter this set. Metadata-only reads of
// other daemon tenants confer neither work authority nor a lifecycle proof.
func (p *goalRPC) recordNativeTurnProof(id, status string) error {
	next := remoteNativeTurnProof{remoteNativeTurn{p.thread, id, status}, len(p.turnEvents)}
	if err := p.reconcileNativeProofRefresh(next); err != nil {
		return err
	}
	if p.turnProofs == nil {
		p.turnProofs = map[string]remoteNativeTurnProof{}
	}
	p.turnProofs[p.thread] = next
	return nil
}

// A new full terminal read can resolve a coherent turn transition, but cannot
// erase an unfinished different turn or hide contradictions behind the same
// old terminal response. Check before changing any proof or event cursor.
func (p *goalRPC) reconcileNativeProofRefresh(next remoteNativeTurnProof) error {
	pending := map[string]bool{}
	for _, event := range p.turnEvents {
		if event.ThreadID != next.ThreadID {
			continue
		}
		if event.Status == "inProgress" {
			pending[event.ID] = true
		} else {
			delete(pending, event.ID)
		}
	}
	if len(pending) != 0 {
		return goalError("remote-control-work-unconfirmed")
	}
	if err := p.reconcileScopedTurnProof(next.ThreadID, next); err != nil {
		return err
	}
	for thread, old := range p.turnProofs {
		if thread == next.ThreadID && (old.ID != next.ID || old.Status != next.Status) {
			continue // validated new terminal proof; all started turns are resolved
		}
		if err := p.reconcileScopedTurnProof(thread, old); err != nil {
			return err
		}
	}
	return nil
}

// Reconcile after the final native read: that read may have consumed a newer
// notice than the terminal proof. Later reads require another reconciliation.
// A previously verified child remains in scope during subsequent root reads.
func (p *goalRPC) reconcileNativeLifecycle() error {
	for thread, proof := range p.turnProofs {
		if err := p.reconcileScopedTurnProof(thread, proof); err != nil {
			return err
		}
	}
	return nil
}

func (p *goalRPC) reconcileScopedTurnProof(thread string, proof remoteNativeTurnProof) error {
	if !p.scopedNativeTurnAgrees(thread, proof.ID, proof.Status) {
		return goalError("remote-control-work-unconfirmed")
	}
	// A later matching notice cannot erase an intervening contradiction.
	for _, event := range p.turnEvents[proof.Events:] {
		if event.ThreadID == thread && (event.ID != proof.ID || event.Status != proof.Status) {
			return goalError("remote-control-work-unconfirmed")
		}
	}
	return nil
}
