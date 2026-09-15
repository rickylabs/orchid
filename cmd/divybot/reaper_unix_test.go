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
	got := collectExited(func(pid int, status *syscall.WaitStatus, options int, usage *syscall.Rusage) (int, error) {
		if pid != -1 || options != syscall.WNOHANG || status == nil || usage != nil {
			t.Fatal("collector must wait nonblocking for exited children only")
		}
		calls++
		switch calls {
		case 1:
			return -1, syscall.EINTR
		case 2, 3:
			return 7, nil
		case 4:
			return 0, nil
		default:
			t.Fatal("collector did not stop when only live children remained")
			return 0, nil
		}
	})
	if got != 2 || calls != 4 {
		t.Fatal("collector must retry interruptions and drain every exited child")
	}
	for _, errno := range []error{syscall.ECHILD, syscall.EINVAL} {
		calls = 0
		got = collectExited(func(int, *syscall.WaitStatus, int, *syscall.Rusage) (int, error) {
			calls++
			if calls > 1 {
				t.Fatal("collector did not stop on a wait error")
			}
			return -1, errno
		})
		if got != 0 {
			t.Fatal("a wait error is not a collected child")
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
