package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The read projection of one 2.1.2 run store (openCodeStoreReadScript output).
// Native-shaped synthetic data only. No API request or real agent is executed.
func openCodeStoreFixture(run *openCodeRun, prompt, answer string) []byte {
	created := int64(2000)
	variant := run.Route.Variant
	if variant == "" {
		variant = "default"
	}
	data, _ := json.Marshal(map[string]any{"info": map[string]any{"id": run.SessionID, "directory": run.Cwd, "created": created, "parentID": nil},
		"messages": []any{
			map[string]any{"id": "msg_user", "type": "user", "seq": 1, "time": map[string]any{"created": created}, "textDigests": []string{shaText([]byte(prompt))}},
			map[string]any{"id": "msg_assistant", "type": "assistant", "seq": 2, "time": map[string]any{"created": created + 1, "completed": created + 2},
				"agent": "build", "model": map[string]any{"id": run.Route.Model, "providerID": run.Route.Provider, "variant": variant},
				"finish": "stop", "failed": false, "content": []any{map[string]any{"type": "text", "text": strings.TrimSpace(answer) != "", "status": nil}}},
			map[string]any{"id": "msg_idle", "type": "idle", "seq": 3, "time": map[string]any{"created": created + 3}, "outcome": "succeeded", "failed": false},
		}})
	return data
}

func editStoreFixture(t *testing.T, raw []byte, edit func(messages []any)) []byte {
	t.Helper()
	var record map[string]any
	if json.Unmarshal(raw, &record) != nil {
		t.Fatal("fixture store")
	}
	edit(record["messages"].([]any))
	out, _ := json.Marshal(record)
	return out
}

