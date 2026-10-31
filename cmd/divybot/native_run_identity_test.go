package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeRunCompletedPrunedBinding(t *testing.T) {
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	record := c.st.nativeRunRecordLocked(701, j, c.cfg.Inbox)
	if record == nil {
		t.Fatal("fixture identity missing")
	}
	record.Phase = "completed"
	record.ObservedAt = stamp(time.Now())
	record.CompletedAt = record.ObservedAt
	c.st.CompletedRuns = map[int]completedRun{701: {NativeRun: record, DispatchKey: j.DispatchKey, NativeSessionID: j.NativeSessionID, Phase: "observed"}}
	delete(c.st.Jobs, 701)
	delete(c.st.SourceBindings, 701)
	for n := 702; n < 1500; n++ {
		c.st.CompletedRuns[n] = completedRun{Phase: "observed"}
	}
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	c.st = loadState(c.st.path)
	c.st.nativeRuns = &nativeRunWatch{generation: "restart", subs: map[chan struct{}]struct{}{}}
	got := c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}})
	if len(got.Records) != 1 {
		t.Fatal(got.Missing)
	}
	r := got.Records[0]
	if r.Issue != record.Issue || r.AgentID != record.AgentID || r.AssignmentID != record.AssignmentID || r.Scope != record.Scope || r.CompletedAt != record.CompletedAt || r.ObservedAt != record.ObservedAt || r.Phase != "completed" {
		t.Fatal("completion lost original identity")
	}
	for _, mutate := range []func(*nativeRunRecord){
		func(r *nativeRunRecord) { r.Label = "claude-701" }, func(r *nativeRunRecord) { r.Agent = "claude" }, func(r *nativeRunRecord) { r.NativeSessionID = "other" }, func(r *nativeRunRecord) { r.Scope.Session = "" }, func(r *nativeRunRecord) { r.AssignmentID = "wrong" }, func(r *nativeRunRecord) { r.Issue.Number = 0 }, func(r *nativeRunRecord) { r.Key = "702" },
	} {
		bad := *record
		mutate(&bad)
		f := c.st.CompletedRuns[701]
		f.NativeRun = &bad
		c.st.CompletedRuns[701] = f
		if len(c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}).Records) != 0 {
			t.Fatal("invalid completed identity accepted")
		}
	}
	c.st.CompletedRuns[701] = completedRun{DispatchKey: record.DispatchKey, NativeSessionID: record.NativeSessionID, Phase: "observed"}
	got = c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}})
	if len(got.Records) != 0 || got.Missing[0].Reason != "native_identity_unavailable" {
		t.Fatal("legacy completion fabricated inbox issue")
	}
}

func TestNativeRunRemoteIdentityAndScope(t *testing.T) {
	for _, mode := range []string{"native", "alternative", "same", "conflict", "missing", "scope"} {
		t.Run(mode, func(t *testing.T) {
			c := nativeRunFixture(t)
			j := c.st.Jobs[701]
			native := j.NativeSessionID
			switch mode {
			case "alternative":
				j.NativeSessionID = ""
				j.RemoteControl = &remoteControlRun{NativeSessionID: native}
			case "same":
				j.RemoteControl = &remoteControlRun{NativeSessionID: native}
			case "conflict":
				j.RemoteControl = &remoteControlRun{NativeSessionID: "other-native"}
			case "missing":
				j.NativeSessionID = ""
			case "scope":
				j.HerdrScope = nil
			}
			got := c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}})
			valid := mode == "native" || mode == "alternative" || mode == "same"
			if valid {
				if len(got.Records) != 1 || got.Records[0].NativeSessionID != native || got.Records[0].Scope.Session != "dispatch" {
					t.Fatal("native identity lost")
				}
			} else if len(got.Records) != 0 {
				t.Fatal("unverified identity certified")
			}
		})
	}
}

func TestNativeRunFullLabelGrammar(t *testing.T) {
	for _, vendor := range []string{"codex", "codex-run", "claude", "agy", "opencode", "opencode-run"} {
		c := nativeRunFixture(t)
		j := c.st.Jobs[701]
		j.Agent = vendor
		j.Label = vendor + "-701"
		raw := fmt.Sprintf(`{"id":"x","method":"run.lookup","params":{"keys":[{"key":"701","label":%q}]}}`, j.Label)
		_, keys, err := decodeNativeRunRPC([]byte(raw))
		if err != nil || len(c.lookupNativeRuns(keys).Records) != 1 {
			t.Fatal("valid full vendor label rejected", vendor)
		}
	}
	for _, q := range []nativeRunQuery{{"701", "codex-0701"}, {"701", "codex-+701"}, {"701", "codex-701-extra"}, {"701", "vendor-701"}, {"9007199254740992", "codex-9007199254740992"}, {"0", "codex-0"}, {"701", "codex-run-702"}} {
		if validNativeRunQuery(q) {
			t.Fatal("invalid label accepted", q)
		}
	}
	c := nativeRunFixture(t)
	c.st.Jobs[701].Agent = "codex-run"
	if len(c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}).Records) != 0 {
		t.Fatal("record vendor mismatch accepted")
	}
}

func TestNativeRunClosedEnvelope(t *testing.T) {
	for _, raw := range []string{
		`{"ID":"x","method":"run.subscribe","params":{}}`,
		`{"id":"x","Method":"run.subscribe","params":{}}`,
		`{"id":"x","method":"run.lookup","params":{"keys":null}}`,
		`{"id":"x","method":"run.lookup","params":{"keys":[{"key":"701","label":"codex-701","scope":{}}]}}`,
		`{"id":"x","method":"run.subscribe","params":{},"schemaVersion":1}`,
		`{"id":"x","method":"run.subscribe","params":{}} {}`,
	} {
		if _, _, err := decodeNativeRunRPC([]byte(raw)); err == nil {
			t.Fatal("noncanonical envelope accepted")
		}
	}
}

func TestNativeRunProjectionPrivacy(t *testing.T) {
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	j.Goal = "PRIVATE-CANARY"
	j.Title = "PRIVATE-CANARY"
	j.Workdir = "/PRIVATE-CANARY"
	j.Pane = "PRIVATE-CANARY"
	j.TerminalID = "PRIVATE-CANARY"
	j.Branch = "PRIVATE-CANARY"
	c.st.SourceBindings[701].Body = "PRIVATE-CANARY"
	raw, err := json.Marshal(c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}))
	if err != nil || strings.Contains(string(raw), "PRIVATE-CANARY") {
		t.Fatal("private job metadata escaped projection")
	}
	// Lookup is independent of the state file/receipt paths after loading.
	c.st.path = filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := os.Stat(c.st.path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(c.lookupNativeRuns([]nativeRunQuery{{"701", "codex-701"}}).Records) != 1 {
		t.Fatal("lookup read a historical path")
	}
}

func TestNativeRunEmptyCensusHealth(t *testing.T) {
	c := nativeRunFixture(t)
	raw := json.RawMessage(`{"keys":[]}`)
	result, err := c.st.nativeRunsLookup(raw)
	if err != nil {
		t.Fatal("empty census refused", err)
	}
	var got nativeRunLookup
	if json.Unmarshal(result, &got) != nil || !got.Complete || got.Generation == "" || got.ObservedAt == "" || len(got.Records) != 0 || len(got.Missing) != 0 {
		t.Fatal("empty census did not prove source health")
	}
}
