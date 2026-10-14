package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestNativePromptAcceptancePinsTerminalAndSession(t *testing.T) {
	before := promptFixture().Agent
	before.TerminalID = "fixture-terminal"
	before.NativeLifecycle = true
	before.AgentSession = []byte(`{"agent":"codex","kind":"id","value":"fixture-thread"}`)
	after := before
	after.AgentStatus = "working"
	after.StateChangeSeq++
	for _, mode := range []string{"terminal", "session", "native"} {
		a := after
		switch mode {
		case "terminal":
			a.TerminalID = "replacement-terminal"
		case "session":
			a.AgentSession = []byte(`{"agent":"codex","kind":"id","value":"other-thread"}`)
		case "native":
			a.NativeLifecycle = false
		}
		if nativePromptObserved(before, a) {
			t.Fatal("replacement or detected state accepted the prompt", mode)
		}
	}
}

func TestServerWaitRequiresNativeAuthorityAndExactOccupant(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	expected := promptFixture().Agent
	for _, mode := range []string{"native", "detected", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			a := expected
			if mode == "detected" {
				a.NativeLifecycle = false
			}
			if mode == "replacement" {
				a.TerminalID = "other-terminal"
			}
			body, _ := json.Marshal(map[string]any{"result": map[string]any{"agent": a}})
			script := "#!/usr/bin/env python3\nimport sys\nassert sys.argv[1:3]==['agent','wait']\nassert '--timeout' in sys.argv\nassert sys.argv[3]=='fixture-agent'\nprint(" + strconv.Quote(string(body)) + ")\n"
			if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := (Host{Home: root}).waitAgent(ctx, expected)
			if (err == nil) != (mode == "native") {
				t.Fatal("server wait followed detected or replacement occupant", mode, err)
			}
		})
	}
}

func TestPromptEventSubscriptionOwnsCancellationAndReadOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	acknowledged := make(chan struct{})
	lose := make(chan struct{})
	go func() {
		var q any
		if json.NewDecoder(server).Decode(&q) != nil {
			return
		}
		close(acknowledged)
		enc := json.NewEncoder(server)
		enc.Encode(map[string]any{"id": "fleet", "result": map[string]string{"type": "subscription_started"}})
		<-lose
		enc.Encode(map[string]any{"id": "fleet", "error": map[string]string{"code": "events_lost"}})
	}()
	expected := promptFixture().Agent
	p := startPromptEvents(ctx, expected, client, func(context.Context) (AgentInfo, error) {
		select {
		case <-acknowledged:
		default:
			t.Error("read before subscription acknowledgement")
		}
		return expected, nil
	})
	defer p.close()
	if _, err := p.observe(ctx); err != nil {
		t.Fatal(err)
	}
	close(lose)
	select {
	case <-p.done:
	case <-ctx.Done():
		t.Fatal("lost stream did not finish")
	}
	// Closing a stream must stop effect-time waits too, not just cache reads.
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if p.wait(waitCtx) || waitCtx.Err() != nil {
		t.Fatal("lost subscription left a delivery wait active")
	}
}

func TestNativePromptCannotAcceptAnUntrustedIdentitySource(t *testing.T) {
	before := promptFixture().Agent
	before.AgentSession = []byte(`{"source":"herdr:codex","agent":"codex","kind":"id","value":"fixture-thread"}`)
	after := before
	after.AgentStatus = "working"
	after.StateChangeSeq++
	after.AgentSession = []byte(`{"source":"screen","agent":"codex","kind":"id","value":"fixture-thread"}`)
	if nativePromptObserved(before, after) {
		t.Fatal("untrusted identity provenance confirmed prompt")
	}
}
