package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// A finished run whose native stop can never be confirmed must not hold its
// seat forever. Every attempt runs after a restart from the persisted state, so
// the bound survives restarts; an event burst between attempts does not count.
func TestUnconfirmableStopEndsRunAfterBoundAcrossRestart(t *testing.T) {
	c, _, j, _, _ := teardownFixture(t)
	j.Agent, j.Label = "codex", "codex-7"
	j.RemoteControl = syntheticRemoteRun(t)
	j.RemoteCleanup = "teardown"                  // the issue closed; cleanup owns the run
	h := canonicalFixtureHost(t, "deadline", nil) // the daemon never confirms the stop
	h.Name = j.Host
	c.hosts[j.Host] = h
	closes := 0
	c.actions.close = func(context.Context, Host, string) error { closes++; return nil }
	if c.st.save() != nil {
		t.Fatal("state fixture unavailable")
	}
	restart := func() {
		c.st = loadState(c.st.path)
		j = c.st.Jobs[7]
		if j == nil {
			t.Fatal("restart lost the retained run")
		}
	}
	// Age the last persisted miss so the next tick may count again.
	age := func() {
		raw, err := os.ReadFile(c.st.path)
		var doc map[string]any
		if err != nil || json.Unmarshal(raw, &doc) != nil {
			t.Fatal("state unreadable")
		}
		doc["jobs"].(map[string]any)["7"].(map[string]any)["last_stop_miss_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
		raw, _ = json.Marshal(doc)
		if os.WriteFile(c.st.path, raw, 0600) != nil {
			t.Fatal("state unwritable")
		}
	}
	tick := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		c.superviseRemoteCleanup(ctx, h, j)
	}
	released := 0
	for attempt := 1; attempt <= 10; attempt++ {
		restart()
		tick()
		if c.st.Jobs[7] == nil {
			released = attempt
			break
		}
		tick()
		if c.st.Jobs[7] == nil {
			t.Fatal("an event burst exhausted the stop bound")
		}
		age()
	}
	if released < 2 || released > 5 {
		t.Fatalf("unconfirmable stop ended the run at attempt %d, want a bounded retry within 5", released)
	}
	if closes != 0 {
		t.Fatal("an unconfirmed stop closed the workspace")
	}
	if loadState(c.st.path).Jobs[7] != nil {
		t.Fatal("released capacity returned after restart")
	}
}

// A verified completion whose cleanup never observes process absence ends
// under the same bound: its seat is freed, while its unobserved completion
// fence remains so the run cannot relaunch, across restarts.
func TestUnconfirmableCompletionCleanupEndsRunAfterBoundAcrossRestart(t *testing.T) {
	c, j, seatGone, _, closes := completionFixture(t)
	ref := completionRef(j)
	if !c.retireCompleted(context.Background(), j.Issue, j, ref, true) || *closes != 1 {
		t.Fatal("verified completion did not fence and close")
	}
	*seatGone = true // the process absence is never observed
	n := j.Issue
	age := func() {
		raw, err := os.ReadFile(c.st.path)
		var doc map[string]any
		if err != nil || json.Unmarshal(raw, &doc) != nil {
			t.Fatal("state unreadable")
		}
		jobs := doc["jobs"].(map[string]any)
		for key := range jobs {
			jobs[key].(map[string]any)["last_stop_miss_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339Nano)
		}
		raw, _ = json.Marshal(doc)
		if os.WriteFile(c.st.path, raw, 0600) != nil {
			t.Fatal("state unwritable")
		}
	}
	released := 0
	for attempt := 1; attempt <= 10; attempt++ {
		age()
		c.st = loadState(c.st.path)
		if j = c.st.Jobs[n]; j == nil {
			t.Fatal("restart lost the retained run")
		}
		c.completionPass(context.Background(), map[int]agentRef{n: ref})
		if c.st.Jobs[n] == nil {
			released = attempt
			break
		}
		c.completionPass(context.Background(), map[int]agentRef{n: ref})
		if c.st.Jobs[n] == nil {
			t.Fatal("an event burst exhausted the cleanup bound")
		}
	}
	if released < 2 || released > 6 {
		t.Fatalf("unconfirmable completion cleanup ended the run at attempt %d, want a bounded retry", released)
	}
	c.st = loadState(c.st.path)
	fence, fenced := c.st.CompletedRuns[n]
	if c.st.Jobs[n] != nil || !fenced || fence.Phase == "observed" || *closes != 1 {
		t.Fatalf("bounded release lost its fence or claimed absence: fenced=%v phase=%s closes=%d", fenced, fence.Phase, *closes)
	}
	if c.activeByAccount()[j.Agent] != 0 {
		t.Fatal("released completion still counts against the governor")
	}
	if c.st.reserveLaunch(n) {
		t.Fatal("released completion relaunched")
	}
}
