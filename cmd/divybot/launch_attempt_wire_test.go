package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Adopted from the REVIEW-o83g probes: every record kind reaching a finished
// decision is validated on its raw wire types against one schema table.

// snapshotCLI is the CLI's read-only verdict and output for a receipt root.
func snapshotCLI(root string) (int, string) {
	s := readLaunchSnapshot(root, time.Now())
	b, _ := json.Marshal(s)
	return snapshotExit(s), string(b)
}

// encoding/json accepts case-insensitive field aliases. A raw lookup of only
// the preferred spelling must not miss the semantic phase the decoder reads.
func TestWireRejectsCaseAliasedPhaseSources(t *testing.T) {
	for _, field := range []string{"stage", "kind", "shutdown"} {
		for _, value := range []string{"null", "missing"} {
			for _, publish := range []bool{false, true} {
				t.Run(fmt.Sprintf("ContextEnded/%s/%s/publish=%v", field, value, publish), func(t *testing.T) {
					a, root := fixtureAttempt(t, publish, false, nil)
					a.p.Stage = "registration"
					a.p.Facts.ContextEnded = &attemptContextEnd{Kind: "deadline", Stage: ""}
					// Missing/null kind decodes empty and the closed vocabulary
					// already rejects it; the other two erase to valid zero values.
					at := stamp(time.Now())
					body, _ := json.Marshal(decideOutcome(a.p, nil, at))
					a.p.Decision = &attemptDecision{Bytes: string(body), ObservedAt: at}
					raw, _ := json.Marshal(a.p)
					var top, facts, ended map[string]json.RawMessage
					_ = json.Unmarshal(raw, &top)
					_ = json.Unmarshal(top["facts"], &facts)
					_ = json.Unmarshal(facts["contextEnded"], &ended)
					if value == "missing" {
						delete(ended, field)
					} else {
						ended[field] = json.RawMessage(`null`)
					}
					delete(facts, "contextEnded")
					facts["ContextEnded"], _ = json.Marshal(ended)
					top["facts"], _ = json.Marshal(facts)
					raw, _ = json.Marshal(top)
					checkWireRecord(t, a, root, raw, false, publish)
				})
			}
		}
	}
}

func TestWireRejectsCaseAliasEvenWithValidValue(t *testing.T) {
	for _, publish := range []bool{false, true} {
		t.Run(fmt.Sprintf("publish=%v", publish), func(t *testing.T) {
			a, root := fixtureAttempt(t, publish, false, nil)
			a.p.Stage = "registration"
			a.p.Facts.ContextEnded = &attemptContextEnd{Kind: "deadline", Stage: ""}
			at := stamp(time.Now())
			body, _ := json.Marshal(decideOutcome(a.p, nil, at))
			a.p.Decision = &attemptDecision{Bytes: string(body), ObservedAt: at}
			raw, _ := json.Marshal(a.p)
			var top, facts map[string]json.RawMessage
			_ = json.Unmarshal(raw, &top)
			_ = json.Unmarshal(top["facts"], &facts)
			facts["ContextEnded"] = facts["contextEnded"]
			delete(facts, "contextEnded")
			top["facts"], _ = json.Marshal(facts)
			raw, _ = json.Marshal(top)
			checkWireRecord(t, a, root, raw, false, publish)
		})
	}
}

// Each field is changed on a writer-marshaled, canonical frozen decision.
// The actual read-only CLI and both recovery instances must retain uncertainty.
func TestWireTypeMatrixFailsClosed(t *testing.T) {
	values := map[string]string{
		"null": "null", "missing": "", "number": "17", "object": "{}", "array": "[]", "false": "false", "true": "true", "empty-string": `""`, "unknown-string": `"future-value"`,
	}
	for _, field := range []string{"stage", "context.stage", "context.kind", "context.shutdown", "herdrStartError", "facts"} {
		for label, value := range values {
			valid := false
			switch field {
			case "stage", "context.stage":
				valid = label == "empty-string"
			case "context.shutdown":
				valid = label == "false" || label == "true"
			case "herdrStartError":
				valid = label == "empty-string" || label == "missing"
			case "facts":
				valid = label == "object"
			}
			for _, publish := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/publish=%v", field, label, publish), func(t *testing.T) {
					a, root := fixtureAttempt(t, publish, false, nil)
					a.p.Stage = ""
					if strings.HasPrefix(field, "context.") {
						a.p.Stage = "registration"
						a.p.Facts.ContextEnded = &attemptContextEnd{Kind: "deadline", Stage: ""}
					}
					// Use a value matching each legitimate replacement so all positive
					// controls keep a byte-identical frozen decision after mutation.
					if field == "context.shutdown" && label == "true" {
						a.p.Facts.ContextEnded.Shutdown = true
					}
					at := stamp(time.Now())
					body, _ := json.Marshal(decideOutcome(a.p, nil, at))
					a.p.Decision = &attemptDecision{Bytes: string(body), ObservedAt: at}
					raw, _ := json.Marshal(a.p)
					var top map[string]json.RawMessage
					_ = json.Unmarshal(raw, &top)
					set := func(m map[string]json.RawMessage, key string) {
						if label == "missing" {
							delete(m, key)
						} else {
							m[key] = json.RawMessage(value)
						}
					}
					switch {
					case field == "stage" || field == "facts":
						set(top, field)
					default:
						var facts map[string]json.RawMessage
						_ = json.Unmarshal(top["facts"], &facts)
						if field == "herdrStartError" {
							set(facts, field)
						} else {
							var ended map[string]json.RawMessage
							_ = json.Unmarshal(facts["contextEnded"], &ended)
							set(ended, strings.TrimPrefix(field, "context."))
							facts["contextEnded"], _ = json.Marshal(ended)
						}
						top["facts"], _ = json.Marshal(facts)
					}
					raw, _ = json.Marshal(top)
					checkWireRecord(t, a, root, raw, valid, publish)
				})
			}
		}
	}
}

