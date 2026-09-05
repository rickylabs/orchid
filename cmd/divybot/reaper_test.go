package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The property under test is not "zombies get reaped" — wait4 does that, and a test cannot fake a
// pid namespace. It is the gate: the reaper must never call wait4 while os/exec is waiting on a
// child of ours, because wait4(-1) would collect that child and leave the Wait to fail with
// ECHILD. That failure mode looks like gh randomly dying, so it is the part worth pinning down.

func TestReapWithWaitsForOutstandingSpawns(t *testing.T) {
	var g childGate
	release := g.hold()

	collected := false
	if n := g.reapWith(func() int { collected = true; return 7 }); n != 0 {
		t.Fatalf("reaped %d while a spawn was outstanding, want 0", n)
	}
	if collected {
		t.Fatal("called wait4 while a spawn was outstanding — this is the ECHILD bug")
	}

	release()
	if n := g.reapWith(func() int { collected = true; return 7 }); n != 7 {
		t.Fatalf("reaped %d after the spawn released, want 7", n)
	}
	if !collected {
		t.Fatal("never collected anything once the gate was clear")
	}
}

func TestReapWithHonoursNestedHolds(t *testing.T) {
	// refreshLocal holds and then calls resolveGHToken, which holds again. The inner release must
	// not open the gate while the outer spawn is still being waited on.
	var g childGate
	outer := g.hold()
	inner := g.hold()

	inner()
	if n := g.reapWith(func() int { return 1 }); n != 0 {
		t.Fatal("the inner release opened the gate while the outer spawn was still running")
	}

	outer()
	if n := g.reapWith(func() int { return 1 }); n != 1 {
		t.Fatal("the gate stayed shut after every hold released")
	}
}

func TestNoSpawnStartsWhileCollecting(t *testing.T) {
	// The other half of the race: a spawn that begins mid-collect would be forked into a window
	// where wait4(-1) is already running and could take it. Holding the mutex across the whole
	// collect is what prevents that, so assert a hold really does block until collect returns.
	var g childGate
	var tookHold atomic.Bool
	blocked := make(chan struct{})

	g.reapWith(func() int {
		go func() {
			close(blocked)
			release := g.hold()
			tookHold.Store(true)
			release()
		}()
		<-blocked
		// The goroutine is running and wants the lock. If the gate were unlocked during collect it
		// would have it within microseconds; this window is orders of magnitude more than enough.
		time.Sleep(50 * time.Millisecond)
		if tookHold.Load() {
			t.Error("a spawn took a hold while the reaper was mid-collect")
		}
		return 0
	})

	// And it must not be blocked forever either — the hold has to land once collect is done.
	deadline := time.Now().Add(2 * time.Second)
	for !tookHold.Load() {
		if time.Now().After(deadline) {
			t.Fatal("a spawn never got its hold after the reap finished")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHoldCountIsRaceFree(t *testing.T) {
	// -race turns this into the real assertion; the count check catches a lost update either way.
	var g childGate
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := g.hold()
			release()
		}()
	}
	wg.Wait()

	if n := g.reapWith(func() int { return 42 }); n != 42 {
		t.Fatalf("gate did not settle back to open after balanced holds (count=%d)", g.count)
	}
}
