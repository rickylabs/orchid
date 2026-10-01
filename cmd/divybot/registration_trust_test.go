package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestManagedCodexStartsWithOnlyItsCheckoutTrusted(t *testing.T) {
	h, calls := registrationHost(t, "needs-trust")
	config := filepath.Join(h.Home, ".codex", "config.toml")
	if os.MkdirAll(filepath.Dir(config), 0700) != nil || os.WriteFile(config, []byte("[projects.\"/fixture/prior\"]\ntrust_level=\"untrusted\"\n"), 0600) != nil {
		t.Fatal("fixture config unavailable")
	}
	before, _ := os.ReadFile(config)
	// The fake native startup parses the actual -c value as TOML, checks only
	// this path is trusted, and refuses readiness without it. No native model.
	cwd := filepath.Join(t.TempDir(), `repo.v1 "quoted" $(touch CANARY) & café`)
	pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", cwd, nil, "codex", Overrides{Model: "fixture-model", Effort: "xhigh"}, registrationReceipt(t, "codex", Overrides{Model: "fixture-model", Effort: "xhigh"}))
	if err != nil || pane != "w1:p1" || ws != "w1" {
		t.Fatal("folder consent still prevents managed readiness")
	}
	after, _ := os.ReadFile(config)
	if string(before) != string(after) || len(calls()) != 3 {
		t.Fatal("startup changed persisted trust, retried, or sent input")
	}
	if _, err := os.Stat("CANARY"); !os.IsNotExist(err) {
		t.Fatal("a checkout path executed shell content")
	}
}

func TestManagedCodexRejectsUnscopedPathsBeforeWorkspace(t *testing.T) {
	for _, cwd := range []string{"", "/", "relative", "/fixture/../other", "/fixture//repo", "/fixture/\nrepo", "/fixture/\x7frepo", "/fixture/\xffrepo"} {
		h, _ := registrationHost(t, "")
		pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", cwd, nil, "codex", Overrides{}, registrationReceipt(t, "codex", Overrides{}))
		if !errors.Is(err, errAgentRegistration) || pane != "" || ws != "" {
			t.Fatal("invalid trust scope reached workspace creation")
		}
		if _, err := os.Stat(os.Getenv("REGISTRATION_CALLS")); !os.IsNotExist(err) {
			t.Fatal("invalid trust scope reached Herdr")
		}
	}
}

func TestManagedTrustSettingDoesNotChangeOtherTransports(t *testing.T) {
	for _, agent := range []string{"claude", "agy", "opencode"} {
		o := Overrides{Model: "fixture-model", Effort: "high", Router: "fixture-provider"}
		kind, args, err := managedInteractiveAgentArgs(agent, o, "")
		wantKind, wantArgs, wantErr := interactiveAgentArgs(agent, o)
		if err != wantErr || kind != wantKind || !reflect.DeepEqual(args, wantArgs) {
			t.Fatal("Codex trust setting affected another transport")
		}
	}
}

func TestStartupTrustDialogReportsBlockedWithoutInput(t *testing.T) {
	for _, mode := range []string{"trust-dialog", "trust-foreign", "trust-name", "trust-pane", "trust-workspace", "trust-cwd", "trust-working", "trust-changed", "trust-unreadable", "trust-malformed", "trust-oversized", "trust-quoted"} {
		t.Run(mode, func(t *testing.T) {
			h, calls := registrationHost(t, mode)
			pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil, "codex", Overrides{}, registrationReceipt(t, "codex", Overrides{}))
			want := "startup_timeout"
			if mode == "trust-dialog" {
				want = "startup_blocked"
			}
			if !errors.Is(err, errAgentRegistration) || registrationFailureKind(err) != want || pane != "w1:p1" || ws != "w1" || strings.Contains(err.Error(), "CANARY") {
				t.Fatal("startup diagnosis lost trust blocking, ownership, cleanup, or privacy")
			}
			starts := 0
			for _, call := range calls() {
				if call[0] == "agent" && call[1] == "start" {
					starts++
				} else if call[0] == "agent" && call[1] == "get" || call[0] == "pane" && call[1] == "read" {
					if call[2] != pane {
						t.Fatal("diagnostic read targeted another pane")
					}
				} else if call[0] != "workspace" && !(call[0] == "pane" && call[1] == "run") {
					t.Fatal("startup diagnostic sent input")
				}
			}
			if starts != 1 {
				t.Fatal("startup diagnostic retried the agent")
			}
		})
	}
}

func TestStartupDialogDiagnosisPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, termination := range []error{context.Canceled, context.DeadlineExceeded} {
		h, _ := registrationHost(t, "trust-dialog")
		want := "startup_cancelled"
		if termination == context.DeadlineExceeded {
			want = "startup_timeout"
		}
		// The direct classifier retains context precedence; cancelled startup
		// additionally must not attempt a diagnostic Herdr read.
		if registrationFailureKind(registrationFailure("", termination)) != want {
			t.Fatal("termination classification changed")
		}
		if registrationFailureKind(h.registrationStartFailure(ctx, "", "codex", "fixture-agent", "/fixture/repo", "w1:p1", "w1")) != "startup_cancelled" {
			t.Fatal("cancelled startup was relabelled blocked")
		}
		if _, err := os.Stat(os.Getenv("REGISTRATION_CALLS")); !os.IsNotExist(err) {
			t.Fatal("cancelled startup read a pane")
		}
	}
}
