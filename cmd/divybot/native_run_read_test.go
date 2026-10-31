package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func nativeRunFixture(t *testing.T) *Coord {
	t.Helper()
	s := loadState(filepath.Join(t.TempDir(), "state.json"))
	c := newCoord(&Config{Inbox: "example/inbox"})
	c.st, c.instance = s, "fixture-generation"
	s.nativeRuns = &nativeRunWatch{generation: c.instance, subs: map[chan struct{}]struct{}{}}
	s.SourceBindings = map[int]*sourceBinding{701: {Repo: "example/project", Issue: 7, Comment: 701}}
	s.Jobs[701] = &Job{Issue: 701, Home: &dispatchIssue{Repo: "example/project", Number: 7},
		Label: "codex-701", Agent: "codex", DispatchKey: fmt.Sprintf("%064x", 701), NativeSessionID: "fixture-native-session"}
	return c
}

func TestNativeRunLookupSurvivesRestartAndHistory(t *testing.T) {
	c := nativeRunFixture(t)
	// More historical records than the consumer cap cannot change a point lookup.
	for n := 1; n <= 700; n++ {
		c.st.SourceBindings[n] = &sourceBinding{Repo: "example/history", Issue: n, Comment: int64(n), Closed: "done"}
	}
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	q := []nativeRunQuery{{Key: "701", Label: "codex-701"}}
	before := c.lookupNativeRuns(q)
	if len(before.Records) != 1 || before.Records[0].Issue != (dispatchIssue{Repo: "example/project", Number: 7}) {
		t.Fatalf("oldest native run lost: %+v", before.Missing)
	}
	c.st = loadState(c.st.path)
	c.st.nativeRuns = &nativeRunWatch{generation: "restarted-generation", subs: map[chan struct{}]struct{}{}}
	// Pane/workspace handles can change after restart; the stable label and native ID are authoritative.
	c.st.Jobs[701].Pane, c.st.Jobs[701].Workspace = "restored-pane", "restored-workspace"
	after := c.lookupNativeRuns(q)
	if len(after.Records) != 1 || after.Records[0].DispatchKey != before.Records[0].DispatchKey ||
		after.Records[0].NativeSessionID != before.Records[0].NativeSessionID || after.Generation == before.Generation {
		t.Fatal("restart changed native association")
	}
	c.st.CompletedRuns = map[int]completedRun{701: {DispatchKey: after.Records[0].DispatchKey,
		NativeSessionID: after.Records[0].NativeSessionID, Phase: "observed"}}
	if c.lookupNativeRuns(q).Records[0].Phase != "completed" {
		t.Fatal("native completion was lost")
	}
}

func TestNativeRunMissingAndMismatchedEvidence(t *testing.T) {
	c := nativeRunFixture(t)
	for _, q := range []nativeRunQuery{{"702", "codex-702"}, {"701", "claude-701"}} {
		result := c.lookupNativeRuns([]nativeRunQuery{q})
		if len(result.Records) != 0 || len(result.Missing) != 1 {
			t.Fatal("invented native association")
		}
	}
	c.st.Jobs[701].NativeSessionID = ""
	if len(c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}).Records) != 0 {
		t.Fatal("invented native ID")
	}
	c.st.nativeRuns.unproven = true
	if c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}).Missing[0].Reason != "source_unavailable" {
		t.Fatal("unproven state became a native fact")
	}
}

