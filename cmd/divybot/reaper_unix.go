//go:build unix

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// reaperSweep retries adoption races and coalesced or missed SIGCHLD notifications.
const reaperSweep = 30 * time.Second

// collectExited waits on one unowned positive PID, never any os/exec-owned child.
func collectExited(pid int, wait func(int, *syscall.WaitStatus, int, *syscall.Rusage) (int, error)) bool {
	if pid <= 0 {
		return false
	}
	for {
		var ws syscall.WaitStatus
		got, err := wait(pid, &ws, syscall.WNOHANG, nil)
		if err == syscall.EINTR {
			continue
		}
		return err == nil && got == pid
	}
}

// startReaper installs the orphan reaper and stops it when stop closes. It does nothing at all
// unless divybot is pid 1 — off pid 1 (a laptop run, a test, a container started with an init)
// orphans go to a real init and there is nothing here to collect.
func startReaper(stop <-chan struct{}) {
	if !reaperEnabled(os.Getpid()) {
		return
	}
	log.Printf("reaper: divybot is pid 1 — collecting orphaned processes every %s", reaperSweep)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGCHLD)

	go func() {
		defer signal.Stop(sigs)
		tick := time.NewTicker(reaperSweep)
		defer tick.Stop()
		reapUntilStopped(stop, sigs, tick.C, children.reapOrphans)
	}()
}

// Signals coalesce; the backstop retries child adoption and enumeration races.
func reapUntilStopped(stop <-chan struct{}, sigs <-chan os.Signal, ticks <-chan time.Time, reap func() int) {
	for {
		select {
		case <-stop:
			return
		case <-sigs:
		case <-ticks:
		}
		if n := reap(); n > 0 {
			log.Printf("reaper: collected %d orphaned process(es)", n)
		}
	}
}
