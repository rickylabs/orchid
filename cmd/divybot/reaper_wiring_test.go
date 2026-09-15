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

// Coverage guard: adding an unregistered command reintroduces the ECHILD race.
// Inspect production files, including launch sites added after the original reaper PR.
func TestReaperEveryExecSiteHoldsGate(t *testing.T) {
	files, err := parser.ParseDir(token.NewFileSet(), ".", func(info fs.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal("production AST unavailable")
	}
	sites := 0
	for _, pkg := range files {
		for _, file := range pkg.Files {
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
			if execAlias == "" {
				continue
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				hold := token.NoPos
				for _, stmt := range fn.Body.List {
					d, ok := stmt.(*ast.DeferStmt)
					if !ok {
						continue
					}
					call, ok := d.Call.Fun.(*ast.CallExpr)
					if !ok {
						continue
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					owner, ok := sel.X.(*ast.Ident)
					if ok && owner.Name == "children" && sel.Sel.Name == "hold" {
						hold = stmt.Pos()
					}
				}
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					owner, ok := sel.X.(*ast.Ident)
					if !ok || owner.Name != execAlias || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
						return true
					}
					sites++
					if hold == token.NoPos || hold > call.Pos() {
						t.Errorf("process launch in %s lacks an earlier deferred child gate", fn.Name.Name)
					}
					return true
				})
			}
		}
	}
	if sites == 0 {
		t.Fatal("no subprocess sites inspected")
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
