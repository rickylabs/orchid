package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Teardown receipts are independent of issue closure and workspace-close
// delivery. Only separate observations of the seat and captured native process
// can prove that an ordinary teardown actually ended the run.
type teardownIntent struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Issue           int    `json:"issue"`
	DispatchKey     string `json:"dispatchKey"`
	NativeRunID     string `json:"nativeRunId"`
	NativeSessionID string `json:"nativeSessionId"`
	Host            string `json:"host"`
	PaneID          string `json:"paneId"`
	WorkspaceID     string `json:"workspaceId"`
	Cause           string `json:"cause"` // operator-timeout | teardown
	StartedAt       string `json:"startedAt"`
}

type teardownObservation struct {
	SchemaVersion int    `json:"schemaVersion"`
	NativeRunID   string `json:"nativeRunId"`
	Kind          string `json:"kind"` // seat_absent | process_absent
	ObservedAt    string `json:"observedAt"`
}

func (c *Coord) teardownPendingRoot() string {
	root := c.cfg.Matrix.ReceiptRoot
	if !privateReceiptRoot(root) {
		return ""
	}
	dir := filepath.Join(root, "teardown-pending")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return ""
	}
	if !privateReceiptRoot(dir) {
		return ""
	}
	return dir
}

func (c *Coord) teardownRecord(j *Job, ws string) (string, string, bool) {
	if !digestPattern.MatchString(j.DispatchKey) || j.Pane == "" || ws == "" || j.Host == "" {
		return "", "", false
	}
	root := c.cfg.Matrix.ReceiptRoot
	if !privateReceiptRoot(root) {
		return "", "", false
	}
	dir := filepath.Join(root, j.DispatchKey)
	record := filepath.Join(dir, "record")
	if !privateReceiptRoot(dir) || !privateReceiptRoot(record) {
		return "", "", false
	}
	return record, "orchid-" + j.DispatchKey, true
}

