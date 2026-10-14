package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeRestoreInstallsForLaunchAccount(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$HOME\" \"$@\" >> " + shq(log) + "\n"
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	h := Host{Home: root}
	for _, agent := range []string{"opencode", "agy", "claude", "codex"} {
		if err := h.ensureNativeRestore(context.Background(), agent); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(log)
	want := root + "\nintegration\ninstall\nopencode\n" + root + "\nintegration\ninstall\nantigravity-cli\n"
	if err != nil || string(body) != want {
		t.Fatal("native integration installation lost its account or touched completed integrations")
	}
	if strings.Contains(string(body), "prompt") {
		t.Fatal("restore reconstructed a prompt")
	}
}

func TestAGYNativeRestoreKeepsPrivateConversationStore(t *testing.T) {
	for _, mode := range []string{"valid", "foreign-session", "replaced", "refused"} {
		t.Run(mode, func(t *testing.T) {
			_, j, _ := agyLiveBindingFixture(t)
			cwd := "/fixture/issue-7"
			directory, _ := agyStoreDirectory(cwd, "orchid-"+j.DispatchKey)
			info := AgentInfo{Agent: "agy", Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, Cwd: cwd, TerminalID: "fixture-terminal", InteractiveReady: true, AgentStatus: "working",
				AgentSession: json.RawMessage(`{"source":"herdr:antigravity_cli","agent":"agy","kind":"id","value":"` + syntheticAGYID + `"}`)}
			calls := 0
			read := func(context.Context, string) (AgentInfo, error) {
				current := info
				if mode == "foreign-session" {
					current.AgentSession = json.RawMessage(`{"value":"foreign"}`)
				}
				if mode == "replaced" && calls > 0 {
					current.TerminalID = "other-terminal"
				}
				return current, nil
			}
			report := func(_ context.Context, args ...string) (string, error) {
				calls++
				text := strings.Join(args, " ")
				if !strings.Contains(text, "--app_data_dir") || !strings.Contains(text, "--conversation "+syntheticAGYID) || strings.Contains(text, "--state") || strings.Contains(text, "--agent-session-id") {
					t.Fatal("resume lost native store or rewrote identity/state")
				}
				if mode == "refused" {
					return `{"error":{"code":"resume_not_accepted"}}`, nil
				}
				return `{"result":{"type":"ok"}}`, nil
			}
			ok := reportAGYRestore(context.Background(), j, &nativeStore{Source: "agy", Directory: directory}, syntheticAGYID, "/fixture/account", read, report)
			if ok != (mode == "valid") {
				t.Fatal("restore accepted foreign/replaced occupant or vendor refusal")
			}
			if mode == "foreign-session" && calls != 0 {
				t.Fatal("foreign conversation gained restore command")
			}
		})
	}
}
