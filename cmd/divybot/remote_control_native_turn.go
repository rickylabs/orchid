package main

import "encoding/json"

type remoteNativeTurn struct {
	ThreadID string
	ID       string
	Status   string
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
	if n.ThreadID != p.thread {
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
	for i := len(p.turnEvents) - 1; i >= 0; i-- {
		if p.turnEvents[i].ThreadID == p.thread {
			return p.turnEvents[i].ID == id && p.turnEvents[i].Status == status
		}
	}
	// A late/restarted observer can miss a transient notification. The exact
	// canonical persisted full-turn read remains the structured native source;
	// absence of both is unconfirmed. Never fall back to the terminal footer.
	return true
}
