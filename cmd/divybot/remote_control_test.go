package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteControlDefaultAndOperatorSwitch(t *testing.T) {
	for body, want := range map[string]map[string]bool{
		`{}`:                                  {"claude": true, "codex": true},
		`{"remote_control":{"claude":false}}`: {"claude": false, "codex": true},
		`{"remote_control":{"codex":true}}`:   {"claude": true, "codex": true},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if os.WriteFile(path, []byte(body), 0600) != nil {
			t.Fatal("fixture config unavailable")
		}
		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatalf("%s: valid configuration refused: %v", body, err)
		}
		b, _ := json.Marshal(cfg)
		var decoded struct {
			RemoteControl map[string]bool `json:"remote_control"`
		}
		_ = json.Unmarshal(b, &decoded)
		for kind, on := range want {
			if v, ok := decoded.RemoteControl[kind]; !ok || v != on {
				t.Fatalf("%s: %s remote control = %v, want %v", body, kind, v, on)
			}
		}
	}
}

// Codex runs only under Remote Control: switching it off is refused by name.
func TestRemoteControlCodexCannotBeSwitchedOff(t *testing.T) {
	for _, body := range []string{`{"remote_control":{"codex":false}}`, `{"remote_control":{"claude":false,"codex":false}}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if os.WriteFile(path, []byte(body), 0600) != nil {
			t.Fatal("fixture config unavailable")
		}
		if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "remote_control.codex cannot be false") {
			t.Fatalf("%s: Codex remote control switched off: %v", body, err)
		}
	}
}

func TestRemoteControlInvalidConfigRefused(t *testing.T) {
	for _, body := range []string{`{"remote_control":null}`, `{"remote_control":{"codex":null}}`,
		`{"remote_control":{"claude":true,"claude":false}}`, `{"remote_control":{"opencode":true}}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		_ = os.WriteFile(path, []byte(body), 0600)
		if _, err := loadConfig(path); err == nil {
			t.Fatal("invalid remote-control configuration accepted")
		}
	}
}

func TestRemoteControlClaudeFlagBeforeGoal(t *testing.T) {
	h := nativeFixtureHost(t, "native", "fixture-thread", "claude")
	cfg := &Config{Hosts: []Host{h}}
	cfg.withDefaults()
	h = cfg.Hosts[0]
	r := registrationReceipt(t, "claude", Overrides{})
	_, _, _ = h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil, "claude", Overrides{}, r, "synthetic task")
	b, _ := os.ReadFile(os.Getenv("REGISTRATION_CALLS"))
	if !strings.Contains(string(b), "--remote-control") || !strings.Contains(string(b), "--name") {
		t.Fatal("managed Claude launch lacks required native remote-control/name flags")
	}
}
