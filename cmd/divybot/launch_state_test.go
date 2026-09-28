package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssueLaunchRefusalStateIsSafeAndSuperseded(t *testing.T) {
	root := privateTestRoot(t)
	c := &Coord{cfg: &Config{Inbox: "example/inbox", Matrix: MatrixConfig{ReceiptRoot: root}},
		st: loadState(filepath.Join(root, "state.json"))}
	is := Issue{ID: "synthetic-private-issue-id", Number: 7, Title: "synthetic-private-title",
		Body: "synthetic-private-body"}
	post := func(context.Context, string, int, string) error { return nil }
	r := refusalFor(matrixReason("routing-invalid"))
	c.reportIssueMatrixRefusal(context.Background(), 7, is, r, post)
	path := launchStatePath(root, c.cfg.Inbox, is)
	read := func() launchStateRecord {
		t.Helper()
		stat, err := os.Lstat(path)
		if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm() != 0600 {
			t.Fatal("launch state mode or type invalid")
		}
		body, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(body), "synthetic-private") {
			t.Fatal("private issue content escaped into launch state")
		}
		var got launchStateRecord
		if json.Unmarshal(body, &got) != nil {
			t.Fatal("launch state not JSON")
		}
		return got
	}
	first := read()
	if first.State != "refused" || first.ReasonCode == nil || *first.ReasonCode != "routing-invalid" ||
		first.Issue != (dispatchIssue{Repo: "example/inbox", Number: 7}) ||
		!strings.HasPrefix(first.DispatchID, "assignment_") || first.ObservedAt == "" {
		t.Fatal("refusal lost issue, closed reason, time or opaque identity")
	}
	c.reportIssueMatrixRefusal(context.Background(), 7, is, r, post)
	if read().ObservedAt != first.ObservedAt {
		t.Fatal("repeat poll changed the first refusal time")
	}
	c.reportIssueMatrixRefusal(context.Background(), 7, is, evaluatorRefusal(), post)
	if read().State != "refused" {
		t.Fatal("inconclusive result corrupted the verified refusal")
	}
	c.publishLaunchState(7, is, "launching", "")
	if got := read(); got.State != "launching" || got.ReasonCode != nil {
		t.Fatal("later launch did not clear the refusal before effects")
	}
	c.publishLaunchState(7, is, "launched", "")
	if got := read(); got.State != "launched" || got.ReasonCode != nil {
		t.Fatal("successful launch did not supersede refusal")
	}
}

func TestLaunchRefusalStateRejectsUnclosedAndUnboundInputs(t *testing.T) {
	root := privateTestRoot(t)
	is := Issue{ID: "fixture-id", Number: 3, Body: "fixture-body"}
	for _, tc := range []struct {
		issue         Issue
		state, reason string
	}{
		{is, "refused", "synthetic-private-reason"},
		{is, "refused", "goal-prompt-unconfirmed"},
		{is, "refused", "launch-failed"},
		{is, "launched", "routing-invalid"},
		{Issue{Number: 3}, "refused", "routing-invalid"},
	} {
		if publishLaunchState(root, nil, "example/inbox", 3, tc.issue, tc.state, tc.reason) == nil {
			t.Fatal("invalid launch state was published")
		}
	}
	if files, _ := filepath.Glob(filepath.Join(root, "launch-*.json")); len(files) != 0 {
		t.Fatal("invalid launch state left a public file")
	}
}

func TestLaunchRefusalStateRejectsAnUnsafeExistingFile(t *testing.T) {
	root := privateTestRoot(t)
	is := Issue{ID: "fixture-id", Number: 3, Body: "fixture-body"}
	path := launchStatePath(root, "example/inbox", is)
	if err := os.WriteFile(path, []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	if publishLaunchState(root, nil, "example/inbox", 3, is, "refused", "routing-invalid") == nil {
		t.Fatal("unsafe mode was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), path); err != nil {
		t.Fatal(err)
	}
	if publishLaunchState(root, nil, "example/inbox", 3, is, "refused", "routing-invalid") == nil {
		t.Fatal("symlink was followed")
	}
}
