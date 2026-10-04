package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Keep the CLI and enclosing effect deadline on the same finite startup budget.
func (h Host) agentStartBudget() (time.Duration, error) {
	if h.AgentStartTimeout == "" {
		return 2 * time.Minute, nil
	}
	d, err := time.ParseDuration(h.AgentStartTimeout)
	if err != nil || d < 5*time.Second || d > 5*time.Minute || d%time.Millisecond != 0 {
		return 0, errAgentRegistration
	}
	return d, nil
}

// Setup gets finite slack; a shorter parent deadline or cancellation still wins.
func (h Host) agentSpawnContext(parent context.Context, agent string) (context.Context, context.CancelFunc, time.Duration, error) {
	budget, err := h.agentStartBudget()
	if err != nil {
		return nil, nil, 0, err
	}
	total := budget + 30*time.Second
	if strings.HasSuffix(agent, "-run") {
		total = 40 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, total)
	return ctx, cancel, budget, nil
}

type agentRegistrationFailure struct{ kind string }

func (e *agentRegistrationFailure) Error() string { return "agent_registration_failed" }
func (e *agentRegistrationFailure) Unwrap() error { return errAgentRegistration }

// Native output is private. Only these closed classifications may reach logs.
func registrationFailure(output string, contextErr error) error {
	kind := "registration_failed"
	if errors.Is(contextErr, context.DeadlineExceeded) {
		kind = "startup_timeout"
	} else if errors.Is(contextErr, context.Canceled) {
		kind = "startup_cancelled"
	} else if len(output) <= 1024*1024 {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		if len(lines) > 0 {
			var response struct {
				Error *struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(lines[len(lines)-1]), &response) == nil && response.Error != nil {
				switch response.Error.Code {
				case "timeout":
					kind = "startup_timeout"
				case "agent_not_ready":
					kind = "startup_blocked"
				case "agent_pane_busy":
					kind = "startup_busy"
				}
			}
		}
	}
	return &agentRegistrationFailure{kind: kind}
}

func registrationFailureKind(err error) string {
	if agySettingsBlocked(err) {
		return string(agySettingsUnreadable)
	}
	if codexClientBlocked(err) {
		return string(codexClientUnavailable)
	}
	var failure *agentRegistrationFailure
	if errors.As(err, &failure) {
		return failure.kind
	}
	return "registration_failed"
}
