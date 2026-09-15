package main

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMatrixRefusalNotification(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	statePath := filepath.Join(privateTestRoot(t), "state.json")
	c := &Coord{cfg: &Config{Inbox: "example/inbox"}, st: loadState(statePath)}
	is := Issue{ID: "synthetic-private-canary", Number: 1, Title: "synthetic-private-canary", Body: "synthetic-private-canary"}
	posts := 0
	fail := true
	post := func(_ context.Context, _ string, _ int, body string) error {
		posts++
		if !strings.Contains(body, "matrix.source") || !strings.Contains(body, "source-missing") || strings.Contains(body, "synthetic-private-canary") {
			t.Fatal("unsafe or unhelpful comment")
		}
		if fail {
			return errors.New("synthetic-private-canary")
		}
		return nil
	}
	r := refusalFor(matrixReason("source-missing"))
	c.reportIssueMatrixRefusal(context.Background(), 1, is, r, post)
	fail = false
	c.reportIssueMatrixRefusal(context.Background(), 1, is, r, post)
	c.st = loadState(statePath)
	c.reportIssueMatrixRefusal(context.Background(), 1, is, r, post)
	if posts != 2 {
		t.Fatal("failed comment was not retried, or restart duplicated notification")
	}
	is.Body += "changed"
	c.reportIssueMatrixRefusal(context.Background(), 1, is, r, post)
	if posts != 3 {
		t.Fatal("changed brief was not reported")
	}
	c.dry = true
	c.reportIssueMatrixRefusal(context.Background(), 1, is, matrixRefusal{"refused", "synthetic-private-canary", ""}, post)
	if posts != 3 || strings.Contains(logs.String(), "synthetic-private-canary") {
		t.Fatal("dry run posted or private error leaked")
	}
	if !strings.Contains(logs.String(), "reason=source-missing field=matrix.source") {
		t.Fatal("log lost field/reason")
	}
}

func TestMatrixBridgeRefusalDecode(t *testing.T) {
	for _, input := range []string{`{"status":"refused","reasonCode":"authorization-required"}`, `{"status":"refused","reasonCode":"synthetic-private-canary"}`, `{"status":"refused","reasonCode":"authorization-required","detail":"synthetic-private-canary"}`} {
		_, err := decodeMatrixResult([]byte(input))
		if err == nil {
			t.Fatal("refusal accepted")
		}
		r := refusalFor(err)
		if strings.Contains(r.ReasonCode, "canary") {
			t.Fatal("untrusted diagnostic accepted")
		}
	}
}

func TestMatrixSiteSurvivesToPublicDiagnostic(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	c := &Coord{cfg: &Config{Inbox: "example/inbox"}, st: loadState(filepath.Join(privateTestRoot(t), "state.json"))}
	is := Issue{Number: 1, ID: "synthetic", Title: "Synthetic", Body: "Synthetic"}
	for _, site := range []string{"receipt.file-create", "receipt.file-write-sync-close", "spawn.receipt-claim", "spawn.agent-start"} {
		e := matrixSite("launch.registration", matrixSite(site, errMatrix))
		if !errors.Is(e, errMatrix) {
			t.Fatal("wrapped sentinel lost")
		}
		r := refusalWithReason(e, "launch-failed")
		c.reportIssueMatrixRefusal(context.Background(), 1, is, r, func(_ context.Context, _ string, _ int, body string) error {
			if !strings.Contains(body, "Cause: `"+site+"`") {
				t.Fatal("originating cause lost in comment")
			}
			return nil
		})
		if !strings.Contains(logs.String(), "cause="+site) {
			t.Fatal("originating cause lost in log")
		}
	}
	if validMatrixRefusal(matrixRefusal{"refused", "launch-failed", "synthetic-private-canary"}) {
		t.Fatal("untrusted site accepted")
	}
}

func TestMatrixReturnsNameTheirSite(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range []string{"matrix.go", "main.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal("source unavailable")
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if r, ok := n.(*ast.ReturnStmt); ok {
				for _, v := range r.Results {
					if id, ok := v.(*ast.Ident); ok && id.Name == "errMatrix" {
						t.Fatal("bare matrix sentinel loses refusal site")
					}
				}
			}
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "matrixSite" {
					lit, ok := c.Args[0].(*ast.BasicLit)
					if !ok {
						t.Fatal("site must be static")
					}
					site, err := strconv.Unquote(lit.Value)
					if err != nil || !matrixSites[site] || seen[site] {
						t.Fatal("site must be distinct and closed")
					}
					seen[site] = true
				}
			}
			return true
		})
	}
	if len(seen) < 30 {
		t.Fatal("site inventory incomplete")
	}
}
