package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func reconciliationOperationID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	b[6], b[8] = (b[6]&15)|64, (b[8]&63)|128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Use the existing private dispatcher action maildir. A CLI never saves State,
// runs a coordinator tick, or asks the observer's read socket to mutate a run.
func enqueueNativeEnd(c *Coord, q nativeRunQuery) nativeEndResult {
	out := nativeEndResult{Key: q.Key, State: "unknown", Reason: "assignment_unavailable"}
	n, _ := strconv.Atoi(q.Key)
	c.st.mu.Lock()
	j := c.st.Jobs[n]
	record := c.st.nativeRunRecordLocked(n, j, c.cfg.Inbox)
	terminal, reason := c.st.terminalNativeRunLocked(n, j, c.cfg.Inbox)
	if record == nil {
		if f, ok := c.st.CompletedRuns[n]; ok {
			record = f.NativeRun
		}
	}
	c.st.mu.Unlock()
	if terminal != nil && reason == "" && terminal.Label == q.Label {
		out.State, out.Reason = "already_recorded", "immutable_terminal"
		return out
	}
	if reason != "" || record == nil || record.Label != q.Label || !validNativeRunRecord(*record) {
		return out
	}
	spool := c.cfg.ActionRequestRoot
	if !privateActionSpoolDir(spool) || !privateActionSpoolDir(filepath.Join(spool, "tmp")) ||
		!privateActionSpoolDir(filepath.Join(spool, "new")) {
		out.Reason = "action_spool_unavailable"
		return out
	}
	id := reconciliationOperationID()
	if id == "" {
		return out
	}
	req := actionRequest{SchemaVersion: 1, OperationID: id, IdempotencyKey: "terminal:" + record.DispatchKey,
		Repository: c.cfg.Inbox, IssueNumber: n, AgentID: record.AgentID, DispatchID: record.AssignmentID,
		ExpectedAgentRevision: nativeEndRevision(*record, j), Action: "reconcile_end"}
	if actionRequestValid(req) != "" {
		return out
	}
	body, err := json.Marshal(req)
	if err != nil {
		return out
	}
	temp, dest := filepath.Join(spool, "tmp", id+".json"), filepath.Join(spool, "new", id+".json")
	if actionSyncFile(temp, body) != nil {
		out.Reason = "action_enqueue_unavailable"
		return out
	}
	// Link, rather than replace, so a conflicting request can never be overwritten.
	if os.Link(temp, dest) != nil || syncDirectory(filepath.Join(spool, "new")) != nil {
		out.Reason = "action_enqueue_unavailable"
		return out
	}
	_ = os.Remove(temp)
	out.State, out.Reason = "enqueued", "dispatcher_revalidation_pending"
	return out
}

func reconcileCompletionsCLI(args []string, stdout, stderr io.Writer) int {
	return reconcileCompletionsWith(args, stdout, stderr, newCoord)
}

func reconcileCompletionsWith(args []string, stdout, stderr io.Writer, makeCoord func(*Config) *Coord) int {
	fs := flag.NewFlagSet("reconcile-completions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	config := fs.String("config", "divybot.json", "dispatcher configuration")
	keysFile := fs.String("keys-file", "", "private JSON file containing at most 128 exact native keys")
	apply := fs.Bool("apply", false, "enqueue for dispatcher revalidation; default is read-only dry-run")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *keysFile == "" {
		return 2
	}
	file, err := os.Open(*keysFile)
	if err != nil {
		fmt.Fprintln(stderr, "reconcile-completions: key selection unavailable")
		return 2
	}
	raw, err := io.ReadAll(io.LimitReader(file, nativeRunFrameLimit+1))
	_ = file.Close()
	keys, decodeErr := decodeNativeRunKeys(raw)
	if err != nil || decodeErr != nil {
		fmt.Fprintln(stderr, "reconcile-completions: bounded exact key selection required")
		return 2
	}
	cfg, err := loadConfig(*config)
	if err != nil {
		fmt.Fprintln(stderr, "reconcile-completions: configuration unavailable")
		return 2
	}
	state, err := os.ReadFile(cfg.StateFile)
	var shape struct {
		Jobs map[int]json.RawMessage `json:"jobs"`
	}
	if err != nil || json.Unmarshal(state, &shape) != nil || shape.Jobs == nil {
		fmt.Fprintln(stderr, "reconcile-completions: dispatcher state unavailable")
		return 2
	}
	c := makeCoord(cfg)
	// Decode the same snapshot that passed validation, never a second raceable read.
	c.st = loadStateSnapshot(cfg.StateFile, state)
	rows := make([]nativeEndResult, 0, len(keys))
	for _, q := range keys {
		if *apply {
			rows = append(rows, enqueueNativeEnd(c, q))
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			rows = append(rows, c.reconcileNativeEnd(ctx, q, "", false))
			cancel()
		}
	}
	body, _ := json.Marshal(struct {
		DryRun bool              `json:"dryRun"`
		Rows   []nativeEndResult `json:"rows"`
	}{!*apply, rows})
	_, _ = io.Copy(stdout, bytes.NewReader(append(body, '\n')))
	return 0
}
