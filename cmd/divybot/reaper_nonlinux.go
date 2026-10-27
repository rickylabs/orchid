//go:build !linux

package main

// Production orphan adoption runs only in the Linux PID namespace.
func (g *childRegistry) reapOrphans() int { return 0 }
