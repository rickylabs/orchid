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

// An idle badge without native readiness cannot authorize trust input or a goal.
func TestCodexGoalWaitsForNativeReadinessWithoutTrustInput(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "calls")
	t.Setenv("TRUST_FIXTURE_CALLS", calls)
	script := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[1:]
with open(os.environ['TRUST_FIXTURE_CALLS'],'a') as f:f.write(' '.join(args[:2])+'\n')
print(json.dumps({'result':{'agent':{'agent':'codex','agent_status':'idle','interactive_ready':False}}}))
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := (Host{Home: root}).injectGoal(ctx, "w1:p1", "goal", false); err == nil {
		t.Fatal("unready Codex accepted a goal")
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if call != "agent get" {
			t.Fatalf("unready Codex received a screen read or input: %s", call)
		}
	}
}
