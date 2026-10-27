package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync"
)

// childRegistry separates os/exec-owned children from adopted orphans. Start and
// PID registration share the sweep mutex; Wait runs outside it and relinquishes
// ownership only after os/exec has collected the child and finished its I/O.
// Long-lived commands therefore never prevent collection of unrelated orphans.
type childRegistry struct {
	mu    sync.Mutex
	owned map[int]*ownedCommand
}

var children childRegistry

// ownedCommand is the sole production subprocess lifecycle. Configure its Cmd
// before Start; finish all StdoutPipe/StderrPipe reads before calling Wait.
// Run, Output and CombinedOutput already wait and must not be waited on again.
type ownedCommand struct {
	*exec.Cmd
	owner *childRegistry
}

func commandContext(ctx context.Context, name string, args ...string) *ownedCommand {
	return children.commandContext(ctx, name, args...)
}

func (g *childRegistry) commandContext(ctx context.Context, name string, args ...string) *ownedCommand {
	return &ownedCommand{Cmd: exec.CommandContext(ctx, name, args...), owner: g}
}

// Start fences the fork-to-registration window against every orphan sweep.
func (c *ownedCommand) Start() error {
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	if err := c.Cmd.Start(); err != nil {
		return err
	}
	if c.owner.owned == nil {
		c.owner.owned = make(map[int]*ownedCommand)
	}
	c.owner.owned[c.Process.Pid] = c
	return nil
}

// Wait preserves os/exec's exit status and pipe-copy completion before release.
func (c *ownedCommand) Wait() error {
	err := c.Cmd.Wait() // guard:owned-wait
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	if c.ProcessState != nil && c.owner.owned[c.Process.Pid] == c {
		delete(c.owner.owned, c.Process.Pid)
	}
	return err
}

func (c *ownedCommand) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

func (c *ownedCommand) Output() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var out bytes.Buffer
	c.Stdout = &out
	// Retain bounded stderr on ExitError, as callers of os/exec.Output expect.
	var diagnostic stderrTail
	capture := c.Stderr == nil
	if capture {
		c.Stderr = &diagnostic
	}
	err := c.Run()
	if exit, ok := err.(*exec.ExitError); capture && ok {
		exit.Stderr = append([]byte(nil), diagnostic.Bytes()...)
	}
	return out.Bytes(), err
}

func (c *ownedCommand) CombinedOutput() ([]byte, error) {
	if c.Stdout != nil || c.Stderr != nil {
		return nil, errors.New("exec: output already set")
	}
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	return out.Bytes(), err
}

// reapWith holds the start fence through enumeration and positive-PID waits.
// A stale candidate cannot become a newly started owned child during a sweep.
func (g *childRegistry) reapWith(list func() []int, collect func(int) bool) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, pid := range list() {
		if pid <= 0 || g.owned[pid] != nil {
			continue
		}
		if collect(pid) {
			n++
		}
	}
	return n
}

// Only namespace init inherits otherwise-unowned processes in production.
func reaperEnabled(pid int) bool { return pid == 1 }