func TestOpenCodeStoreRequiresRoutePromptAndStoppedAnswer(t *testing.T) {
	run := &openCodeRun{Route: openCodeRoute{"fixture-provider", "fixture-model", "high"}, Cwd: "/fixture/checkout", SessionID: "ses_fixture", NotBefore: 1000}
	run.ExpectedPromptDigest = shaText([]byte(runPointer))
	good := openCodeStoreFixture(run, runPointer, "OK")
	if confirmed, complete, created, err := inspectOpenCodeStore(good, run); !confirmed || !complete || created != 2000 || err != nil {
		t.Fatal("native answer was not confirmed", err)
	}
	assistant := func(field string, value any) []byte {
		return editStoreFixture(t, good, func(m []any) { m[1].(map[string]any)[field] = value })
	}
	idle := func(field string, value any) []byte {
		return editStoreFixture(t, good, func(m []any) { m[2].(map[string]any)[field] = value })
	}
	model := func(field, value string) []byte {
		return editStoreFixture(t, good, func(m []any) { m[1].(map[string]any)["model"].(map[string]any)[field] = value })
	}
	for _, tc := range []struct {
		name   string
		raw    []byte
		reason string
	}{
		{"empty", openCodeStoreFixture(run, runPointer, ""), "opencode-empty-answer"},
		{"whitespace", openCodeStoreFixture(run, runPointer, " \n\t"), "opencode-empty-answer"},
		{"reasoning only", assistant("content", []any{map[string]any{"type": "reasoning", "text": true}}), "opencode-empty-answer"},
		{"wrong session", []byte(strings.Replace(string(good), `"id":"ses_fixture"`, `"id":"ses_foreign"`, 1)), "opencode-output-unconfirmed"},
		{"wrong cwd", []byte(strings.Replace(string(good), "/fixture/checkout", "/fixture/foreign", 1)), "opencode-output-unconfirmed"},
		{"child session", []byte(strings.Replace(string(good), `"parentID":null`, `"parentID":"ses_parent"`, 1)), "opencode-output-unconfirmed"},
		{"wrong provider", model("providerID", "foreign"), "opencode-route-mismatch"},
		{"wrong model", model("id", "foreign"), "opencode-route-mismatch"},
		{"wrong variant", model("variant", "low"), "opencode-route-mismatch"},
		{"default for named variant", model("variant", "default"), "opencode-route-mismatch"},
		{"wrong agent", assistant("agent", "plan"), "opencode-route-mismatch"},
		{"answer before prompt", editStoreFixture(t, good, func(m []any) { m[0], m[1] = m[1], m[0] }), "opencode-route-mismatch"},
		{"wrong prompt", openCodeStoreFixture(run, "different prompt", "OK"), "opencode-output-unconfirmed"},
		{"provider error", assistant("failed", true), "opencode-provider-error"},
		{"error finish", assistant("finish", "error"), "opencode-provider-error"},
		{"failed idle", idle("outcome", "failed"), "opencode-provider-error"},
		{"unknown type", idle("type", "future"), "opencode-output-unconfirmed"},
		{"duplicate id", idle("id", "msg_user"), "opencode-output-unconfirmed"},
		{"seq order", idle("seq", 0), "opencode-output-unconfirmed"},
		{"duplicate field", []byte(strings.Replace(string(good), `"finish":"stop"`, `"finish":"stop","finish":"stop"`, 1)), "opencode-output-unconfirmed"},
		{"oversized", append(append([]byte{}, good...), []byte(strings.Repeat(" ", 1024*1024))...), "opencode-output-unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			confirmed, complete, _, err := inspectOpenCodeStore(tc.raw, run)
			if confirmed || complete || err != matrixReason(tc.reason) {
				t.Fatalf("bad native evidence passed: confirmed=%v completed=%v err=%v", confirmed, complete, err)
			}
		})
	}
	// Turn-in-progress evidence confirms submission and never completion.
	for name, raw := range map[string][]byte{
		"no idle record":    editStoreFixture(t, good, func(m []any) {}),
		"streaming":         assistant("time", map[string]any{"created": 2001}),
		"length finish":     assistant("finish", "length"),
		"tool-calls finish": assistant("finish", "tool-calls"),
		"pending tool":      assistant("content", []any{map[string]any{"type": "text", "text": true}, map[string]any{"type": "tool", "status": "running"}}),
		"interrupted idle":  idle("outcome", "interrupted"),
	} {
		if name == "no idle record" {
			var record map[string]any
			_ = json.Unmarshal(good, &record)
			record["messages"] = record["messages"].([]any)[:2]
			raw, _ = json.Marshal(record)
		}
		if confirmed, complete, _, err := inspectOpenCodeStore(raw, run); !confirmed || complete || err != nil {
			t.Fatalf("%s: unfinished turn got completion or lost acceptance (%v %v %v)", name, confirmed, complete, err)
		}
	}
	finished := assistant("content", []any{map[string]any{"type": "tool", "status": "completed"}, map[string]any{"type": "tool", "status": "error"}, map[string]any{"type": "text", "text": true}})
	if confirmed, complete, _, err := inspectOpenCodeStore(finished, run); !confirmed || !complete || err != nil {
		t.Fatal("finished tools blocked a final stop", err)
	}
	// A later prompt (steering) resets completion until its own answer stops.
	later := editStoreFixture(t, good, func(m []any) {})
	var record map[string]any
	_ = json.Unmarshal(later, &record)
	record["messages"] = append(record["messages"].([]any), map[string]any{"id": "msg_next", "type": "user", "seq": 4, "time": map[string]any{"created": 2004}, "textDigests": []string{"other"}})
	later, _ = json.Marshal(record)
	if confirmed, complete, _, err := inspectOpenCodeStore(later, run); confirmed || complete || err != nil {
		t.Fatal("an earlier answer completed a later user turn")
	}
	if confirmed, complete, _, err := inspectOpenCodeStore([]byte(`{"info":null,"messages":[]}`), run); confirmed || complete || err != nil {
		t.Fatal("a store without the session is not an empty observation")
	}
	stale := *run
	stale.NotBefore = 3000
	if _, _, _, err := inspectOpenCodeStore(good, &stale); err == nil {
		t.Fatal("old session confirmed a new launch")
	}
	stale.NotBefore = 0
	if _, _, _, err := inspectOpenCodeStore(good, &stale); err == nil {
		t.Fatal("missing native freshness baseline was accepted")
	}
	stale.NotBefore, stale.CreatedAt = run.NotBefore, 9999
	if _, _, _, err := inspectOpenCodeStore(good, &stale); err == nil {
		t.Fatal("store replaced the bound session creation time")
	}
	defaulted := *run
	defaulted.Route.Variant = ""
	if confirmed, complete, _, err := inspectOpenCodeStore(openCodeStoreFixture(&defaulted, runPointer, "OK"), &defaulted); !confirmed || !complete || err != nil {
		t.Fatal("provider default variant was not matched", err)
	}
}

