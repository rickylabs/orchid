package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The screen codex 0.159.2 shows in a new work directory, as herdr 0.9.1's
// `pane read` returned it (harness #509, 2026-09-30).
const codexTrustScreen = "  Folder access\n  /ephemeral/orch-work/issue-509\n  Trust this folder? Codex can read, edit, and run\n  files here, subject to your permission settings.\n› 1. Trust and continue\n  2. Quit\n  enter continue · esc quit\n"

func TestCodexTrustDialogIsRecognized(t *testing.T) {
	if !codexTrustDialog(codexTrustScreen) {
		t.Fatal("the codex 0.159.2 trust dialog was not recognized")
	}
	for _, screen := range []string{"", "  >_ OpenAI Codex (v0.159.2)\n  permissions: YOLO mode\n›", "Trust this folder? (quoted in a brief, no dialog)"} {
		if codexTrustDialog(screen) {
			t.Fatalf("recognized a trust dialog in %q", screen)
		}
	}
}

// A fake herdr that behaves as codex 0.159.2 under herdr 0.9.1 did in #509:
// the trust dialog is reported idle (default fallback), a prompt sent while it
// shows is lost, an Enter accepts it, and codex then reports working while it
// boots before settling to done.
func TestCodexGoalIsDeliveredPastTheTrustDialog(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "dialog"), []byte(codexTrustScreen), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRUST_FIXTURE", state)
	script := `#!/usr/bin/env python3
import json,os,sys
d=os.environ['TRUST_FIXTURE']; a=sys.argv[1:]
has=lambda n: os.path.exists(os.path.join(d,n))
def touch(n): open(os.path.join(d,n),'a').close()
def log(x): open(os.path.join(d,'events'),'a').write(x+'\n')
def count(n):
 p=os.path.join(d,n); c=int(open(p).read()) if os.path.exists(p) else 0; open(p,'w').write(str(c+1)); return c
trusted=has('trusted')
if a[:2]==['pane','read']:
 text=open(os.path.join(d,'dialog')).read() if not trusted else '  >_ OpenAI Codex (v0.159.2)\n  permissions: YOLO mode\n› '
 print(json.dumps({'result':{'text':text}}))
elif a[:2]==['pane','send-keys']:
 if not trusted: touch('trusted'); log('trust-accepted')
 else: log('enter')
 print(json.dumps({'result':{}}))
elif a[:2]==['agent','prompt']:
 log('goal-'+('lost' if not trusted else 'delivered'))
 if trusted: touch('working')
 print(json.dumps({'result':{'accepted':True}}))
elif a[:2]==['agent','get']:
 if not trusted: s='idle'
 elif has('working'): s='working'
 else: s='working' if count('boot')<2 else 'done'
 print(json.dumps({'result':{'agent':{'agent':'codex','name':'codex-509','pane_id':'w43:p1','workspace_id':'w43','agent_status':s,'state_change_seq':1}}}))
else: print(json.dumps({'result':{}}))
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	host := Host{Home: root, Name: "fixture-host"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := host.injectGoal(ctx, "w43:p1", "Work the linked GitHub issue according to its description.", false); err != nil {
		t.Fatalf("goal not confirmed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(state, "events"))
	if err != nil {
		t.Fatal(err)
	}
	events := strings.Fields(string(raw))
	if strings.Contains(string(raw), "goal-lost") {
		t.Fatalf("the goal was typed into the trust dialog and lost: %v", events)
	}
	if len(events) < 2 || events[0] != "trust-accepted" || events[1] != "goal-delivered" {
		t.Fatalf("want the dialog accepted, then the goal delivered once; got %v", events)
	}
	if n := strings.Count(string(raw), "goal-delivered"); n != 1 {
		t.Fatalf("goal delivered %d times, want once: %v", n, events)
	}
}
