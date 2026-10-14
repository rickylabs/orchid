package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
