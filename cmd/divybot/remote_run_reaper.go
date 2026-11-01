package main

import (
	"context"
	"log"
	"reflect"
	"sort"
	"time"
)

const remoteReapPerTick = 2

// A missing agent row is not a missing workspace. Inspect the complete native
// workspace inventory for both the captured workspace and its launch label.
func (h Host) remoteSeatAbsent(ctx context.Context, j *Job) bool {
	if j.Workspace == "" || j.Label == "" {
		return false
	}
	out, err := h.herdr(ctx, "workspace", "list")
	if err != nil {
		return false
	}
	raw, err := herdrUnwrap(out)
	var rows struct {
		Type       string `json:"type"`
		Workspaces []struct {
			ID    string `json:"workspace_id"`
			Label string `json:"label"`
		} `json:"workspaces"`
	}
	if err != nil || len(raw) > 1024*1024 || decodeNativeJSON(raw, &rows) != nil || rows.Type != "workspace_list" || rows.Workspaces == nil {
		return false
	}
	for _, ws := range rows.Workspaces {
		if ws.ID == "" || ws.ID == j.Workspace || ws.Label == j.Label {
			return false
		}
	}
	return ctx.Err() == nil
}

// Idle alone cannot retire daemon work: root goal and the full owned tree must
// be inactive, then archive must observe every exact thread unloaded.
func (p *goalRPC) releaseInactiveRemote(reserve func() bool) error {
	children, err := p.remoteChildren()
	if err != nil {
		return err
	}
	if !p.remoteResourcesAbsent(children) {
		goal, err := p.get()
		if err != nil || (goal != nil && goal.Status != "complete" && goal.Status != "paused") {
			return goalError("remote-control-work-unconfirmed")
		}
		if err := p.remoteWorkIdle(true); err != nil {
			return err
		}
		current, err := p.get()
		if err != nil || !reflect.DeepEqual(goal, current) {
			return goalError("remote-control-work-unconfirmed")
		}
	}
	if reserve != nil && !reserve() {
		return goalError("remote-control-reap-fence-unconfirmed")
	}
	return p.releaseRemoteResources()
}

// Shared proof for disappearance and closed-inbox teardown. This path never
// captures a TUI process tree or treats an unreadable runtime as inactive.
func (c *Coord) releaseMissingRemote(ctx context.Context, h Host, j *Job, reserve func() bool) string {
	if j == nil || j.RemoteControl == nil || !h.remoteSeatAbsent(ctx, j) {
		return ""
	}
	before, id, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, j.Agent)
	if err != nil || id != j.RemoteControl.NativeSessionID {
		return ""
	}
	proof := ""
	switch j.Agent {
	case "codex":
		err = h.withCanonicalConnection(ctx, id, func(p *goalRPC) error { return p.releaseInactiveRemote(reserve) })
		if err == nil {
			proof = "codex-native-tree-unloaded"
		}
	case "claude":
		if h.claudeSessionEnded(ctx, j) && (reserve == nil || reserve()) {
			proof = "claude-native-session-ended"
		}
	}
	after, current, bindingErr := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, j.Agent)
	if proof == "" || bindingErr != nil || current != id || !reflect.DeepEqual(before.dispatch, after.dispatch) ||
		!h.remoteSeatAbsent(ctx, j) || ctx.Err() != nil {
		return ""
	}
	return "herdr-workspace-and-label-absent," + proof
}

// Called once per tick with its existing fleet snapshot. Unknown observations
// are retained indefinitely; retry bounds never turn uncertainty into proof.
func (c *Coord) reapRemoteRuns(ctx context.Context, status map[int]agentRef, up map[string]bool, allOpen map[int]bool) {
	if c.dry {
		return
	}
	c.st.mu.Lock()
	var candidates []int
	for n, j := range c.st.Jobs {
		if j.RemoteControl == nil || j.RunMode || j.RemoteCleanup != "" || j.RemoteStopOperation != "" || capacityRecoveryOwns(j) {
			continue
		}
		if _, completing := c.st.CompletedRuns[n]; completing {
			continue
		}
		if ref, known := status[n]; known {
			if jobRefMatches(ref, j) {
				j.Misses, j.LastMissAt = 0, time.Time{}
			}
			continue
		}
		h, ok := c.hosts[j.Host]
		if !ok || !up[h.forJob(j).observerKey()] || !allOpen[n] || time.Since(j.SpawnedAt) < 3*time.Minute || time.Since(j.LastMissAt) < 30*time.Second {
			continue
		}
		candidates = append(candidates, n)
	}
	c.st.mu.Unlock()
	sort.Ints(candidates)
	if len(candidates) == 0 {
		return
	}
	start := int(time.Now().Unix()/30) % len(candidates)
	for i := 0; i < len(candidates) && i < remoteReapPerTick && ctx.Err() == nil; i++ {
		n := candidates[(start+i)%len(candidates)]
		c.st.mu.Lock()
		j := c.st.Jobs[n]
		c.st.mu.Unlock()
		if j == nil {
			continue
		}
		h := c.hosts[j.Host].forJob(j)
		check, cancel := context.WithTimeout(ctx, runCapacityReadBudget)
		if !h.remoteSeatAbsent(check, j) {
			c.st.mu.Lock()
			j.Misses, j.LastMissAt = 0, time.Time{}
			c.st.mu.Unlock()
			cancel()
			continue
		}
		c.st.mu.Lock()
		j.LastMissAt, j.Misses = time.Now(), j.Misses+1
		ready := c.st.saveLocked() == nil && j.Misses >= deadMissThreshold
		c.st.mu.Unlock()
		if !ready {
			cancel()
			continue
		}
		var previous launchBlock
		had, reserved := false, false
		reserve := func() bool {
			c.st.mu.Lock()
			defer c.st.mu.Unlock()
			if c.st.Jobs[n] != j {
				return false
			}
			previous, had = c.st.LaunchBlocks[n]
			if c.st.LaunchBlocks == nil {
				c.st.LaunchBlocks = map[int]launchBlock{}
			}
			c.st.LaunchBlocks[n] = launchBlock{Reason: "agent_disappeared"}
			if c.st.saveLocked() != nil {
				if had {
					c.st.LaunchBlocks[n] = previous
				} else {
					delete(c.st.LaunchBlocks, n)
				}
				return false
			}
			reserved = true
			return true
		}
		evidence := c.releaseMissingRemote(check, h, j, reserve)
		cancel()
		c.st.mu.Lock()
		released := evidence != "" && reserved && c.st.Jobs[n] == j
		if released {
			delete(c.st.Jobs, n)
			if c.st.saveLocked() != nil {
				c.st.Jobs[n], released = j, false
			}
		} else if reserved && c.st.Jobs[n] == j {
			current := c.st.LaunchBlocks[n]
			if current == (launchBlock{Reason: "agent_disappeared"}) {
				if had {
					c.st.LaunchBlocks[n] = previous
				} else {
					delete(c.st.LaunchBlocks, n)
				}
				if c.st.saveLocked() != nil {
					c.st.LaunchBlocks[n] = current
				}
			}
		}
		c.st.mu.Unlock()
		if released {
			c.clearRemoteControl(j)
			c.clearClaudeWorking(j)
			log.Printf("issue #%d: capacity released evidence=%s; launch fenced agent_disappeared", n, evidence)
		}
	}
}
