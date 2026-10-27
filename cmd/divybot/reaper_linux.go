//go:build linux

package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const reaperProcRoot = "/proc"

func (g *childRegistry) reapOrphans() int {
	return g.reapWith(directChildren, func(pid int) bool { return collectExited(pid, syscall.Wait4) })
}

// directChildren scans this PID namespace once, filtering PPID across all Go
// threads. Per-task children lists can omit entries during concurrent exits.
// Disappearing processes are harmless; the signal/backstop retries new adoptions.
func directChildren() []int {
	entries, err := os.ReadDir(reaperProcRoot)
	if err != nil {
		log.Print("reaper: child enumeration unavailable")
		return nil
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		stat, err := os.ReadFile(reaperProcRoot + "/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		if parent, ok := procParent(stat); ok && parent == os.Getpid() {
			pids = append(pids, pid)
		}
	}
	return pids
}

// The command name in stat is parenthesized and may itself contain spaces or ')'.
func procParent(stat []byte) (int, bool) {
	text := string(stat)
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) < 2 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[1])
	return pid, err == nil && pid > 0
}
