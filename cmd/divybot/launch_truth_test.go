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

func TestMatrixLaunchOutcomePreservesRegisteredAgent(t *testing.T) {
	for _, tc := range []struct {
		name, wantReason, wantState string
		err                         error
	}{
		{"prompt-unconfirmed", "goal-prompt-unconfirmed", "dispatched", matrixReason("goal-prompt-unconfirmed")},
		{"state-save-failed", "launch-inconclusive", "uncertain", matrixSite("launch.state-save", errMatrix)},
		{"agent-start-ambiguous", "launch-inconclusive", "uncertain", matrixSite("spawn.agent-start", errMatrix)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTestRoot(t)
			cfg := &Config{Inbox: "example/inbox", Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: strings.Repeat("b", 40), TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)}}, Governor: Gov{WeeklyCeiling: 92}}
			c := &Coord{cfg: cfg, st: loadState(filepath.Join(root, "state.json"))}
			now := time.Now()
			c.gov.q = map[string]quota{"claude": {ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()}}}
			is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic", Body: "/swarm\ntier: feature\nrole: implementation\n\nSynthetic task"}
			var refusal matrixRefusal
			deps := matrixAttemptDeps{
				report: func(r matrixRefusal) { refusal = r },
				read: func(context.Context, string, string, string) (string, error) {
					return "| `routing` | matrix `implementation` row |", nil
				},
				resolve: func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) { return syntheticRoute(), nil },
				host:    func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, true },
				persist: persistMatrixReceipt,
				launch: func(_ context.Context, _ int, _ Issue, _ Host, _ string, _ Overrides, r *durableMatrixReceipt) error {
					if tc.name == "prompt-unconfirmed" {
						if e := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); e != nil {
							t.Fatal(e)
						}
					}
					return tc.err
				},
			}
			if _, ok := c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, map[string]int{"claude": 1}, deps); ok {
				t.Fatal("uncertain launch claimed success")
			}
			if refusal.ReasonCode != tc.wantReason || refusal.Status != "inconclusive" || !validMatrixRefusal(refusal) {
				t.Fatalf("false launch outcome: %+v", refusal)
			}
			body, err := os.ReadFile(launchStatePath(root, cfg.Inbox, is))
			var state launchStateRecord
			if err != nil || json.Unmarshal(body, &state) != nil || state.State != "launching" || state.ReasonCode != nil {
				t.Fatal("ambiguous launch was exported as a refusal")
			}
			files, e := filepath.Glob(filepath.Join(root, "*", "record", "dispatch.json"))
			if e != nil || len(files) != 1 {
				t.Fatal("dispatch receipt missing")
			}
			var d dispatchBinding
			body, e = os.ReadFile(files[0])
			if e != nil || json.Unmarshal(body, &d) != nil || d.State != tc.wantState {
				t.Fatal("registered receipt was invalidated or uncertain effect was trusted")
			}
			var comment string
			c.reportIssueMatrixRefusal(context.Background(), 1, is, refusal, func(_ context.Context, _ string, _ int, body string) error { comment = body; return nil })
			if strings.Contains(comment, "No agent was launched") || !strings.Contains(comment, "Launch outcome requires inspection") {
				t.Fatal("ambiguous launch was publicly described as a no-agent failure")
			}
		})
	}
}

func TestDoneOccupantReleasesAdmissionOnly(t *testing.T) {
	j := &Job{Agent: "codex", Host: "h1", Pane: "w1:p1", Workspace: "w1"}
	ref := agentRef{Agent: "codex", Host: "h1", Pane: "w1:p1", Workspace: "w1", Status: "done"}
	if occupiesAdmissionSlot(j, ref, true) {
		t.Fatal("ended turn still holds the active slot")
	}
	for _, tc := range []struct {
		name  string
		ref   agentRef
		known bool
	}{
		{"working", agentRef{Agent: "codex", Host: "h1", Pane: "w1:p1", Workspace: "w1", Status: "working"}, true},
		{"idle", agentRef{Agent: "codex", Host: "h1", Pane: "w1:p1", Workspace: "w1", Status: "idle"}, true},
		{"wrong-host", agentRef{Agent: "codex", Host: "h2", Pane: "w1:p1", Workspace: "w1", Status: "done"}, true},
		{"wrong-pane", agentRef{Agent: "codex", Host: "h1", Pane: "w2:p1", Workspace: "w1", Status: "done"}, true},
		{"wrong-workspace", agentRef{Agent: "codex", Host: "h1", Pane: "w1:p1", Workspace: "w2", Status: "done"}, true},
		{"wrong-agent", agentRef{Agent: "claude", Host: "h1", Pane: "w1:p1", Workspace: "w1", Status: "done"}, true},
		{"unknown", ref, false},
	} {
		if !occupiesAdmissionSlot(j, tc.ref, tc.known) {
			t.Fatalf("%s freed another run's slot", tc.name)
		}
	}
}
