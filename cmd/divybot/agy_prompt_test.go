package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAGYPromptUsesNativeUserStepForItsExactLaunch(t *testing.T) {
	for _, mode := range []string{"native", "wrong-label", "screen-only"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			cwd := filepath.Join(root, "checkout")
			j := &Job{Agent: "agy", Label: "fixture-agent", Pane: "w1:p1", Workspace: "w1", Host: "fixture", DispatchKey: strings.Repeat("d", 64)}
			storePath, _ := agyStoreDirectory(cwd, "orchid-"+j.DispatchKey)
			if os.MkdirAll(filepath.Join(storePath, "conversations"), 0700) != nil {
				t.Fatal("fixture directory")
			}
			if _, err := matrixCommand(context.Background(), "", "python3", nil, "-c", agyCompletionFixtureSQL, storePath, syntheticAGYID, ""); err != nil {
				t.Fatal(err)
			}
			a := AgentInfo{Agent: "agy", Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, TerminalID: "fixture-terminal", Cwd: cwd, InteractiveReady: true, AgentStatus: "idle"}
			if mode == "wrong-label" {
				a.Name = "foreign-agent"
			}
			t.Setenv("AGY_PROMPT_AGENT", string(fixtureJSON(t, map[string]any{"result": map[string]any{"agent": a}})))
			t.Setenv("AGY_PROMPT_STORE", filepath.Join(storePath, "conversations", syntheticAGYID+".db"))
			t.Setenv("AGY_PROMPT_MODE", mode)
			t.Setenv("PROMPT_FIXTURE_SOCKET", herdrEventFixture(t))
			calls := filepath.Join(root, "calls")
			t.Setenv("AGY_PROMPT_CALLS", calls)
			bin := filepath.Join(root, ".local", "bin")
			if os.MkdirAll(bin, 0700) != nil {
				t.Fatal("fixture")
			}
			script := `#!/usr/bin/env python3
import json,os,sqlite3,sys
args=sys.argv[1:]
if args[:2] in (['agent','get'],['agent','wait']):print(os.environ['AGY_PROMPT_AGENT'])
elif args[:2]==['status','server']:print(json.dumps({'running':True,'socket':os.environ['PROMPT_FIXTURE_SOCKET']}))
elif args[:2]==['agent','prompt']:
 with open(os.environ['AGY_PROMPT_CALLS'],'a') as f:f.write('prompt\n')
 if os.environ['AGY_PROMPT_MODE']!='screen-only':
  db=sqlite3.connect(os.environ['AGY_PROMPT_STORE']);db.execute('INSERT INTO steps VALUES(2,14,3,NULL,NULL,NULL,0)');db.commit();db.close()
 print(json.dumps({'result':{}}))
else:sys.exit(1)
`
			if os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700) != nil {
				t.Fatal("fixture CLI")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := (Host{Home: root, Name: j.Host}).injectAGYGoal(ctx, a, "fixture assignment", j)
			if (err == nil) != (mode == "native") {
				t.Fatal("native prompt scope/proof mismatch", mode, err)
			}
			logged, _ := os.ReadFile(calls)
			if strings.Count(string(logged), "prompt") > 1 {
				t.Fatal("uncertain native proof resubmitted")
			}
			if mode == "wrong-label" && len(logged) > 0 {
				t.Fatal("foreign occupant received the prompt")
			}
		})
	}
}
