package main

import "sync"

// divybot runs as pid 1 inside the orchid container, which makes it the reaper of last resort for
// the whole pid namespace: when any process in the container loses its parent, the kernel
// re-parents the orphan onto us. Go collects only the children it started itself — every
// (*exec.Cmd).Run, .Output and .CombinedOutput waits on its own child — so a re-parented orphan is
// waited on by nobody and stays in the process table as a zombie for the life of the daemon,
// holding a pid slot each. On the live coordinator 371 of them accumulated over two days (370 gh,
// one git), every one with PPid 1. That is what this file exists to stop.
//
// The hazard is stealing an exit status. wait4(-1) collects *any* child, so a reaper firing while
// os/exec sits in Wait can take that child first and leave the Wait to fail with ECHILD — turning
// a healthy gh call into "waitid: no child processes" and stopping dispatch. That is a far worse
// bug than the leak. So the reaper never runs while divybot owns a child of its own: every spawn
// takes a hold, and reaping happens under the same mutex, which confines it to the gaps where the
// only children left in the namespace are orphans.

// childGate counts the child processes divybot spawned and is still waiting on.
type childGate struct {
	mu    sync.Mutex
	count int
}

// children guards every exec site in this program. A spawn that does not take a hold here is a
// spawn the reaper may race, so keep this the only way divybot starts a process.
var children childGate

// hold registers a spawn and returns its release. The intended use is one deferred call at the top
// of the spawning function:
//
//	defer children.hold()()
//
// which takes the hold now and releases it when the function — and so the Wait inside it — is done.
// Holds nest: a function that holds and then calls another that holds is counted twice and gated
// until both return.
func (g *childGate) hold() func() {
	g.mu.Lock()
	g.count++
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		g.count--
		g.mu.Unlock()
	}
}

// reapOrphans collects every child that has already exited, but only while divybot is waiting on
// none of its own, and reports how many it took.
func (g *childGate) reapOrphans() int { return g.reapWith(waitAllExited) }

// reapWith is reapOrphans with the collector supplied, so the gating can be tested without forking
// anything. Holding the mutex across the whole collect is the point of the design: it stops a
// spawn from starting mid-reap and being collected before its own Wait ever sees it.
func (g *childGate) reapWith(collect func() int) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.count != 0 {
		return 0
	}
	return collect()
}