// writeOpenCodeRunStore writes the two 2.1.2 tables the read projection selects.
func writeOpenCodeRunStore(t *testing.T, home, name, session, cwd string, rows [][3]string) string {
	t.Helper()
	dir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	encoded, _ := json.Marshal(rows)
	script := `import json,sqlite3,sys
db=sqlite3.connect(sys.argv[1]);db.execute('create table session_v2(id text primary key,directory text,time_created integer,parent_id text)')
db.execute('create table session_message(id text primary key,session_id text,type text,seq integer,time_created integer,data text)')
db.execute('insert into session_v2 values(?,?,?,null)',(sys.argv[2],sys.argv[3],2000))
for i,(mid,kind,data) in enumerate(json.loads(sys.argv[4])):db.execute('insert into session_message values(?,?,?,?,?,?)',(mid,sys.argv[2],kind,i+1,2000+i,data))
db.commit()`
	if out, err := exec.Command("python3", "-c", script, path, session, cwd, string(encoded)).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenCodeRunStoreReadIsReadOnlyAndKeepsPromptPrivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	cwd := filepath.Join(home, "checkout")
	h := Host{Home: home, SSH: "localhost", OpenCodeCLI: &OpenCodeConfig{}}
	run := &openCodeRun{Route: openCodeRoute{"fixture-provider", "fixture-model", "high"}, Cwd: cwd, SessionID: "ses_fixture", NotBefore: 1000,
		ExpectedPromptDigest: shaText([]byte(runPointer))}
	if raw, err := h.openCodeStore(context.Background(), run); err != nil || string(raw) != "{\"info\": null, \"messages\": []}\n" {
		t.Fatalf("a run without its store is not an empty observation: %q %v", raw, err)
	}
	user, _ := json.Marshal(map[string]any{"time": map[string]any{"created": 2000}, "text": runPointer + " ", "files": []any{}})
	answer, _ := json.Marshal(map[string]any{"time": map[string]any{"created": 2001, "completed": 2002}, "agent": "build",
		"model": map[string]any{"id": "fixture-model", "providerID": "fixture-provider", "variant": "high"}, "finish": "stop",
		"content": []any{map[string]any{"type": "reasoning", "text": "private"}, map[string]any{"type": "text", "text": "OK"}}})
	idle, _ := json.Marshal(map[string]any{"time": map[string]any{"created": 2003}, "outcome": "succeeded"})
	path := writeOpenCodeRunStore(t, home, "opencode-v2-ses_fixture.db", run.SessionID, cwd,
		[][3]string{{"msg_user", "user", string(user)}, {"msg_answer", "assistant", string(answer)}, {"msg_idle", "idle", string(idle)}})
	before, _ := os.Stat(path)
	raw, err := h.openCodeStore(context.Background(), run)
	if err != nil || strings.Contains(string(raw), runPointer) || strings.Contains(string(raw), "private") {
		t.Fatalf("store read failed or published native text: %v", err)
	}
	// The TUI appends one space to a pasted prompt; the digest of the rendered goal still binds.
	if confirmed, complete, created, err := inspectOpenCodeStore(raw, run); !confirmed || !complete || created != 2000 || err != nil {
		t.Fatal("real store projection was not confirmed", err)
	}
	after, _ := os.Stat(path)
	if after.ModTime() != before.ModTime() || after.Size() != before.Size() {
		t.Fatal("observer changed the run store")
	}
	if os.Chmod(path, 0666) != nil {
		t.Fatal("chmod")
	}
	if _, err := h.openCodeStore(context.Background(), run); err == nil {
		t.Fatal("a store others can write was read")
	}
}

// The confirmed/completed/error view the observation tests assert on.
func inspectStoreFixture(raw []byte, run *openCodeRun) (bool, bool, error) {
	confirmed, completed, _, err := inspectOpenCodeStore(raw, run)
	return confirmed, completed, err
}

