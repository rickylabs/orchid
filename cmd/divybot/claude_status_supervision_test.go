package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// harness#613 (2026-10-06): an inbox Claude root finished its turn seven seconds after dispatch and
// sat at its prompt, which herdr reports as "done". Supervision suppresses input for a done root,
// and the read-only status observation sat inside that gate, so no tick ever recorded the stopped
// root: the issue agent read Unknown for its whole run. A matched root is observed in every state;
// input stays suppressed for done.
func TestSupervisionObservesMatchedClaudeRootInEveryState(t *testing.T) {
	for _, status := range []string{"done", "idle", "working"} {
		t.Run(status, func(t *testing.T) {
			_, c, j, record, id := boundClaudeSupervision(t, status)
			ref := completionRef(j)
			ref.Status = status
			c.superviseActive(context.Background(), 7, j, map[int]agentRef{7: ref}, Issue{Number: 7})
			var row claudeStatusObservation
			if err := readPrivateActionJSON(filepath.Join(record, "claude-status.json"), &row); err != nil ||
				row.Status != status || row.NativeSessionID != id || row.RunID != "orchid-"+j.DispatchKey {
				t.Fatalf("a matched %s Claude root was not observed: %+v %v", status, row, err)
			}
		})
	}
}

// boundClaudeSupervision is the completion fixture as a bound Claude root on a remote host whose
// `herdr agent get` answers for the job's own pane with the given status.
func boundClaudeSupervision(t *testing.T, status string) (string, *Coord, *Job, string, string) {
	t.Helper()
	_, sshLog := completionSupervisionCommands(t)
	c, j, _, _, _ := completionFixture(t)
	h := c.hosts[j.Host]
	h.SSH = "fixture-host"
	c.hosts[j.Host] = h
	record := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
	j.Agent, j.Label = "claude", "claude-fixture"
	var dispatch dispatchBinding
	if readPrivateActionJSON(filepath.Join(record, "dispatch.json"), &dispatch) != nil || dispatch.Location == nil {
		t.Fatal("fixture dispatch")
	}
	dispatch.Source = "claude"
	body, _ := json.Marshal(dispatch)
	if os.WriteFile(filepath.Join(record, "dispatch.json"), body, 0600) != nil {
		t.Fatal("fixture dispatch")
	}
	id := privateTestID(t)
	binding, _ := json.Marshal(map[string]string{"NativeSessionID": id, "Repo": j.Repo})
	if os.WriteFile(filepath.Join(record, "binding.json"), binding, 0600) != nil {
		t.Fatal("fixture binding")
	}
	info := claudeAgentInfo(t, id)
	agent := info["agent"].(map[string]any)
	agent["name"], agent["pane_id"], agent["workspace_id"], agent["agent_status"] =
		j.Label, dispatch.Location.PaneID, dispatch.Location.WorkspaceID, status
	herdrAgentGet(t, map[string]any{"id": "fixture", "result": info})
	return sshLog, c, j, record, id
}

// herdrAgentGet answers `herdr agent get` over the fixture ssh with one agent_info document and
// every other remote command with an empty list.
func herdrAgentGet(t *testing.T, response any) {
	t.Helper()
	dir := t.TempDir()
	body, _ := json.Marshal(response)
	if os.WriteFile(filepath.Join(dir, "agent-get.json"), body, 0600) != nil {
		t.Fatal("fixture herdr")
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$COMPLETION_SSH_LOG\"\ncase \"$*\" in\n *\"'agent' 'get'\"*) cat \"" +
		filepath.Join(dir, "agent-get.json") + "\" ;;\n *) printf '[]\\n' ;;\nesac\n"
	if os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700) != nil {
		t.Fatal("fixture ssh")
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A seat that does not match the job is never read: no status row, and no native read or input,
// even while the other seat reports working.
func TestSupervisionNeverObservesAMismatchedClaudeSeat(t *testing.T) {
	sshLog, c, j, record, _ := boundClaudeSupervision(t, "working")
	ref := completionRef(j)
	ref.Status, ref.Pane = "working", "other:pane"
	c.superviseActive(context.Background(), 7, j, map[int]agentRef{7: ref}, Issue{Number: 7})
	remote, _ := os.ReadFile(sshLog)
	if strings.Contains(string(remote), "'agent' 'get'") || strings.Contains(string(remote), "'send'") {
		t.Fatalf("a mismatched seat was read or sent input: %s", remote)
	}
	if _, err := os.Stat(filepath.Join(record, "claude-status.json")); !os.IsNotExist(err) {
		t.Fatal("a mismatched seat produced a status row")
	}
}

// The bound-goal retry still sends nothing to a suppressed seat (done, or not the job's own); only
// the read-only status observation moved out of that gate.
func TestSupervisionGoalRetryStaysInputGated(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "if !suppressInput && (ref.Status == \"working\" || ref.Status == \"blocked\") {\n\t\tc.retryBoundGoal(ctx, host, j)") {
		t.Fatal("goal retry is no longer behind the input-suppression gate")
	}
	if !strings.Contains(text, "if !suppressInput {\n\t\tc.bindLiveNativeIdentity(ctx, host, j)\n\t}") {
		t.Fatal("native identity binding is no longer behind the input-suppression gate")
	}
}
