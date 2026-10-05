package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	receipts := c.cfg.Matrix.ReceiptRoot
	if !privateReceiptRoot(receipts) || c.st == nil || c.st.path == "" {
		return ""
	}
	state, err := filepath.Abs(c.st.path)
	if err != nil {
		return ""
	}
	dir := filepath.Join(filepath.Dir(state), "teardown-pending")
	// The queue is Orchid's work, never part of the reader-facing receipt tree.
	if relative, err := filepath.Rel(receipts, dir); err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return ""
	}
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return ""
	}
	if !privateReceiptRoot(dir) {
		return ""
	}
	if err := migrateTeardownPending(filepath.Join(receipts, "teardown-pending"), dir); err != nil {
		return ""
	}
	return dir
}

// Older deployments kept this private work queue beneath the public receipt
// root. Copy each file durably before removing the old entry, then remove the
// old directory. Retrying after a crash accepts only an identical copy.
func migrateTeardownPending(old, current string) error {
	if _, err := os.Lstat(old); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if !privateReceiptRoot(old) {
		return os.ErrInvalid
	}
	entries, err := os.ReadDir(old)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(old, entry.Name())
		info, err := os.Lstat(from)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return os.ErrInvalid
		}
		body, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		to := filepath.Join(current, entry.Name())
		if prior, err := os.ReadFile(to); err == nil {
			if !bytes.Equal(prior, body) {
				return os.ErrExist
			}
		} else if !os.IsNotExist(err) {
			return err
		} else {
			temp, err := os.CreateTemp(current, ".teardown-")
			if err != nil {
				return err
			}
			name := temp.Name()
			if err = temp.Chmod(0600); err == nil {
				_, err = temp.Write(body)
			}
			if err == nil {
				err = temp.Sync()
			}
			closeErr := temp.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Link(name, to)
			}
			_ = os.Remove(name)
			if err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			if err := syncDirectory(current); err != nil {
				return err
			}
			if prior, err := os.ReadFile(to); err != nil || !bytes.Equal(prior, body) {
				return os.ErrExist
			}
		}
		if err := os.Remove(from); err != nil {
			return err
		}
	}
	if err := syncDirectory(old); err != nil {
		return err
	}
	if err := os.Remove(old); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(old))
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
		dispatch.Issue != c.issueHome(n) ||
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
	complete := lstatRegular(seatPath) == nil && lstatRegular(procPath) == nil
	if complete {
		c.st.mu.Lock()
		if flight, ok := c.st.RetryFlights[intent.Issue]; ok && flight.DispatchKey == intent.DispatchKey {
			delete(c.st.RetryFlights, intent.Issue)
			if c.st.saveLocked() != nil {
				c.st.RetryFlights[intent.Issue] = flight
			}
		}
		c.st.mu.Unlock()
	}
	return complete
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
