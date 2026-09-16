package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every identifier, title and number in these controls is synthetic.
func TestNativeGoalBudget(t *testing.T) {
	for _, v := range []struct {
		s       string
		present bool
		want    *int64
	}{
		{"", false, nil}, {"0", true, goalInt(0)}, {"010", true, goalInt(10)}, {"500k", true, goalInt(500000)},
		{"1.5K", true, goalInt(1500)}, {"0.001m", true, goalInt(1000)}, {"2M", true, goalInt(2000000)},
		{"9007199254740991", true, goalInt(maxGoalNumber)},
	} {
		got, e := parseGoalBudget(v.s, v.present)
		if e != nil || (got == nil) != (v.want == nil) || got != nil && *got != *v.want {
			t.Fatal("valid budget lost exact value or unknown")
		}
	}
	for _, v := range []string{"", " ", "-1", "+1", ".5k", "1.", "1e3", "1kk", "0.1", "0.0001k", "1.0011K", "1 k", "1k ", "9007199254740992", "999999999999999999999999999999999999999999", strings.Repeat("1", 129), strings.Repeat("0", 128) + "1"} {
		if _, e := parseGoalBudget(v, true); e == nil {
			t.Fatal("invalid budget accepted")
		}
	}
}
func goalInt(v int64) *int64    { return &v }
func fixtureIntent() goalIntent { return goalIntent{"fixture/inbox#7: synthetic task", goalInt(100)} }
func fixtureGoal(status string) *nativeGoal {
	return &nativeGoal{"fixture-thread", fixtureIntent().Objective, status, goalInt(100), 10, 2, 1, 3}
}
func goalJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func response(id int, v any) any     { return map[string]any{"id": id, "result": v} }
func updated(g *nativeGoal) any {
	return map[string]any{"method": "thread/goal/updated", "params": map[string]any{"threadId": g.ThreadID, "goal": g}}
}
func goalPort(frames ...any) (*goalRPC, *bytes.Buffer) {
	var input, output bytes.Buffer
	for _, v := range frames {
		b, _ := json.Marshal(v)
		output.Write(b)
		output.WriteByte('\n')
	}
	p := newGoalRPC(&input, &output, "fixture-thread")
	p.serial = 1
	return p, &input
}
func TestNativeGoalObjective(t *testing.T) {
	o, e := nativeGoalIntent("fixture/inbox", Issue{Number: 7, Title: "synthetic task"}, Overrides{})
	if e != nil || o.Objective != fixtureIntent().Objective || o.TokenBudget != nil {
		t.Fatal("valid assignment intent failed")
	}
	for _, s := range []string{"", "  ", "x\x00", string([]byte{255}), strings.Repeat("x", 4001)} {
		if validGoalObjective(s) {
			t.Fatal("invalid objective accepted")
		}
	}
	if !validGoalObjective(strings.Repeat("界", 4000)) {
		t.Fatal("objective limit counted bytes rather than scalars")
	}
	for _, v := range []struct {
		repo  string
		n     int
		title string
	}{{"bad", 7, "task"}, {"fixture/inbox", 0, "task"}, {"fixture/inbox", 7, ""}, {"fixture/inbox", 7, strings.Repeat("x", 4000)}} {
		if _, e := nativeGoalIntent(v.repo, Issue{Number: v.n, Title: v.title}, Overrides{}); e == nil {
			t.Fatal("invalid assignment accepted")
		}
	}
	if _, e := nativeGoalIntent("fixture/inbox", Issue{Number: 7, Title: "task"}, Overrides{MaxTokens: "invalid"}); e == nil {
		t.Fatal("invalid assignment budget bypassed intent guard")
	}
}
func TestNativeGoalCreation(t *testing.T) {
	g := fixtureGoal("active")
	p, input := goalPort(response(2, map[string]any{"goal": nil}), response(3, map[string]any{"goal": g}), updated(g))
	if e := createDispatchGoal(p, fixtureIntent()); e != nil {
		t.Fatal("valid creation failed", e)
	}
	var wire map[string]json.RawMessage
	lines := strings.Split(strings.TrimSpace(input.String()), "\n")
	_ = json.Unmarshal([]byte(lines[1]), &wire)
	var params map[string]json.RawMessage
	_ = json.Unmarshal(wire["params"], &params)
	if len(params) != 4 || string(params["status"]) != `"active"` || string(params["tokenBudget"]) != "100" {
		t.Fatal("creation lost authorized budget or active status")
	}
	p, input = goalPort(response(2, map[string]any{"goal": fixtureGoal("paused")}))
	if e := createDispatchGoal(p, fixtureIntent()); e != goalError("goal-already-exists") || strings.Contains(input.String(), "thread/goal/set") {
		t.Fatal("pre-existing goal replaced")
	}
	unknown := fixtureIntent()
	unknown.TokenBudget = nil
	g = fixtureGoal("active")
	g.TokenBudget = nil
	p, input = goalPort(response(2, map[string]any{"goal": nil}), updated(g), response(3, map[string]any{"goal": g}))
	if e := createDispatchGoal(p, unknown); e != nil || !strings.Contains(input.String(), `"tokenBudget":null`) {
		t.Fatal("unknown budget became zero or omitted")
	}
}
func TestNativeGoalDecodeGuards(t *testing.T) {
	valid := fixtureGoal("active")
	if _, e := decodeGoal(goalJSON(valid), "fixture-thread"); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"threadId", "objective", "status", "tokenBudget", "tokensUsed", "timeUsedSeconds", "createdAt", "updatedAt"} {
		var m map[string]any
		_ = json.Unmarshal(goalJSON(valid), &m)
		delete(m, key)
		if _, e := decodeGoal(goalJSON(m), "fixture-thread"); e == nil {
			t.Fatal("missing native field accepted")
		}
		_ = json.Unmarshal(goalJSON(valid), &m)
		m[key] = nil
		if key != "tokenBudget" {
			if _, e := decodeGoal(goalJSON(m), "fixture-thread"); e == nil {
				t.Fatal("null native field became a zero")
			}
		}
	}
	for key, values := range map[string][]any{"threadId": {"other-thread"}, "objective": {""}, "status": {"invented"}, "tokenBudget": {-1, maxGoalNumber + 1, 0.5}, "tokensUsed": {-1, maxGoalNumber + 1}, "timeUsedSeconds": {-1, maxGoalNumber + 1}, "createdAt": {-1, maxGoalNumber + 1}, "updatedAt": {-1, maxGoalNumber + 1}} {
		for _, v := range values {
			var m map[string]any
			_ = json.Unmarshal(goalJSON(valid), &m)
			m[key] = v
			if _, e := decodeGoal(goalJSON(m), "fixture-thread"); e == nil {
				t.Fatal("invalid native field accepted")
			}
		}
	}
	for _, raw := range []string{"null", "[]", `{"threadId":"fixture-thread","threadId":"other"}`} {
		if _, e := decodeGoal([]byte(raw), "fixture-thread"); e == nil {
			t.Fatal("malformed goal accepted")
		}
	}
}
func TestNativeGoalRPCGuards(t *testing.T) {
	broken, _ := goalPort()
	if e := broken.send(make(chan int)); e != goalError("goal-request-invalid") {
		t.Fatal("unencodable request accepted")
	}
	for _, method := range []string{"thread/list", "thread/goal/clear", "initialize", "process/spawn"} {
		p, in := goalPort()
		if _, e := p.request(method, nil); e == nil || in.Len() != 0 {
			t.Fatal("unsupported request escaped capability")
		}
	}
	p, in := goalPort()
	p.serial = 0
	if _, e := p.request("thread/goal/get", nil); e == nil || in.Len() != 0 {
		t.Fatal("request before initialize")
	}
	for _, v := range []any{map[string]any{"id": 3, "result": map[string]any{}}, map[string]any{"id": "2", "result": map[string]any{}}, map[string]any{"id": 2, "method": "server-request", "result": nil}, map[string]any{"id": 2, "result": nil, "error": nil}, map[string]any{"id": 2}} {
		p, _ := goalPort(v)
		if _, e := p.get(); e == nil {
			t.Fatal("uncorrelated response accepted")
		}
	}
	for _, raw := range []string{"null\n", "not-json\n", strings.Repeat("x", 1048577) + "\n", `{"id":2,"id":2,"result":{}}` + "\n"} {
		p := newGoalRPC(io.Discard, strings.NewReader(raw), "fixture-thread")
		p.serial = 1
		if _, e := p.get(); e == nil {
			t.Fatal("invalid frame accepted")
		}
	}
	for _, v := range []any{nil, map[string]any{}, map[string]any{"goal": map[string]any{}}} {
		p, _ := goalPort(response(2, v))
		if _, e := p.get(); e == nil {
			t.Fatal("absent/malformed goal interpreted as null")
		}
	}
	for _, v := range []struct {
		code    int
		message string
		want    goalError
	}{{-32600, "thread not found: fixture-thread", "goal-thread-not-found"}, {-32600, "thread not found: other-thread", "goal-daemon-refused"}, {-1, "thread not found: fixture-thread", "goal-daemon-refused"}} {
		p, _ := goalPort(map[string]any{"id": 2, "error": map[string]any{"code": v.code, "message": v.message}})
		if _, e := p.get(); e != v.want {
			t.Fatal("daemon refusal lost its distinction or identity correlation")
		}
	}
	p, _ = goalPort()
	p.input = goalBrokenWriter{}
	if _, e := p.get(); e != goalError("goal-transport-unavailable") {
		t.Fatal("write failure accepted")
	}
	p, _ = goalPort()
	p.input = goalShortWriter{}
	if _, e := p.get(); e != goalError("goal-transport-unavailable") {
		t.Fatal("short write accepted")
	}
}

