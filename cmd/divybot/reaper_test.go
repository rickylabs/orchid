package main

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReaperExcludesOnlyOwnedPIDs(t *testing.T) {
	g := childRegistry{owned: map[int]*ownedCommand{7: {}}}
	var waited []int
	n := g.reapWith(func() []int { return []int{-1, 0, 7, 8, 9} }, func(pid int) bool {
		waited = append(waited, pid)
		return pid == 8
	})
	if n != 1 || len(waited) != 2 || waited[0] != 8 || waited[1] != 9 {
		t.Fatal("sweep must skip owned PIDs while collecting unrelated orphans")
	}
}

func TestReaperStartIsFencedDuringCollection(t *testing.T) {
	var g childRegistry
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := g.commandContext(ctx, "sh", "-c", "exit 0")
	started := make(chan error, 1)
	g.reapWith(func() []int { return []int{7} }, func(int) bool {
		if g.mu.TryLock() {
			g.mu.Unlock()
			t.Error("collector ran without excluding starts")
		}
		go func() { started <- cmd.Start() }()
		select {
		case <-started:
			t.Error("command started during a sweep")
		case <-time.After(30 * time.Millisecond):
		}
		return false
	})
	if err := <-started; err != nil {
		t.Fatal("command did not start after the sweep")
	}
	if cmd.Wait() != nil {
		t.Fatal("command did not retain its wait status")
	}
}

func TestReaperOwnedCommandLifecycle(t *testing.T) {
	for _, method := range []string{"Run", "Output", "CombinedOutput", "StartWait"} {
		t.Run(method, func(t *testing.T) {
			var g childRegistry
			cmd := g.commandContext(context.Background(), "sh", "-c", "printf fixture; exit 23")
			var err error
			var out []byte
			switch method {
			case "Run":
				err = cmd.Run()
			case "Output":
				out, err = cmd.Output()
			case "CombinedOutput":
				out, err = cmd.CombinedOutput()
			case "StartWait":
				if cmd.Start() != nil {
					t.Fatal("fixture start failed")
				}
				g.mu.Lock()
				registered := g.owned[cmd.Process.Pid] == cmd
				g.mu.Unlock()
				if !registered {
					t.Error("started command was not registered")
				}
				err = cmd.Wait()
			}
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 23 || cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
				t.Fatal("owned command did not wait and preserve its exit status")
			}
			if method == "Output" || method == "CombinedOutput" {
				if string(out) != "fixture" {
					t.Fatal("owned command did not finish output copying")
				}
			}
			if len(g.owned) != 0 {
				t.Fatal("waited command retained ownership")
			}
		})
	}
}

func TestReaperOwnedCommandFailureAndCancellation(t *testing.T) {
	var g childRegistry
	missing := g.commandContext(context.Background(), "orchid-missing-fixture-command")
	if missing.Run() == nil || len(g.owned) != 0 {
		t.Fatal("failed start retained ownership")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := g.commandContext(ctx, "sh", "-c", "read value")
	in, err := cmd.StdinPipe()
	if err != nil || cmd.Start() != nil {
		t.Fatal("cancellation fixture start failed")
	}
	cancel()
	in.Close()
	if cmd.Wait() == nil || cmd.ProcessState == nil || len(g.owned) != 0 {
		t.Fatal("cancelled command did not wait and release ownership")
	}
}

func TestReaperOwnedPipeReadsBeforeWait(t *testing.T) {
	var g childRegistry
	cmd := g.commandContext(context.Background(), "sh", "-c", "printf 'fixture output'")
	out, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		t.Fatal("pipe fixture start failed")
	}
	body, err := io.ReadAll(out)
	if err != nil || string(body) != "fixture output" || cmd.Wait() != nil || len(g.owned) != 0 {
		t.Fatal("pipe fixture did not read output before waiting")
	}
}

func TestReaperConcurrentOwnedCommands(t *testing.T) {
	var g childRegistry
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				g.reapOrphans()
				time.Sleep(time.Millisecond)
			}
		}
	}()
	defer func() { close(stop); <-done }()
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := g.commandContext(context.Background(), "sh", "-c", "printf fixture")
			out, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "fixture" {
				t.Error("concurrent command lost output or exit status")
			}
		}()
	}
	wg.Wait()
	if len(g.owned) != 0 {
		t.Fatal("concurrent waits retained ownership")
	}
}

func TestReaperOnlyEnabledAtPIDOne(t *testing.T) {
	for _, pid := range []int{-1, 0, 1, 2, 42} {
		if reaperEnabled(pid) != (pid == 1) {
			t.Fatal("reaper activation must be confined to namespace init")
		}
	}
}
