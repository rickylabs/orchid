package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"reflect"
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
	input   io.Writer
	scan    *bufio.Scanner
	serial  int
	thread  string
	updates []*nativeGoal
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
	if !(method == "initialize" && p.serial == 0 || (method == "thread/goal/get" || method == "thread/goal/set") && p.serial > 0) {
		return nil, goalError("goal-method-refused")
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
	defer children.hold()()
	if !privateNativeID(thread) {
		return goalError("goal-identity-unavailable")
	}
	// Native identity, objective and budget travel over stdin, never argv.
	script := fmt.Sprintf(`export HOME=%s; export PATH="$HOME/.local/bin:/usr/local/bin:$PATH"; exec codex app-server`, shq(h.agentHome()))
	var cmd *exec.Cmd
	if h.isLocal() {
		cmd = exec.CommandContext(ctx, "bash", "-c", script)
	} else {
		cmd = exec.CommandContext(ctx, "ssh", append(h.sshBase(), h.SSH, script)...)
	}
	in, e := cmd.StdinPipe()
	if e != nil {
		return goalError("goal-transport-unavailable")
	}
	defer in.Close()
	out, e := cmd.StdoutPipe()
	if e != nil {
		return goalError("goal-transport-unavailable")
	}
	if cmd.Start() != nil {
		return goalError("goal-transport-unavailable")
	}
	defer func() { _ = in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	p := newGoalRPC(in, out, thread)
	if e := p.initialize(); e != nil {
		return e
	}
	return use(p)
}
