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

// Profiles are Harness Markdown (decision B). A target repository carries none, so a
// launch reads the profile from Harness at the configured profile revision (matrix.revision
// when unset) and never from the target.
func TestMatrixProfilesComeFromHarness(t *testing.T) {
	routing, pinned, target := strings.Repeat("b", 40), strings.Repeat("d", 40), strings.Repeat("c", 40)
	const profileText = "| `routing` | matrix `implementation` row |"
	// Harness holds leaf at both revisions and fix only at the newer profile pin; the target
	// holds a decoy fix that must never be read.
	harness := map[string]map[string]bool{
		routing: {"profiles/leaf.md": true},
		pinned:  {"profiles/leaf.md": true, "profiles/fix.md": true},
	}
	read := func(calls *[]string) func(context.Context, string, string, string) (string, error) {
		return func(_ context.Context, repo, revision, name string) (string, error) {
			*calls = append(*calls, repo+"@"+revision+":"+name)
			switch {
			case repo == matrixSourceRepository && harness[revision][name]:
				return profileText, nil
			case repo == "example/project" && revision == target && name == "profiles/fix.md":
				return profileText, nil
			}
			return "", errMatrix
		}
	}
	for _, tc := range []struct {
		name, profile, profilePin, wantReason, wantRevision string
	}{
		{"default-leaf-at-routing-revision", "", "", "", routing},
		{"fix-at-profile-pin", "fix", pinned, "", pinned},
		{"leaf-at-profile-pin", "", pinned, "", pinned},
		{"fix-absent-at-routing-revision", "fix", "", "profile-unavailable", ""},
		{"invalid-profile-pin", "fix", "HEAD", "revision-invalid", ""},
	} {
		body := "/swarm\ntier: feature\nrole: implementation\n"
		if tc.profile != "" {
			body += "profile: " + tc.profile + "\n"
		}
		is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic", Body: body + "\nSynthetic task"}
		t.Run("attempt/"+tc.name, func(t *testing.T) {
			root := privateTestRoot(t)
			cfg := &Config{Inbox: "example/inbox", Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: routing, ProfileRevision: tc.profilePin,
				TargetRevisions: map[string]string{"example/project": target}}, Governor: Gov{WeeklyCeiling: 92}}
			c := &Coord{cfg: cfg}
			now := time.Now()
			c.gov.q = map[string]quota{"claude": {ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()}}}
			var calls []string
			var refusal matrixRefusal
			var bound dispatchBinding
			resolved := false
			deps := matrixAttemptDeps{
				report: func(r matrixRefusal) { refusal = r },
				read:   read(&calls),
				resolve: func(_ context.Context, _ MatrixConfig, req matrixRequest) (matrixRoute, error) {
					resolved = true
					if req.ProfileText != profileText {
						t.Fatal("resolution did not receive the Harness profile")
					}
					return syntheticRoute(), nil
				},
				host:    func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, true },
				persist: persistMatrixReceipt,
				launch: func(_ context.Context, _ int, _ Issue, _ Host, _ string, _ Overrides, r *durableMatrixReceipt) error {
					data, err := os.ReadFile(filepath.Join(filepath.Dir(r.file), "dispatch.json"))
					if err != nil || json.Unmarshal(data, &bound) != nil {
						t.Fatal("dispatch binding unreadable")
					}
					return nil
				},
			}
			_, ok := c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, map[string]int{"claude": 1}, deps)
			for _, call := range calls {
				if strings.HasPrefix(call, "example/project@") {
					t.Fatalf("profile read from the target repository: %s", call)
				}
			}
			if tc.wantReason == "" {
				if !ok || !resolved || refusal.ReasonCode != "" {
					t.Fatalf("Harness profile refused: ok=%v reason=%s calls=%v", ok, refusal.ReasonCode, calls)
				}
				if bound.ProfileRevision != tc.wantRevision {
					t.Fatalf("receipt pinned profile revision %q, want %q", bound.ProfileRevision, tc.wantRevision)
				}
				exp := retryExpectation{OperationID: testActionID, Dispatch: bound, Tier: syntheticRoute().Tier, Role: syntheticRoute().Role}
				// Only the profile pin is under test; the budget resolution has its own retry controls.
				exp.Dispatch.State, exp.Dispatch.TokenBudget, exp.Dispatch.BudgetSource = "dispatched", syntheticRoute().TokenBudget, syntheticRoute().BudgetSource
				if !retryPinsMatch(exp, cfg.Matrix, bound.Profile, syntheticRoute(), Host{Name: "fixture-node"}) {
					t.Fatal("retry refused the profile revision it was dispatched with")
				}
				moved := cfg.Matrix
				moved.ProfileRevision = strings.Repeat("e", 40)
				if retryPinsMatch(exp, moved, bound.Profile, syntheticRoute(), Host{Name: "fixture-node"}) {
					t.Fatal("retry accepted a moved profile pin")
				}
			}
			if tc.wantReason != "" && (ok || resolved || refusal.ReasonCode != tc.wantReason) {
				t.Fatalf("profile not refused: ok=%v reason=%s", ok, refusal.ReasonCode)
			}
		})
		t.Run("preflight/"+tc.name, func(t *testing.T) {
			if tc.wantReason == "revision-invalid" {
				m := syntheticSource(t)
				m.ReceiptRoot, m.ProfileRevision = privateTestRoot(t), tc.profilePin
				m.TargetRevisions = map[string]string{"example/project": target}
				p := validateMatrixConfig(context.Background(), &Config{Inbox: "example/inbox", Targets: []Target{{Repo: "example/project"}}, Matrix: m})
				found := false
				for _, problem := range p {
					found = found || problem.Field == "matrix.profile_revision"
				}
				if !found {
					t.Fatalf("invalid profile pin accepted by config validation: %+v", p)
				}
				return
			}
			m := MatrixConfig{Source: privateTestRoot(t), Revision: routing, ProfileRevision: tc.profilePin, TargetRevisions: map[string]string{"example/project": target}}
			cfg := &Config{Inbox: "example/inbox", Targets: []Target{{Repo: "example/project"}}, Matrix: m}
			var calls []string
			deps := configDeps()
			deps.read = read(&calls)
			deps.resolve = func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) { return syntheticRoute(), nil }
			p := validateMatrixIssue(context.Background(), cfg, is, "example/project", deps)
			for _, call := range calls {
				if strings.HasPrefix(call, "example/project@") {
					t.Fatalf("preflight read the profile from the target repository: %s", call)
				}
			}
			unavailable := false
			for _, problem := range p {
				if problem.Reason == "profile-unavailable" {
					unavailable = problem.Field == "matrix.profile_revision"
				}
			}
			if (tc.wantReason != "") != unavailable {
				t.Fatalf("preflight profile verdict wrong: %+v", p)
			}
		})
	}
	if d := matrixReasons["profile-unavailable"]; d.field != "matrix.profile_revision" || !strings.Contains(d.hint, "Harness") {
		t.Fatal("served remedy still points at the target repository")
	}
}