func checkWireRecord(t *testing.T, a *launchAttempt, root string, raw []byte, valid, publish bool) {
	t.Helper()
	path := attemptFile(root, "attempt", a.p.AttemptID)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("d", 64)
	dir := filepath.Join(root, key, "record")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateJSON(filepath.Join(dir, "dispatch.json"), ".d-", nil, dispatchBinding{SchemaVersion: 1, RunID: "orchid-" + key, Issue: a.p.Issue, Source: "codex", State: "dispatched"}); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateJSON(filepath.Join(root, "producer.json"), ".p-", nil, producerHeartbeat{Instance: a.p.Instance, StartedAt: stamp(time.Now()), ObservedAt: stamp(time.Now())}); err != nil {
		t.Fatal(err)
	}
	want := 2
	if valid {
		want = 0
	}
	code, out := snapshotCLI(root)
	t.Logf("initial exit=%d want=%d snapshot=%s", code, want, out)
	if code != want {
		t.Errorf("wire value certified: exit=%d want=%d", code, want)
	}
	if !valid && !strings.Contains(out, `"unknownReason":"evidence-invalid"`) {
		t.Errorf("invalid record lost closed unknown reason: %s", out)
	}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	for _, instance := range []string{a.p.Instance, strings.Repeat("b", 32)} {
		recoverLaunchAttempts(root, nil, publish, instance, time.Now())
		code, out = snapshotCLI(root)
		t.Logf("recovery instance=%s exit=%d want=%d snapshot=%s", instance, code, want, out)
		if code != want {
			t.Errorf("recovery certified wire value: exit=%d want=%d", code, want)
		}
		if !valid {
			remaining, e := os.ReadFile(path)
			if e != nil || !bytes.Equal(raw, remaining) {
				t.Errorf("invalid record removed or changed: %v", e)
			}
			for _, prefix := range []string{"admitted", "outcome"} {
				if _, e := os.Stat(attemptFile(root, prefix, a.p.AttemptID)); !os.IsNotExist(e) {
					t.Errorf("invalid %s published: %v", prefix, e)
				}
			}
		}
	}
	if !valid && !strings.Contains(logs.String(), "launch attempt record invalid; kept for operator inspection") {
		t.Error("invalid record not logged")
	}
	if valid {
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			t.Errorf("valid finished record not retired: %v", e)
		}
		for _, prefix := range []string{"admitted", "outcome"} {
			data, e := os.ReadFile(attemptFile(root, prefix, a.p.AttemptID))
			if publish {
				if e != nil {
					t.Errorf("valid %s not published: %v", prefix, e)
				}
				if prefix == "outcome" && !bytes.Equal(data, []byte(a.p.Decision.Bytes)) {
					t.Error("outcome bytes changed")
				}
			} else if !os.IsNotExist(e) {
				t.Errorf("OFF consumer %s written: %v", prefix, e)
			}
		}
	}
}

// Every record the production tracker writes, at every lifecycle point and
// for every reducer branch, passes the strict wire reader.
func TestWriterRecordsPassWireSchema(t *testing.T) {
	retry := &retryExpectation{OperationID: "op-1", Dispatch: dispatchBinding{RunID: "orchid-" + strings.Repeat("d", 64)}}
	for _, tc := range []struct {
		name  string
		retry *retryExpectation
		run   bool
		steps func(a *launchAttempt)
	}{
		{"begin-only", nil, false, func(a *launchAttempt) {}},
		{"committed", nil, false, func(a *launchAttempt) {
			a.enter(stageRegistration)
			a.registered()
			a.enter(stageGoalConfirmation)
			a.goalCommitted()
		}},
		{"run", nil, true, func(a *launchAttempt) { a.enter(stageEnvironment); a.runStarted() }},
		{"client-check", nil, false, func(a *launchAttempt) { a.enter(stageClientSelection); a.codexClientCheckFailed() }},
		{"herdr-error", nil, false, func(a *launchAttempt) { a.enter(stageRegistration); a.herdrStartError(`{"error":{"code":"timeout"}}`) }},
		{"context-end", nil, false, func(a *launchAttempt) {
			a.enter(stageWorkspace)
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(errOrchidShutdown)
			a.contextEnded(ctx)
		}},
		{"retry", retry, false, func(a *launchAttempt) { a.enter(stagePreparation) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, root := fixtureAttempt(t, false, tc.run, tc.retry)
			a.p.Instance = "fedcba9876543210fedcba9876543210"
			a.begin()
			tc.steps(a)
			check := func(when string) {
				raw, err := os.ReadFile(attemptFile(root, "attempt", a.p.AttemptID))
				if err != nil {
					t.Fatalf("%s: no record", when)
				}
				if p, ok := readAttemptProgress(attemptFile(root, "attempt", a.p.AttemptID)); !ok || !snapshotProgressValid(p) {
					t.Fatalf("%s: the writer's own record rejected: %s", when, raw)
				}
			}
			check("in flight")
			// Freeze a decision durably but keep the record (directory sync withheld).
			orig := syncAttemptDir
			syncAttemptDir = func(string) error { return errMatrix }
			a.finish(context.Background(), errMatrix)
			syncAttemptDir = orig
			if _, err := os.Stat(attemptFile(root, "attempt", a.p.AttemptID)); err == nil {
				check("decided")
			}
		})
	}
}
