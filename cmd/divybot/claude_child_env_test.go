package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeChildEventRootStaysInAgentHome(t *testing.T) {
	home := t.TempDir()
	h := Host{Home: home}
	for _, root := range []string{"", filepath.Join(home, "runs", "child-events")} {
		h.ClaudeChildEventRoot = root
		if !h.validClaudeChildEventRoot() {
			t.Fatal("private child event root inside agent home was refused")
		}
	}
	for _, root := range []string{"relative", home, filepath.Dir(home),
		filepath.Join(home, "..", "sibling"), home + "/runs/../events", home + "/events\nother"} {
		h.ClaudeChildEventRoot = root
		if h.validClaudeChildEventRoot() {
			t.Fatal("invalid or out-of-home child event root was accepted")
		}
	}
}

func TestOnlyDispatchedClaudePaneReceivesChildEventRoot(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "opencode", "claude-run"} {
		t.Run(agent, func(t *testing.T) {
			h, calls := registrationHost(t, "")
			h.ClaudeChildEventRoot = filepath.Join(h.Home, "runs", "child-events")
			_, _, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil,
				agent, Overrides{}, registrationReceipt(t, agent, Overrides{}))
			if err != nil {
				t.Fatal("synthetic registration failed")
			}
			paneCommand := calls()[1][3]
			got := strings.Contains(paneCommand, "export HARNESS_CLAUDE_CHILD_EVENT_ROOT=")
			if got != (agent == "claude") {
				t.Fatal("child event root escaped the dispatched Claude pane gate")
			}
		})
	}
}

func TestChildEventRootUnsetHasNoExport(t *testing.T) {
	h, calls := registrationHost(t, "")
	_, _, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil,
		"claude", Overrides{}, registrationReceipt(t, "claude", Overrides{}))
	if err != nil {
		t.Fatal("synthetic registration failed")
	}
	if strings.Contains(calls()[1][3], "HARNESS_CLAUDE_CHILD_EVENT_ROOT") {
		t.Fatal("unset child event root leaked into Claude pane")
	}
}

func TestLoadConfigRejectsOutOfHomeChildEventRoot(t *testing.T) {
	name := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(name, []byte(`{"hosts":[{"home":"/fixture-home","claude_child_event_root":"/outside"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(name); err == nil || !strings.Contains(err.Error(), "claude_child_event_root invalid") {
		t.Fatal("runtime config accepted an out-of-home event root")
	}
}
