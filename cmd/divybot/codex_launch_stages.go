package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Per-launch stage evidence: monotonic durations between fixed stage marks
// and bounded counters, logged once per launch. It carries no identifiers,
// paths, native text or usage, never decides anything, and is never asserted
// on wall-clock values by tests. It exists so an attachment or acceptance
// improvement can be claimed from measured stages, not from end-to-end time.

const (
	stageMarkThreadPrepared = "thread-prepared"
	stageMarkRegistered     = "registered"
	stageMarkAttached       = "attachment-proven"
	stageMarkRemoteProof    = "remote-proof"
	stageMarkDispatched     = "dispatched"
	stageMarkGoalProof      = "goal-proof"
	stageMarkReady          = "ready"
	stageMarkSubscribed     = "subscribed"
	stageMarkBaseline       = "baseline"
	stageMarkAttempted      = "attempted"
	stageMarkSubmitAcked    = "submit-acked"
	stageMarkFirstTurn      = "first-turn-signal"
	stageMarkAccepted       = "accepted"
	stageMarkCommitted      = "committed"

	stageCountAttachmentReads = "attachment-reads"
	stageCountPageReads       = "page-reads"
	stageCountWakes           = "wakes"
	stageCountSignals         = "signals"
	stageCountDuplicates      = "duplicate-signals"

	stageMaxMarks = 32
)

type stageMark struct {
	name  string
	since time.Duration // since the previous mark (or the clock start)
}

// launchStageClock is shared by pointer across one launch; nil is a no-op.
type launchStageClock struct {
	mu     sync.Mutex
	now    func() time.Time
	start  time.Time
	last   time.Time
	marks  []stageMark
	seen   map[string]bool
	counts map[string]int
}

func newLaunchStageClock(now func() time.Time) *launchStageClock {
	if now == nil {
		now = time.Now
	}
	t := now()
	return &launchStageClock{now: now, start: t, last: t, seen: map[string]bool{}, counts: map[string]int{}}
}

// mark records the first arrival at a stage; repeats are ignored.
func (c *launchStageClock) mark(name string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[name] || len(c.marks) >= stageMaxMarks {
		return
	}
	t := c.now()
	c.seen[name] = true
	c.marks = append(c.marks, stageMark{name, t.Sub(c.last)})
	c.last = t
}

func (c *launchStageClock) count(name string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.counts[name]++
	c.mu.Unlock()
}

// summary is the one log line: stage deltas in order, total, then counters.
func (c *launchStageClock) summary() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.marks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(c.marks)+len(c.counts)+1)
	for _, m := range c.marks {
		parts = append(parts, fmt.Sprintf("%s=%dms", m.name, m.since.Milliseconds()))
	}
	parts = append(parts, fmt.Sprintf("total=%dms", c.last.Sub(c.start).Milliseconds()))
	for _, name := range []string{stageCountAttachmentReads, stageCountPageReads, stageCountWakes, stageCountSignals, stageCountDuplicates} {
		if n := c.counts[name]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", name, n))
		}
	}
	return strings.Join(parts, " ")
}
