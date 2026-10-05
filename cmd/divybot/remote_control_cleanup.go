package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

func (c *Coord) beginRemoteCleanup(j *Job, cause string) bool {
	if j == nil || j.RemoteControl == nil || (cause != "teardown" && cause != "operator-timeout") {
		return false
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	if j.RemoteCleanup != "" {
		return true
	}
	prior, had := c.st.LaunchBlocks[j.Issue]
	if c.st.LaunchBlocks == nil {
		c.st.LaunchBlocks = map[int]launchBlock{}
	}
	j.RemoteCleanup = cause
	if j.RemoteStopOperation == "" {
		c.st.LaunchBlocks[j.Issue] = launchBlock{Reason: "registration_incomplete", Notified: true}
	}
	if c.st.saveLocked() != nil {
		j.RemoteCleanup = ""
		if had {
			c.st.LaunchBlocks[j.Issue] = prior
		} else {
			delete(c.st.LaunchBlocks, j.Issue)
		}
		return false
	}
	return true
}

func (c *Coord) remoteTeardownOccupant(ctx context.Context, h Host, j *Job) bool {
	if j.RemoteControl == nil || j.Agent != "codex" {
		return true
	}
	var intent teardownIntent
	record := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
	if readPrivateActionJSON(filepath.Join(record, "teardown-intent.json"), &intent) == nil && intent.Issue == j.Issue && intent.Host == j.Host && intent.PaneID == j.Pane && intent.WorkspaceID == j.Workspace && intent.DispatchKey == j.DispatchKey && intent.NativeSessionID == j.RemoteControl.NativeSessionID && c.teardownObserveOne(ctx, intent) && ctx.Err() == nil {
		return true
	}
	return c.checkRemoteHook(ctx, h, j)
}

func (c *Coord) superviseRemoteCleanup(ctx context.Context, h Host, j *Job) {
	if c.dry || j.RemoteStopOperation != "" {
		return
	} // action observer owns its paired receipt
	c.clearRemoteControl(j)
	if !c.teardown(ctx, j.Issue, j, j.RemoteCleanup) {
		return
	}
	if j.RemoteCleanup == "operator-timeout" {
		check, cancel := context.WithTimeout(ctx, 20*time.Second)
		var err error
		if _, binding := c.sourceBinding(j.Issue); binding {
			if !c.replySource(check, j.Issue, "stopped", "operator-timeout", "", fmt.Sprintf("⏱️ divybot: operator timeout of %s exceeded; native work stopped; seat and process absence observed. Retry requires a new /swarm comment.", j.Overrides.Timeout)) {
				err = errMatrix
			}
		} else {
			_, err = run(check, "gh", "issue", "close", fmt.Sprint(j.Issue), "--repo", c.cfg.Inbox, "--comment",
				fmt.Sprintf("⏱️ divybot: operator timeout of %s exceeded — native work stopped; seat and process absence observed; issue closed. Retry requires a new inbox issue or /swarm request.", j.Overrides.Timeout))
		}
		cancel()
		if err != nil {
			return
		}
	}
	c.st.mu.Lock()
	delete(c.st.Jobs, j.Issue)
	if c.st.saveLocked() != nil {
		c.st.Jobs[j.Issue] = j
	}
	c.st.mu.Unlock()
}

// Stop delivery retains a managed remote run until both existing observations
// are validated. A missing TUI cannot silently release daemon-owned capacity.
func (c *Coord) finishRemoteActionStop(ctx context.Context, h Host, dir string, result actionReceipt) {
	c.st.mu.Lock()
	j := c.st.Jobs[result.IssueNumber]
	c.st.mu.Unlock()
	if j == nil || j.RemoteControl == nil || j.RemoteStopOperation != result.OperationID || j.Host != result.Host || j.Pane != result.PaneID || j.Workspace != result.WorkspaceID || result.NativeRunID != "orchid-"+j.DispatchKey {
		return
	}
	for _, row := range []struct{ name, kind string }{{"seat-observed.json", "seat_absent"}, {"process-observed.json", "process_absent"}} {
		var observation actionStopObservation
		if readPrivateActionJSON(filepath.Join(dir, row.name), &observation) != nil || observation.SchemaVersion != 1 || observation.OperationID != result.OperationID || observation.RequestDigest != result.RequestDigest || observation.Kind != row.kind {
			return
		}
	}
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if h.stopRemoteRun(check, j) != nil || check.Err() != nil {
		return
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	if c.st.Jobs[j.Issue] != j {
		return
	}
	delete(c.st.Jobs, j.Issue)
	if c.st.saveLocked() != nil {
		c.st.Jobs[j.Issue] = j
	}
}
