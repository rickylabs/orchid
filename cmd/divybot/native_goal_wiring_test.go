package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeGoalDispatchPreflight(t *testing.T) {
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox"}}
	for _, agent := range []string{"codex", "claude", "agy", "codex-run"} {
		e := c.spawn(context.Background(), 7, Issue{Number: 7, Title: "synthetic"}, Host{}, agent, Overrides{MaxTokens: "invalid"}, nil)
		if agent == "codex" {
			if refusalFor(e).ReasonCode != "goal-budget-invalid" {
				t.Fatal("Codex budget preflight skipped")
			}
		} else if matrixCause(e) != "launch.target" {
			t.Fatal("native goal policy rerouted another transport")
		}
	}
}

// Like the existing reaper wiring guard, this covers production call sites in
// addition to behavioral controls of each hook and the separate live proof.
func TestNativeGoalDispatchWiring(t *testing.T) {
	f, e := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if e != nil {
		t.Fatal("dispatcher AST unavailable")
	}
	want := map[string]map[string]int{"spawn": {"startDispatchGoal": 1}, "supervise": {"transitionGoal": 2}, "tick": {"finishAssignmentGoal": 1, "transitionGoal": 1}}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		methods, ok := want[fn.Name.Name]
		if !ok {
			continue
		}
		counts := map[string]int{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			s, ok := call.Fun.(*ast.SelectorExpr)
			if ok {
				counts[s.Sel.Name]++
			}
			return true
		})
		for method, n := range methods {
			if counts[method] != n {
				t.Fatal("native goal lifecycle hook disconnected")
			}
		}
		delete(want, fn.Name.Name)
	}
	if len(want) != 0 {
		t.Fatal("dispatcher lifecycle surface missing")
	}
}

func TestNativeGoalRefusalDoesNotDenyAnExistingLaunch(t *testing.T) {
	for _, reason := range []string{"goal-prompt-delivery-failed", "goal-budget-invalid"} {
		c := &Coord{cfg: &Config{Inbox: "fixture/inbox"}, st: loadState(filepath.Join(t.TempDir(), "state.json"))}
		body := ""
		c.reportIssueMatrixRefusal(context.Background(), 7, Issue{ID: "fixture-issue", Title: "fixture task"}, matrixRefusal{"refused", reason, ""}, func(_ context.Context, _ string, _ int, text string) error { body = text; return nil })
		if body == "" {
			t.Fatal("refusal comment missing")
		}
		if reason == "goal-prompt-delivery-failed" {
			if strings.Contains(body, "No agent was launched") || !strings.Contains(body, "requires inspection") {
				t.Fatal("uncertain prompt delivery denied an existing launch")
			}
		} else if !strings.Contains(body, "No agent was launched") {
			t.Fatal("preflight refusal lost its meaning")
		}
	}
}

// These fences couple the tested helpers to launch ordering. A successful goal
// helper alone cannot prove that failed prompt delivery never invokes it.
func TestNativeGoalLaunchOwnershipWiring(t *testing.T) {
	f, e := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if e != nil {
		t.Fatal("dispatcher AST unavailable")
	}
	render := func(n ast.Node) string {
		var b bytes.Buffer
		_ = format.Node(&b, token.NewFileSet(), n)
		return b.String()
	}
	foundOwnership, foundPromptFence := false, false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "spawn" {
			continue
		}
		for _, stmt := range fn.Body.List {
			condition, ok := stmt.(*ast.IfStmt)
			if ok && strings.Contains(render(condition.Body), "j.NativeGoal =") {
				foundOwnership = render(condition.Cond) == `agent == "codex"`
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			condition, ok := n.(*ast.IfStmt)
			if !ok || condition.Init == nil || !strings.Contains(render(condition.Init), "host.injectGoal(") {
				return true
			}
			if render(condition.Cond) != "err != nil" || len(condition.Body.List) == 0 {
				return true
			}
			ret, ok := condition.Body.List[len(condition.Body.List)-1].(*ast.ReturnStmt)
			foundPromptFence = ok && render(ret) == `return matrixReason("goal-prompt-delivery-failed")`
			return true
		})
	}
	if !foundOwnership {
		t.Fatal("native goal ownership is not scoped to the Codex launch")
	}
	if !foundPromptFence {
		t.Fatal("failed prompt delivery can reach native goal creation")
	}
}
