package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
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
	if _, completed := s.CompletedRuns[n]; completed {
		return false
	}
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
	case "registration_failed", "agent_disappeared", "goal-prompt-unconfirmed":
		reason = block.Reason
	case "agy-settings-unreadable", "codex-client-unavailable":
		reason = block.Reason
	case "opencode-empty-answer", "opencode-output-unconfirmed", "opencode-route-mismatch", "opencode-provider-error":
		reason = block.Reason
	}
	body := fmt.Sprintf("divybot: automatic launch abandoned (%s). The agent was not confirmed ready, or disappeared after supervision. No automatic respawn will occur for this inbox issue, including after dispatcher restart. Inspect the failed launch before requesting a new dispatch in a new inbox issue.", reason)
	if reason == string(agySettingsUnreadable) {
		body = "divybot: AGY launch BLOCKED (`agy-settings-unreadable`). Effective settings or onboarding state is unreadable to the launch user. No native workspace or agent was created, and no task was delivered. An operator must repair file ownership or permissions. No automatic respawn will occur for this inbox issue, including after dispatcher restart. Inspect this fenced attempt before explicitly requesting a new dispatch."
	}
	if reason == string(codexClientUnavailable) {
		body = "divybot: Codex launch BLOCKED (`codex-client-unavailable`). No installed Codex client matches the version of the running Codex app-server, so no workspace, thread or agent was created and no task was delivered. An operator must install the matching client or run the app-server on an installed version. No automatic respawn will occur for this inbox issue, including after dispatcher restart. Inspect this fenced attempt before explicitly requesting a new dispatch."
	}
	if reason == "goal-prompt-unconfirmed" {
		body = "divybot: goal delivery BLOCKED (`goal-prompt-unconfirmed`). A registered agent may exist, but its assignment was not confirmed delivered. This issue remains open and needs inspection; automatic prompt replay, idle pokes and timeout-completed closure are disabled, including after restart. Inspect the existing agent before explicitly requesting another attempt."
	}
	if strings.HasPrefix(reason, "opencode-") {
		body = fmt.Sprintf("divybot: OpenCode run BLOCKED (`%s`). Native route/output evidence failed. A completed empty answer is a failure, never a verdict. The issue stays open; automatic prompt replay, PR supervision/merge, idle pokes and timeout-completed closure are disabled, including after restart. Inspect this run before explicitly requesting a new dispatch.", reason)
	}
	commentCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, binding := c.sourceBinding(n); binding {
		body = strings.NewReplacer("for this inbox issue", "for this trigger", "in a new inbox issue", "with a new /swarm comment").Replace(body)
	}
	if err := c.postIssueNotice(commentCtx, n, "blocked", reason, body, postMatrixComment); err != nil {
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
