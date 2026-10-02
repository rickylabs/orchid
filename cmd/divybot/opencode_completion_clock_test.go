package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOpenCodeCompletionBoundsEveryConsumedTextClock(t *testing.T) {
	run := &openCodeRun{Route: openCodeRoute{"fixture-provider", "fixture-model", "high"}, Cwd: "/fixture/checkout", SessionID: "ses_fixture", NotBefore: 1000, ExpectedPromptDigest: shaText([]byte(runPointer))}
	for _, test := range []struct {
		name       string
		start, end int64
		valid      bool
	}{
		{"ordinary-no-end", 2001, 0, true},
		{"ordinary-complete", 2001, 2002, true},
		{"later-end", 2001, 2003, false},
		{"later-start-no-end", 2003, 0, false},
		{"future-start-no-end", time.Now().Add(time.Hour).UnixMilli(), 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var record map[string]any
			if json.Unmarshal(openCodeExportFixture(run, "OK"), &record) != nil {
				t.Fatal("fixture export")
			}
			assistant := record["messages"].([]any)[1].(map[string]any)
			part := assistant["parts"].([]any)[0].(map[string]any)
			clock := map[string]any{"start": test.start}
			if test.end != 0 {
				clock["end"] = test.end
			}
			part["time"] = clock
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			confirmed, completed, err := inspectOpenCodeExport(raw, run)
			if test.valid {
				if !confirmed || !completed || err != nil {
					t.Fatal("ordinary native completion refused")
				}
			} else if confirmed || completed || err != matrixReason("opencode-output-unconfirmed") {
				t.Fatal("completion preceded an included native text timestamp")
			}
		})
	}
}
