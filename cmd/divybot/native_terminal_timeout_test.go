package main

// Regression adapted from the independent review reproductions.
import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeTerminalRemoteTimeoutTeardownRetried(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"gh", "ssh", "herdr", "python3", "sh", "bash"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/false\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin) // no real gh/ssh/herdr can run
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	j.RemoteControl = &remoteControlRun{NativeSessionID: j.NativeSessionID}
	j.Workspace, j.Pane = "fixture-ws", "fixture-pane"
	j.Deadline = time.Now().Add(-time.Minute)
	j.RemoteCleanup = "operator-timeout" // persisted by tick 1's beginRemoteCleanup
	c.hosts[j.Host] = Host{Name: j.Host}
	c.cfg.Hosts = []Host{c.hosts[j.Host]}
	ctx := context.Background()
	// Tick 1: superviseRemoteCleanup -> teardown(cause=operator-timeout); the stop is unconfirmed.
	if c.teardown(ctx, 701, j, "operator-timeout") {
		t.Fatal("fixture unexpectedly confirmed teardown")
	}
	t.Logf("tick 1: StopMisses=%d", j.StopMisses)
	// Ticks 2..4: the only driver of a remote operator-timeout cleanup is superviseActive.
	for tick := 2; tick <= 4; tick++ {
		j.LastStopMissAt = time.Time{} // pacing elapsed
		c.superviseActive(ctx, 701, j, map[int]agentRef{}, Issue{Number: 701})
		t.Logf("tick %d: StopMisses=%d tracked=%v", tick, j.StopMisses, c.st.Jobs[701] == j)
	}
	if j.StopMisses != 4 {
		t.Fatalf("remote operator-timeout teardown attempts after 4 ticks = %d, want 4 (retry driver stalled)", j.StopMisses)
	}
}

// Drive every per-tick path that can own an open-issue remote job; expect release at the stop bound.
func TestNativeTerminalRemoteTimeoutReleasedAtStopBound(t *testing.T) {
	for _, closureFails := range []bool{false, true} {
		t.Run(fmt.Sprint(closureFails), func(t *testing.T) {
			bin := t.TempDir()
			for _, name := range []string{"gh", "ssh", "herdr", "python3", "sh", "bash"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/false\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			c := nativeRunFixture(t)
			j := c.st.Jobs[701]
			if closureFails {
				j.Home = nil
				delete(c.st.SourceBindings, 701)
			}
			j.RemoteControl = &remoteControlRun{NativeSessionID: j.NativeSessionID}
			j.Workspace, j.Pane = "fixture-ws", "fixture-pane"
			j.Deadline = time.Now().Add(-time.Minute)
			j.RemoteCleanup = "operator-timeout"
			c.hosts[j.Host] = Host{Name: j.Host}
			c.cfg.Hosts = []Host{c.hosts[j.Host]}
			ctx := context.Background()
			_ = c.teardown(ctx, 701, j, "operator-timeout") // tick 1
			c.st = loadState(c.st.path)                     // both terminal and pending cleanup survive restart
			j = c.st.Jobs[701]
			up := map[string]bool{c.hosts[j.Host].forJob(j).observerKey(): true}
			open := map[int]bool{701: true}
			for tick := 2; tick <= 8 && c.st.Jobs[701] != nil; tick++ {
				j.LastStopMissAt, j.LastMissAt = time.Time{}, time.Time{}
				c.completionPass(ctx, map[int]agentRef{})
				c.reapRemoteRuns(ctx, nil, up, open)
				if c.st.Jobs[701] != nil {
					c.superviseActive(ctx, 701, j, map[int]agentRef{}, Issue{Number: 701})
				}
				t.Logf("tick %d: StopMisses=%d tracked=%v", tick, j.StopMisses, c.st.Jobs[701] != nil)
			}
			if c.st.Jobs[701] != nil {
				t.Fatalf("timed-out remote run still tracked after 8 ticks (StopMisses=%d); capacity never released", j.StopMisses)
			}
		})
	}
}

func TestNativeTerminalOrdinaryTimeoutRetriesWhenIssueCloseFails(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"gh", "ssh", "herdr", "python3", "sh", "bash"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/false\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	j.Home = nil
	delete(c.st.SourceBindings, 701) // issue close must use the refusing gh fixture
	j.Deadline = time.Now().Add(-time.Minute)
	// No host can confirm the first stop. Its recorded end must not prevent retry.
	if c.teardown(context.Background(), 701, j, "operator-timeout") || j.StopMisses != 1 {
		t.Fatal("first unknown cleanup did not retain its job")
	}
	c.hosts[j.Host] = Host{Name: j.Host}
	c.superviseActive(context.Background(), 701, j, nil, Issue{Number: 701})
	if c.st.Jobs[701] != nil || loadState(c.st.path).Jobs[701] != nil {
		t.Fatal("ordinary timeout stayed tracked after cleanup despite failed source close")
	}
}
