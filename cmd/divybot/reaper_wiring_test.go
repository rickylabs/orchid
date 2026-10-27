package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// Coverage guard: bypassing the owned command helper reintroduces the wait race.
func TestReaperEveryExecSiteUsesOwnedCommand(t *testing.T) {
	files, err := parser.ParseDir(token.NewFileSet(), ".", func(info fs.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal("production AST unavailable")
	}
	constructors, callers := 0, 0
	for _, pkg := range files {
		for filename, file := range pkg.Files {
			execAlias := ""
			for _, imp := range file.Imports {
				value, _ := strconv.Unquote(imp.Path.Value)
				if value == "os/exec" {
					execAlias = "exec"
					if imp.Name != nil {
						execAlias = imp.Name.Name
					}
					if execAlias == "." {
						t.Fatal("dot imports conceal process creation")
					}
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "commandContext" {
					callers++
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if owner, ok := sel.X.(*ast.Ident); ok && owner.Name == execAlias && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
					constructors++
					if filename != "reaper.go" {
						t.Error("raw command construction bypasses ownership")
					}
				}
				if raw, ok := sel.X.(*ast.SelectorExpr); ok && raw.Sel.Name == "Cmd" {
					switch sel.Sel.Name {
					case "Start", "Wait", "Run", "Output", "CombinedOutput":
						if filename != "reaper.go" {
							t.Error("raw command lifecycle bypasses ownership")
						}
					}
				}
				return true
			})
		}
	}
	if constructors != 1 || callers == 0 {
		t.Fatal("expected one owned constructor and production callers")
	}
}

func TestReaperNeverSignalsProcesses(t *testing.T) {
	files, err := parser.ParseDir(token.NewFileSet(), ".", func(info fs.FileInfo) bool {
		return strings.HasPrefix(info.Name(), "reaper") && strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal("reaper AST unavailable")
	}
	for _, pkg := range files {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "Kill", "Signal", "Syscall", "Syscall6", "RawSyscall", "RawSyscall6":
					t.Error("the reaper must not signal processes or introduce raw syscalls")
				}
				return true
			})
		}
	}
}

func TestReaperMainStartsReaper(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal("main AST unavailable")
	}
	calls := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			continue
		}
		for _, stmt := range fn.Body.List {
			expr, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expr.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			name, ok := call.Fun.(*ast.Ident)
			if ok && name.Name == "startReaper" {
				calls++
			}
		}
	}
	if calls != 1 {
		t.Fatal("main must start the guarded reaper exactly once")
	}
}
