package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

// This compiles against main before the read API: it fails on the missing
// production method, not missing test types. No history read can hide key 1.
func TestNativeRunStandardPointSeam(t *testing.T) {
	s := loadState(filepath.Join(t.TempDir(), "state.json"))
	s.SourceBindings = map[int]*sourceBinding{1: {Repo: "example/source", Issue: 948, Comment: 1}}
	s.Jobs[1] = &Job{Issue: 1, Home: &dispatchIssue{Repo: "example/source", Number: 948}, Host: "host", HerdrScope: &herdrScope{Session: "dispatch", Machine: "machine"}, Label: "codex-1", Agent: "codex", DispatchKey: fmt.Sprintf("%064x", 1), NativeSessionID: "native-fixture"}
	s.CompletedRuns = map[int]completedRun{}
	for n := 2; n <= 702; n++ {
		s.CompletedRuns[n] = completedRun{DispatchKey: fmt.Sprintf("%064x", n), NativeSessionID: "historical", Phase: "observed"}
		s.SourceBindings[n] = &sourceBinding{Repo: "example/history", Issue: n, Comment: int64(n), Closed: "done"}
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	seam, ok := any(s).(interface {
		nativeRunsLookup(json.RawMessage) (json.RawMessage, error)
	})
	if !ok {
		t.Fatal("missing native point lookup State seam")
	}
	raw, err := seam.nativeRunsLookup(json.RawMessage(`{"keys":[{"key":"1","label":"codex-1"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Records []struct {
			Key   string `json:"key"`
			Issue struct {
				Repo   string `json:"repo"`
				Number int    `json:"number"`
			} `json:"issue"`
			AgentID      string `json:"agentId"`
			AssignmentID string `json:"assignmentId"`
		} `json:"records"`
	}
	if json.Unmarshal(raw, &got) != nil || len(got.Records) != 1 || got.Records[0].Key != "1" || got.Records[0].Issue.Repo != "example/source" || got.Records[0].Issue.Number != 948 || got.Records[0].AgentID == "" || got.Records[0].AssignmentID == "" {
		t.Fatal("oldest active association unavailable")
	}
}
