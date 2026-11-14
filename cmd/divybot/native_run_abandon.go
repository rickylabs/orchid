package main

import (
	"context"
	"time"
)

// Ordinary seats own their workspace. A missing agent row merely selects a
// candidate; the native inventory must prove both workspace and label absent.
// Shared-daemon jobs retain the stronger owned-tree reaper instead.
func (c *Coord) abandonMissingNativeSeat(ctx context.Context, j *Job) bool {
	c.st.mu.Lock()
	if j == nil || j.RemoteControl != nil || c.st.Jobs[j.Issue] != j {
		c.st.mu.Unlock()
		return false
	}
	copy := *j
	record := c.st.nativeRunRecordLocked(j.Issue, j, c.cfg.Inbox)
	if record == nil {
		c.st.mu.Unlock()
		return false // unknown native identity is never a terminal association
	}
	revision := nativeEndRevision(*record, j)
	c.st.mu.Unlock()
	h, ok := c.hosts[copy.Host]
	if !ok {
		return false
	}
	check, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if !h.forJob(&copy).remoteSeatAbsent(check, &copy) {
		return false
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	current := c.st.nativeRunRecordLocked(j.Issue, c.st.Jobs[j.Issue], c.cfg.Inbox)
	if c.st.Jobs[j.Issue] != j || current == nil || nativeEndRevision(*current, j) != revision || check.Err() != nil {
		return false
	}
	if err := c.recordNativeEndLocked(j.Issue, j, "ended_unobserved"); err != nil {
		return false
	}
	delete(c.st.Jobs, j.Issue)
	if c.st.saveLocked() != nil {
		c.st.Jobs[j.Issue] = j
		return false
	}
	// The terminal commit already fenced launch. No close-by-label call can
	// accidentally tear down a different workspace created after this read.
	return true
}
