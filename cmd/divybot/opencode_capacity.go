package main

import "strings"

// This is an explicit operator concurrency limit, not a subscription/credit
// meter. No provider is enabled implicitly and OpenCode never borrows Codex quota.
type OpenCodeConfig struct {
	Providers map[string]OpenCodeProvider `json:"providers"`
}
type OpenCodeProvider struct {
	MaxActive int `json:"max_active"`
}

func (cfg OpenCodeConfig) valid() bool {
	if len(cfg.Providers) > 128 {
		return false
	}
	for id, provider := range cfg.Providers {
		if !openCodeProviderID.MatchString(id) || provider.MaxActive < 0 || provider.MaxActive > 256 {
			return false
		}
	}
	return true
}

func openCodeCapacityBudget(cfg OpenCodeConfig, jobs map[int]*Job, status map[int]agentRef) map[string]int {
	out := map[string]int{"opencode": 0}
	if !cfg.valid() {
		return out
	}
	for provider, limit := range cfg.Providers {
		remaining := limit.MaxActive
		for _, job := range jobs {
			if job == nil || job.Agent != "opencode" {
				continue
			}
			// An unbound/adopted/blocked OpenCode seat cannot grant spare capacity
			// in a guessed provider pool. Count it in each pool until inspected.
			if job.OpenCode == nil || job.OpenCode.Route.Provider == "" || job.OpenCode.Route.Provider == provider {
				remaining--
			}
		}
		out["opencode:"+provider] = remaining
		if remaining > 0 {
			out["opencode"] += remaining
		}
	}
	return out
}

func routeRouterError(route matrixRoute, o Overrides) error {
	if route.Transport != "opencode" {
		if o.Router != "" {
			return matrixReason("router-unsupported")
		}
		return nil
	}
	// A matrix route already names the physical provider. Router may agree, never
	// qualify an unqualified matrix model or replace the configured provider.
	if !strings.Contains(route.Model, "/") {
		return matrixReason("opencode-route-invalid")
	}
	_, err := resolveOpenCodeRoute(Overrides{Model: route.Model, Router: o.Router, Effort: route.Effort})
	return err
}