func (c *Coord) teardownStart(ctx context.Context, n int, j *Job, ws, cause string) {
	if cause != "operator-timeout" && cause != "teardown" {
		return
	}
	record, runID, ok := c.teardownRecord(j, ws)
	if !ok || c.teardownPendingRoot() == "" {
		return
	}
	var dispatch dispatchBinding
	if readPrivateActionJSON(filepath.Join(record, "dispatch.json"), &dispatch) != nil ||
		dispatch.SchemaVersion != 1 || dispatch.State != "dispatched" || dispatch.RunID != runID ||
		dispatch.Issue.Repo != c.cfg.Inbox || dispatch.Issue.Number != n ||
		dispatch.Host != j.Host || dispatch.Location == nil ||
		dispatch.Location.PaneID != j.Pane || dispatch.Location.WorkspaceID != ws {
		return
	}
	var bindingFields map[string]json.RawMessage
	var binding struct {
		NativeSessionID string
		Repo            string
	}
	if readPrivateActionJSON(filepath.Join(record, "binding.json"), &bindingFields) != nil ||
		json.Unmarshal(bindingFields["NativeSessionID"], &binding.NativeSessionID) != nil ||
		json.Unmarshal(bindingFields["Repo"], &binding.Repo) != nil ||
		binding.Repo != j.Repo || !privateNativeID(binding.NativeSessionID) {
		return
	}
	host, ok := c.hosts[j.Host]
	if !ok {
		return
	}
	agents, err := c.actionList(ctx, host)
	if err != nil {
		return
	}
	matches := 0
	for _, a := range agents {
		if issueFromCwd(a.Cwd) == n && a.PaneID == j.Pane && a.WorkspaceID == ws && accountKey(a.Agent) == accountKey(j.Agent) {
			matches++
		}
	}
	if matches != 1 {
		return
	}
	anchor, err := c.actionStopProcess(ctx, host, j.Pane, j.Agent)
	if err != nil || anchor == nil {
		return
	}
	intent := teardownIntent{SchemaVersion: 1, Issue: n, DispatchKey: j.DispatchKey, NativeRunID: runID,
		NativeSessionID: binding.NativeSessionID, Host: j.Host, PaneID: j.Pane, WorkspaceID: ws,
		Cause: cause, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := actionImmutableJSON(record, "teardown-anchor.json", anchor); err != nil {
		return
	}
	if err := actionImmutableJSON(record, "teardown-intent.json", intent); err != nil {
		return
	}
	if err := c.publishActionOwner(record, filepath.Join(record, "teardown-anchor.json"), filepath.Join(record, "teardown-intent.json")); err != nil {
		return
	}
	pending := c.teardownPendingRoot()
	if pending != "" {
		_ = actionImmutableJSON(pending, j.DispatchKey+".json", intent)
	}
}

func (c *Coord) teardownObservePending(ctx context.Context) {
	pending := c.teardownPendingRoot()
	if pending == "" || c.dry {
		return
	}
	entries, err := os.ReadDir(pending)
	if err != nil || len(entries) == 0 {
		return
	}
	start := int(time.Now().Unix()/30) % len(entries)
	inspected := 0
	for i := 0; i < len(entries) && inspected < 2 && ctx.Err() == nil; i++ {
		entry := entries[(start+i)%len(entries)]
		key := strings.TrimSuffix(entry.Name(), ".json")
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !digestPattern.MatchString(key) {
			continue
		}
		inspected++
		file := filepath.Join(pending, entry.Name())
		var intent teardownIntent
		if readPrivateActionJSON(file, &intent) != nil || intent.DispatchKey != key {
			continue
		}
		started, err := time.Parse(time.RFC3339Nano, intent.StartedAt)
		if err != nil || time.Since(started) > 10*time.Minute || time.Since(started) < 0 {
			_ = os.Remove(file)
			continue
		}
		if c.teardownObserveOne(ctx, intent) {
			_ = os.Remove(file)
		}
	}
}

func (c *Coord) teardownObserveOne(ctx context.Context, intent teardownIntent) bool {
	if intent.SchemaVersion != 1 || intent.Issue <= 0 || !digestPattern.MatchString(intent.DispatchKey) ||
		intent.NativeRunID != "orchid-"+intent.DispatchKey || !privateNativeID(intent.NativeSessionID) ||
		intent.PaneID == "" || intent.WorkspaceID == "" || (intent.Cause != "operator-timeout" && intent.Cause != "teardown") {
		return false
	}
	host, ok := c.hosts[intent.Host]
	if !ok {
		return false
	}
	record, _, ok := c.teardownRecord(&Job{DispatchKey: intent.DispatchKey, Host: intent.Host, Pane: intent.PaneID}, intent.WorkspaceID)
	if !ok {
		return false
	}
	var persisted teardownIntent
	var anchor actionStopProcess
	if readPrivateActionJSON(filepath.Join(record, "teardown-intent.json"), &persisted) != nil || persisted != intent ||
		readPrivateActionJSON(filepath.Join(record, "teardown-anchor.json"), &anchor) != nil {
		return false
	}
	seatPath := filepath.Join(record, "teardown-seat-observed.json")
	procPath := filepath.Join(record, "teardown-process-observed.json")
	seatMissing := os.IsNotExist(lstatRegular(seatPath))
	procMissing := os.IsNotExist(lstatRegular(procPath))
	if seatMissing {
		check, cancel := context.WithTimeout(ctx, 12*time.Second)
		agents, err := c.actionList(check, host)
		gone := false
		if err == nil {
			gone, err = c.actionWorkspaceGone(check, host, intent.WorkspaceID)
			if gone {
				for _, a := range agents {
					if a.PaneID == intent.PaneID || a.WorkspaceID == intent.WorkspaceID {
						gone = false
						break
					}
				}
			}
		}
		cancel()
		if err == nil && gone {
			c.teardownWriteObservation(record, intent, "seat_absent", "teardown-seat-observed.json")
		}
	}
	if procMissing {
		check, cancel := context.WithTimeout(ctx, 12*time.Second)
		gone, err := c.actionProcessGone(check, host, anchor)
		cancel()
		if err == nil && gone {
			c.teardownWriteObservation(record, intent, "process_absent", "teardown-process-observed.json")
		}
	}
	return lstatRegular(seatPath) == nil && lstatRegular(procPath) == nil
}

func lstatRegular(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return os.ErrInvalid
	}
	return nil
}

func (c *Coord) teardownWriteObservation(record string, intent teardownIntent, kind, name string) {
	o := teardownObservation{SchemaVersion: 1, NativeRunID: intent.NativeRunID,
		Kind: kind, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := actionImmutableJSON(record, name, o); err != nil && !os.IsExist(err) {
		return
	}
	_ = c.publishActionOwner(record, filepath.Join(record, name))
}
