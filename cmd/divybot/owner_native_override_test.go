package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func nativeOwnerFixture(tool string) *ownerNativeOverride {
	model := "fictional-native-model-not-in-any-matrix"
	if tool == "opencode" {
		model = "fixture-provider/" + model
	}
	return &ownerNativeOverride{Authorizer: "eric", Rationale: "Synthetic delegated owner selection",
		Route: ownerNativeRoute{Harness: tool, Provider: "fixture-provider", Model: model, Effort: "provider_default"}}
}

func TestOwnerNativeAdmissionAndSharedGuards(t *testing.T) {
	cases := []string{"claude", "codex", "agy", "opencode", "opencode-price-alias", "custom-opencode-variant", "ordinary-matrix", "owner-evaluator",
		"other-authorizer", "missing-authorizer", "missing-route", "missing-harness", "missing-provider", "missing-model", "missing-effort",
		"unknown-tool", "blank-rationale", "legacy-conflict", "pin-conflict", "harness-conflict", "router-conflict", "stale-grant",
		"no-quota", "stale-quota", "no-capacity", "no-host", "opencode-no-pool", "paid-budget", "invalid-token-budget", "valid-token-budget", "reservation-failure"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			tool := "claude"
			if containsString([]string{"codex", "agy", "opencode"}, name) {
				tool = name
			}
			if name == "opencode-no-pool" || name == "paid-budget" || name == "router-conflict" || name == "opencode-price-alias" || name == "custom-opencode-variant" {
				tool = "opencode"
			}
			root := privateTestRoot(t)
			cfg := &Config{Inbox: "example/inbox", Matrix: MatrixConfig{Source: root, ReceiptRoot: root,
				Revision: strings.Repeat("b", 40), TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)}},
				Governor: Gov{WeeklyCeiling: 92}, UnmeteredTransports: UnmeteredTransportLimits{"agy": {MaxActive: 1}},
				OpenCode: OpenCodeConfig{Providers: map[string]OpenCodeProvider{"fixture-provider": {MaxActive: 1}}}}
			c := &Coord{cfg: cfg}
			now := time.Now()
			q := quota{ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()}}
			c.gov.q = map[string]quota{"claude": q, "codex": q}
			beforeQuota := map[string]quota{"claude": q, "codex": q}
			grant := nativeOwnerFixture(tool)
			if name == "opencode-price-alias" {
				grant.Route.Model = "fixture-provider/~fictional-native-price-alias"
			}
			if name == "custom-opencode-variant" {
				grant.Route.Effort = "fictional_variant"
			}
			is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic",
				Body: "/swarm\ntier: feature\nrole: implementation\n\nSynthetic task"}
			if name == "owner-evaluator" {
				is.Body = "/swarm\ntier: feature\nrole: implementation_evaluation\n\nSynthetic task"
			}
			if name == "valid-token-budget" || name == "invalid-token-budget" {
				value := "6000"
				if name == "invalid-token-budget" {
					value = "broken"
				}
				is.Body = "/swarm\ntier: feature\nrole: implementation\nmax-tokens: " + value + "\n\nSynthetic task"
			}
			if name == "harness-conflict" {
				is.Body = "/swarm\ntier: feature\nrole: implementation\nharness: agy\n\nSynthetic task"
			}
			if name == "pin-conflict" || name == "stale-grant" {
				is.Body = "/swarm\ntier: feature\nrole: implementation\nmodel: conflicting-choice\n\nSynthetic task"
			}
			if name == "router-conflict" {
				is.Body = "/swarm\ntier: feature\nrole: implementation\nrouter: other-provider\n\nSynthetic task"
			}
			cfg.Matrix.Grants = []MatrixGrant{{IssueID: is.ID, Repo: "example/project", BriefDigest: briefDigest(is), NativeOverride: grant}}
			budget := map[string]int{"claude": 1, "codex": 1, "agy": 1, "opencode": 1, "opencode:fixture-provider": 1}
			switch name {
			case "other-authorizer":
				grant.Authorizer = "owner"
			case "missing-authorizer":
				grant.Authorizer = ""
			case "missing-route":
				grant.Route = ownerNativeRoute{}
			case "missing-harness":
				grant.Route.Harness = ""
			case "missing-provider":
				grant.Route.Provider = ""
			case "missing-model":
				grant.Route.Model = ""
			case "missing-effort":
				grant.Route.Effort = ""
			case "unknown-tool":
				grant.Route.Harness = "invented-tool"
			case "blank-rationale":
				grant.Rationale = " "
			case "legacy-conflict":
				cfg.Matrix.Grants[0].Override = &MatrixOverride{Authorizer: "owner"}
			case "stale-grant":
				is.Body += " changed"
			case "ordinary-matrix":
				cfg.Matrix.Grants = nil
			case "no-quota":
				delete(c.gov.q, tool)
			case "stale-quota":
				q.at = now.Add(-time.Hour)
				c.gov.q[tool] = q
			case "no-capacity":
				budget[tool] = 0
			case "opencode-no-pool":
				cfg.OpenCode.Providers = nil
			case "paid-budget":
				paid, _, _, _ := budgetFixture(t)
				paid.Models[0].Provider, paid.Models[0].Model, paid.Models[0].Limit = "fixture-provider", "fictional-native-model-not-in-any-matrix", budgetString("0")
				cfg.ProviderBudgets = paid
			}
			resolved, persisted, launched := 0, 0, 0
			var refusal matrixRefusal
			deps := matrixAttemptDeps{
				report: func(r matrixRefusal) { refusal = r },
				read: func(context.Context, string, string, string) (string, error) {
					return "| `routing` | matrix `implementation` row |", nil
				},
				resolve: func(_ context.Context, _ MatrixConfig, req matrixRequest) (matrixRoute, error) {
					resolved++
					if req.Pin != nil {
						return matrixRoute{}, matrixReason("override-required")
					}
					return syntheticRoute(), nil
				},
				host: func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, name != "no-host" },
				persist: func(root, key, command string, receipt matrixReceipt, binding any, owner ...*receiptOwner) (*durableMatrixReceipt, error) {
					persisted++
					if name == "reservation-failure" {
						return nil, errMatrix
					}
					return persistMatrixReceipt(root, key, command, receipt, binding, owner...)
				},
				launch: func(_ context.Context, _ int, _ Issue, _ Host, agent string, o Overrides, receipt *durableMatrixReceipt) error {
					launched++
					if !receipt.claim(mustBuildAgentCmd(t, agent, o)) {
						t.Fatal("command did not consume the durable reservation")
					}
					var record matrixReceipt
					b, err := os.ReadFile(receipt.file)
					if err != nil || json.Unmarshal(b, &record) != nil {
						t.Fatal("missing pre-launch receipt")
					}
					if name == "ordinary-matrix" {
						if record.OwnerNativeOverride != nil || record.Resolution.SourceRepository != matrixSourceRepository || record.Resolution.SourceRevision != cfg.Matrix.Revision || o.Model != syntheticRoute().Model {
							t.Fatal("ordinary matrix receipt/route changed")
						}
					} else {
						wantEffort := grant.Route.Effort
						if wantEffort == "provider_default" {
							wantEffort = ""
						}
						if agent != tool || o.Model != grant.Route.Model || o.Effort != wantEffort || record.OwnerNativeOverride == nil || record.OwnerNativeOverride.Authorizer != "eric" || record.Resolution.SourceRepository != ownerNativeSource || record.Resolution.SourceRevision != "" || record.Resolution.Selected.LogicalModel != "" {
							t.Fatal("native route/default or owner provenance was changed/invented")
						}
						for _, observed := range record.Observed {
							if observed.Status != "unknown" {
								t.Fatal("authorization invented an observed identity")
							}
						}
						private, err := os.ReadFile(filepath.Join(filepath.Dir(receipt.file), "binding.json"))
						var binding struct {
							Request                    matrixRequest
							Route                      matrixRoute
							IssueID, BriefDigest, Repo string
						}
						if err != nil || json.Unmarshal(private, &binding) != nil || !reflect.DeepEqual(binding.Request.NativeOverride, grant) || binding.IssueID != is.ID || binding.BriefDigest != briefDigest(is) || binding.Repo != "example/project" || binding.Route.LogicalModel != "" || binding.Route.Family != "" {
							t.Fatal("owner authorization was not bound before launch")
						}
						if receipt.dispatch.MatrixSource != ownerNativeSource || receipt.dispatch.MatrixRevision != "" {
							t.Fatal("owner selection claimed a matrix revision")
						}
					}
					if name == "valid-token-budget" && (receipt.dispatch.TokenBudget == nil || *receipt.dispatch.TokenBudget != 6000 || receipt.dispatch.BudgetSource != "issue") {
						t.Fatal("owner route lost its token limit")
					}
					return nil
				},
			}
			_, ok := c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, budget, deps)
			wantSuccess := containsString([]string{"claude", "codex", "agy", "opencode", "opencode-price-alias", "custom-opencode-variant", "ordinary-matrix", "owner-evaluator", "valid-token-budget"}, name)
			if ok != wantSuccess || launched != boolInt(wantSuccess) {
				t.Fatalf("admission=%v launch=%d refusal=%s", ok, launched, refusal.ReasonCode)
			}
			if wantSuccess && resolved != boolInt(name == "ordinary-matrix") {
				t.Fatal("owner native route consulted the matrix, or ordinary route bypassed it")
			}
			if !wantSuccess && name != "reservation-failure" && persisted != 0 {
				t.Fatal("refusal reached reservation effects")
			}
			if containsString([]string{"other-authorizer", "missing-authorizer", "missing-route", "missing-harness", "missing-provider", "missing-model", "missing-effort", "unknown-tool", "blank-rationale", "legacy-conflict", "pin-conflict"}, name) && refusal.ReasonCode != "override-invalid" {
				t.Fatal("invalid owner grant fell through to a different admission path")
			}
			if name == "paid-budget" && refusal.ReasonCode != "budget-reached" {
				t.Fatal("owner selection bypassed paid budget")
			}
			if name == "agy" && (!reflect.DeepEqual(c.gov.q, beforeQuota) || budget["claude"] != 1) {
				t.Fatal("AGY moved a subscription meter/Claude capacity")
			}
			if name == "opencode" && budget["opencode:fixture-provider"] != 0 {
				t.Fatal("owner OpenCode launch did not consume actual provider capacity")
			}
		})
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestOwnerNativeGrantConfigAndIssuePreflight(t *testing.T) {
	base := syntheticSource(t)
	base.ReceiptRoot = privateTestRoot(t)
	base.TargetRevisions = map[string]string{"example/project": strings.Repeat("c", 40)}
	is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic", Body: "/swarm\ntier: architecture\nrole: deep_research\n\nSynthetic task"}
	for _, authorizer := range []string{"eric", "owner", "milestone_coordinator", ""} {
		t.Run("authorizer-"+authorizer, func(t *testing.T) {
			grant := nativeOwnerFixture("agy")
			grant.Authorizer = authorizer
			cfg := &Config{Inbox: "example/inbox", Targets: []Target{{Repo: "example/project"}}, Matrix: base}
			cfg.Matrix.Grants = []MatrixGrant{{IssueID: is.ID, Repo: "example/project", BriefDigest: briefDigest(is), NativeOverride: grant}}
			if problems := validateMatrixConfig(context.Background(), cfg); (len(problems) == 0) != (authorizer == "eric") {
				t.Fatal("config accepted untrusted authorizer or refused Eric")
			}
			deps := configDeps()
			deps.read = func(context.Context, string, string, string) (string, error) {
				return "| `routing` | matrix `implementation` row |", nil
			}
			deps.resolve = func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) {
				t.Fatal("native preflight consulted the matrix")
				return matrixRoute{}, errMatrix
			}
			if problems := validateMatrixIssue(context.Background(), cfg, is, "example/project", deps); (len(problems) == 0) != (authorizer == "eric") {
				t.Fatal("issue preflight lost native authorization")
			}
		})
	}
}

func TestOwnerNativeJSONRejectsNullUnknownAndDuplicateFields(t *testing.T) {
	for _, raw := range []string{
		`{"ownerNativeOverride":null}`,
		`{"ownerNativeOverride":{"authorizer":"eric","authorizer":"owner"}}`,
		`{"ownerNativeOverride":{"authorizer":"eric","invented":"PRIVATE-NATIVE-CANARY"}}`,
		`null`,
	} {
		var grant MatrixGrant
		if json.Unmarshal([]byte(raw), &grant) == nil {
			t.Fatal("ambiguous/null owner grant fell back to ordinary routing")
		}
	}
	var grant MatrixGrant
	if json.Unmarshal([]byte(`{"tier":"feature","role":"implementation"}`), &grant) != nil {
		t.Fatal("ordinary grant decoding changed")
	}
}
