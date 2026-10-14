package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavedMachineRoutingIsScopedAndIndependent(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("MACHINE_CALLS", calls)
	script := `#!/bin/sh
if [ "$1 $2" = 'machine list' ]; then
 printf '%s\n' '[{"id":"machine-one","label":"build","target":"fixture-account","session":"default","enabled":true},{"id":"machine-two","label":"other","target":"fixture-other","session":"default","enabled":true}]'
 exit 0
fi
printf '%s\n' "$@" >> "$MACHINE_CALLS"
[ "$1" = '--machine' ] || exit 4
[ "$2" = 'machine-two' ] || exit 3
printf '%s\n' '{"result":{"type":"agent_list","agents":[]}}'
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	for _, h := range []Host{
		{SSH: "fixture-account", HerdrMachine: "missing"},
		{SSH: "fixture-wrong", HerdrMachine: "build"},
		{SSH: "fixture-account", HerdrMachine: "build", HerdrSession: "other-session"},
	} {
		if _, err := h.herdr(ctx, "agent", "list"); err == nil {
			t.Fatal("unavailable or wrong-account/session profile routed")
		}
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("invalid binding reached a remote mutation")
	}
	if _, err := (Host{SSH: "fixture-account", HerdrMachine: "build"}).herdr(ctx, "agent", "list"); err == nil {
		t.Fatal("disconnected profile fell back")
	}
	if _, err := (Host{SSH: "fixture-other", HerdrMachine: "other"}).herdr(ctx, "agent", "list"); err != nil {
		t.Fatal("one disconnected profile blocked another", err)
	}
	body, _ := os.ReadFile(calls)
	if string(body) != "--machine\nmachine-one\nagent\nlist\n--machine\nmachine-two\nagent\nlist\n" {
		t.Fatal("routing did not use exact saved ids")
	}
	if strings.Contains(string(body), "ssh") {
		t.Fatal("saved routing constructed remote shell")
	}
}