type goalBrokenWriter struct{}

func (goalBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic private error") }

type goalShortWriter struct{}

func (goalShortWriter) Write(b []byte) (int, error) { return len(b) - 1, nil }
func TestNativeGoalNotificationGuards(t *testing.T) {
	g := fixtureGoal("active")
	for _, mutate := range []func(*nativeGoal){func(v *nativeGoal) { v.Objective = "other" }, func(v *nativeGoal) { v.Status = "paused" }, func(v *nativeGoal) { v.TokenBudget = goalInt(1) }} {
		bad := *g
		mutate(&bad)
		p, _ := goalPort(response(2, map[string]any{"goal": &bad}), updated(&bad))
		if _, e := p.set(map[string]any{}, fixtureIntent(), "active"); e == nil {
			t.Fatal("write response changed intended goal")
		}
	}
	for _, event := range []any{nil, map[string]any{}, map[string]any{"id": 3, "result": nil}, map[string]any{"method": "thread/goal/updated", "params": map[string]any{"threadId": "fixture-thread", "goal": fixtureGoal("paused")}}, map[string]any{"method": "thread/goal/cleared", "params": map[string]any{"threadId": "fixture-thread"}}} {
		p, _ := goalPort(response(2, map[string]any{"goal": g}), event)
		if _, e := p.set(map[string]any{}, fixtureIntent(), "active"); e == nil {
			t.Fatal("missing/mismatched notification accepted")
		}
	}
	other := *g
	other.ThreadID = "other-thread"
	p, _ := goalPort(updated(&other), map[string]any{"method": "unrelated", "params": nil}, response(2, map[string]any{"goal": g}), updated(g))
	if _, e := p.set(nil, fixtureIntent(), "active"); e != nil {
		t.Fatal("unrelated traffic blocked matching notification")
	}
	p, _ = goalPort()
	for i := 0; i < 128; i++ {
		if e := p.notification(mapRaw(updated(g))); e != nil {
			t.Fatal(e)
		}
	}
	if p.notification(mapRaw(updated(g))) != goalError("goal-notification-limit") {
		t.Fatal("notification queue unbounded")
	}
	p, _ = goalPort()
	bad := mapRaw(updated(g))
	bad["params"] = []byte(`null`)
	if p.notification(bad) == nil {
		t.Fatal("malformed notification accepted")
	}
}
func mapRaw(v any) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(goalJSON(v), &m)
	return m
}
func TestNativeGoalTransition(t *testing.T) {
	for _, status := range []string{"paused", "blocked", "complete"} {
		old, next := fixtureGoal("active"), fixtureGoal(status)
		p, input := goalPort(response(2, map[string]any{"goal": old}), response(3, map[string]any{"goal": next}), updated(next))
		changed, e := transitionDispatchGoal(p, fixtureIntent(), status)
		if e != nil || !changed {
			t.Fatal("valid lifecycle transition refused", e)
		}
		lines := strings.Split(strings.TrimSpace(input.String()), "\n")
		var m struct {
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal([]byte(lines[1]), &m)
		if len(m.Params) != 2 || m.Params["status"] != status || m.Params["threadId"] != "fixture-thread" {
			t.Fatal("status update reset intent or accounting")
		}
	}
	for _, status := range []string{"active", "budgetLimited", "usageLimited", "unknown"} {
		p, in := goalPort()
		if _, e := transitionDispatchGoal(p, fixtureIntent(), status); e == nil || in.Len() != 0 {
			t.Fatal("unlicensed transition sent")
		}
	}
	for _, old := range []*nativeGoal{nil, {ThreadID: "fixture-thread", Objective: "other", Status: "active", TokenBudget: goalInt(100)}, {ThreadID: "fixture-thread", Objective: fixtureIntent().Objective, Status: "active", TokenBudget: goalInt(1)}} {
		p, input := goalPort(response(2, map[string]any{"goal": old}))
		if _, e := transitionDispatchGoal(p, fixtureIntent(), "complete"); e == nil || strings.Contains(input.String(), "thread/goal/set") {
			t.Fatal("unowned goal mutated")
		}
	}
	for _, oldStatus := range []string{"complete", "budgetLimited", "usageLimited", "paused", "blocked"} {
		p, input := goalPort(response(2, map[string]any{"goal": fixtureGoal(oldStatus)}))
		changed, e := transitionDispatchGoal(p, fixtureIntent(), "blocked")
		if e != nil || changed || strings.Contains(input.String(), "thread/goal/set") {
			t.Fatal("generic blocked event overwrote protected state")
		}
	}
	for _, status := range []string{"budgetLimited", "usageLimited"} {
		g := fixtureGoal("complete")
		p, _ := goalPort(response(2, map[string]any{"goal": fixtureGoal(status)}), response(3, map[string]any{"goal": g}), updated(g))
		if ok, e := transitionDispatchGoal(p, fixtureIntent(), "complete"); !ok || e != nil {
			t.Fatal("explicit completion could not finish limited assignment")
		}
	}
	for _, mutate := range []func(*nativeGoal){func(g *nativeGoal) { g.CreatedAt = 2 }, func(g *nativeGoal) { g.TokensUsed = 9 }, func(g *nativeGoal) { g.SecondsUsed = 1 }} {
		next := fixtureGoal("paused")
		mutate(next)
		p, _ := goalPort(response(2, map[string]any{"goal": fixtureGoal("active")}), response(3, map[string]any{"goal": next}), updated(next))
		if _, e := transitionDispatchGoal(p, fixtureIntent(), "paused"); e != goalError("goal-accounting-regressed") {
			t.Fatal("accounting regression accepted")
		}
	}
}
func TestNativeGoalAssignmentStatus(t *testing.T) {
	for _, v := range []struct{ s, r, w string }{{"CLOSED", "COMPLETED", "complete"}, {"CLOSED", "NOT_PLANNED", "paused"}, {"OPEN", "COMPLETED", ""}, {"CLOSED", "unknown", ""}, {"", "", ""}} {
		if assignmentGoalStatus(v.s, v.r) != v.w {
			t.Fatal("assignment state fabricated")
		}
	}
}
func fixtureGoalBinding(t *testing.T) (*durableMatrixReceipt, *Job, string) {
	t.Helper()
	r := registrationReceipt(t, "codex", Overrides{})
	r.dispatch.Source = "codex"
	loc := &dispatchLocation{"w1:p1", "w1"}
	if r.writeDispatch("dispatched", loc) != nil {
		t.Fatal("fixture dispatch failed")
	}
	b := map[string]any{"Repo": "fixture/target", "NativeSessionID": "fixture-thread"}
	if os.WriteFile(filepath.Join(filepath.Dir(r.file), "binding.json"), goalJSON(b), 0600) != nil {
		t.Fatal("fixture binding failed")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(r.file)))
	j := &Job{Issue: 7, Repo: "fixture/target", Agent: "codex", Label: "fixture-agent", Pane: loc.PaneID, Workspace: loc.WorkspaceID, NativeGoal: &dispatchGoal{ReceiptKey: strings.Repeat("d", 64), Intent: fixtureIntent()}}
	return r, j, root
}
func nativeReport(j *Job, identity bool) json.RawMessage {
	a := map[string]any{"agent": "codex", "name": j.Label, "pane_id": j.Pane, "workspace_id": j.Workspace, "interactive_ready": true}
	if identity {
		a["agent_session"] = map[string]any{"source": "herdr:codex", "agent": "codex", "kind": "id", "value": "fixture-thread"}
	}
	return goalJSON(map[string]any{"type": "agent_info", "agent": a})
}
func TestNativeGoalBindingAcquisition(t *testing.T) {
	r, j, _ := fixtureGoalBinding(t)
	_ = os.WriteFile(filepath.Join(filepath.Dir(r.file), "binding.json"), goalJSON(map[string]any{"Repo": "fixture/target"}), 0600)
	reads := 0
	id, e := acquireNativeGoalBinding(context.Background(), r, j, func() (json.RawMessage, error) { reads++; return nativeReport(j, reads == 2), nil }, func(context.Context) bool { return true })
	if e != nil || id != "fixture-thread" || reads != 2 {
		t.Fatal("late authoritative report was not bound")
	}
	var b struct{ NativeSessionID string }
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(r.file), "binding.json"))
	_ = json.Unmarshal(data, &b)
	if b.NativeSessionID != id {
		t.Fatal("binding was not persisted")
	}
	for _, change := range []func(*durableMatrixReceipt, *Job){func(r *durableMatrixReceipt, j *Job) { r.dispatch = nil }, func(r *durableMatrixReceipt, j *Job) { r.dispatch.State = "uncertain" }, func(r *durableMatrixReceipt, j *Job) { r.dispatch.Source = "claude" }, func(r *durableMatrixReceipt, j *Job) { r.dispatch.Issue.Number++ }, func(r *durableMatrixReceipt, j *Job) { r.dispatch.Location = nil }, func(r *durableMatrixReceipt, j *Job) { j.Agent = "claude" }, func(r *durableMatrixReceipt, j *Job) { j.Pane = "other" }, func(r *durableMatrixReceipt, j *Job) { j.Workspace = "other" }} {
		r, j, _ := fixtureGoalBinding(t)
		change(r, j)
		called := false
		if _, e := acquireNativeGoalBinding(context.Background(), r, j, func() (json.RawMessage, error) { called = true; return nativeReport(j, true), nil }, func(context.Context) bool { return false }); e == nil || called {
			t.Fatal("unconfirmed dispatch used native identity")
		}
	}
	r, j, _ = fixtureGoalBinding(t)
	other := *j
	other.Label = "other-occupant"
	if _, e := acquireNativeGoalBinding(context.Background(), r, j, func() (json.RawMessage, error) { return nativeReport(&other, true), nil }, func(context.Context) bool { return false }); e == nil {
		t.Fatal("concurrent unrelated occupant accepted")
	}
	reads = 0
	if _, e := acquireNativeGoalBinding(context.Background(), r, j, func() (json.RawMessage, error) { reads++; return nativeReport(j, false), nil }, func(context.Context) bool { return true }); e == nil || reads != 40 {
		t.Fatal("missing identity lookup was unbounded or guessed")
	}
	reads = 0
	if _, e := acquireNativeGoalBinding(context.Background(), r, j, func() (json.RawMessage, error) { reads++; return nativeReport(j, false), nil }, func(context.Context) bool { return false }); e == nil || reads != 1 {
		t.Fatal("cancelled lookup continued")
	}
	if _, e := acquireNativeGoalBinding(context.Background(), r, j, func() (json.RawMessage, error) { return nil, io.EOF }, func(context.Context) bool { return false }); e == nil {
		t.Fatal("failed source became identity")
	}
	_ = os.Remove(filepath.Join(filepath.Dir(r.file), "binding.json"))
	if _, e := acquireNativeGoalBinding(context.Background(), r, j, func() (json.RawMessage, error) { return nativeReport(j, true), nil }, func(context.Context) bool { return false }); e == nil {
		t.Fatal("failed binding persistence accepted")
	}
}
func TestNativeGoalPrivateBindingRead(t *testing.T) {
	_, j, root := fixtureGoalBinding(t)
	if id, e := readGoalIdentity(root, j, "fixture/inbox"); e != nil || id != "fixture-thread" {
		t.Fatal("valid binding unavailable")
	}
	for _, change := range []func(*Job){func(j *Job) { j.NativeGoal = nil }, func(j *Job) { j.NativeGoal.ReceiptKey = ".." }, func(j *Job) { j.Issue++ }, func(j *Job) { j.Repo = "fixture/other" }} {
		_, j, root := fixtureGoalBinding(t)
		change(j)
		if _, e := readGoalIdentity(root, j, "fixture/inbox"); e == nil {
			t.Fatal("unbound job read an identity")
		}
	}
	for _, field := range []string{"schemaVersion", "state", "source", "repo", "number", "runId", "parentRunId"} {
		r, j, root := fixtureGoalBinding(t)
		p := filepath.Join(filepath.Dir(r.file), "dispatch.json")
		b, _ := os.ReadFile(p)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		switch field {
		case "schemaVersion":
			m[field] = 2
		case "state":
			m[field] = "uncertain"
		case "source":
			m[field] = "claude"
		case "repo":
			m["issue"].(map[string]any)[field] = "other"
		case "number":
			m["issue"].(map[string]any)[field] = 8
		default:
			m[field] = "other"
		}
		_ = os.WriteFile(p, goalJSON(m), 0600)
		if _, e := readGoalIdentity(root, j, "fixture/inbox"); e == nil {
			t.Fatal("different dispatch binding accepted")
		}
	}
	for _, part := range []string{"root", "reservation", "record", "binding", "dispatch"} {
		r, j, root := fixtureGoalBinding(t)
		p := root
		switch part {
		case "reservation":
			p = filepath.Dir(filepath.Dir(r.file))
		case "record":
			p = filepath.Dir(r.file)
		case "binding":
			p = filepath.Join(filepath.Dir(r.file), "binding.json")
		case "dispatch":
			p = filepath.Join(filepath.Dir(r.file), "dispatch.json")
		}
		_ = os.Chmod(p, 0755)
		if _, e := readGoalIdentity(root, j, "fixture/inbox"); e == nil {
			t.Fatal("publicly readable identity accepted")
		}
	}
	for _, raw := range []string{`{}`, `{"Repo":"fixture/target","NativeSessionID":"bad identity"}`, strings.Repeat("x", 1048577), `{"Repo":"fixture/target","NativeSessionID":"fixture-thread","NativeSessionID":"other"}`} {
		r, j, root := fixtureGoalBinding(t)
		_ = os.WriteFile(filepath.Join(filepath.Dir(r.file), "binding.json"), []byte(raw), 0600)
		if _, e := readGoalIdentity(root, j, "fixture/inbox"); e == nil {
			t.Fatal("malformed binding accepted")
		}
	}
}

