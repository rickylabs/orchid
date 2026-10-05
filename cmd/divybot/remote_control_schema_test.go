package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// schemaViolations validates v against the subset of JSON Schema 2020-12 that
// remote-control.schema.json uses. Any other keyword fails the test, so the
// validator can never silently pass a schema it does not understand.
func schemaViolations(t *testing.T, schema map[string]any, v any, at string) []string {
	t.Helper()
	var out []string
	bad := func(format string, args ...any) { out = append(out, at+": "+fmt.Sprintf(format, args...)) }
	for key := range schema {
		switch key {
		case "$schema", "title", "description", "type", "const", "enum", "pattern", "minLength", "maxLength",
			"format", "required", "additionalProperties", "properties", "allOf", "if", "then":
		default:
			t.Fatalf("schema keyword %q at %s is not supported by this validator", key, at)
		}
	}
	if types, ok := schema["type"]; ok {
		names := []string{}
		switch x := types.(type) {
		case string:
			names = append(names, x)
		case []any:
			for _, n := range x {
				names = append(names, n.(string))
			}
		}
		matched := false
		for _, name := range names {
			switch name {
			case "null":
				matched = matched || v == nil
			case "string":
				_, is := v.(string)
				matched = matched || is
			case "object":
				_, is := v.(map[string]any)
				matched = matched || is
			case "integer":
				f, is := v.(float64)
				matched = matched || (is && f == math.Trunc(f))
			default:
				t.Fatalf("schema type %q at %s is not supported by this validator", name, at)
			}
		}
		if !matched {
			bad("type %v", names)
		}
	}
	if c, ok := schema["const"]; ok && !reflect.DeepEqual(c, v) {
		bad("const")
	}
	if e, ok := schema["enum"]; ok {
		found := false
		for _, x := range e.([]any) {
			found = found || reflect.DeepEqual(x, v)
		}
		if !found {
			bad("enum")
		}
	}
	if str, is := v.(string); is {
		if p, ok := schema["pattern"].(string); ok && !regexp.MustCompile(p).MatchString(str) {
			bad("pattern")
		}
		if n, ok := schema["minLength"].(float64); ok && utf8.RuneCountInString(str) < int(n) {
			bad("minLength")
		}
		if n, ok := schema["maxLength"].(float64); ok && utf8.RuneCountInString(str) > int(n) {
			bad("maxLength")
		}
		if f, ok := schema["format"].(string); ok {
			if f != "date-time" {
				t.Fatalf("format %q at %s is not supported by this validator", f, at)
			}
			if _, err := time.Parse(time.RFC3339Nano, str); err != nil {
				bad("format")
			}
		}
	}
	if obj, is := v.(map[string]any); is {
		props, _ := schema["properties"].(map[string]any)
		if req, ok := schema["required"].([]any); ok {
			for _, k := range req {
				if _, present := obj[k.(string)]; !present {
					bad("required %s", k)
				}
			}
		}
		if extra, ok := schema["additionalProperties"]; ok && extra == false {
			for k := range obj {
				if _, known := props[k]; !known {
					bad("additional property")
				}
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if sub, ok := props[k].(map[string]any); ok {
				out = append(out, schemaViolations(t, sub, obj[k], at+"."+k)...)
			}
		}
	}
	if all, ok := schema["allOf"].([]any); ok {
		for _, sub := range all {
			out = append(out, schemaViolations(t, sub.(map[string]any), v, at)...)
		}
	}
	if cond, ok := schema["if"].(map[string]any); ok && len(schemaViolations(t, cond, v, at)) == 0 {
		if then, ok := schema["then"].(map[string]any); ok {
			out = append(out, schemaViolations(t, then, v, at)...)
		}
	}
	return out
}

func loadRemoteControlSchema(t *testing.T) map[string]any {
	t.Helper()
	var schema map[string]any
	body, err := os.ReadFile("remote-control.schema.json")
	if err != nil || json.Unmarshal(body, &schema) != nil {
		t.Fatal("published schema unavailable")
	}
	return schema
}

// Rows the production writer actually produces, and the consumer fixtures,
// validate against the published schema; the validator itself refuses
// violations (so it is not vacuous).
func TestRemoteControlProducedRowsMatchPublishedSchema(t *testing.T) {
	schema := loadRemoteControlSchema(t)
	link := "https://claude.ai/code/" + fixtureBridge
	run := syntheticRemoteRun(t)
	name := run.Name
	for _, tc := range []struct {
		name, kind, state, reason string
		sessionName, link         *string
	}{
		{"claude-linked", "claude", "unconfirmed", "remote-control-unconfirmed", nil, &link},
		{"claude-unlinked", "claude", "unconfirmed", "remote-control-unconfirmed", nil, nil},
		{"codex-connected", "codex", "connected", "", &name, nil},
		{"codex-unconfirmed", "codex", "unconfirmed", "remote-control-unconfirmed", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := registrationReceipt(t, tc.kind, Overrides{})
			r.dispatch.Host = "fixture-host"
			if err := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); err != nil {
				t.Fatal("fixture dispatch")
			}
			if err := writeRemoteObservation(context.Background(), r, tc.kind, run, tc.state, tc.reason, tc.sessionName, tc.link); err != nil {
				t.Fatalf("control: writer refused a valid row: %v", err)
			}
			body, err := os.ReadFile(filepath.Join(filepath.Dir(r.file), "remote-control.json"))
			var row any
			if err != nil || json.Unmarshal(body, &row) != nil {
				t.Fatal("produced row unreadable")
			}
			if v := schemaViolations(t, schema, row, "$"); len(v) != 0 { // guard:schema-produced-row
				t.Fatalf("produced row violates the published schema: %s", strings.Join(v, "; "))
			}
		})
	}
	fixtures, _ := filepath.Glob(filepath.Join("testdata", "remote-control", "*.json"))
	if len(fixtures) != 4 {
		t.Fatalf("expected 4 consumer fixtures, found %d", len(fixtures))
	}
	for _, path := range fixtures {
		body, _ := os.ReadFile(path)
		var row any
		if json.Unmarshal(body, &row) != nil || len(schemaViolations(t, schema, row, "$")) != 0 {
			t.Fatalf("consumer fixture %s violates the published schema", filepath.Base(path))
		}
	}
	// Negative controls: each mutation of a valid row must be refused.
	base := func() map[string]any {
		var row map[string]any
		body, _ := os.ReadFile(filepath.Join("testdata", "remote-control", "claude-unconfirmed-linked.json"))
		_ = json.Unmarshal(body, &row)
		return row
	}
	for name, mutate := range map[string]func(map[string]any){
		"http-link":        func(r map[string]any) { r["link"] = "http://claude.ai/code/" + fixtureBridge },
		"other-host":       func(r map[string]any) { r["link"] = "https://example.com/code/" + fixtureBridge },
		"query":            func(r map[string]any) { r["link"] = "https://claude.ai/code/" + fixtureBridge + "?x=1" },
		"non-string-link":  func(r map[string]any) { r["link"] = 7.0 },
		"chatgpt-link":     func(r map[string]any) { r["vendor"] = "chatgpt" },
		"claude-connected": func(r map[string]any) { r["state"], r["reason"] = "connected", nil },
		"extra-key":        func(r map[string]any) { r["linkReason"] = "x" },
		"missing-link":     func(r map[string]any) { delete(r, "link") },
	} {
		row := base()
		if len(schemaViolations(t, schema, row, "$")) != 0 {
			t.Fatal("control: linked fixture invalid")
		}
		mutate(row)
		if len(schemaViolations(t, schema, row, "$")) == 0 {
			t.Fatalf("schema accepted %s", name)
		}
	}
}
