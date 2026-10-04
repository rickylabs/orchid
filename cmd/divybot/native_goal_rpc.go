package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
)

// Closed diagnostics only: native protocol bodies and process errors are private.
type goalError string

func (e goalError) Error() string { return string(e) }

type nativeGoal struct {
	ThreadID    string `json:"threadId"`
	Objective   string `json:"objective"`
	Status      string `json:"status"`
	TokenBudget *int64 `json:"tokenBudget"`
	TokensUsed  int64  `json:"tokensUsed"`
	SecondsUsed int64  `json:"timeUsedSeconds"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

func goalStatus(s string) bool {
	switch s {
	case "active", "paused", "blocked", "usageLimited", "budgetLimited", "complete":
		return true
	}
	return false
}

const maxGoalNumber int64 = 9007199254740991

func goalNumber(n int64) bool { return n >= 0 && n <= maxGoalNumber }

// A separate write capability. Harness's read-only transport is not widened.
type goalRPC struct {
	ctx        context.Context
	input      io.Writer
	scan       *bufio.Scanner
	serial     int
	thread     string
	updates    []*nativeGoal
	turnEvents []remoteNativeTurn
	turnProofs map[string]remoteNativeTurnProof
	shadow     *shadowPublisher
	// Optional ordered journal for scoped turn notices; nil for other callers.
	onTurnNotice func(remoteNativeTurn) error
}

func newGoalRPC(input io.Writer, output io.Reader, thread string) *goalRPC {
	scan := bufio.NewScanner(output)
	scan.Buffer(make([]byte, 4096), 1048577)
	return &goalRPC{input: input, scan: scan, thread: thread}
}
func (p *goalRPC) send(v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return goalError("goal-request-invalid")
	}
	b = append(b, '\n')
	n, e := p.input.Write(b)
	if e != nil || n != len(b) {
		return goalError("goal-transport-unavailable")
	}
	return nil
}
func (p *goalRPC) frame() (map[string]json.RawMessage, error) {
	if !p.scan.Scan() {
		return nil, goalError("goal-source-closed-or-frame-limit")
	}
	var m map[string]json.RawMessage
	if decodeNativeJSON(p.scan.Bytes(), &m) != nil || m == nil {
		return nil, goalError("goal-response-invalid")
	}
	return m, nil
}
func (p *goalRPC) notification(m map[string]json.RawMessage) error {
	var method string
	if json.Unmarshal(m["method"], &method) != nil {
		return goalError("goal-response-invalid")
	}
	if method == "turn/started" || method == "turn/completed" {
		return p.nativeTurnNotification(method, m["params"])
	}
	if method != "thread/goal/updated" && method != "thread/goal/cleared" {
		return nil
	}
	var n struct {
		ThreadID string          `json:"threadId"`
		Goal     json.RawMessage `json:"goal"`
	}
	var fields map[string]json.RawMessage
	if decodeNativeJSON(m["params"], &fields) != nil || fields == nil || json.Unmarshal(m["params"], &n) != nil {
		return goalError("goal-response-invalid")
	}
	if n.ThreadID != p.thread {
		return nil
	} // Other threads can notify on the same connection.
	if method == "thread/goal/cleared" {
		return goalError("goal-changed-concurrently")
	}
	g, e := decodeGoal(n.Goal, p.thread)
	if e != nil {
		return e
	}
	if len(p.updates) >= 128 {
		return goalError("goal-notification-limit")
	}
	p.updates = append(p.updates, g)
	return nil
}
func (p *goalRPC) request(method string, params any) (json.RawMessage, error) {
	if !goalMethodAllowed(method, p.serial) {
		return nil, goalError("goal-method-refused")
	}
	if method == "thread/start" && p.thread != "" {
		return nil, goalError("goal-method-refused")
	}
	if method != "initialize" && method != "remoteControl/status/read" && method != "thread/start" && method != "thread/loaded/list" {
		b, e := json.Marshal(params)
		var scope struct {
			ThreadID         string `json:"threadId"`
			AncestorThreadID string `json:"ancestorThreadId"`
		}
		if e != nil || json.Unmarshal(b, &scope) != nil || !privateNativeID(p.thread) || ((method != "thread/list" && scope.ThreadID != p.thread) || (method == "thread/list" && scope.AncestorThreadID != p.thread)) {
			return nil, goalError("goal-response-mismatch")
		}
	}
	p.serial++
	id := p.serial
	if e := p.send(map[string]any{"id": id, "method": method, "params": params}); e != nil {
		return nil, e
	}
	for frames := 0; frames < 128; frames++ {
		m, e := p.frame()
		if e != nil {
			return nil, e
		}
		if raw, ok := m["id"]; ok {
			var got int
			_, request := m["method"]
			_, result := m["result"]
			_, failure := m["error"]
			if json.Unmarshal(raw, &got) != nil || got != id || request || result == failure {
				return nil, goalError("goal-response-mismatch")
			}
			if failure {
				var v struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				}
				if json.Unmarshal(m["error"], &v) == nil && v.Code == -32600 && v.Message == "thread not found: "+p.thread {
					return nil, goalError("goal-thread-not-found")
				}
				return nil, goalError("goal-daemon-refused")
			}
			return m["result"], nil
		}
		if e := p.notification(m); e != nil {
			return nil, e
		}
	}
	return nil, goalError("goal-notification-limit")
}

// The newest persisted native turn is the failure source for a retry. A goal
// status or a missing seat alone cannot distinguish failure from success.
func (p *goalRPC) lastTurnFailed() (bool, error) {
	raw, err := p.request("thread/turns/list", map[string]any{
		"threadId": p.thread, "limit": 1, "sortDirection": "desc", "itemsView": "notLoaded",
	})
	if err != nil {
		return false, err
	}
	var result struct {
		Data []json.RawMessage `json:"data"`
	}
	if decodeNativeJSON(raw, &result) != nil || len(result.Data) != 1 {
		return false, goalError("goal-turn-source-unavailable")
	}
	var turn struct {
		Status string `json:"status"`
	}
	if decodeNativeJSON(result.Data[0], &turn) != nil {
		return false, goalError("goal-turn-source-unavailable")
	}
	switch turn.Status {
	case "failed":
		return true, nil
	case "completed", "interrupted", "inProgress":
		return false, nil
	default:
		return false, goalError("goal-turn-source-unavailable")
	}
}

// Installed native schema: newest completed turn, full items and an explicit
// final_answer agent message. Commentary, failed turns and pending questions
// never prove assignment completion. Native text remains private.
func (p *goalRPC) lastTurnCompleted() (bool, error) {
	raw, err := p.request("thread/turns/list", map[string]any{
		"threadId": p.thread, "limit": 1, "sortDirection": "desc", "itemsView": "full",
	})
	if err != nil {
		return false, err
	}
	var result struct {
		Data []json.RawMessage `json:"data"`
	}
	if decodeNativeJSON(raw, &result) != nil || len(result.Data) != 1 {
		return false, goalError("goal-turn-source-unavailable")
	}
	var turn struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		ItemsView   string `json:"itemsView"`
		CompletedAt *int64 `json:"completedAt"`
		Items       []struct {
			Type      string            `json:"type"`
			Phase     string            `json:"phase"`
			Text      string            `json:"text"`
			Questions []json.RawMessage `json:"questions"`
		} `json:"items"`
	}
	if decodeNativeJSON(result.Data[0], &turn) != nil || turn.ID == "" || turn.Status != "completed" ||
		turn.ItemsView != "full" || turn.CompletedAt == nil || *turn.CompletedAt <= 0 || *turn.CompletedAt > time.Now().Unix() ||
		len(turn.Items) == 0 || len(turn.Items) > 512 {
		return false, nil
	}
	if !p.nativeTurnAgrees(turn.ID, turn.Status) {
		return false, nil
	}
	final := false
	for _, item := range turn.Items {
		if len(item.Questions) != 0 {
			return false, nil
		}
		if item.Type == "agentMessage" && item.Phase == "final_answer" && strings.TrimSpace(item.Text) != "" {
			final = true
		}
	}
	if final {
		if err := p.recordNativeTurnProof(turn.ID, turn.Status); err != nil {
			return false, err
		}
	}
	return final, nil
}
func decodeGoal(raw json.RawMessage, thread string) (*nativeGoal, error) {
	var fields map[string]json.RawMessage
	if decodeNativeJSON(raw, &fields) != nil || fields == nil {
		return nil, goalError("goal-response-invalid")
	}
	for _, key := range []string{"threadId", "objective", "status", "tokenBudget", "tokensUsed", "timeUsedSeconds", "createdAt", "updatedAt"} {
		if _, ok := fields[key]; !ok {
			return nil, goalError("goal-response-invalid")
		}
		if key != "tokenBudget" && string(fields[key]) == "null" {
			return nil, goalError("goal-response-invalid")
		}
	}
	var g nativeGoal
	if json.Unmarshal(raw, &g) != nil {
		return nil, goalError("goal-response-invalid")
	}
	if g.ThreadID != thread {
		return nil, goalError("goal-identity-mismatch")
	}
	if !validGoalObjective(g.Objective) || !goalStatus(g.Status) || g.TokenBudget != nil && !goalNumber(*g.TokenBudget) || !goalNumber(g.TokensUsed) || !goalNumber(g.SecondsUsed) || !goalNumber(g.CreatedAt) || !goalNumber(g.UpdatedAt) {
		return nil, goalError("goal-response-invalid")
	}
	return &g, nil
}
func (p *goalRPC) get() (*nativeGoal, error) {
	raw, e := p.request("thread/goal/get", map[string]any{"threadId": p.thread})
	if e != nil {
		return nil, e
	}
	var m map[string]json.RawMessage
	if decodeNativeJSON(raw, &m) != nil {
		return nil, goalError("goal-response-invalid")
	}
	g, ok := m["goal"]
	if !ok {
		return nil, goalError("goal-response-invalid")
	}
	if string(g) == "null" {
		return nil, nil
	}
	return decodeGoal(g, p.thread)
}
func sameGoalIntent(g *nativeGoal, i goalIntent) bool {
	return g != nil && g.Objective == i.Objective && reflect.DeepEqual(g.TokenBudget, i.TokenBudget)
}
func (p *goalRPC) set(params map[string]any, intent goalIntent, status string) (*nativeGoal, error) {
	// Pre-write notifications cannot certify this write, even if values match.
	p.updates = nil
	raw, e := p.request("thread/goal/set", params)
	if e != nil {
		return nil, e
	}
	var m map[string]json.RawMessage
	if decodeNativeJSON(raw, &m) != nil {
		return nil, goalError("goal-response-invalid")
	}
	g, e := decodeGoal(m["goal"], p.thread)
	if e != nil {
		return nil, e
	}
	if !sameGoalIntent(g, intent) || g.Status != status {
		return nil, goalError("goal-write-mismatch")
	}
	for frames := 0; frames < 128; frames++ {
		for _, n := range p.updates {
			if reflect.DeepEqual(g, n) {
				return g, nil
			}
		}
		m, e := p.frame()
		if e != nil {
			return nil, goalError("goal-notification-unavailable")
		}
		if _, ok := m["id"]; ok {
			return nil, goalError("goal-response-mismatch")
		}
		if e := p.notification(m); e != nil {
			return nil, e
		}
	}
	return nil, goalError("goal-notification-limit")
}
func (p *goalRPC) initialize() error {
	raw, e := p.request("initialize", map[string]any{"clientInfo": map[string]string{"name": "orchid_dispatch_goals", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}})
	if e != nil {
		return e
	}
	var v struct {
		UserAgent string `json:"userAgent"`
	}
	if decodeNativeJSON(raw, &v) != nil || v.UserAgent == "" {
		return goalError("goal-initialize-invalid")
	}
	return p.send(map[string]string{"method": "initialized"})
}
func (h Host) withGoalConnection(ctx context.Context, thread string, use func(*goalRPC) error) error {
	if !privateNativeID(thread) {
		return goalError("goal-identity-unavailable")
	}
	if h.CanonicalCodex {
		return h.withCanonicalConnection(ctx, thread, use)
	}
	script := fmt.Sprintf(`export HOME=%s; export PATH="$HOME/.local/bin:/usr/local/bin:$PATH"; exec codex app-server`, shq(h.agentHome()))
	return h.withGoalScript(ctx, script, thread, use)
}

func goalMethodAllowed(method string, serial int) bool {
	if method == "initialize" {
		return serial == 0
	}
	if serial == 0 {
		return false
	}
	switch method {
	case "thread/goal/get", "thread/goal/set", "thread/goal/clear", "thread/turns/list", "thread/read", "thread/start", "thread/resume", "thread/name/set", "remoteControl/status/read", "turn/interrupt", "thread/queue/list", "thread/backgroundTerminals/list", "thread/backgroundTerminals/terminate", "thread/list", "thread/loaded/list":
		return true
	}
	return false
}
