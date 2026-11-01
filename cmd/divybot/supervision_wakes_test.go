package main

import (
	"context"
	"testing"
	"time"
)

func supervisionWakeFixture(t *testing.T, timing supervisionWakeTiming) (*supervisionWakes, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	fleet := newHerdrFleet(ctx, nil, timing)
	w := fleet.wakes
	t.Cleanup(func() { cancel(); <-w.done })
	return w, fleet.wake
}

func takeSupervisionWake(t *testing.T, out <-chan struct{}) {
	t.Helper()
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("pending native invalidation was lost")
	}
}

func TestSupervisionWakeDebounce(t *testing.T) {
	debounce := 80 * time.Millisecond
	w, out := supervisionWakeFixture(t, supervisionWakeTiming{debounce: debounce})
	started := time.Now()
	w.githubChanged()
	takeSupervisionWake(t, out)
	if time.Since(started) < debounce {
		t.Fatal("GitHub handoff bypassed the debounce window")
	}
}

func TestSupervisionWakeSpacingStartsAtPickup(t *testing.T) {
	spacing := 80 * time.Millisecond
	w, out := supervisionWakeFixture(t, supervisionWakeTiming{debounce: time.Millisecond, minSpacing: spacing})
	w.githubChanged()
	// A busy receiver must not consume the cooldown while its wake is merely pending.
	<-time.After(3 * spacing)
	takeSupervisionWake(t, out)
	w.githubChanged()
	select {
	case <-out:
		t.Fatal("buffered publication time replaced actual pickup spacing")
	case <-time.After(spacing / 2):
	}
	takeSupervisionWake(t, out)
}

func TestSupervisionWakeBurstDoesNotStarve(t *testing.T) {
	w, out := supervisionWakeFixture(t, supervisionWakeTiming{debounce: 10 * time.Millisecond, minSpacing: 50 * time.Millisecond})
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			w.githubChanged()
			select {
			case <-stop:
				return
			case <-tick.C:
			}
		}
	}()
	defer func() { close(stop); <-done }()
	select {
	case <-out:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("sustained native events extended debounce indefinitely")
	}
}

func TestSupervisionWakeFleetPathRemainsImmediate(t *testing.T) {
	w, out := supervisionWakeFixture(t, supervisionWakeTiming{debounce: time.Hour, minSpacing: time.Hour})
	for range 3 {
		w.fleetChanged()
		takeSupervisionWake(t, out)
	}
}

func TestSupervisionWakeCancellationWithBusyOwner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan struct{})
	w := newSupervisionWakes(ctx, out, supervisionWakeTiming{})
	w.githubChanged()
	w.fleetChanged()
	cancel()
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("a blocked handoff prevented broker shutdown")
	}
}
