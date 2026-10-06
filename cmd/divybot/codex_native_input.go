package main

import (
	"context"
	"strings"
)

// Every Codex input after the initial goal (an operator steer or send, a
// stranded-goal poke, a PR relay, continuation or nudge, a fan-out nudge) is
// submitted natively on the job's bound Remote Control thread through the
// canonical daemon: into the active turn with turn/steer, pinned to that turn,
// or as a new turn with turn/start. The RPC result is the native confirmation.
// Nothing is typed into the pane, Enter is never sent, and an uncertain
// submission is never resent.

const codexInputMaxBytes = acceptanceMaxText

// codexActiveTurn is the bound thread's newest turn id when it is in progress,
// "" when the newest turn has ended or there is none.
func (p *goalRPC) codexActiveTurn() (string, error) {
	raw, err := p.request("thread/turns/list", map[string]any{"threadId": p.thread, "limit": 1, "sortDirection": "desc", "itemsView": "notLoaded"})
	if err != nil {
		return "", err
	}
	var result struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if decodeNativeJSON(raw, &result) != nil || len(result.Data) > 1 {
		return "", goalError("codex-input-turn-unavailable")
	}
	if len(result.Data) == 0 {
		return "", nil
	}
	turn := result.Data[0]
	switch {
	case !privateNativeID(turn.ID):
		return "", goalError("codex-input-turn-unavailable")
	case turn.Status == "inProgress":
		return turn.ID, nil
	case turn.Status == "completed" || turn.Status == "interrupted" || turn.Status == "failed":
		return "", nil
	}
	return "", goalError("codex-input-turn-unavailable")
}

// submitCodexInput delivers text as user input on h's bound thread. It needs
// the canonical Remote Control binding with its proven resume; anything less
// is refused before any effect.
func (h Host) submitCodexInput(ctx context.Context, text string) error {
	if !h.CanonicalCodex || h.RemoteRun == nil || !codexResumeProven(h.RemoteRun) { // guard:codex-input-native-binding
		return errPromptUnconfirmed
	}
	if strings.TrimSpace(text) == "" || len(text) > codexInputMaxBytes {
		return errPromptUnconfirmed
	}
	thread := h.RemoteRun.NativeSessionID
	input := []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}
	return h.withCanonicalConnection(ctx, thread, func(p *goalRPC) error {
		active, err := p.codexActiveTurn()
		if err != nil {
			return errPromptUnconfirmed
		}
		if active != "" {
			raw, err := p.request("turn/steer", map[string]any{"threadId": thread, "input": input, "expectedTurnId": active}) // guard:codex-input-steer-pinned
			var v struct {
				TurnID string `json:"turnId"`
			}
			if err != nil || decodeNativeJSON(raw, &v) != nil || v.TurnID != active {
				return errPromptUnconfirmed
			}
			return nil
		}
		raw, err := p.request("turn/start", map[string]any{"threadId": thread, "input": input})
		var v struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err != nil || decodeNativeJSON(raw, &v) != nil || !privateNativeID(v.Turn.ID) || v.Turn.ID == active {
			return errPromptUnconfirmed
		}
		return nil
	})
}
