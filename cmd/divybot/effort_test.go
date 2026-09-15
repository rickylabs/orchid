package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mustBuildAgentCmd(t *testing.T, agent string, o Overrides) string {
	t.Helper()
	command, err := buildAgentCmd(agent, o)
	if err != nil {
		t.Fatal("valid fixture command refused")
	}
	return command
}

func TestCodexEffortHigh(t *testing.T) {
	kind, args, err := interactiveAgentArgs("codex", Overrides{Model: "fixture-model", Effort: "high"})
	want := []string{"--dangerously-bypass-approvals-and-sandbox", "-m", "fixture-model", "-c", `model_reasoning_effort="high"`}
	if err != nil || kind != "codex" || !reflect.DeepEqual(args, want) {
		t.Fatal("selected high effort missing from native argv")
	}
}

func TestCodexEffortEmpty(t *testing.T) {
	_, args, err := interactiveAgentArgs("codex", Overrides{})
	if err != nil || !reflect.DeepEqual(args, []string{"--dangerously-bypass-approvals-and-sandbox"}) {
		t.Fatal("empty effort invented a native override")
	}
	r := registrationReceipt(t, "codex", Overrides{})
	b, err := os.ReadFile(filepath.Join(filepath.Dir(r.file), "dispatch.json"))
	var record map[string]any
	if err != nil || json.Unmarshal(b, &record) != nil {
		t.Fatal("fixture record unavailable")
	}
	if _, present := record["effort"]; present {
		t.Fatal("empty effort must be absent from dispatch record")
	}
}

func TestCodexEffortOverridesDefault(t *testing.T) {
	// Execute the rendered shell command through an argv recorder, never a
	// native model. The assertion is the CLI override, not a runtime verdict.
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "config.toml"), "model_reasoning_effort = \"low\"\n")
	bin := filepath.Join(root, "codex")
	if os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700) != nil {
		t.Fatal("argv fixture unavailable")
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	command := mustBuildAgentCmd(t, "codex", Overrides{Effort: "high"})
	b, err := exec.Command("sh", "-c", command).Output()
	if err != nil || string(b) != "--dangerously-bypass-approvals-and-sandbox\n-c\nmodel_reasoning_effort=\"high\"\n" {
		t.Fatal("rendered argv does not override the differing default with high")
	}
	config, _ := os.ReadFile(filepath.Join(root, "config.toml"))
	if string(config) != "model_reasoning_effort = \"low\"\n" {
		t.Fatal("rendering changed the default")
	}
}

func TestCodexEffortInvalid(t *testing.T) {
	for _, effort := range []string{"invalid", "HIGH", " high", "high\n", `high"; exit 0`} {
		kind, args, err := interactiveAgentArgs("codex", Overrides{Effort: effort})
		if !errors.Is(err, errCodexEffort) || kind != "" || len(args) != 0 {
			t.Fatal("invalid effort was forwarded")
		}
		command, err := buildAgentCmd("codex", Overrides{Effort: effort})
		if !errors.Is(err, errCodexEffort) || command != "" {
			t.Fatal("invalid effort rendered an executable command")
		}
	}
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		_, args, err := interactiveAgentArgs("codex", Overrides{Effort: effort})
		if err != nil || args[len(args)-1] != `model_reasoning_effort="`+effort+`"` {
			t.Fatal("matrix effort was refused or normalized")
		}
	}
}

func TestCodexEffortInvalidBeforeWorkspace(t *testing.T) {
	h, _ := registrationHost(t, "")
	_, _, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil, "codex", Overrides{Effort: "invalid"}, registrationReceipt(t, "codex", Overrides{}))
	if err == nil || !strings.Contains(err.Error(), "spawn.command-render") {
		t.Fatal("render error was not propagated before launch")
	}
	if _, err := os.Stat(os.Getenv("REGISTRATION_CALLS")); !os.IsNotExist(err) {
		t.Fatal("invalid effort reached the workspace executor")
	}
}

func TestCodexEffortOtherTransports(t *testing.T) {
	wants := map[string]struct {
		kind string
		args []string
	}{
		"agy":      {"agy", []string{"--dangerously-skip-permissions", "--model", "fixture-model", "--effort", "fixture-effort"}},
		"claude":   {"claude", []string{"--dangerously-skip-permissions", "--model", "fixture-model"}},
		"default":  {"claude", []string{"--dangerously-skip-permissions", "--model", "fixture-model"}},
		"opencode": {"opencode", []string{"--model", "fixture-provider/fixture-model"}},
	}
	for transport, want := range wants {
		kind, args, err := interactiveAgentArgs(transport, Overrides{Model: "fixture-model", Router: "fixture-provider", Effort: "fixture-effort"})
		if err != nil || kind != want.kind || !reflect.DeepEqual(args, want.args) {
			t.Fatalf("%s transport behavior changed", transport)
		}
	}
}

func TestCodexEffortMatrixValues(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			kind, args, err := interactiveAgentArgs("codex", Overrides{Effort: effort})
			want := []string{"--dangerously-bypass-approvals-and-sandbox", "-c", `model_reasoning_effort="` + effort + `"`}
			if err != nil || kind != "codex" || !reflect.DeepEqual(args, want) {
				t.Fatal("matrix effort lost its native config override")
			}
		})
	}
}