// Review round 1: new work after a turn's end reopens it, and the end must follow the answer.
func TestOpenCodeStoreCompactionReopensAndIdleFollowsAnswer(t *testing.T) {
	run := &openCodeRun{Route: openCodeRoute{"fixture-provider", "fixture-model", "high"}, Cwd: "/fixture/checkout", SessionID: "ses_fixture", NotBefore: 1000}
	run.ExpectedPromptDigest = shaText([]byte(runPointer))
	good := openCodeStoreFixture(run, runPointer, "OK")
	with := func(extra ...any) []byte {
		var record map[string]any
		_ = json.Unmarshal(good, &record)
		record["messages"] = append(record["messages"].([]any), extra...)
		out, _ := json.Marshal(record)
		return out
	}
	for _, kind := range []string{"compaction", "synthetic", "shell"} {
		reopened := with(map[string]any{"id": "msg_" + kind, "type": kind, "seq": 4, "time": map[string]any{"created": 2004}})
		if confirmed, complete, _, err := inspectOpenCodeStore(reopened, run); !confirmed || complete || err != nil {
			t.Fatalf("%s after a succeeded turn kept its completion (%v %v %v)", kind, confirmed, complete, err)
		}
		idleOnly := with(map[string]any{"id": "msg_" + kind, "type": kind, "seq": 4, "time": map[string]any{"created": 2004}},
			map[string]any{"id": "msg_idle2", "type": "idle", "seq": 5, "time": map[string]any{"created": 2005}, "outcome": "succeeded", "failed": false})
		if _, complete, _, _ := inspectOpenCodeStore(idleOnly, run); complete {
			t.Fatalf("%s then an idle reused the old answer as completion", kind)
		}
	}
	early := editStoreFixture(t, good, func(m []any) {
		m[1].(map[string]any)["time"] = map[string]any{"created": 2001, "completed": 2010}
		m[2].(map[string]any)["time"] = map[string]any{"created": 2005}
	})
	if _, complete, _, _ := inspectOpenCodeStore(early, run); complete {
		t.Fatal("a succeeded idle before the answer completed was accepted")
	}
}

// Review round 1: the run database name is the Harness reader's contract, never configuration.
func TestOpenCodeRunDatabaseNameMatchesHarnessReader(t *testing.T) {
	if openCodeRunDatabasePrefix != "opencode-v2-" {
		t.Fatal("the run database prefix drifted from Harness OPENCODE_RUN_DB_PREFIX")
	}
	if db, err := openCodeRunDatabase("ses_fixture"); err != nil || db != "opencode-v2-ses_fixture.db" {
		t.Fatal("run database not named by its session", db, err)
	}
	cmd, err := buildAgentCmd("opencode-run", Overrides{Model: "fixture-provider/fixture-model", Effort: "high"})
	if err != nil || !strings.Contains(cmd, `--session "${`+openCodeRunSessionEnv+`:?}"`) {
		t.Fatal("opencode run does not use the pre-assigned session that names its database", cmd, err)
	}
}

// Review round 1: blobs are bounded before they are materialized; over the cap is no observation.
func TestOpenCodeRunStoreReadRefusesOversizedRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", "")
	cwd := filepath.Join(home, "checkout")
	h := Host{Home: home, SSH: "localhost", OpenCodeCLI: &OpenCodeConfig{}}
	run := &openCodeRun{Route: openCodeRoute{"fixture-provider", "fixture-model", "high"}, Cwd: cwd, SessionID: "ses_fixture", NotBefore: 1000,
		ExpectedPromptDigest: shaText([]byte(runPointer))}
	path := writeOpenCodeRunStore(t, home, "opencode-v2-ses_fixture.db", run.SessionID, cwd, [][3]string{{"msg_user", "user", `{"time":{"created":2000},"text":"x","files":[]}`}})
	grow := `import sqlite3,sys
import json
body={"time":{"created":2001,"completed":2002},"agent":"build","model":{"id":"fixture-model","providerID":"fixture-provider","variant":"high"},
 "finish":"stop","content":[{"type":"text","text":"x"*(1048576+1)}]}
db=sqlite3.connect(sys.argv[1]);db.execute("insert into session_message values('msg_big',?, 'assistant', 2, 2001, ?)",(sys.argv[2],json.dumps(body)));db.commit()`
	if out, err := exec.Command("python3", "-c", grow, path, run.SessionID).CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	if raw, err := h.openCodeStore(context.Background(), run); err == nil {
		t.Fatalf("an oversized row was materialized and projected: %d bytes", len(raw))
	}
}
