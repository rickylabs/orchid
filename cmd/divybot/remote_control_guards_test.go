package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type remoteFixtureWriter struct {
	output   *bytes.Buffer
	requests []string
	respond  func(string, map[string]any) any
}

func (w *remoteFixtureWriter) Write(b []byte) (int, error) {
	var q struct {
		ID     int            `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if json.Unmarshal(b, &q) != nil {
		return 0, fmt.Errorf("fixture request malformed")
	}
	if q.ID == 0 {
		return len(b), nil
	}
	w.requests = append(w.requests, q.Method)
	row, _ := json.Marshal(response(q.ID, w.respond(q.Method, q.Params)))
	w.output.Write(append(row, '\n'))
	return len(b), nil
}
func remoteFixturePort(handler func(string, map[string]any) any, thread string) (*goalRPC, *remoteFixtureWriter) {
	out := &bytes.Buffer{}
	w := &remoteFixtureWriter{output: out, respond: handler}
	p := newGoalRPC(w, out, thread)
	p.serial = 1
	return p, w
}
func syntheticRemoteRun(t *testing.T) *remoteControlRun {
	t.Helper()
	id := privateTestID(t)
	id = id[:8] + "-" + id[8:12] + "-4" + id[13:16] + "-8" + id[17:20] + "-" + id[20:]
	return &remoteControlRun{NativeSessionID: id, Cwd: t.TempDir(), Name: "#7 synthetic task", Model: "fixture-model", Effort: "low", IdentitySource: "herdr-session-start", HookConfirmed: true}
}
func remoteThreadFixture(run *remoteControlRun) map[string]any {
	return map[string]any{"thread": map[string]any{"id": run.NativeSessionID, "sessionId": run.NativeSessionID, "parentThreadId": nil, "cwd": run.Cwd, "name": run.Name, "model": run.Model, "reasoningEffort": run.Effort, "status": map[string]string{"type": "idle"}},
		"cwd": run.Cwd, "model": run.Model, "reasoningEffort": run.Effort, "approvalPolicy": "never", "sandbox": map[string]string{"type": "dangerFullAccess"}}
}

func TestRemoteControlScopedTrustAndExplicitDaemon(t *testing.T) {
	run := syntheticRemoteRun(t)
	params, err := remoteThreadParams(run, map[string]string{"FIXTURE": "scoped"})
	if err != nil {
		t.Fatal("valid owned checkout refused")
	}
	config := params["config"].(map[string]any)
	projects := config["projects"].(map[string]any)
	if len(projects) != 1 || !reflect.DeepEqual(projects[run.Cwd], map[string]string{"trust_level": "trusted"}) {
		t.Fatal("trust broadened beyond owned checkout")
	}
	if params["cwd"] != run.Cwd || params["approvalPolicy"] != "never" || params["sandbox"] != "danger-full-access" || params["historyMode"] != "paginated" || params["ephemeral"] != false {
		t.Fatal("native preparation lost launch constraints")
	}
	args, err := remoteCodexArgs([]string{"--model", run.Model}, run.Cwd, run.NativeSessionID)
	if err != nil || !reflect.DeepEqual(args[len(args)-4:], []string{"--remote", "unix://", "resume", run.NativeSessionID}) || strings.Contains(strings.Join(args, " "), "--cd") {
		t.Fatal("canonical daemon/resume route missing")
	}
	for _, cwd := range []string{"/", "relative", "/owned/../other", "/owned\nother"} {
		bad := *run
		bad.Cwd = cwd
		if _, err := remoteThreadParams(&bad, nil); err == nil {
			t.Fatal("unsafe trust scope accepted")
		}
	}
	v := false
	h := Host{RemoteControl: &RemoteControlConfig{Codex: &v}}
	if h.remoteEnabled("codex") || !h.forJob(&Job{Agent: "codex", RemoteControl: run}).CanonicalCodex || h.forJob(&Job{Agent: "codex"}).CanonicalCodex {
		t.Fatal("launch switch changed retained run transport")
	}
}

func TestRemoteControlPreparationRequiresConnectionAndReadback(t *testing.T) {
	for _, mode := range []string{"valid", "absent", "disconnected", "failed", "wrong-cwd", "wrong-model", "wrong-effort", "wrong-policy", "wrong-sandbox", "wrong-name", "wrong-read-id", "wrong-resume-id"} {
		t.Run(mode, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			expected := *run
			start := remoteThreadFixture(run)
			read := remoteThreadFixture(run)
			resume := remoteThreadFixture(run)
			status := any(map[string]string{"status": "connected"})
			switch mode {
			case "absent":
				status = map[string]any{}
			case "disconnected", "failed":
				status = map[string]string{"status": mode}
			case "wrong-cwd":
				start["cwd"] = t.TempDir()
			case "wrong-model":
				start["model"] = "other-model"
			case "wrong-effort":
				start["reasoningEffort"] = "high"
			case "wrong-policy":
				start["approvalPolicy"] = "on-request"
			case "wrong-sandbox":
				start["sandbox"] = map[string]string{"type": "workspaceWrite"}
			case "wrong-name":
				read["thread"].(map[string]any)["name"] = "other task"
			case "wrong-read-id":
				read["thread"].(map[string]any)["id"] = privateTestID(t)
			case "wrong-resume-id":
				resume["thread"].(map[string]any)["id"] = privateTestID(t)
			}
			p, w := remoteFixturePort(func(method string, params map[string]any) any {
				switch method {
				case "remoteControl/status/read":
					return status
				case "thread/start":
					return start
				case "thread/read":
					return read
				case "thread/resume":
					return resume
				default:
					return map[string]any{}
				}
			}, "")
			params, _ := remoteThreadParams(run, nil)
			err := p.prepareRemoteThread(run, params)
			if err == nil {
				err = p.verifyRemoteThread(run, true)
			}
			if (err == nil) != (mode == "valid") {
				t.Fatal("connection or exact native readback guard failed")
			}
			if mode == "valid" && run.NativeSessionID != expected.NativeSessionID {
				t.Fatal("prepared native identity changed")
			}
			for _, method := range w.requests {
				if strings.Contains(strings.ToLower(method), "pair") || strings.Contains(strings.ToLower(method), "enable") || strings.Contains(strings.ToLower(method), "config/write") {
					t.Fatal("owner consent was automated")
				}
			}
			if mode == "failed" || mode == "absent" || mode == "disconnected" {
				if len(w.requests) != 1 {
					t.Fatal("unconnected daemon gained a native thread")
				}
			}
		})
	}
}

func TestRemoteControlRPCScopes(t *testing.T) {
	p, w := remoteFixturePort(func(string, map[string]any) any { return map[string]any{} }, privateTestID(t))
	for _, method := range []string{"thread/name/set", "thread/read", "turn/interrupt", "thread/goal/clear", "thread/backgroundTerminals/terminate", "thread/list"} {
		if _, err := p.request(method, map[string]any{"threadId": privateTestID(t), "ancestorThreadId": privateTestID(t)}); err == nil {
			t.Fatal("foreign native thread gained authority")
		}
	}
	if _, err := p.request("thread/start", map[string]any{}); err == nil {
		t.Fatal("bound transport created an unrelated thread")
	}
	if _, err := p.request("remoteControl/start", map[string]any{}); err == nil {
		t.Fatal("transport gained pairing authority")
	}
	if len(w.requests) != 0 {
		t.Fatal("refused native mutation escaped to transport")
	}
}

func TestRemoteControlClaudeConnectedProof(t *testing.T) {
	for _, screen := range []string{"/rc active", "/rc active · synthetic status", "/remote-control is active · Continue here, on your phone, or at synthetic target"} {
		if !claudeRemoteConnected(screen) {
			t.Fatal("native connected footer refused")
		}
	}
	for _, screen := range []string{"", "/rc", "requested remote control", "Enable Remote Control", "/rc activeish", "/rc active\nRemote Control failed", "/rc active\nCouldn't reconnect", "quoted: /rc active", "quoted: /remote-control is active", "/remote-control is active"} {
		if claudeRemoteConnected(screen) {
			t.Fatal("absent or failed remote control certified connected")
		}
	}
	if _, err := remoteSessionName(0, "task"); err == nil {
		t.Fatal("unnamed issue accepted")
	}
	if _, err := remoteSessionName(7, "task\ncommand"); err == nil {
		t.Fatal("control character became a native name")
	}
}

func remoteOccupantFixture(t *testing.T, run *remoteControlRun) json.RawMessage {
	t.Helper()
	row := nativeStartFixture(t, run.NativeSessionID)
	row["type"] = "agent_info"
	a := row["agent"].(map[string]any)
	a["cwd"] = run.Cwd
	a["agent_status"] = "idle"
	return fixtureJSON(t, row)
}
func TestRemoteControlBoundedProof(t *testing.T) {
	for _, mode := range []string{"valid", "absent", "failed", "changed-session", "changed-sequence", "late", "wrong-cwd"} {
		t.Run(mode, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			raw := remoteOccupantFixture(t, run)
			reads := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := awaitRemoteProof(ctx, "codex", "fixture-agent", run.Cwd, run.NativeSessionID, &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"},
				func(context.Context) (json.RawMessage, error) {
					reads++
					if mode == "changed-session" && reads > 1 {
						return remoteOccupantFixture(t, syntheticRemoteRun(t)), nil
					}
					if mode == "changed-sequence" && reads > 1 {
						var row map[string]any
						_ = json.Unmarshal(raw, &row)
						row["agent"].(map[string]any)["state_change_seq"] = 2
						return fixtureJSON(t, row), nil
					}
					if mode == "wrong-cwd" {
						var row map[string]any
						_ = json.Unmarshal(raw, &row)
						row["agent"].(map[string]any)["cwd"] = t.TempDir()
						return fixtureJSON(t, row), nil
					}
					return raw, nil
				},
				func(context.Context) (bool, error) {
					if mode == "late" {
						cancel()
					}
					return mode != "absent" && mode != "failed", nil
				}, func(context.Context) bool { return false })
			if (err == nil) != (mode == "valid") {
				t.Fatal("native connected proof accepted uncertainty or lateness")
			}
		})
	}
}

func nativeStatusFixture(run *remoteControlRun) string {
	return "/status\n│ Model: " + run.Model + " (reasoning " + run.Effort + ") │\n│ Directory: " + run.Cwd + " │\n│ Thread name: " + run.Name + " │\n│ Session: " + run.NativeSessionID + " │\n› Ask Codex to do anything\n"
}

// writeResumeTrace writes a synthetic canonical-daemon trace with one codex-tui
// thread/resume per connection on thread, in the daemon's real span shape.
func writeResumeTrace(t *testing.T, home, thread, version string, connections ...string) {
	t.Helper()
	type row struct {
		Thread string `json:"thread"`
		Body   string `json:"body"`
	}
	rows := []row{}
	for _, conn := range connections {
		rows = append(rows, row{thread, `app_server.request{otel.kind="server" otel.name="thread/resume" rpc.system="jsonrpc" rpc.method="thread/resume" rpc.transport="unix_socket" rpc.request_id=5 app_server.connection_id=` +
			conn + ` app_server.api_version="v2" app_server.client_name="codex-tui" app_server.client_version="` + version + `"}:resume_running_thread: clearing thread listener`})
	}
	raw, _ := json.Marshal(rows)
	script := "import json,sqlite3,sys\nc=sqlite3.connect(sys.argv[1])\nc.execute('create table if not exists logs (id integer primary key, ts integer, thread_id text, feedback_log_body text)')\n" +
		"for r in json.loads(sys.argv[2]):c.execute('insert into logs (ts,thread_id,feedback_log_body) values (1,?,?)',(r['thread'],r['body']))\nc.commit()\n"
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("python3", "-c", script, filepath.Join(home, ".codex", "logs_2.sqlite"), string(raw)).CombinedOutput(); err != nil {
		t.Fatalf("trace fixture: %v %s", err, out)
	}
}

// The daemon's own trace proves attachment: exactly one codex-tui connection at
// the pinned version ran thread/resume on the prepared thread. Synthetic rows use
// the daemon's real span shape; nothing reads a screen or types into the agent.
func TestRemoteCodexResumeIdentityFromDaemonTrace(t *testing.T) {
	span := func(conn, client, version string) string {
		return `app_server.request{otel.kind="server" otel.name="thread/resume" rpc.system="jsonrpc" rpc.method="thread/resume" rpc.transport="unix_socket" rpc.request_id=5 app_server.connection_id=` +
			conn + ` app_server.api_version="v2" app_server.client_name="` + client + `" app_server.client_version="` + version + `"}:resume_running_thread: clearing thread listener`
	}
	run := syntheticRemoteRun(t)
	run.ClientVersion = "9.1.0"
	other := privateTestID(t)
	type row struct {
		Thread string `json:"thread"`
		Body   string `json:"body"`
	}
	for _, tc := range []struct {
		name string
		rows []row
		db   bool
		ok   bool
	}{
		{"exactly-one-tui-resume", []row{{run.NativeSessionID, span("252", "codex-tui", "9.1.0")}, {run.NativeSessionID, span("252", "codex-tui", "9.1.0")}}, true, true},
		{"no-resume-yet", []row{{run.NativeSessionID, "thread/start by orchid"}}, true, false},
		{"two-tui-connections", []row{{run.NativeSessionID, span("252", "codex-tui", "9.1.0")}, {run.NativeSessionID, span("253", "codex-tui", "9.1.0")}}, true, false},
		{"not-the-tui", []row{{run.NativeSessionID, span("251", "orchid_dispatch_goals", "9.1.0")}}, true, false},
		{"other-client-version", []row{{run.NativeSessionID, span("252", "codex-tui", "9.2.0")}}, true, false},
		{"other-thread", []row{{other, span("252", "codex-tui", "9.1.0")}}, true, false},
		{"no-trace", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", "")
			if tc.db {
				rows, _ := json.Marshal(tc.rows)
				script := "import json,sqlite3,sys\nc=sqlite3.connect(sys.argv[1])\nc.execute('create table logs (id integer primary key, ts integer, thread_id text, feedback_log_body text)')\n" +
					"for r in json.loads(sys.argv[2]):c.execute('insert into logs (ts,thread_id,feedback_log_body) values (1,?,?)',(r['thread'],r['body']))\nc.commit()\n"
				if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command("python3", "-c", script, filepath.Join(home, ".codex", "logs_2.sqlite"), string(rows)).CombinedOutput(); err != nil {
					t.Fatalf("trace fixture: %v %s", err, out)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			err := Host{Name: "fixture-host", Home: home}.awaitCodexResumeIdentity(ctx, run)
			if (err == nil) != tc.ok {
				t.Fatalf("resume identity verdict wrong: err=%v", err)
			}
		})
	}
}

// The pre-goal proof requires the pinned client version: with none known, a
// resume by any codex-tui build does not prove this launch's attachment.
func TestRemoteCodexResumeIdentityUnpinnedRefuses(t *testing.T) {
	run := syntheticRemoteRun(t)
	run.ClientVersion = ""
	home := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	writeResumeTrace(t, home, run.NativeSessionID, "9.1.0", "252")
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := (Host{Name: "fixture-host", Home: home}).awaitCodexResumeIdentity(ctx, run); err == nil {
		t.Fatal("unpinned client proved the attachment")
	}
}

// The pre-goal Codex identity no longer types into the agent or reads a screen.
func TestRemoteCodexIdentityHasNoScreenPath(t *testing.T) {
	src := productionSources(t)["remote_control_identity.go"]
	for _, banned := range []string{`"/status"`, "promptSnapshot(", "agent\", \"prompt", "nativeCodexStatusMatches", "establishCodexStatusIdentity", "nativeCodexFooterIdentity", "verifyCodexFooterAttachment"} {
		if strings.Contains(src, banned) {
			t.Fatalf("screen-based identity path present: %s", banned)
		}
	}
	if !strings.Contains(src, "h.awaitCodexResumeIdentity(ctx, run) // guard:codex-resume-identity") {
		t.Fatal("pre-goal identity does not use the daemon trace")
	}
}

func remoteIdleHandler(run *remoteControlRun, mode string) func(string, map[string]any) any {
	return func(method string, params map[string]any) any {
		switch method {
		case "thread/read":
			row := remoteThreadFixture(run)
			if mode == "active" {
				row["thread"].(map[string]any)["status"] = map[string]string{"type": "active"}
			}
			return row
		case "thread/queue/list", "thread/backgroundTerminals/list":
			data := []any{}
			if mode == "queue" && method == "thread/queue/list" || mode == "background" && method == "thread/backgroundTerminals/list" {
				data = append(data, map[string]any{"processId": "fixture-process"})
			}
			next := any(nil)
			if mode == "paged" {
				next = "more"
			}
			return map[string]any{"data": data, "nextCursor": next}
		case "thread/turns/list":
			status := "completed"
			if mode == "streaming" {
				status = "inProgress"
			}
			items := []any{}
			if mode == "tool-running" {
				items = append(items, map[string]string{"type": "commandExecution", "status": "inProgress"})
			}
			return map[string]any{"data": []any{map[string]any{"id": "fixture-turn", "status": status, "itemsView": "full", "items": items}}}
		case "thread/list", "thread/loaded/list":
			return map[string]any{"data": []any{}, "nextCursor": nil}
		case "thread/goal/get":
			return map[string]any{"goal": nil}
		default:
			return map[string]any{}
		}
	}
}
func TestRemoteControlNativeWorkQuiescence(t *testing.T) {
	for _, mode := range []string{"valid", "streaming", "tool-running", "queue", "background", "paged", "active"} {
		t.Run(mode, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			p, _ := remoteFixturePort(remoteIdleHandler(run, mode), run.NativeSessionID)
			err := p.remoteWorkIdle(false)
			if (err == nil) != (mode == "valid") {
				t.Fatal("nonterminal or queued daemon work certified stopped")
			}
		})
	}
}
func TestRemoteControlChildScopeAndLateChild(t *testing.T) {
	for _, mode := range []string{"valid", "foreign-parent", "cycle", "duplicate", "paged", "late-child", "active-child"} {
		t.Run(mode, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			child := privateTestID(t)
			listReads := 0
			base := remoteIdleHandler(run, "valid")
			p, _ := remoteFixturePort(func(method string, params map[string]any) any {
				if method == "thread/list" {
					if params["archived"] == true {
						return map[string]any{"data": []any{}, "nextCursor": nil}
					}
					listReads++
					parent := run.NativeSessionID
					if mode == "foreign-parent" {
						parent = privateTestID(t)
					}
					if mode == "cycle" {
						parent = child
					}
					data := []any{map[string]any{"id": child, "parentThreadId": parent}}
					if mode == "duplicate" {
						data = append(data, data[0])
					}
					if mode == "late-child" && listReads > 1 {
						data = append(data, map[string]any{"id": privateTestID(t), "parentThreadId": run.NativeSessionID})
					}
					next := any(nil)
					if mode == "paged" {
						next = "more"
					}
					return map[string]any{"data": data, "nextCursor": next}
				}
				if method == "thread/read" && params["threadId"] == child {
					v := remoteThreadFixture(run)
					v["thread"].(map[string]any)["id"] = child
					v["thread"].(map[string]any)["parentThreadId"] = run.NativeSessionID
					if mode == "active-child" {
						v["thread"].(map[string]any)["status"] = map[string]string{"type": "active"}
					}
					return v
				}
				return base(method, params)
			}, run.NativeSessionID)
			if err := p.remoteWorkIdle(false); (err == nil) != (mode == "valid") {
				t.Fatal("unproven or unrelated descendant certified idle")
			}
		})
	}
}

func TestRemoteControlNativeStopReadbackAndDeadline(t *testing.T) {
	for _, mode := range []string{"valid", "interrupt-ack-only", "terminate-false", "late"} {
		t.Run(mode, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			base := remoteIdleHandler(run, "valid")
			reads := 0
			terminated := false
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
			defer cancel()
			p, w := remoteFixturePort(func(method string, params map[string]any) any {
				if method == "thread/turns/list" {
					reads++
					if reads == 1 || mode == "interrupt-ack-only" {
						return map[string]any{"data": []any{map[string]string{"id": "fixture-turn", "status": "inProgress", "itemsView": "full"}}, "nextCursor": nil}
					}
				}
				if method == "turn/interrupt" && mode == "late" {
					cancel()
				}
				if method == "thread/backgroundTerminals/list" && !terminated {
					return map[string]any{"data": []any{map[string]string{"processId": "fixture-process"}}, "nextCursor": nil}
				}
				if method == "thread/backgroundTerminals/terminate" {
					terminated = true
					return map[string]bool{"terminated": mode != "terminate-false"}
				}
				return base(method, params)
			}, run.NativeSessionID)
			p.ctx = ctx
			if err := p.stopRemoteWork(nil); (err == nil) != (mode == "valid") {
				t.Fatal("native stop acknowledgement or deadline became terminal proof")
			}
			if len(w.requests) == 0 {
				t.Fatal("native work was never inspected")
			}
		})
	}
}

func TestRemoteControlObservationEnvelopeAndRevocation(t *testing.T) {
	run := syntheticRemoteRun(t)
	r := registrationReceipt(t, "codex", Overrides{})
	if err := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); err != nil {
		t.Fatal("fixture dispatch unavailable")
	}
	path := filepath.Join(filepath.Dir(r.file), "remote-control.json")
	if writeRemoteObservation(context.Background(), r, "codex", run, "connected", "", &run.Name) != nil {
		t.Fatal("native observation refused")
	}
	var row remoteControlObservation
	b, err := os.ReadFile(path)
	if err != nil || strictJSON(b, &row) != nil || row.SchemaVersion != 1 || row.Link != nil || row.SessionName == nil || *row.SessionName != run.Name || row.Vendor != "chatgpt" || row.State != "connected" || row.Reason != nil || row.RunID != r.dispatch.RunID || row.NativeSessionID != run.NativeSessionID {
		t.Fatal("private envelope contract changed")
	}
	at, e := time.Parse(time.RFC3339Nano, row.ObservedAt)
	until, e2 := time.Parse(time.RFC3339Nano, row.ValidUntil)
	info, _ := os.Stat(path)
	if e != nil || e2 != nil || until.Sub(at) != 30*time.Second || info.Mode().Perm() != 0600 {
		t.Fatal("private observation privacy/freshness changed")
	}
	if writeRemoteObservation(context.Background(), r, "codex", run, "unconfirmed", "remote-control-unconfirmed", nil) != nil {
		t.Fatal("refusal unavailable")
	}
	b, _ = os.ReadFile(path)
	_ = json.Unmarshal(b, &row)
	if row.SessionName != nil || row.Link != nil || row.Reason == nil {
		t.Fatal("refusal retained capability")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if writeRemoteObservation(ctx, r, "codex", run, "connected", "", &run.Name) == nil {
		t.Fatal("cancelled observation published")
	}
	for _, state := range []string{"enabled", "ready", "failed"} {
		if writeRemoteObservation(context.Background(), r, "codex", run, state, "", nil) == nil {
			t.Fatal("unknown connection state published")
		}
	}
	if writeRemoteObservation(context.Background(), r, "claude", run, "connected", "", nil) == nil {
		t.Fatal("wrong dispatch vendor published")
	}
}

func TestRemoteControlClaudeAuthPreservesOwnerChoices(t *testing.T) {
	for _, mode := range []string{"existing", "new", "malformed", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".claude.json")
			canonical := []byte(`{"oauthAccount":{"accountUuid":"fixture-account"},"userID":"fixture-user","hasUsedRemoteControl":true,"remoteDialogSeen":true,"projects":{"foreign":{"hasTrustDialogAccepted":true}}}`)
			metadata, err := claudeAccountMetadata(canonical)
			if err != nil {
				t.Fatal("account metadata refused")
			}
			original := []byte(`{"hasUsedRemoteControl":false,"remoteDialogSeen":false,"ownerUnknown":{"preserve":true}}`)
			if mode == "existing" {
				_ = os.WriteFile(path, original, 0600)
			}
			if mode == "malformed" {
				_ = os.WriteFile(path, []byte(`{"remoteDialogSeen":false,"remoteDialogSeen":true}`), 0600)
			}
			if mode == "symlink" {
				target := filepath.Join(t.TempDir(), "owner.json")
				_ = os.WriteFile(target, original, 0600)
				_ = os.Symlink(target, path)
			}
			cmd := exec.Command("python3", "-c", mergeClaudeAccountPython)
			cmd.Env = append(os.Environ(), "HOME="+root)
			cmd.Stdin = bytes.NewReader(metadata)
			err = cmd.Run()
			if (err == nil) != (mode == "existing" || mode == "new") {
				t.Fatal("owner config safety guard failed")
			}
			if err != nil {
				return
			}
			b, _ := os.ReadFile(path)
			var v map[string]json.RawMessage
			_ = json.Unmarshal(b, &v)
			if mode == "existing" && (string(v["hasUsedRemoteControl"]) != "false" || string(v["remoteDialogSeen"]) != "false" || v["ownerUnknown"] == nil) {
				t.Fatal("actual owner choices overwritten")
			}
			if mode == "new" && (v["hasUsedRemoteControl"] != nil || v["remoteDialogSeen"] != nil || v["projects"] != nil) {
				t.Fatal("canonical auth manufactured owner consent/trust")
			}
		})
	}
}

func TestRemoteControlCapacityHeldUntilRemoval(t *testing.T) {
	j := &Job{Agent: "codex", Pane: "w1:p1", Workspace: "w1", Host: "fixture-host", RemoteControl: syntheticRemoteRun(t)}
	ref := agentRef{Agent: j.Agent, Pane: j.Pane, Workspace: j.Workspace, Host: j.Host, Status: "done"}
	if !occupiesAdmissionSlot(j, ref, true) || !occupiesAdmissionSlot(j, agentRef{}, false) {
		t.Fatal("unconfirmed daemon work released capacity")
	}
	j.RemoteControl = nil
	if occupiesAdmissionSlot(j, ref, true) {
		t.Fatal("legacy matched done seat changed admission")
	}
}
