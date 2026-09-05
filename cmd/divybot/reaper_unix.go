//go:build unix

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// reaperSweep is the backstop interval. Signals coalesce — a burst of exits can arrive as a single
// SIGCHLD — and an orphan that dies while a spawn holds the gate is skipped and has to wait for a
// later pass, so the reaper cannot rely on SIGCHLD alone.
const reaperSweep = 30 * time.Second

// waitAllExited collects every child that has already exited and reports how many. It never
// blocks: WNOHANG makes wait4 return immediately once the remaining children are still running.
//
// Call it only through childGate.reapOrphans, which is what keeps it off the children os/exec is
// waiting on.
func waitAllExited() int {
	n := 0
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err == syscall.EINTR {
			continue
		}
		// pid 0: children exist, none has exited yet. pid -1 with ECHILD: no children at all.
		if pid <= 0 || err != nil {
			return n
		}
		n++
	}
}

// startReaper installs the orphan reaper and stops it when stop closes. It does nothing at all
// unless divybot is pid 1 — off pid 1 (a laptop run, a test, a container started with an init)
// orphans go to a real init and there is nothing here to collect.
func startReaper(stop <-chan struct{}) {
	if os.Getpid() != 1 {
		return
	}
	log.Printf("reaper: divybot is pid 1 — collecting orphaned processes every %s", reaperSweep)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGCHLD)

	go func() {
		defer signal.Stop(sigs)
		tick := time.NewTicker(reaperSweep)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-sigs:
			case <-tick.C:
			}
			if n := children.reapOrphans(); n > 0 {
				log.Printf("reaper: collected %d orphaned process(es)", n)
			}
		}
	}()
}