func TestNativeGoalFrameAndLoopBounds(t *testing.T) {
	huge := map[string]any{"id": 2, "result": map[string]any{"goal": nil}, "padding": strings.Repeat("x", 1048576)}
	p, _ := goalPort(huge)
	if _, e := p.get(); e != goalError("goal-source-closed-or-frame-limit") {
		t.Fatal("oversized valid JSON bypassed frame bound")
	}
	frames := []any{}
	for i := 0; i < 128; i++ {
		frames = append(frames, map[string]any{"method": "unrelated"})
	}
	frames = append(frames, response(2, map[string]any{"goal": nil}))
	p, _ = goalPort(frames...)
	if _, e := p.get(); e != goalError("goal-notification-limit") {
		t.Fatal("request consumed unbounded notifications")
	}
	p, _ = goalPort(frames[1:]...)
	if g, e := p.get(); e != nil || g != nil {
		t.Fatal("bounded valid response was refused")
	}
	g := fixtureGoal("active")
	frames = []any{response(2, map[string]any{"goal": g})}
	for i := 0; i < 128; i++ {
		frames = append(frames, map[string]any{"method": "unrelated"})
	}
	frames = append(frames, updated(g))
	p, _ = goalPort(frames...)
	if _, e := p.set(nil, fixtureIntent(), "active"); e != goalError("goal-notification-limit") {
		t.Fatal("write consumed unbounded notifications")
	}
	r, j, root := fixtureGoalBinding(t)
	large := map[string]any{"Repo": "fixture/target", "NativeSessionID": "fixture-thread", "padding": strings.Repeat("x", 1048576)}
	_ = os.WriteFile(filepath.Join(filepath.Dir(r.file), "binding.json"), goalJSON(large), 0600)
	if _, e := readGoalIdentity(root, j, "fixture/inbox"); e != goalError("goal-binding-invalid") {
		t.Fatal("oversized valid private binding accepted")
	}
}

