package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateTestID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		t.Fatal("fixture identity unavailable")
	}
	return hex.EncodeToString(b[:])
}
func nativeStartFixture(t *testing.T, id string) map[string]any {
	t.Helper()
	return map[string]any{"type": "agent_started", "agent": map[string]any{
		"agent": "codex", "name": "fixture-agent", "pane_id": "w1:p1", "workspace_id": "w1", "interactive_ready": true,
		"agent_session": map[string]any{"source": "herdr:codex", "agent": "codex", "kind": "id", "value": id}}}
}
func fixtureJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal("fixture encoding failed")
	}
	return b
}

func TestNativeSessionAuthority(t *testing.T) {
	for _, test := range []string{"valid", "absent", "wrong-source", "wrong-agent", "path", "empty", "oversized", "kind", "name", "pane", "workspace", "not-ready", "wrong-response", "duplicate", "malformed", "unsupported"} {
		t.Run(test, func(t *testing.T) {
			id := privateTestID(t)
			v := nativeStartFixture(t, id)
			a := v["agent"].(map[string]any)
			s := a["agent_session"].(map[string]any)
			kind := "codex"
			switch test {
			case "absent":
				delete(a, "agent_session")
			case "wrong-source":
				s["source"] = "screen"
			case "wrong-agent":
				s["agent"] = "claude"
			case "path":
				s["kind"] = "path"
			case "empty":
				s["value"] = ""
			case "oversized":
				s["value"] = strings.Repeat("a", 257)
			case "kind":
				a["agent"] = "claude"
			case "name":
				a["name"] = "other-agent"
			case "pane":
				a["pane_id"] = "w2:p1"
			case "workspace":
				a["workspace_id"] = "w2"
			case "not-ready":
				a["interactive_ready"] = false
			case "wrong-response":
				v["type"] = "agent_info"
			case "unsupported":
				kind = "claude"
			}
			raw := fixtureJSON(t, v)
			if test == "duplicate" {
				raw = append([]byte(`{"type":"wrong",`), raw[1:]...)
			}
			if test == "malformed" {
				raw = []byte(`{"agent":`)
			}
			got, reason := nativeSessionFromStart(raw, kind, "fixture-agent", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"})
			if test == "valid" {
				if got != id || reason != "" {
					t.Fatal("authoritative native hook rejected")
				}
			} else if got != "" || reason == "" {
				t.Fatal("unauthoritative native identity accepted")
			}
		})
	}
}

func nativeFixtureHost(t *testing.T, mode, id string) Host {
	t.Helper()
	h, _ := registrationHost(t, "")
	t.Setenv("NATIVE_FIXTURE_MODE", mode)
	t.Setenv("NATIVE_FIXTURE_ID", id)
	script := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[1:];mode=os.environ['NATIVE_FIXTURE_MODE']
with open(os.environ['REGISTRATION_CALLS'],'a') as f:f.write(json.dumps(args)+'\n')
if args[:2]==['workspace','create']:
 print(json.dumps({'result':{'workspace':{'workspace_id':'w1'},'root_pane':{'pane_id':'w1:p1'}}}))
elif args[:2] in [['agent','start'],['agent','get']]:
 a={'agent':'codex','name':'fixture-agent','pane_id':'w1:p1','workspace_id':'w1','interactive_ready':True}
 if mode!='absent' and not (mode=='get' and args[1]=='start'):
  a['agent_session']={'agent':'codex','source':'herdr:codex','kind':'id','value':os.environ['NATIVE_FIXTURE_ID']}
 if mode=='failed-start':
  print(json.dumps({'error':{'code':'agent_not_ready','message':os.environ['NATIVE_FIXTURE_ID']}}));sys.exit(1)
 print(json.dumps({'result':{'type':'agent_started' if args[1]=='start' else 'agent_info','agent':a}}))
else:print(json.dumps({'result':{}}))
`
	if os.WriteFile(filepath.Join(h.Home, ".local", "bin", "herdr"), []byte(script), 0700) != nil {
		t.Fatal("herdr fixture unavailable")
	}
	return h
}

func TestNativeLaunchBinding(t *testing.T) {
	for _, mode := range []string{"native", "get", "absent", "failed-start"} {
		t.Run(mode, func(t *testing.T) {
			id := privateTestID(t)
			h := nativeFixtureHost(t, mode, id)
			r := registrationReceipt(t, "codex", Overrides{})
			var logs bytes.Buffer
			prior := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(prior)
			_, _, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil, "codex", Overrides{}, r)
			if (err != nil) != (mode == "failed-start") {
				t.Fatal("identity availability changed launch outcome")
			}
			raw, e := os.ReadFile(filepath.Join(filepath.Dir(r.file), "binding.json"))
			if e != nil {
				t.Fatal("binding unavailable")
			}
			var binding struct{ NativeSessionID *string }
			if json.Unmarshal(raw, &binding) != nil {
				t.Fatal("binding invalid")
			}
			success := mode == "native" || mode == "get"
			if success {
				if binding.NativeSessionID == nil || *binding.NativeSessionID != id {
					t.Fatal("successful launch lacks native binding")
				}
			} else if binding.NativeSessionID != nil {
				t.Fatal("inconclusive or failed launch published identity")
			}
			if !success && mode != "failed-start" && !strings.Contains(logs.String(), "INCONCLUSIVE reason=native-") {
				t.Fatal("missing closed inconclusive diagnostic")
			}
			public, e := os.ReadFile(filepath.Join(filepath.Dir(r.file), "dispatch.json"))
			if e != nil {
				t.Fatal("dispatch unavailable")
			}
			var dispatch map[string]any
			if json.Unmarshal(public, &dispatch) != nil || dispatch["parentRunId"] != nil {
				t.Fatal("public ancestry was repurposed")
			}
			if mode == "failed-start" && dispatch["state"] != "uncertain" {
				t.Fatal("failure lost uncertain state")
			}
			calls, _ := os.ReadFile(os.Getenv("REGISTRATION_CALLS"))
			for _, v := range []string{string(public), logs.String(), string(calls)} {
				if strings.Contains(v, id) {
					t.Fatal("native identity leaked to public record, log, or argv")
				}
			}
			st, _ := os.Stat(filepath.Join(filepath.Dir(r.file), "binding.json"))
			if st.Mode().Perm() != 0600 {
				t.Fatal("binding mode widened")
			}
		})
	}
}

func TestNativeBindingFailureAndInvalidation(t *testing.T) {
	for _, mode := range []string{"before-success", "owner-failure", "uncertain", "uncertain-owner-failure"} {
		t.Run(mode, func(t *testing.T) {
			r := registrationReceipt(t, "codex", Overrides{})
			id := privateTestID(t)
			loc := &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}
			if mode != "before-success" && r.writeDispatch("dispatched", loc) != nil {
				t.Fatal("fixture dispatch failed")
			}
			if mode == "owner-failure" {
				r.owner = &receiptOwner{chown: func(string, int, int) error { return os.ErrPermission }}
			}
			e := r.writeNativeIdentity(&id)
			if mode == "uncertain" || mode == "uncertain-owner-failure" {
				if e != nil {
					t.Fatal("fixture native binding failed")
				}
				if mode == "uncertain-owner-failure" {
					r.owner = &receiptOwner{chown: func(string, int, int) error { return os.ErrPermission }}
				}
				if e := r.writeDispatch("uncertain", loc); (e != nil) != (mode == "uncertain-owner-failure") {
					t.Fatal("uncertain transition verdict incorrect")
				}
			} else if e == nil {
				t.Fatal("failed or premature native publication accepted")
			}
			raw, _ := os.ReadFile(filepath.Join(filepath.Dir(r.file), "binding.json"))
			if bytes.Contains(raw, []byte("NativeSessionID")) {
				t.Fatal("failed or uncertain launch retained identity")
			}
		})
	}
}
