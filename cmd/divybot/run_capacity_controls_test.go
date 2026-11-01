package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRemoteRunClaudeEndRequiresExitedNativeProcess(t *testing.T) {
	_, j, h, _, _, _ := nativeCapacityFixture(t, "valid")
	j.Agent = "claude"
	j.RemoteControl.Cwd = filepath.Join(t.TempDir(), "issue-7")
	j.TerminalID = "synthetic-terminal"
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "stat"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(raw)[strings.LastIndex(string(raw), ")")+1:])
	j.RemoteControl.ClaudeProcess = &claudeProcess{PID: cmd.Process.Pid, Start: fields[19]}
	event := vendorLifecycleEvent{Agent: j.Agent, Name: j.Label, Pane: j.Pane, Workspace: j.Workspace, Cwd: j.RemoteControl.Cwd,
		Terminal: j.TerminalID, Session: j.RemoteControl.NativeSessionID, Event: "SessionEnd", Reason: "other", Seq: 42}
	file := vendorLifecycleFile(j.Agent, j.Label, j.Pane, j.Workspace, j.RemoteControl.Cwd)
	if os.MkdirAll(filepath.Dir(file), 0700) != nil {
		t.Fatal("event directory")
	}
	write := func(e vendorLifecycleEvent) {
		t.Helper()
		if os.WriteFile(file, goalJSON(e), 0600) != nil {
			t.Fatal("event write")
		}
	}
	write(event)
	if h.claudeSessionEnded(context.Background(), j) {
		t.Fatal("live native runtime released by an end hook")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if !h.claudeSessionEnded(context.Background(), j) {
		t.Fatal("native exited runtime and terminal hook not accepted")
	}
	for _, reason := range []string{"clear", "resume", "", "future-reason"} {
		e := event
		e.Reason = reason
		write(e)
		if h.claudeSessionEnded(context.Background(), j) {
			t.Fatal("nonterminal or legacy reason accepted", reason)
		}
	}
	write(event)
	for _, field := range []string{"session", "terminal", "workspace", "label"} {
		e := event
		switch field {
		case "session":
			e.Session = "foreign"
		case "terminal":
			e.Terminal = "foreign"
		case "workspace":
			e.Workspace = "foreign"
		case "label":
			e.Name = "foreign"
		}
		write(e)
		if h.claudeSessionEnded(context.Background(), j) {
			t.Fatal("foreign native end evidence accepted", field)
		}
	}
	write(event)
	if os.Chmod(file, 0644) != nil {
		t.Fatal("event mode")
	}
	if h.claudeSessionEnded(context.Background(), j) {
		t.Fatal("untrusted native receipt mode accepted")
	}
}

func TestRemoteRunClaudeDoneProofDoesNotPublish(t *testing.T) {
	c, j, _, _, _ := claudeCoordinatorFixture(t)
	j.RemoteControl = &remoteControlRun{NativeSessionID: "thread-fixture", Cwd: completionAgent(j).Cwd}
	dir, err := c.finalPublicationDir(j.DispatchKey, true)
	if err != nil {
		t.Fatal(err)
	}
	scope := finalReportScope{SchemaVersion: 1, Key: j.DispatchKey, Destination: jobHome(c.cfg.Inbox, j), Host: j.Host, Source: j.Agent,
		Repo: j.Repo, Cwd: completionAgent(j).Cwd, BindingDigest: strings.Repeat("b", 64), DispatchDigest: strings.Repeat("c", 64)}
	if os.WriteFile(filepath.Join(dir, "scope.json"), goalJSON(scope), 0600) != nil {
		t.Fatal("scope write")
	}
	c.finalCalls.proof = func(context.Context, Host, *Job, finalReportScope) error { return nil }
	c.finalCalls.report = func(context.Context, Host, finalReportScope) (string, error) {
		return "Synthetic completed assignment", nil
	}
	c.finalCalls.write = func(string, string, any) error { t.Fatal("capacity probe wrote publication intent"); return nil }
	c.finalCalls.post = func(context.Context, dispatchIssue, string) (int64, error) {
		t.Fatal("capacity probe posted final report")
		return 0, nil
	}
	c.actions.completionPR = func(context.Context, *Job) (bool, error) { return true, nil }
	ref := completionRef(j)
	ref.StateChangeSeq = 42
	if c.retireCompleted(context.Background(), j.Issue, j, ref, true) || !nativeCapacityDone(j, time.Now()) {
		t.Fatal("pending-PR Claude completion did not release account capacity")
	}
	if c.st.Jobs[j.Issue] != j || len(c.st.CompletedRuns) != 0 {
		t.Fatal("capacity probe retired a managed run")
	}
	if _, err := os.Stat(filepath.Join(dir, "intent.json")); !os.IsNotExist(err) {
		t.Fatal("capacity probe froze final publication")
	}
}

func TestRemoteRunReaperPacingSurvivesRestart(t *testing.T) {
	c, j, h, _, _, _ := nativeCapacityFixture(t, "valid")
	issue := j.Issue
	for round := 1; round <= deadMissThreshold; round++ {
		ageRemoteMiss(j)
		c.reapRemoteRuns(context.Background(), nil, map[string]bool{h.observerKey(): true}, map[int]bool{issue: true})
		if round < deadMissThreshold {
			c.reapRemoteRuns(context.Background(), nil, map[string]bool{h.observerKey(): true}, map[int]bool{issue: true})
			if j.Misses != round {
				t.Fatal("event burst accelerated absence debounce")
			}
			c.st = loadState(c.st.path)
			j = c.st.Jobs[issue]
			if j == nil || j.Misses != round {
				t.Fatal("restart lost paced absence evidence")
			}
		}
	}
	if c.st.Jobs[issue] != nil {
		t.Fatal("persistent native-inactive absence was not reaped")
	}
}

func TestRemoteRunDoneProbeDebouncesEquivalentScopes(t *testing.T) {
	c, j, h, ref, _, _ := nativeCapacityFixture(t, "valid")
	ref.HerdrScope = &herdrScope{Session: "default"}
	c.observeRemoteDone(context.Background(), h, j, ref)
	if !nativeCapacityDone(j, time.Now()) {
		t.Fatal("fixture not natively done")
	}
	ref.HerdrScope = &herdrScope{Session: "default"} // next fleet snapshot allocates a new scope
	c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
		t.Fatal("equivalent fleet scope bypassed native probe debounce")
		return nil, nil
	}
	c.refreshRunCapacity(map[int]agentRef{j.Issue: ref})
	c.observeRemoteDone(context.Background(), h, j, ref)
}

// Keep behavioral controls connected to the production lifecycle boundaries.
func TestRemoteRunProductionWiring(t *testing.T) {
	for _, target := range []struct{ file, fn, call string }{
		{"main.go", "tick", "reapRemoteRuns"}, {"main.go", "tick", "refreshRunCapacity"},
		{"completion.go", "retireCompleted", "observeRemoteDone"}, {"matrix.go", "matrixAttempt", "reconcileRunCapacityBudget"},
		{"action_delivery.go", "actionSend", "acquireRunCapacity"}, {"main.go", "teardownConfirmed", "releaseMissingRemote"},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), target.file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Name.Name != target.fn {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					if method, ok := call.Fun.(*ast.SelectorExpr); ok && method.Sel.Name == target.call {
						found = true
					}
				}
				return true
			})
		}
		if !found {
			t.Fatal("native capacity control disconnected", target.file, target.fn, target.call)
		}
	}
}
