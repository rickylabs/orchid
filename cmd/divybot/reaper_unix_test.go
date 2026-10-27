//go:build unix

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestReaperWaitContract(t *testing.T) {
	calls := 0
	got := collectExited(7, func(pid int, status *syscall.WaitStatus, options int, usage *syscall.Rusage) (int, error) {
		if pid != 7 || options != syscall.WNOHANG || status == nil || usage != nil {
			t.Fatal("collector must wait nonblocking for one positive child PID")
		}
		calls++
		if calls == 1 {
			return -1, syscall.EINTR
		}
		if calls > 2 {
			t.Fatal("collector must stop after collecting its candidate")
		}
		return 7, nil
	})
	if !got || calls != 2 {
		t.Fatal("collector must retry interruptions and collect the candidate")
	}
	for _, result := range []struct {
		pid int
		err error
	}{
		{0, nil}, {-1, syscall.ECHILD}, {-1, syscall.EINVAL},
	} {
		calls = 0
		if collectExited(7, func(int, *syscall.WaitStatus, int, *syscall.Rusage) (int, error) {
			calls++
			return result.pid, result.err
		}) || calls != 1 {
			t.Fatal("a live child or a wait error must not count as collected")
		}
	}
	for _, pid := range []int{-1, 0} {
		if collectExited(pid, func(int, *syscall.WaitStatus, int, *syscall.Rusage) (int, error) {
			t.Fatal("nonpositive PID reached wait4")
			return 0, nil
		}) {
			t.Fatal("invalid PID counted as collected")
		}
	}
}

func TestReaperSignalBackstopAndStop(t *testing.T) {
	stop := make(chan struct{})
	sigs := make(chan os.Signal)
	ticks := make(chan time.Time)
	collected := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		reapUntilStopped(stop, sigs, ticks, func() int { collected <- struct{}{}; return 0 })
	}()
	send := func(signal bool) {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		if signal {
			select {
			case sigs <- syscall.SIGCHLD:
			case <-timer.C:
				t.Fatal("signal wake was ignored")
			}
		} else {
			select {
			case ticks <- time.Time{}:
			case <-timer.C:
				t.Fatal("backstop wake was ignored")
			}
		}
		select {
		case <-collected:
		case <-timer.C:
			t.Fatal("wake did not collect")
		}
	}
	send(true)
	send(false)
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reaper did not stop")
	}
	select {
	case <-collected:
		t.Fatal("stop must not collect")
	default:
	}
}
