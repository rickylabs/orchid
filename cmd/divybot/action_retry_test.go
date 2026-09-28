package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func retryActionFixture(t *testing.T) (*Coord, string, string, actionRequest, Issue, string) {
	t.Helper()
	c, spool, receipts := actionFixture(t)
	is := Issue{ID: "fixture-node", Number: 7, Title: "Fixture task", Body: "/swarm\ntier: feature\nrole: implementation\nFixture work", Labels: []string{"fixture"}}
	c.cfg.Targets = []Target{{Label: "fixture", Repo: "example/work", Agent: "codex"}}
	key := strings.Repeat("d", 64)
	runID := "orchid-" + key
	record := filepath.Join(receipts, key, "record")
	if err := os.Mkdir(filepath.Join(receipts, key), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(record, 0700); err != nil {
		t.Fatal(err)
	}
	d := dispatchBinding{SchemaVersion: 1, RunID: runID, Issue: dispatchIssue{Repo: "example/repo", Number: 7},
		Source: "codex", Provider: "fixture", Model: "fixture-model", Effort: "high", State: "dispatched",
		Host: "fixture-host", Profile: "leaf", ProfileRevision: strings.Repeat("a", 40), MatrixRevision: strings.Repeat("b", 40),
		TokenBudget: goalInt(100), BudgetSource: "route", Location: &dispatchLocation{PaneID: "pane-fixture", WorkspaceID: "workspace-fixture"}}
	var receipt matrixReceipt
	receipt.SchemaVersion = 1
	receipt.Resolution.SourceRevision = d.MatrixRevision
	receipt.Requested = map[string]string{"transport": "codex", "model": d.Model, "tier": "feature", "role": "implementation"}
	write := func(name string, value any) {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := actionSyncFile(filepath.Join(record, name), body); err != nil {
			t.Fatal(err)
		}
	}
	write("dispatch.json", d)
	write("binding.json", map[string]any{"IssueID": is.ID, "BriefDigest": briefDigest(is), "Repo": "example/work", "NativeSessionID": "fixture-thread"})
	write("receipt.json", receipt)
	intent := teardownIntent{SchemaVersion: 1, Issue: 7, DispatchKey: key, NativeRunID: runID, NativeSessionID: "fixture-thread",
		Host: d.Host, PaneID: d.Location.PaneID, WorkspaceID: d.Location.WorkspaceID, Cause: "teardown", StartedAt: "2026-01-01T00:00:00Z"}
	write("teardown-intent.json", intent)
	write("teardown-seat-observed.json", teardownObservation{SchemaVersion: 1, NativeRunID: runID, Kind: "seat_absent", ObservedAt: "2026-01-01T00:00:01Z"})
	write("teardown-process-observed.json", teardownObservation{SchemaVersion: 1, NativeRunID: runID, Kind: "process_absent", ObservedAt: "2026-01-01T00:00:02Z"})
	req := actionTestRequest()
	req.Action = "retry"
	req.AgentID = actionOpaque("agent", runID)
	req.DispatchID = actionOpaque("assignment", runID)
	c.hosts[d.Host] = Host{Name: d.Host}
	return c, spool, receipts, req, is, record
}

func TestRetryBoundFailedRootDispatchesOnceWithExactPins(t *testing.T) {
	c, spool, receipts, req, is, _ := retryActionFixture(t)
	state := "CLOSED"
	reopens, attempts := 0, 0
	c.actions.retryIssue = func(context.Context, int) (Issue, string, error) { return is, state, nil }
	c.actions.retryReopen = func(context.Context, int) error { reopens++; state = "OPEN"; return nil }
	c.actions.retryNativeFailed = func(_ context.Context, host Host, thread string) (bool, error) {
		if host.Name != "fixture-host" || thread != "fixture-thread" {
			t.Fatal("wrong failure source")
		}
		return true, nil
	}
	c.actions.retryAttempt = func(_ context.Context, got Issue, _ Target, expected retryExpectation, preflight bool) (bool, matrixRefusal) {
		attempts++
		if got.ID != is.ID || expected.OperationID != req.OperationID || expected.Dispatch.ProfileRevision != strings.Repeat("a", 40) || expected.Dispatch.MatrixRevision != strings.Repeat("b", 40) || expected.Tier != "feature" || expected.Role != "implementation" {
			t.Fatal("original pins not carried")
		}
		if preflight && attempts != 1 || !preflight && (attempts != 2 || reopens != 1 || c.st.RetryFlights[7].OperationID != req.OperationID) {
			t.Fatal("retry order/fence wrong")
		}
		return true, matrixRefusal{}
	}
	actionDrop(t, spool, req)
	c.actionTick(context.Background())
	r := actionResult(t, receipts)
	wantKey := retryReservationKey(is, "example/work", req.OperationID)
	if r.Outcome != "accepted" || r.Reason != "retry_dispatched" || r.NativeRunID != "orchid-"+wantKey ||
		r.ReplacementAgentID != actionOpaque("agent", r.NativeRunID) || r.ReplacementDispatchID != actionOpaque("assignment", r.NativeRunID) ||
		attempts != 2 || reopens != 1 {
		t.Fatalf("retry receipt/effect: %+v attempts=%d reopens=%d", r, attempts, reopens)
	}
	saved := loadState(c.st.path)
	if saved.RetryFlights[7].OperationID != req.OperationID || saved.RetryFlights[7].DispatchKey != wantKey {
		t.Fatal("retry fence not durable")
	}
	var duplicate actionReceipt
	c.deliverRetryAction(context.Background(), req, &duplicate)
	if duplicate.Outcome != "rejected" || duplicate.Reason != "retry_in_flight" || attempts != 2 {
		t.Fatal("second retry bypassed in-flight fence")
	}
}

func TestRetryRefusesMissingPinsOrTerminalProof(t *testing.T) {
	for _, mode := range []string{"prebinding", "seat-only", "succeeded", "changed-brief", "wrong-identity"} {
		t.Run(mode, func(t *testing.T) {
			c, spool, receipts, req, is, record := retryActionFixture(t)
			if mode == "prebinding" {
				if err := os.Remove(filepath.Join(record, "dispatch.json")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "seat-only" {
				if err := os.Remove(filepath.Join(record, "teardown-process-observed.json")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed-brief" {
				is.Body += "\nChanged"
			}
			if mode == "wrong-identity" {
				req.DispatchID = "assignment_" + strings.Repeat("e", 64)
			}
			c.actions.retryIssue = func(context.Context, int) (Issue, string, error) { return is, "OPEN", nil }
			c.actions.retryNativeFailed = func(context.Context, Host, string) (bool, error) { return mode != "succeeded", nil }
			c.actions.retryAttempt = func(context.Context, Issue, Target, retryExpectation, bool) (bool, matrixRefusal) {
				t.Fatal("unproven retry reached route/launch")
				return false, matrixRefusal{}
			}
			actionDrop(t, spool, req)
			c.actionTick(context.Background())
			r := actionResult(t, receipts)
			want := "retry_terminal_unproven"
			if mode == "prebinding" {
				want = "retry_pins_unavailable"
			}
			if mode == "changed-brief" || mode == "wrong-identity" {
				want = "retry_pins_unavailable"
			}
			if r.Outcome != "rejected" || r.Reason != want || len(c.st.RetryFlights) != 0 {
				t.Fatalf("%s: %+v", mode, r)
			}
		})
	}
}

func TestRetryPinTupleRequiresEverySourceField(t *testing.T) {
	budget := int64(100)
	base := retryExpectation{OperationID: testActionID, Tier: "feature", Role: "implementation", Dispatch: dispatchBinding{
		State: "dispatched", Source: "codex", Provider: "fixture", Model: "fixture-model", Effort: "high", Profile: "leaf",
		ProfileRevision: strings.Repeat("a", 40), MatrixRevision: strings.Repeat("b", 40), Host: "fixture-host", TokenBudget: &budget, BudgetSource: "route"}}
	cfg := MatrixConfig{Revision: strings.Repeat("b", 40)}
	route := matrixRoute{Transport: "codex", Provider: "fixture", Model: "fixture-model", Effort: "high", Tier: "feature", Role: "implementation", TokenBudget: &budget, BudgetSource: "route"}
	host := Host{Name: "fixture-host"}
	if !retryPinsMatch(base, cfg, strings.Repeat("a", 40), "leaf", route, host) {
		t.Fatal("exact tuple refused")
	}
	for _, mutate := range []func(*retryExpectation){
		func(v *retryExpectation) { v.Dispatch.Model = "other" }, func(v *retryExpectation) { v.Dispatch.Effort = "low" },
		func(v *retryExpectation) { v.Dispatch.ProfileRevision = strings.Repeat("c", 40) }, func(v *retryExpectation) { v.Dispatch.MatrixRevision = strings.Repeat("c", 40) },
		func(v *retryExpectation) { v.Dispatch.Host = "other" }, func(v *retryExpectation) { v.Dispatch.TokenBudget = goalInt(101) },
		func(v *retryExpectation) { v.Tier = "architecture" }, func(v *retryExpectation) { v.Role = "plan" },
	} {
		v := base
		mutate(&v)
		if retryPinsMatch(v, cfg, strings.Repeat("a", 40), "leaf", route, host) {
			t.Fatal("pin drift accepted")
		}
	}
}

func TestRetryMatrixAttemptRefusesDriftBeforePersistence(t *testing.T) {
	root := privateTestRoot(t)
	cfg := &Config{Inbox: "example/inbox", Matrix: MatrixConfig{Source: root, ReceiptRoot: root,
		Revision: strings.Repeat("b", 40), TargetRevisions: map[string]string{"example/work": strings.Repeat("a", 40)}}, Governor: Gov{WeeklyCeiling: 92}}
	c := &Coord{cfg: cfg}
	now := time.Now()
	c.gov.q = map[string]quota{"claude": {ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()}}}
	is := Issue{ID: "fixture-node", Number: 7, Title: "Fixture task", Body: "/swarm\ntier: feature\nrole: implementation"}
	dept := Target{Label: "fixture", Repo: "example/work", Agent: "claude"}
	route := syntheticRoute()
	expected := retryExpectation{OperationID: testActionID, Tier: route.Tier, Role: route.Role, Dispatch: dispatchBinding{
		State: "dispatched", Source: route.Transport, Provider: route.Provider, Model: route.Model, Effort: route.Effort,
		Profile: "leaf", ProfileRevision: cfg.Matrix.TargetRevisions[dept.Repo], MatrixRevision: cfg.Matrix.Revision,
		Host: "fixture-node", BudgetSource: "unset"}}
	makeDeps := func(e *retryExpectation, persisted *bool, refusal *matrixRefusal) matrixAttemptDeps {
		return matrixAttemptDeps{retry: e, preflight: true,
			report: func(r matrixRefusal) { *refusal = r },
			read: func(context.Context, string, string, string) (string, error) {
				return "| `routing` | matrix `implementation` row |", nil
			},
			resolve: func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) { return route, nil },
			host:    func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, true },
			persist: func(string, string, string, matrixReceipt, any, ...*receiptOwner) (*durableMatrixReceipt, error) {
				*persisted = true
				t.Fatal("retry preflight persisted before pin check")
				return nil, nil
			},
		}
	}
	persisted := false
	var refusal matrixRefusal
	if _, ok := c.matrixAttempt(context.Background(), is.Number, is, dept, map[string]int{"claude": 1}, makeDeps(&expected, &persisted, &refusal)); !ok || persisted {
		t.Fatalf("exact retry preflight refused: %+v", refusal)
	}
	expected.Dispatch.Model = "drifted-model"
	refusal = matrixRefusal{}
	if _, ok := c.matrixAttempt(context.Background(), is.Number, is, dept, map[string]int{"claude": 1}, makeDeps(&expected, &persisted, &refusal)); ok || persisted || refusal.ReasonCode != "retry-pins-unavailable" {
		t.Fatalf("pin drift reached effect: %+v", refusal)
	}
}
