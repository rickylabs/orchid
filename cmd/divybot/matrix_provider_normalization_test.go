package main

import (
	"context"
	"path/filepath"
	"testing"
)

func providerAliasSource(t *testing.T, alias, provider, effort, agent string) MatrixConfig {
	t.Helper()
	cfg := syntheticSource(t)
	root := filepath.Join(cfg.Source, "packages", "routing", "matrix")
	writeFixture(t, filepath.Join(root, "contract.ts"), `export const EFFORTS=["low","high"];export const PROVIDER_KINDS=["`+alias+`"];`)
	writeFixture(t, filepath.Join(root, "delegation-matrix.ts"), `
export const WORKLOAD_TIERS=["feature"];
export const DELEGATION_ROLES=["implementation","deep_research"];
export const COORDINATOR_TIERS=["milestone"];
export const MODEL_TRANSPORTS=["claude","`+alias+`"];
export const MODEL_CATALOG={fixture:{capabilities:[{transport:"`+alias+`",model:"`+provider+`/fixture-model"}]}};
export function assertPrivilegedTierAuthorization(){};
export function isTransportAllowedForRole(role){return role!=="deep_research";}
`)
	writeFixture(t, filepath.Join(root, "routing-policy.ts"), `
export function resolveWorkloadRoute(r){if(r.unavailableTransports.includes("`+alias+`"))throw Error();return {agent:"`+agent+`",provider:"`+alias+`",transport:"`+alias+`",model:"`+provider+`/fixture-model",logicalModel:"fixture",family:"fixture-family",effort:"low",requestedEffort:"`+effort+`"};}
export const resolveCoordinatorRoute=resolveWorkloadRoute;
`)
	writeFixture(t, filepath.Join(root, "cli", "delegation-matrix-table.ts"), `console.log(JSON.stringify({schemaVersion:1,tiers:[{tier:"feature",routes:[{model:"fixture",effort:"`+effort+`"}]}]}));`)
	testCommand(t, cfg.Source, "git", "add", ".")
	testCommand(t, cfg.Source, "git", "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic provider alias")
	cfg.Revision = testCommand(t, cfg.Source, "git", "rev-parse", "HEAD")
	return cfg
}

func TestBridgeNormalizesConfiguredProviderAliases(t *testing.T) {
	for _, alias := range []string{"github_copilot", "ollama", "opencode_go", "openrouter"} {
		t.Run(alias, func(t *testing.T) {
			cfg := providerAliasSource(t, alias, "fixture-provider", "low", "opencode")
			req := matrixRequest{Tier: "feature", Role: "implementation", Available: []string{"opencode"}, OpenCodeProviders: []string{"fixture-provider"}}
			route, err := resolveMatrix(context.Background(), cfg, req)
			if err != nil || route.Transport != "opencode" || route.Provider != "fixture-provider" || route.Model != "fixture-provider/fixture-model" || route.Effort != "low" {
				t.Fatal("exact provider alias failed normalization", route, err)
			}
			for _, providers := range [][]string{nil, {"foreign-provider"}, {"fixture-provider", "fixture-provider"}} {
				req.OpenCodeProviders = providers
				if _, err := resolveMatrix(context.Background(), cfg, req); err == nil {
					t.Fatal("unconfigured or ambiguous pool reached launch")
				}
			}
			req.OpenCodeProviders = []string{"fixture-provider"}
			req.Available = nil
			if _, err := resolveMatrix(context.Background(), cfg, req); err == nil {
				t.Fatal("provider pool bypassed exhausted aggregate capacity")
			}
			req.Available = []string{"opencode"}
			req.Role = "deep_research"
			if _, err := resolveMatrix(context.Background(), cfg, req); err == nil {
				t.Fatal("normalization bypassed source role restriction")
			}
		})
	}
}

func TestBridgePreservesProviderDefaultAndRejectsWrongExecutor(t *testing.T) {
	req := matrixRequest{Tier: "feature", Role: "implementation", Available: []string{"opencode"}, OpenCodeProviders: []string{"fixture-provider"}}
	cfg := providerAliasSource(t, "ollama", "fixture-provider", "provider_default", "opencode")
	route, err := resolveMatrix(context.Background(), cfg, req)
	if err != nil || route.Effort != "provider_default" || route.RequestedEffort != "provider_default" {
		t.Fatal("provider default became a guessed native variant", route, err)
	}
	cfg = providerAliasSource(t, "ollama", "fixture-provider", "low", "claude")
	if _, err := resolveMatrix(context.Background(), cfg, req); err == nil {
		t.Fatal("provider alias accepted another executor")
	}
}

func TestBridgeRefusesSelectedForeignPool(t *testing.T) {
	cfg := providerAliasSource(t, "ollama", "fixture-provider", "low", "opencode")
	catalog := filepath.Join(cfg.Source, "packages/routing/matrix/delegation-matrix.ts")
	writeFixture(t, catalog, `
export const WORKLOAD_TIERS=["feature"];export const DELEGATION_ROLES=["implementation"];
export const COORDINATOR_TIERS=["milestone"];export const MODEL_TRANSPORTS=["ollama"];
export const MODEL_CATALOG={fixture:{capabilities:[{transport:"ollama",model:"fixture-provider/fixture-model"},{transport:"ollama",model:"foreign-provider/fixture-model"}]}};
export function assertPrivilegedTierAuthorization(){};export function isTransportAllowedForRole(){return true;}`)
	policy := filepath.Join(cfg.Source, "packages/routing/matrix/routing-policy.ts")
	writeFixture(t, policy, `export function resolveWorkloadRoute(){return {agent:"opencode",provider:"ollama",transport:"ollama",model:"foreign-provider/fixture-model",logicalModel:"fixture",family:"fixture-family",effort:"low",requestedEffort:"low"};}export const resolveCoordinatorRoute=resolveWorkloadRoute;`)
	testCommand(t, cfg.Source, "git", "add", ".")
	testCommand(t, cfg.Source, "git", "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic foreign selection")
	cfg.Revision = testCommand(t, cfg.Source, "git", "rev-parse", "HEAD")
	if _, err := resolveMatrix(context.Background(), cfg, matrixRequest{Tier: "feature", Role: "implementation", Available: []string{"opencode"}, OpenCodeProviders: []string{"fixture-provider"}}); err == nil {
		t.Fatal("selected foreign pool bypassed configured prefix")
	}
}
