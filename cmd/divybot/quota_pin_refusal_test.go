package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A pinned route refused only because its transport is quota-blocked is a quota refusal,
// not the override-required the resolver reports once that transport is left out of
// Available (coordinator 2026-09-30).
func TestQuotaBlockedPinReportsQuota(t *testing.T) {
	for _, tc := range []struct {
		name       string
		codexQuota bool
		resolve    func(req matrixRequest) (matrixRoute, error)
		want       string
		resolves   int
	}{
		{name: "pinned-transport-quota-blocked", want: "quota-unavailable", resolves: 2,
			resolve: func(req matrixRequest) (matrixRoute, error) {
				if containsString(req.Available, "codex") {
					route := syntheticRoute()
					route.Transport = "codex"
					return route, nil
				}
				return matrixRoute{}, matrixReason("override-required")
			}},
		{name: "deviation-regardless-of-quota", want: "override-required", resolves: 2,
			resolve: func(matrixRequest) (matrixRoute, error) { return matrixRoute{}, matrixReason("override-required") }},
		{name: "nothing-quota-blocked", codexQuota: true, want: "override-required", resolves: 1,
			resolve: func(matrixRequest) (matrixRoute, error) { return matrixRoute{}, matrixReason("override-required") }},
		{name: "unconstrained-route-on-an-available-transport", want: "override-required", resolves: 2,
			resolve: func(req matrixRequest) (matrixRoute, error) {
				if len(req.Available) == len(matrixTransports) {
					return syntheticRoute(), nil // claude, which the quota check allowed
				}
				return matrixRoute{}, matrixReason("override-required")
			}},
		{name: "unconstrained-evaluator-route", want: "override-required", resolves: 2,
			resolve: func(req matrixRequest) (matrixRoute, error) {
				if containsString(req.Available, "codex") {
					route := syntheticRoute()
					route.Transport, route.Role = "codex", "implementation_evaluation"
					return route, nil
				}
				return matrixRoute{}, matrixReason("override-required")
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTestRoot(t)
			cfg := &Config{Inbox: "example/inbox", Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: strings.Repeat("b", 40), TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)}}, Governor: Gov{WeeklyCeiling: 92}, UnmeteredTransports: UnmeteredTransportLimits{"agy": {1}}}
			c := &Coord{cfg: cfg}
			now := time.Now()
			q := quota{ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()}}
			c.gov.q = map[string]quota{"claude": q, "agy": q}
			if tc.codexQuota {
				c.gov.q["codex"] = q
			}
			budget := map[string]int{"claude": 1, "codex": 1, "agy": 1}
			is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic", Body: "/swarm\ntier: feature\nrole: implementation\n\nSynthetic task"}
			refusals := []matrixRefusal{}
			resolves := 0
			deps := matrixAttemptDeps{
				report: func(r matrixRefusal) { refusals = append(refusals, r) },
				read: func(context.Context, string, string, string) (string, error) {
					return "| `routing` | matrix `implementation` row |", nil
				},
				resolve: func(_ context.Context, _ MatrixConfig, req matrixRequest) (matrixRoute, error) {
					resolves++
					return tc.resolve(req)
				},
				host: func(Target, string) (Host, bool) { t.Fatal("a refused route reached placement"); return Host{}, false },
				persist: func(string, string, string, matrixReceipt, any, ...*receiptOwner) (*durableMatrixReceipt, error) {
					t.Fatal("a refused route was persisted")
					return nil, nil
				},
			}
			if _, ok := c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, budget, deps); ok {
				t.Fatal("refused route launched")
			}
			if len(refusals) != 1 || refusals[0].ReasonCode != tc.want || !validMatrixRefusal(refusals[0]) {
				t.Fatalf("want one valid %s refusal, got %+v", tc.want, refusals)
			}
			if tc.want == "quota-unavailable" && !strings.Contains(refusals[0].Detail, "codex: absent") {
				t.Fatalf("quota refusal does not name the blocked transport: %q", refusals[0].Detail)
			}
			if resolves != tc.resolves {
				t.Fatalf("resolved %d times, want %d", resolves, tc.resolves)
			}
		})
	}
}
