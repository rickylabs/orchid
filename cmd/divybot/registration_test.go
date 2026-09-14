package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func registrationReceipt(t *testing.T, agent string, o Overrides) *durableMatrixReceipt {
	t.Helper()
	key := strings.Repeat("d", 64)
	r, err := persistMatrixReceipt(privateTestRoot(t), key, buildAgentCmd(agent, o), receiptFor(MatrixConfig{}, syntheticRoute()), nil)
	if err != nil {
		t.Fatal("fixture matrix receipt failed")
	}
	r.dispatch = &dispatchBinding{SchemaVersion: 1, RunID: "orchid-" + key, Issue: dispatchIssue{Repo: "fixture/inbox", Number: 7}, Source: "claude"}
	if r.writeDispatch("reserved", nil) != nil {
		t.Fatal("fixture dispatch binding failed")
	}
	return r
}

// Only synthetic handles and argv. This fixture never invokes a native agent.
func registrationHost(t *testing.T, fail string) (Host, func() [][]string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "calls.jsonl")
	t.Setenv("REGISTRATION_CALLS", logPath)
	t.Setenv("REGISTRATION_FAILURE", fail)
	script := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[1:]
with open(os.environ['REGISTRATION_CALLS'],'a') as f:f.write(json.dumps(args)+'\n')
if args[:2]==['workspace','create']:
 print(json.dumps({'result':{'workspace':{'workspace_id':'w1'},'root_pane':{'pane_id':'w1:p1'}}}))
elif args[:2]==['agent','start'] and os.environ.get('REGISTRATION_FAILURE')=='busy':
 print(json.dumps({'error':{'code':'agent_pane_busy','message':'synthetic busy pane'}}));sys.exit(1)
elif args[:2]==['agent','start'] and os.environ.get('REGISTRATION_FAILURE')=='envelope':
 print(json.dumps({'error':{'code':'agent_not_ready','message':'synthetic not ready'}}))
elif args[:2]==['agent','start'] and os.environ.get('REGISTRATION_FAILURE')=='malformed':
 print('not a response')
else:print(json.dumps({'result':{}}))
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	calls := func() [][]string {
		b, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		var result [][]string
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var args []string
			if err := json.Unmarshal([]byte(line), &args); err != nil {
				t.Fatal(err)
			}
			result = append(result, args)
		}
		return result
	}
	return Host{Home: root}, calls
}

func TestRegistrationBeforeGoalUsesExactPaneAndConfiguredArgv(t *testing.T) {
	expectedArgs := map[string][]string{
		"codex":    {"--dangerously-bypass-approvals-and-sandbox", "-m", "fixture model 'quoted'"},
		"claude":   {"--dangerously-skip-permissions", "--model", "fixture model 'quoted'"},
		"opencode": {"--model", "fixture-provider/fixture model 'quoted'"},
		"agy":      {"--dangerously-skip-permissions", "--model", "fixture model 'quoted'", "--effort", "fixture-effort"},
	}
	for kind, configuredArgs := range expectedArgs {
		t.Run(kind, func(t *testing.T) {
			h, calls := registrationHost(t, "")
			o := Overrides{Model: "fixture model 'quoted'", Effort: "fixture-effort", Router: "fixture-provider"}
			pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), map[string]string{"FIXTURE_ENV": "fixture-value"}, kind, o, registrationReceipt(t, kind, o))
			if err != nil || pane != "w1:p1" || ws != "w1" {
				t.Fatal("registration did not return the created handles")
			}
			got := calls()
			if len(got) != 3 {
				t.Fatalf("wanted workspace, environment, registration; got %d calls", len(got))
			}
			if strings.Contains(got[1][3], "exec ") || !strings.Contains(got[1][3], "export FIXTURE_ENV=") {
				t.Fatal("environment preparation must leave the shell available")
			}
			expected := append([]string{"agent", "start", "fixture-agent", "--kind", kind, "--pane", "w1:p1", "--timeout", "30000", "--"}, configuredArgs...)
			if !reflect.DeepEqual(got[2], expected) {
				t.Fatalf("argv lost identity or quoting: %q", got[2])
			}
		})
	}
}

func TestRegistrationRefusesBusyNotReadyAndMalformedResponses(t *testing.T) {
	for _, failure := range []string{"busy", "envelope", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			h, calls := registrationHost(t, failure)
			pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil, "codex", Overrides{}, registrationReceipt(t, "codex", Overrides{}))
			if !errors.Is(err, errAgentRegistration) {
				t.Fatal("failed registration was accepted")
			}
			if pane != "w1:p1" || ws != "w1" {
				t.Fatal("failure must retain owned cleanup handles")
			}
			if len(calls()) != 3 {
				t.Fatal("registration failure retried or delivered a goal")
			}
		})
	}
}

func TestNonInteractiveRunKeepsDeadlineSupervisedLaunch(t *testing.T) {
	h, calls := registrationHost(t, "")
	if _, _, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil, "codex-run", Overrides{}, registrationReceipt(t, "codex-run", Overrides{})); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 2 || !strings.Contains(got[1][3], "exec bash") {
		t.Fatal("run mode must not pretend to register an interactive agent")
	}
}

func TestRegistrationFenceSurvivesRestartAndRefusesRepeatedAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st := loadState(path)
	if !st.reserveLaunch(7) {
		t.Fatal("positive reservation failed")
	}
	for i := 0; i < 20; i++ {
		st = loadState(path)
		if st.reserveLaunch(7) {
			t.Fatal("restart allowed a duplicate launch")
		}
	}
	st.blockLaunch(7, "registration_failed")
	st = loadState(path)
	if st.LaunchBlocks[7].Reason != "registration_failed" || st.reserveLaunch(7) {
		t.Fatal("abandonment did not persist")
	}
	if !st.reserveLaunch(8) {
		t.Fatal("one abandoned issue blocked an unrelated assignment")
	}
}

func TestRegistrationCannotLaunchWithoutDurableState(t *testing.T) {
	st := loadState(filepath.Join(t.TempDir(), "missing", "state.json"))
	if st.reserveLaunch(7) {
		t.Fatal("failed persistence licensed a launch")
	}
}

func TestAbandonmentCommentRetriesOnlyNotification(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "comments")
	t.Setenv("REGISTRATION_COMMENT_CALLS", calls)
	t.Setenv("REGISTRATION_COMMENT_FAIL", "1")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
printf 'comment\n' >> "$REGISTRATION_COMMENT_CALLS"
[ "$REGISTRATION_COMMENT_FAIL" = 0 ]
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	st := loadState(filepath.Join(root, "state.json"))
	if !st.reserveLaunch(7) {
		t.Fatal("reservation failed")
	}
	c := &Coord{st: st, cfg: &Config{Inbox: "fixture/inbox"}}
	if !c.reportBlockedLaunch(context.Background(), 7) || st.LaunchBlocks[7].Notified {
		t.Fatal("failed comment lost launch fence")
	}
	t.Setenv("REGISTRATION_COMMENT_FAIL", "0")
	if !c.reportBlockedLaunch(context.Background(), 7) || !st.LaunchBlocks[7].Notified {
		t.Fatal("comment retry failed")
	}
	if !c.reportBlockedLaunch(context.Background(), 7) {
		t.Fatal("notified fence disappeared")
	}
	b, err := os.ReadFile(calls)
	if err != nil || string(b) != "comment\ncomment\n" {
		t.Fatal("comment duplicated after success")
	}
	if loadState(st.path).reserveLaunch(7) {
		t.Fatal("comment success allowed a launch")
	}
}
