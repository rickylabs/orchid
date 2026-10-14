package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestSavedMachineEventSubscriptionUsesNativeEndpoint(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("", "hm-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	path := filepath.Join(socketDir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("PROFILE_SOCKET", path)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/usr/bin/env python3
import json,os,sys
a=sys.argv[1:]
if a==['machine','list','--json']:
 print(json.dumps([{'id':'machine-one','label':'build','target':'fixture-account','session':'work','enabled':True}]))
elif a==['--machine','machine-one','status','server','--json']:
 # This is the actual Herdr 0.9.3 saved-machine status shape.
 print(json.dumps({'socket':'machine:machine-one/work','running':True}))
elif a==['--session','work','status','server','--json']:
 print(json.dumps({'socket':os.environ['PROFILE_SOCKET'],'running':True}))
elif a==['--machine','machine-one','api','snapshot']:
 print(json.dumps({'result':{'type':'session_snapshot','snapshot':{'agents':[]}}}))
else:sys.exit(5)
`
	ssh := `#!/usr/bin/env python3
import os,signal,subprocess,sys
if '-N' in sys.argv:
 local,remote=sys.argv[sys.argv.index('-L')+1].split(':',1)
 os.symlink(remote,local)
 signal.pause()
else:sys.exit(subprocess.run(['/bin/bash','-c',sys.argv[-1]]).returncode)
`
	for name, body := range map[string]string{"herdr": script, "ssh": ssh} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	acknowledged := make(chan bool, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acknowledged <- false
			return
		}
		defer conn.Close()
		var request struct {
			Method string `json:"method"`
		}
		if json.NewDecoder(conn).Decode(&request) != nil || request.Method != "events.subscribe" {
			acknowledged <- false
			return
		}
		if json.NewEncoder(conn).Encode(map[string]any{"id": "fleet", "result": map[string]string{"type": "subscription_started"}}) != nil {
			acknowledged <- false
			return
		}
		acknowledged <- true
		_, _ = io.Copy(io.Discard, conn)
	}()
	h := Host{Home: root, SSH: "fixture-account", HerdrMachine: "build", HerdrSession: "work"}
	status, statusErr := h.herdr(ctx, "status", "server", "--json")
	var machineStatus struct {
		Socket string `json:"socket"`
	}
	if statusErr != nil || json.Unmarshal([]byte(status), &machineStatus) != nil || machineStatus.Socket != "machine:machine-one/work" {
		t.Fatal("saved-machine status fixture drifted from the native contract")
	}
	stream, err := h.herdrEventConnection(ctx)
	if err != nil {
		t.Fatal("saved-machine label was used as a socket endpoint", err)
	}
	published := false
	_ = reconcileHerdrEvents(ctx, nil, stream, h.agentSnapshot, func(agents []AgentInfo, up bool) {
		if up {
			published = agents != nil
			cancel()
		}
	}, nil)
	if !published || !<-acknowledged {
		t.Fatal("saved profile did not reach subscription acknowledgement and scoped native snapshot")
	}
}
