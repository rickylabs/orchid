package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"path/filepath"
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
	c.reportIssueMatrixRefusal(context.Background(), 1, is, matrixRefusal{"refused", "synthetic-private-canary"}, post)
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
