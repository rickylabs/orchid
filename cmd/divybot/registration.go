package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

var errAgentRegistration = errors.New("agent_registration_failed")

type launchBlock struct {
	Reason   string `json:"reason"`
	Notified bool   `json:"notified,omitempty"`
}

// reserveLaunch is a one-attempt ceiling. A crash/timeout cannot license another
// spawn; only a fully registered, durably tracked job releases this reservation.
func (s *State) reserveLaunch(n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LaunchBlocks == nil {
		s.LaunchBlocks = map[int]launchBlock{}
	}
	if _, blocked := s.LaunchBlocks[n]; blocked {
		return false
	}
	s.LaunchBlocks[n] = launchBlock{Reason: "registration_incomplete"}
	return s.saveLocked() == nil
}

func (s *State) blockLaunch(n int, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LaunchBlocks == nil {
		s.LaunchBlocks = map[int]launchBlock{}
	}
	prior := s.LaunchBlocks[n]
	s.LaunchBlocks[n] = launchBlock{Reason: reason, Notified: prior.Notified}
	// The pre-effect reservation remains on disk if this refinement cannot be saved.
	if s.saveLocked() != nil {
		log.Printf("issue #%d: launch remains fenced; reason persistence failed", n)
	}
}

// reportBlockedLaunch retries only the explanatory comment, never the launch.
// A crash after posting may repeat the comment; it cannot repeat the agent effect.
func (c *Coord) reportBlockedLaunch(ctx context.Context, n int) bool {
	c.st.mu.Lock()
	block, blocked := c.st.LaunchBlocks[n]
	c.st.mu.Unlock()
	if !blocked || block.Notified || c.dry {
		return blocked
	}
	// Keep native handles, paths, process output and credentials out of the issue.
	reason := "registration_incomplete"
	switch block.Reason {
	case "registration_failed", "agent_disappeared":
		reason = block.Reason
	}
	body := fmt.Sprintf("divybot: automatic launch abandoned (%s). The agent was not confirmed ready, or disappeared after supervision. No automatic respawn will occur for this inbox issue, including after dispatcher restart. Inspect the failed launch before requesting a new dispatch in a new inbox issue.", reason)
	commentCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := run(commentCtx, "gh", "issue", "comment", fmt.Sprint(n), "--repo", c.cfg.Inbox, "--body", body); err != nil {
		log.Printf("issue #%d: launch abandonment comment unavailable; will retry comment only", n)
		return true
	}
	c.st.mu.Lock()
	block.Notified = true
	c.st.LaunchBlocks[n] = block
	_ = c.st.saveLocked()
	c.st.mu.Unlock()
	return true
}
