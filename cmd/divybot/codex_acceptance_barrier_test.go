package main

import (
	"encoding/json"
	"io"
	"testing"
)

// A callback inserts received frames during reduction, without scheduler sleeps.
func TestAcceptanceBarrierQuiescence(t *testing.T) {
	for _, tc := range []struct {
		name, nextThread, nextState, wantReason string
		following                               int
		item, afterBegin                        bool
	}{
		{name: "idle"},
		{name: "bound-active", nextThread: "barrier-thread", nextState: "active", following: 1, wantReason: "baseline-active"},
		{name: "bound-item", nextThread: "barrier-thread", following: 1, item: true, wantReason: "baseline-active"},
		{name: "foreign-burst", nextThread: "foreign-thread", nextState: "active", following: 3},
		{name: "duplicate-burst", nextThread: "barrier-thread", nextState: "idle", following: 3},
		{name: "exact-budget", nextThread: "barrier-thread", nextState: "idle", following: acceptanceFrameLimit - 1},
		{name: "duplicate-flood", nextThread: "barrier-thread", nextState: "idle", following: acceptanceFrameLimit + 1, wantReason: "barrier-busy"},
		{name: "foreign-flood", nextThread: "foreign-thread", nextState: "active", following: acceptanceFrameLimit + 1, wantReason: "barrier-busy"},
		{name: "after-begin", afterBegin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const thread, generation = "barrier-thread", "barrier-generation"
			pump := &framePump{lines: make(chan pumpLine, 4)}
			p := newGoalRPC(io.Discard, nil, thread)
			p.pump, p.observeOnly = pump, true
			s := newAcceptanceSignals(generation, thread)
			enqueue := func(thread, state string, item bool) {
				method := "thread/status/changed"
				status := map[string]any{"type": state}
				if state == "active" {
					status["activeFlags"] = []string{}
				}
				params := map[string]any{"threadId": thread, "status": status}
				if item {
					method = "item/started"
					params = map[string]any{"threadId": thread, "turnId": "barrier-turn", "item": map[string]any{
						"id": "barrier-item", "type": "userMessage", "content": []any{},
					}}
				}
				b, err := json.Marshal(map[string]any{"method": method, "params": params})
				if err != nil {
					t.Fatal(err)
				}
				seq := pump.received.Add(1)
				pump.lines <- pumpLine{line: b, ok: true, seq: seq}
			}
			remaining := tc.following
			p.onSignal = func(method string, params json.RawMessage) error {
				sig, ok, err := decodeSignal(method, params)
				if !ok || err != nil {
					t.Fatalf("invalid native fixture: recognized=%v error=%v", ok, err)
				}
				if remaining > 0 {
					remaining--
					enqueue(tc.nextThread, tc.nextState, tc.item)
				}
				if sig.Thread != thread {
					return nil
				}
				sig.Generation, sig.Seq = generation, p.lineSeq
				class, refusal := s.classify(sig)
				if refusal == nil {
					s.apply(sig, class)
				}
				return refusal
			}
			enqueue(thread, "idle", false)
			err := p.drainThrough(pump.received.Load())
			submits := 0
			if err == nil {
				s.begin(p.lineSeq)
				submits++
			}
			if tc.afterBegin && err == nil {
				enqueue(thread, "active", false)
				line, ok, readErr := pump.next(&p.lineSeq)
				if !ok || readErr != nil {
					t.Fatalf("post-begin frame unavailable: %v", readErr)
				}
				err = p.unsolicited(line)
				if s.aheadOfAck != 1 {
					t.Fatalf("post-begin frame not classified in flight: %d", s.aheadOfAck)
				}
			}
			if tc.wantReason != "" {
				if err == nil || err.Error() != deliveryRefused(tc.wantReason).Error() || submits != 0 {
					t.Fatalf("barrier error=%v submits=%d received=%d consumed=%d; want %s and zero submits", err, submits, pump.received.Load(), p.lineSeq, tc.wantReason)
				}
				if tc.wantReason == "barrier-busy" && p.lineSeq != acceptanceFrameLimit {
					t.Fatalf("barrier consumed %d frames, want exact budget %d", p.lineSeq, acceptanceFrameLimit)
				}
			} else if err != nil || submits != 1 || len(pump.lines) != 0 {
				t.Fatalf("valid control error=%v submits=%d queued=%d; want one submit and empty queue", err, submits, len(pump.lines))
			}
		})
	}
}
