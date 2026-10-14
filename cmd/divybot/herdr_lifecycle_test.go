package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A session hook gives identity; a detected working badge is not native lifecycle.
func TestClaudeDetectedStateCannotPublishNativeLifecycle(t *testing.T) {
	root, job, _ := claudeLiveBindingFixture(t)
	info := claudeAgentInfo(t, privateTestID(t))
	info["agent"].(map[string]any)["agent_status"] = "working"
	info["agent"].(map[string]any)["screen_detection_skipped"] = false
	read := func(context.Context, string) (json.RawMessage, error) { return fixtureJSON(t, info), nil }
	if bound, err := retryLiveNativeBinding(context.Background(), root, "fixture/inbox", nil, job, read); err != nil || !bound {
		t.Fatal("fixture binding", err)
	}
	if observeClaudeWorking(context.Background(), root, "fixture/inbox", nil, job, read, time.Now()) {
		t.Fatal("screen-detected state became native lifecycle")
	}
	if _, err := os.Stat(filepath.Join(root, job.DispatchKey, "record", "claude-status.json")); !os.IsNotExist(err) {
		t.Fatal("detected status retained a native receipt")
	}
}

func TestNativeLifecycleHookUsesExactOccupantAndBoundedVendorEvents(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		hooks := nativeLifecycleHooks(agent, "fixture-agent", "w1:p1", "w1", "/fixture/project")
		for event, entries := range hooks {
			hook := entries.([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
			if event == "SessionEnd" && hook["timeout"].(int) > 3 {
				t.Fatal("session end exceeds the vendor deadline")
			}
		}
		for _, mode := range []string{"working", "stopped", "permission", "release", "foreign-pane", "foreign-session", "child", "wrong-cwd"} {
			t.Run(agent+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				calls := filepath.Join(root, "calls")
				bin := filepath.Join(root, "herdr")
				t.Setenv("HERDR_BIN_PATH", bin)
				t.Setenv("LIFECYCLE_FIXTURE_CALLS", calls)
				t.Setenv("LIFECYCLE_FIXTURE_MODE", mode)
				t.Setenv("LIFECYCLE_FIXTURE_AGENT", agent)
				script := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[1:]
if args[:2]==['agent','get']:
 a={'agent':os.environ['LIFECYCLE_FIXTURE_AGENT'],'name':'fixture-agent','pane_id':'w1:p1','workspace_id':'w1','cwd':'/fixture/project','agent_session':{'agent':os.environ['LIFECYCLE_FIXTURE_AGENT'],'kind':'id','value':'fixture-thread'}}
 if os.environ['LIFECYCLE_FIXTURE_MODE']=='foreign-pane':a['pane_id']='w2:p1'
 if os.environ['LIFECYCLE_FIXTURE_MODE']=='foreign-session':a['agent_session']['value']='foreign-thread'
 print(json.dumps({'result':{'agent':a}}))
else:
 with open(os.environ['LIFECYCLE_FIXTURE_CALLS'],'a') as f:f.write(json.dumps(args)+'\n')
`
				if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				event := map[string]any{"session_id": "fixture-thread", "cwd": "/fixture/project", "hook_event_name": "UserPromptSubmit"}
				want := "working"
				switch mode {
				case "stopped":
					event["hook_event_name"] = "Stop"
					want = "idle"
				case "permission":
					event["hook_event_name"] = "PermissionRequest"
					want = "blocked"
				case "release":
					event["hook_event_name"] = "SessionEnd"
					want = "release-agent"
				case "child":
					event["agent_id"] = "child"
				case "wrong-cwd":
					event["cwd"] = "/fixture/other"
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second*4)
				defer cancel()
				cmd := exec.CommandContext(ctx, "python3", "-c", nativeLifecycleHook, agent, "fixture-agent", "w1:p1", "w1", "/fixture/project")
				cmd.Stdin = bytes.NewReader(fixtureJSON(t, event))
				out, err := cmd.CombinedOutput()
				if err != nil || string(out) != "{}\n" {
					t.Fatal("hook changed vendor behavior", err, string(out))
				}
				logged, err := os.ReadFile(calls)
				if strings.HasPrefix(mode, "foreign") || mode == "child" || mode == "wrong-cwd" {
					if !os.IsNotExist(err) {
						t.Fatal("foreign event changed this occupant")
					}
					return
				}
				if err != nil || !strings.Contains(string(logged), want) || !strings.Contains(string(logged), "orchid:"+agent) || !strings.Contains(string(logged), "--seq") {
					t.Fatal("native event was not reported", err, string(logged))
				}
			})
		}
	}
}

func TestCodexNativeDeliveryIgnoresComposerBeforeSubmission(t *testing.T) {
	expected := promptFixture().Agent
	calls := 0
	err := deliverCodexPrompt(context.Background(), expected, promptFixtureGoal, promptCalls{
		observe: func(context.Context) (promptSnapshot, error) { s := promptFixture(); s.Screen = ""; return s, nil },
		submit:  func(context.Context, string) error { calls++; return nil },
		native:  func(ctx context.Context, _ string, submit func(context.Context) error) error { return submit(ctx) },
		wait:    func(context.Context) bool { return false }, now: time.Now,
	})
	if err != nil || calls != 1 {
		t.Fatal("native delivery still depends on composer text", err, calls)
	}
}
