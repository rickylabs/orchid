package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A fake herdr that records every call and serves the job's Codex occupant.
func shadowInputsHost(t *testing.T, j *Job) (Host, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if os.MkdirAll(bin, 0700) != nil {
		t.Fatal("fixture setup failed")
	}
	calls := filepath.Join(root, "herdr-calls")
	t.Setenv("SHADOW_FIXTURE_CALLS", calls)
	t.Setenv("SHADOW_FIXTURE_CWD", j.RemoteControl.Cwd)
	t.Setenv("SHADOW_FIXTURE_ID", j.RemoteControl.NativeSessionID)
	script := `#!/usr/bin/env python3
import sys,json,os
a=sys.argv[1:]
open(os.environ['SHADOW_FIXTURE_CALLS'],'a').write(' '.join(a)+'\n')
if a[:2]==['agent','get']:
 r={'type':'agent_info','agent':{'agent':'codex','name':'harness','pane_id':'w1:p1','workspace_id':'w1','cwd':os.environ['SHADOW_FIXTURE_CWD'],'agent_status':'idle','interactive_ready':True,'state_change_seq':4,'agent_session':{'source':'herdr:codex','agent':'codex','kind':'id','value':os.environ['SHADOW_FIXTURE_ID']}}}
elif a[:2]==['pane','read']:
 print('/rc active');sys.exit(0)
else:r={'type':'ok'}
print(json.dumps({'result':r}))
`
	if os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700) != nil {
		t.Fatal("fixture executable unavailable")
	}
	return Host{Name: "fixture-host", Home: root}, calls
}

// The attachment check behind every Codex remote hook reads no pane since #82:
// the shadow records the native inputs it consumed, never a screen footer.
func TestRemoteHookShadowRecordsNativeInputsAndReadsNoPane(t *testing.T) {
	j := shadowFixtureJob(t, "codex")
	j.RemoteControl.ClientVersion = "9.1.0"
	j.RemoteControl.Resume = &codexResumeProof{Producer: strings.Repeat("ab", 32), Connection: "252"}
	h, calls := shadowInputsHost(t, j)
	c := &Coord{st: &State{}, cfg: &Config{}, shadow: newNativeEvidenceShadow(time.Now)}
	if !c.checkRemoteHook(context.Background(), h, j) {
		t.Fatal("control: the native attachment check refused a matching occupant")
	}
	l := c.shadow.ledger(j)
	if len(l) != 1 {
		t.Fatalf("want one comparison, got %d", len(l))
	}
	if l[0].Today != "pass" || l[0].ScreenDerived {
		t.Fatalf("a native hook decision recorded as screen-derived: %+v", l[0])
	}
	if want := []string{shadowInputHerdrAgent, shadowInputResumeProof}; !reflect.DeepEqual(l[0].Provenance, want) {
		t.Fatalf("provenance %v, want %v", l[0].Provenance, want)
	}
	raw, _ := os.ReadFile(calls)
	if !strings.Contains(string(raw), "agent get") || strings.Contains(string(raw), "pane read") {
		t.Fatalf("herdr calls: %q", raw)
	}
}

// The Codex connection proof's recorded inputs name what it reads: the occupant,
// the stored resume proof and the canonical daemon's status (plus the TUI process
// for a native-status identity). Claude's stays the occupant alone.
func TestRemoteProofShadowInputsAreNative(t *testing.T) {
	j := shadowFixtureJob(t, "codex")
	if got, want := remoteProofInputs(j), []string{shadowInputHerdrAgent, shadowInputResumeProof, shadowInputCodexStatus}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codex proof inputs %v, want %v", got, want)
	}
	j.RemoteControl.IdentitySource = "codex-native-status"
	if got := remoteProofInputs(j); got[len(got)-1] != shadowInputTUIProcess || len(got) != 4 {
		t.Fatalf("native-status proof inputs %v", got)
	}
	if got := remoteProofInputs(shadowFixtureJob(t, "claude")); !reflect.DeepEqual(got, []string{shadowInputHerdrAgent}) {
		t.Fatalf("claude proof inputs %v", got)
	}
	s := newNativeEvidenceShadow(time.Now)
	s.compare(j, shadowSiteConnection, "connected", remoteProofInputs(j))
	if l := s.ledger(j); len(l) != 1 || l[0].ScreenDerived {
		t.Fatalf("a native connection proof recorded as screen-derived: %+v", l)
	}
	// Control: a genuinely screen-read input is still marked.
	s.compare(j, shadowSiteConnection, "connected", []string{shadowInputHerdrAgent, shadowInputScreenFooter})
	if l := s.ledger(j); len(l) != 2 || !l[1].ScreenDerived {
		t.Fatalf("a screen footer input lost its mark: %+v", l)
	}
}