func TestNativeRunProtocolIsReadOnlyAndBounded(t *testing.T) {
	for _, raw := range []string{
		`{"id":"x","method":"run.subscribe","params":{}}`,
		`{"id":"x","method":"run.lookup","params":{"keys":[{"key":"701","label":"codex-701"}]}}`,
	} {
		if _, _, err := decodeNativeRunRPC([]byte(raw)); err != nil {
			t.Fatal("valid native RPC rejected")
		}
	}

	for _, raw := range []string{
		`{"id":"x","method":"agent.start","params":{}}`,
		`{"id":"x","method":"run.subscribe","params":null}`,
		`{"id":"x","method":"run.lookup","params":{"keys":[{"key":"701","label":"codex-702"}]}}`,
		`{"id":"x","method":"run.lookup","params":{"keys":[],"extra":true}}`,
		`{"id":"x","method":"run.lookup","params":{"keys":[]},"method":"run.subscribe"}`,
	} {
		if _, _, err := decodeNativeRunRPC([]byte(raw)); err == nil {
			t.Fatal("accepted invalid native RPC")
		}
	}
	queries := make([]nativeRunQuery, nativeRunBatchLimit+1)
	for n := range queries {
		queries[n] = nativeRunQuery{fmt.Sprint(n + 1), fmt.Sprintf("codex-%d", n+1)}
	}
	raw, _ := json.Marshal(map[string]any{"id": "x", "method": "run.lookup", "params": map[string]any{"keys": queries}})
	if _, _, err := decodeNativeRunRPC(raw); err == nil {
		t.Fatal("accepted unbounded batch")
	}
}

func TestNativeRunFailedCommitCannotCertifyNativeState(t *testing.T) {
	c := nativeRunFixture(t)
	path := c.st.path
	c.st.path = filepath.Join(path, "missing", "state.json")
	if err := c.st.save(); err == nil {
		t.Fatal("expected failed durable state commit")
	}
	q := []nativeRunQuery{{"701", "codex-701"}}
	if got := c.lookupNativeRuns(q); len(got.Records) != 0 || got.Missing[0].Reason != "source_unavailable" {
		t.Fatal("failed save published unproven native facts")
	}
	c.st.path = path
	c.st.Jobs[701].Goal = "PRIVATE-GOAL-CANARY"
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	got := c.lookupNativeRuns(q)
	if len(got.Records) != 1 {
		t.Fatal("successful native commit did not recover")
	}
	raw, _ := json.Marshal(got)
	if bytes.Contains(raw, []byte("PRIVATE-")) {
		t.Fatal("private job body crossed native projection")
	}
}

func TestNativeRunSubscribeBeforeBaselineAndOwnedSocket(t *testing.T) {
	c := nativeRunFixture(t)
	dir, err := os.MkdirTemp("", "nr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "native.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := c.listenNativeRuns(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := c.listenNativeRuns(ctx, socket); err == nil {
		t.Fatal("replaced foreign endpoint")
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := json.NewEncoder(conn).Encode(map[string]any{"id": "watch", "method": "run.subscribe", "params": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	var ack struct {
		Result struct {
			Type string `json:"type"`
		} `json:"result"`
	}
	line, err := reader.ReadBytes('\n')
	if err != nil || json.Unmarshal(line, &ack) != nil || ack.Result.Type != "subscription_started" {
		t.Fatal("subscription not acknowledged")
	}
	for n := 0; n < 100; n++ {
		c.st.mu.Lock()
		c.st.notifyNativeRunChangeLocked()
		c.st.mu.Unlock()
	}
	var event struct {
		Event struct {
			Type     string `json:"type"`
			Revision int    `json:"revision"`
		} `json:"event"`
	}
	line, err = reader.ReadBytes('\n')
	if err != nil || json.Unmarshal(line, &event) != nil || event.Event.Type != "runs_changed" || event.Event.Revision < 1 {
		t.Fatal("native change notification lost")
	}
}

// Save notifications can run without State.mu while a subscriber joins or leaves.
func TestNativeRunNotificationOwnsItsLock(t *testing.T) {
	c := nativeRunFixture(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := 0; n < 1000; n++ {
			c.st.publishNativeRunCommit(n%2 == 0)
		}
	}()
	for n := 0; n < 1000; n++ {
		w := c.st.nativeRuns
		ch := make(chan struct{}, 1)
		w.mu.Lock()
		w.subs[ch] = struct{}{}
		w.mu.Unlock()
		_ = c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}})
		w.mu.Lock()
		delete(w.subs, ch)
		w.mu.Unlock()
	}
	<-done
}
