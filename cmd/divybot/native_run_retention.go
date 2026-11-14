package main

import (
	"sort"
	"strconv"
	"time"
)

const defaultNativeTerminalLimit = 1000

func (c *Config) nativeTerminalRetentionLimit() int {
	if c.NativeTerminalLimit > 0 {
		return c.NativeTerminalLimit
	}
	return defaultNativeTerminalLimit
}

type nativeTerminalPrune struct {
	facts  map[string]nativeTerminalFact
	keys   map[int]string
	blocks map[int]launchBlock
}

// Caller holds State.mu. Prune before encoding so every successful save bounds
// the hot state file, including the first save after loading older state.
// Tracked jobs are pinned, even when their number exceeds the configured limit.
func (s *State) pruneNativeTerminalsLocked() *nativeTerminalPrune {
	limit := s.nativeTerminalLimit
	if limit <= 0 {
		limit = defaultNativeTerminalLimit
	}
	excess := len(s.NativeTerminals) - limit
	if excess <= 0 {
		return nil
	}
	type candidate struct {
		key string
		at  time.Time
	}
	var candidates []candidate
	for key, fact := range s.NativeTerminals {
		n, _ := strconv.Atoi(fact.Record.Key)
		if j := s.Jobs[n]; j != nil && j.DispatchKey == key { // guard:terminal-retention-tracked
			continue
		}
		at, _ := time.Parse(time.RFC3339Nano, fact.Record.CompletedAt)
		candidates = append(candidates, candidate{key, at})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].at.Equal(candidates[j].at) {
			return candidates[i].key < candidates[j].key
		}
		return candidates[i].at.Before(candidates[j].at)
	})
	pruned := &nativeTerminalPrune{facts: map[string]nativeTerminalFact{}, keys: map[int]string{}, blocks: map[int]launchBlock{}}
	for i := 0; i < len(candidates) && i < excess; i++ {
		key := candidates[i].key
		pruned.facts[key] = s.NativeTerminals[key]
		delete(s.NativeTerminals, key)
	}
	// An older fact must not remove a newer assignment's index or block.
	for n, key := range s.NativeTerminalKeys {
		if _, removed := pruned.facts[key]; !removed {
			continue
		}
		pruned.keys[n] = key
		delete(s.NativeTerminalKeys, n)
		if block := s.LaunchBlocks[n]; block.Reason == "assignment_ended" {
			pruned.blocks[n] = block
			delete(s.LaunchBlocks, n)
		}
	}
	return pruned
}

func (s *State) restoreNativeTerminalPruneLocked(pruned *nativeTerminalPrune) {
	if pruned == nil {
		return
	}
	for key, fact := range pruned.facts {
		s.NativeTerminals[key] = fact
	}
	for n, key := range pruned.keys {
		s.NativeTerminalKeys[n] = key
	}
	for n, block := range pruned.blocks {
		s.LaunchBlocks[n] = block
	}
}
