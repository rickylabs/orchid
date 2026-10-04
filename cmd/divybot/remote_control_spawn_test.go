package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An isolated CLI/Herdr fixture, never a native launch. In particular, the
// failed/absent RC controls must not publish a dispatched/native decoration.
func TestRemoteControlClaudeSpawnGate(t *testing.T) {
	for _, mode := range []string{"connected", "absent", "failed"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			cwd := t.TempDir()
			bin := filepath.Join(root, ".local", "bin")
			if os.MkdirAll(bin, 0700) != nil {
				t.Fatal("fixture setup failed")
			}
			t.Setenv("REMOTE_FIXTURE_CWD", cwd)
			t.Setenv("REMOTE_FIXTURE_ID", privateTestID(t))
			t.Setenv("REMOTE_FIXTURE_MODE", mode)
			script := `#!/usr/bin/env python3
import sys,json,os
a=sys.argv[1:]
agent={'agent':'claude','name':'fixture-agent','pane_id':'w1:p1','workspace_id':'w1','cwd':os.environ['REMOTE_FIXTURE_CWD'],'agent_status':'idle','interactive_ready':True,'state_change_seq':1,'agent_session':{'source':'herdr:claude','agent':'claude','kind':'id','value':os.environ['REMOTE_FIXTURE_ID']}}
if a[:2]==['workspace','create']:r={'type':'workspace_created','workspace':{'workspace_id':'w1'},'root_pane':{'pane_id':'w1:p1','workspace_id':'w1'}}
elif a[:2]==['agent','start']:r={'type':'agent_started','agent':agent}
elif a[:2]==['agent','get']:r={'type':'agent_info','agent':agent}
elif a[:2]==['pane','read']:
 print('/rc active' if os.environ['REMOTE_FIXTURE_MODE']=='connected' else 'Remote Control failed' if os.environ['REMOTE_FIXTURE_MODE']=='failed' else 'idle composer');sys.exit(0)
else:r={'type':'ok'}
print(json.dumps({'result':r}))
`
			if os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700) != nil {
				t.Fatal("fixture executable unavailable")
			}
			on := true
			host := Host{Name: "fixture-host", Home: root, RemoteControl: &RemoteControlConfig{Claude: &on}}
			receipt := registrationReceipt(t, "claude", Overrides{})
			budget := 800 * time.Millisecond
			if mode == "connected" {
				budget = 3 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			_, _, err := host.spawnAgent(ctx, "fixture-agent", cwd, nil, "claude", Overrides{}, receipt, "synthetic task")
			if (err == nil) != (mode == "connected") {
				t.Fatal("managed spawn accepted absent/failed RC or refused connected control")
			}
			path := filepath.Join(filepath.Dir(receipt.file), "remote-control.json")
			if mode != "connected" {
				if _, e := os.Lstat(path); !os.IsNotExist(e) {
					t.Fatal("failed remote launch published capability")
				}
				if receipt.dispatch.State == "dispatched" {
					t.Fatal("failed remote launch published successful dispatch")
				}
			} else {
				b, e := os.ReadFile(path)
				var row remoteControlObservation
				if e != nil || json.Unmarshal(b, &row) != nil || row.State != "connected" || row.SessionName != nil {
					t.Fatal("connected launch lost truthful name/connection distinction")
				}
			}
		})
	}
}

func TestRemoteControlPostPromptHookCannotReplacePreparedThread(t *testing.T) {
	for _, mode := range []string{"match", "mismatch", "absent", "late"} {
		t.Run(mode, func(t *testing.T) {
			r, j, _ := fixtureGoalBinding(t)
			j.RemoteControl = syntheticRemoteRun(t)
			j.RemoteControl.NativeSessionID = "fixture-thread"
			if mode == "mismatch" {
				j.RemoteControl.NativeSessionID = privateTestID(t)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id, err := acquireNativeGoalBinding(ctx, r, j, func() (json.RawMessage, error) {
				if mode == "late" {
					cancel()
				}
				return nativeReport(j, mode != "absent"), nil
			}, func(context.Context) bool { return false })
			if (err == nil) != (mode == "match") {
				t.Fatal("post-prompt hook conflict/absence/late report certified identity")
			}
			if mode == "match" && id != j.RemoteControl.NativeSessionID {
				t.Fatal("native hook changed prepared identity")
			}
		})
	}
}
