package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexPromptUsesInstalledPlainReadContractAndExactPane(t *testing.T) {
	for _, format := range []string{"plain", "json"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, ".local", "bin")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODEx_PROMPT_FIXTURE", root)
			t.Setenv("CODEx_PROMPT_FORMAT", format)
			script := `#!/usr/bin/env python3
import json,os,sys
root=os.environ['CODEx_PROMPT_FIXTURE'];args=sys.argv[1:]
with open(os.path.join(root,'calls'),'a') as f:f.write(json.dumps(args)+'\n')
path=os.path.join(root,'prompt');submitted=os.path.exists(path)
if args[:2]==['agent','get']:
 a={'agent':'codex','name':'fixture-agent','pane_id':'w1:p1','workspace_id':'w1','cwd':'/fixture/project','interactive_ready':True,'agent_status':'working' if submitted else 'done','state_change_seq':8 if submitted else 7}
 print(json.dumps({'result':{'type':'agent_info','agent':a}}))
elif args[:2]==['pane','read']:
 if '--source' not in args or args[args.index('--source')+1]!='visible':sys.exit(1)
 text='› '+open(path).read()+'\n• Reading fixture assignment\n' if submitted else 'OpenAI Codex (v0.159.3)\n› Anything interesting on the docket?\n'
 if os.environ['CODEx_PROMPT_FORMAT']=='long-tail' and submitted:
  rows=[]
  for line in text.split('\n'):
   rows.extend(line[i:i+100] for i in range(0,len(line),100))
  text='\n'.join(rows[-60:])
 if os.environ['CODEx_PROMPT_FORMAT']!='json':print(text)
 else:print(json.dumps({'result':{'text':text}}))
elif args[:2]==['agent','prompt']:
 if args[2]!='w1:p1' or submitted:sys.exit(1)
 open(path,'w').write(args[3]);print(json.dumps({'result':{'accepted':True}}))
else:sys.exit(1)
`
			if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// The readiness gate reads the installed plain (or JSON) visible
			// contract of the exact pane.
			snap, err := (Host{Home: root}).promptSnapshot(ctx, "w1:p1")
			if err != nil || !snap.Stable || !codexReady(snap) {
				t.Fatalf("real-format visible read not usable by the gate: %v %+v", err, snap)
			}
			// A Codex host without Remote Control has no delivery path: nothing is sent.
			if err := (Host{Home: root}).injectGoal(ctx, "w1:p1", promptFixtureGoal, true); err == nil {
				t.Fatal("a non-Remote-Control Codex delivery was attempted")
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil || strings.Contains(string(calls), `"agent", "prompt"`) || strings.Contains(string(calls), `"send-keys"`) ||
				!strings.Contains(string(calls), `"pane", "read", "w1:p1", "--source", "visible"`) {
				t.Fatalf("calls: %s", calls)
			}
		})
	}
}
func TestCodexPromptTrustDialogDoesNotReceiveTheGoal(t *testing.T) {
	h := &promptHarness{limit: 20, read: func(h *promptHarness) (promptSnapshot, error) {
		s := promptFixture()
		// The dialog's last prompt line is empty: only the dialog itself refuses it.
		s.Screen = "Folder access\nTrust this folder?\n1. Trust and continue\n2. No, quit\n›"
		return s, nil
	}}
	// Remote Control never answers the dialog, and the goal never reaches it.
	if err := h.run(); err == nil || h.submits != 0 || h.accepts != 0 {
		t.Fatal("trust dialog acquired the assignment")
	}
}

func TestCodexVisibleReadRefusesFailedMalformedAndOversizedOutput(t *testing.T) {
	for _, variant := range []string{"failed", "missing-text", "error-envelope", "malformed", "oversized"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, ".local", "bin")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PROMPT_READ_VARIANT", variant)
			script := `#!/usr/bin/env python3
import os,sys
v=os.environ['PROMPT_READ_VARIANT']
if v=='failed':sys.exit(1)
if v=='missing-text':print('{"result":{}}')
if v=='error-envelope':print('{"error":{"code":"fixture","message":"private fixture detail"}}')
if v=='malformed':print('{not-json')
if v=='oversized':print('› '+('x'*65536))
`
			if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if screen, err := (Host{Home: root}).visiblePromptScreen(context.Background(), "w1:p1"); err == nil || screen != "" || strings.Contains(err.Error(), "private") {
				t.Fatal("failed pane read became screen evidence or exposed a raw error")
			}
		})
	}
}
