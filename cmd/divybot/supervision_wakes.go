package main

import (
	"context"
	"time"
)

const (
	githubWakeDebounce   = 250 * time.Millisecond
	githubWakeMinSpacing = 30 * time.Second
)

type supervisionWakeTiming struct {
	debounce, minSpacing time.Duration
}

// Two bounded producer bits feed the existing owner. Only the owner runs ticks.
type supervisionWakes struct {
	fleet, github chan struct{}
	done          chan struct{}
}

func newSupervisionWakes(ctx context.Context, out chan<- struct{}, timing supervisionWakeTiming) *supervisionWakes {
	w := &supervisionWakes{fleet: make(chan struct{}, 1), github: make(chan struct{}, 1), done: make(chan struct{})}
	go w.run(ctx, out, timing)
	return w
}

func (w *supervisionWakes) fleetChanged()  { coalesceSupervisionWake(w.fleet) }
func (w *supervisionWakes) githubChanged() { coalesceSupervisionWake(w.github) }

func coalesceSupervisionWake(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (w *supervisionWakes) run(ctx context.Context, out chan<- struct{}, timing supervisionWakeTiming) {
	defer close(w.done)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	var lastHandoff, eligible time.Time
	var fleetDirty, githubDirty bool
	for ctx.Err() == nil {
		var deliver chan<- struct{}
		var ready <-chan time.Time
		if fleetDirty || (githubDirty && !time.Now().Before(eligible)) {
			deliver = out
		} else if githubDirty {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(time.Until(eligible))
			ready = timer.C
		}
		select {
		case <-ctx.Done():
			return
		case <-w.fleet:
			fleetDirty = true // native runtime observations retain their immediate path
		case <-w.github:
			if !githubDirty {
				eligible = time.Now().Add(timing.debounce)
				if spaced := lastHandoff.Add(timing.minSpacing); spaced.After(eligible) {
					eligible = spaced
				}
			}
			githubDirty = true // leading debounce cannot be extended by an endless burst
		case <-ready:
		case deliver <- struct{}{}:
			// out is unbuffered: pacing starts at actual receive handoff, not earlier publication.
			lastHandoff = time.Now()
			fleetDirty, githubDirty = false, false
		}
	}
}

func newHerdrFleet(ctx context.Context, hosts map[string]Host, timing supervisionWakeTiming) *herdrFleet {
	f := &herdrFleet{hosts: hosts, agents: map[string][]AgentInfo{}, up: map[string]bool{}, wake: make(chan struct{})}
	f.wakes = newSupervisionWakes(ctx, f.wake, timing)
	return f
}
