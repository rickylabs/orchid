package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRemoteControlPrivateConsumerFixtures(t *testing.T) {
	var schema struct {
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	body, err := os.ReadFile("remote-control.schema.json")
	if err != nil || json.Unmarshal(body, &schema) != nil || schema.AdditionalProperties || len(schema.Required) != 13 || len(schema.Properties) != 13 {
		t.Fatal("closed private schema unavailable")
	}
	for _, name := range []string{"chatgpt-connected", "claude-connected-unnamed", "chatgpt-unconfirmed"} {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata", "remote-control", name+".json"))
			var fixture remoteControlObservation
			var fields map[string]json.RawMessage
			if err != nil || strictJSON(body, &fixture) != nil || strictJSON(body, &fields) != nil || len(fields) != len(schema.Required) {
				t.Fatal("private reader fixture changed schema")
			}
			for _, key := range schema.Required {
				if _, ok := fields[key]; !ok {
					t.Fatal("required private field absent")
				}
			}
			at, e1 := time.Parse(time.RFC3339Nano, fixture.ObservedAt)
			until, e2 := time.Parse(time.RFC3339Nano, fixture.ValidUntil)
			if e1 != nil || e2 != nil || until.Sub(at) != remoteControlFreshness || fixture.Link != nil {
				t.Fatal("fixture freshness/target semantics changed")
			}
			kind := "codex"
			if fixture.Vendor == "claude" {
				kind = "claude"
			}
			r := registrationReceipt(t, kind, Overrides{})
			if r.writeDispatch("dispatched", &dispatchLocation{PaneID: fixture.PaneID, WorkspaceID: fixture.WorkspaceID}) != nil {
				t.Fatal("fixture dispatch unavailable")
			}
			run := syntheticRemoteRun(t)
			run.NativeSessionID = fixture.NativeSessionID
			reason := ""
			if fixture.Reason != nil {
				reason = *fixture.Reason
			}
			if writeRemoteObservation(context.Background(), r, kind, run, fixture.State, reason, fixture.SessionName, nil) != nil {
				t.Fatal("producer refused reader fixture semantics")
			}
			out, _ := os.ReadFile(filepath.Join(filepath.Dir(r.file), "remote-control.json"))
			var actual remoteControlObservation
			if strictJSON(out, &actual) != nil {
				t.Fatal("producer private schema changed")
			}
			actual.RunID, actual.Host, actual.ObservedAt, actual.ValidUntil = fixture.RunID, fixture.Host, fixture.ObservedAt, fixture.ValidUntil
			if !reflect.DeepEqual(actual, fixture) {
				t.Fatal("consumer fixture drifted from exact producer envelope")
			}
		})
	}
}