func TestNativeGoalBudgetPresence(t *testing.T) {
	for _, body := range []string{"/swarm\nmax-tokens:", "/swarm\nmax_tokens : ", "/swarm\nmax-tokens: # synthetic comment"} {
		o := parseOverrides(body)
		if _, e := parseGoalBudget(o.MaxTokens, o.MaxTokensPresent); e == nil {
			t.Fatal("explicit empty assignment budget became unknown")
		}
	}
	o := parseOverrides("/swarm\nprofile: leaf")
	if v, e := parseGoalBudget(o.MaxTokens, o.MaxTokensPresent); e != nil || v != nil {
		t.Fatal("absent assignment budget fabricated")
	}
}

func TestNativeGoalFreshWriteNotification(t *testing.T) {
	g := fixtureGoal("active")
	for _, fresh := range []bool{false, true} {
		frames := []any{response(2, map[string]any{"goal": g})}
		if fresh {
			frames = append(frames, updated(g))
		}
		p, _ := goalPort(frames...)
		// An update observed during a previous get has the exact set-result values.
		if e := p.notification(mapRaw(updated(g))); e != nil {
			t.Fatal("fixture update failed")
		}
		_, e := p.set(nil, fixtureIntent(), "active")
		if fresh && e != nil {
			t.Fatal("fresh matching update refused")
		}
		if !fresh && e != goalError("goal-notification-unavailable") {
			t.Fatal("stale pre-write notification certified a write")
		}
	}
}
