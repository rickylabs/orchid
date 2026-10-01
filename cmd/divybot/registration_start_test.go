package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentStartBudgetAndEnclosingDeadlineStayConsistent(t *testing.T) {
	for _, configured := range []string{"", "5s", "120s", "180s", "300s"} {
		h := Host{AgentStartTimeout: configured}
		budget, err := h.agentStartBudget()
		if err != nil {
			t.Fatal("valid budget refused")
		}
		if configured == "" && budget != 120*time.Second {
			t.Fatal("default budget must cover a cold startup beyond the old 30s cliff")
		}
		before := time.Now()
		ctx, cancel, cliBudget, err := h.agentSpawnContext(context.Background(), "codex")
		if err != nil {
			t.Fatal(err)
		}
		deadline, exists := ctx.Deadline()
		cancel()
		if !exists || cliBudget != budget || deadline.Before(before.Add(budget+30*time.Second)) || deadline.After(time.Now().Add(budget+31*time.Second)) {
			t.Fatal("enclosing deadline truncates the configured CLI startup budget or loses setup slack")
		}
	}
	ctx, cancel, _, err := (Host{}).agentSpawnContext(context.Background(), "codex-run")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	deadline, _ := ctx.Deadline()
	if time.Until(deadline) > 40*time.Second || time.Until(deadline) < 39*time.Second {
		t.Fatal("noninteractive effect deadline changed")
	}
}

func TestAgentStartBudgetRejectsInvalidBeforeAnyLaunchEffect(t *testing.T) {
	for _, configured := range []string{"0s", "-1s", "4s", "301s", "5s1ns", "PRIVATE-BUDGET-CANARY"} {
		h, _ := registrationHost(t, "")
		h.AgentStartTimeout = configured
		if _, err := h.agentStartBudget(); err == nil {
			t.Fatal("invalid startup budget accepted")
		}
		pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil, "codex", Overrides{}, registrationReceipt(t, "codex", Overrides{}))
		if !errors.Is(err, errAgentRegistration) || pane != "" || ws != "" {
			t.Fatal("invalid budget did not refuse before workspace creation")
		}
		if _, err := os.Stat(os.Getenv("REGISTRATION_CALLS")); !os.IsNotExist(err) {
			t.Fatal("invalid budget reached Herdr")
		}
		config := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(config, []byte(`{"hosts":[{"agent_start_timeout":"`+configured+`"}]}`), 0600); err != nil {
			t.Fatal(err)
		}
		_, err = loadConfig(config)
		if err == nil || strings.Contains(err.Error(), configured) {
			t.Fatal("invalid config must fail without echoing its value")
		}
	}
}

func TestAgentStartParentCancellationAndShorterDeadlineWin(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	ctx, cancel, _, err := (Host{}).agentSpawnContext(parent, "codex")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	defer cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("startup escaped parent cancellation")
	}
	parent, stop = context.WithTimeout(context.Background(), time.Second)
	defer stop()
	ctx, cancel, _, err = (Host{AgentStartTimeout: "300s"}).agentSpawnContext(parent, "codex")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	want, _ := parent.Deadline()
	got, _ := ctx.Deadline()
	if !got.Equal(want) {
		t.Fatal("startup replaced the parent's tighter deadline")
	}
}

func TestDelayedReadinessUsesConfiguredBudgetWithoutSecondSpawn(t *testing.T) {
	for _, configured := range []string{"", "180s"} {
		h, calls := registrationHost(t, "delayed")
		h.AgentStartTimeout = configured
		pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil, "codex", Overrides{}, registrationReceipt(t, "codex", Overrides{}))
		if err != nil || pane != "w1:p1" || ws != "w1" {
			t.Fatal("scaled cold startup was truncated at the old deadline")
		}
		got := calls()
		if len(got) != 3 || got[2][0] != "agent" || got[2][1] != "start" {
			t.Fatal("delayed startup caused a retry or goal submission")
		}
		if configured != "" && got[2][8] != "180000" {
			t.Fatal("host budget did not reach the native startup CLI")
		}
	}
}

func TestStartupFailureClassificationCannotReflectNativeOutput(t *testing.T) {
	for _, tc := range []struct{ output, want string }{
		{`{"error":{"code":"timeout","message":"PRIVATE-STARTUP-CANARY"}}`, "startup_timeout"},
		{`{"error":{"code":"agent_not_ready","message":"PRIVATE-STARTUP-CANARY"}}`, "startup_blocked"},
		{`{"error":{"code":"agent_pane_busy","message":"PRIVATE-STARTUP-CANARY"}}`, "startup_busy"},
		{`{"error":{"code":"PRIVATE-STARTUP-CANARY"}}`, "registration_failed"},
		{"PRIVATE-STARTUP-CANARY", "registration_failed"},
		{strings.Repeat("x", 1024*1024+1), "registration_failed"},
	} {
		err := registrationFailure(tc.output, nil)
		if !errors.Is(err, errAgentRegistration) || registrationFailureKind(err) != tc.want || strings.Contains(err.Error(), "CANARY") {
			t.Fatal("startup diagnostic is not a closed safe classification")
		}
	}
	if registrationFailureKind(registrationFailure("", context.DeadlineExceeded)) != "startup_timeout" || registrationFailureKind(registrationFailure("", context.Canceled)) != "startup_cancelled" {
		t.Fatal("context termination was not classified")
	}
}
