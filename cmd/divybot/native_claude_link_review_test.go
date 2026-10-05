package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Review o89 probes, adopted as guards. The production record reader seeds the
// cache; then the native occupant is replaced (same placement and label,
// another native session) or ended while the next off-path process lookup is
// pending. The published row must not keep a live link.
func TestReviewCachedLinkRevocation(t *testing.T) {
	for _, mode := range []string{"replaced", "ended"} {
		t.Run(mode, func(t *testing.T) {
			r := registrationReceipt(t, "claude", Overrides{})
			r.dispatch.Host = "fixture-host"
			if err := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(filepath.Dir(filepath.Dir(r.file)))
			run := syntheticRemoteRun(t)
			j := &Job{Issue: 7, Agent: "claude", Repo: "fixture/repo", Label: "fixture-agent", Host: "fixture-host", Pane: "w1:p1", Workspace: "w1", DispatchKey: strings.Repeat("d", 64), RemoteControl: run}
			binding, _ := json.Marshal(map[string]string{"Repo": j.Repo, "NativeSessionID": run.NativeSessionID})
			if err := os.WriteFile(filepath.Join(filepath.Dir(r.file), "binding.json"), binding, 0600); err != nil {
				t.Fatal(err)
			}
			rec := map[string]any{"pid": os.Getpid(), "sessionId": run.NativeSessionID, "procStart": procStart(t, os.Getpid()), "bridgeSessionId": fixtureBridge}
			h := claudeBridgeHost(t, []int{os.Getpid()}, map[int]any{os.Getpid(): rec})
			writeBridgeTranscript(t, h.Home, run.Cwd, run.NativeSessionID, bridgeStatus(run.NativeSessionID, "https://claude.ai/code/"+fixtureBridge))
			c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, shadow: newNativeEvidenceShadow(time.Now), claudeLinks: &claudeLinkStore{}}
			h.ShadowScope, h.ClaudeLinks = c.shadow.scope(j), c.claudeLinks
			h.readClaudeBridge(j)
			if c.claudeLinks.fresh(j.DispatchKey, run.NativeSessionID, time.Now()) == nil {
				t.Fatal("control: production bound read did not seed link")
			}
			replacement := claudeAgentInfo(t, syntheticRemoteRun(t).NativeSessionID)
			a := replacement["agent"].(map[string]any)
			a["cwd"], a["agent_status"], a["state_change_seq"] = run.Cwd, "idle", 1
			info, _ := json.Marshal(map[string]any{"result": replacement})
			release := filepath.Join(h.Home, "release-process-read")
			recordPath := filepath.Join(h.Home, ".claude", "sessions", fmt.Sprintf("%d.json", os.Getpid()))
			if err := os.Remove(recordPath); err != nil {
				t.Fatal(err)
			}
			script := "#!/usr/bin/env python3\nimport os,sys,time\na=sys.argv[1:]\nif a[:2]==['pane','process-info']:\n while not os.path.exists(" + fmt.Sprintf("%q", release) + "): time.sleep(0.01)\n print('{\"result\":{\"process_info\":{\"foreground_processes\":[]}}}')\nelif a[:2]==['agent','get']:\n"
			if mode == "ended" {
				script += " sys.exit(1)\n"
			} else {
				script += " print(" + fmt.Sprintf("%q", string(info)) + ")\n"
			}
			script += "else: sys.exit(1)\n"
			writeFixture(t, filepath.Join(h.Home, ".local", "bin", "herdr"), script)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			c.observeRemoteControl(ctx, h, j)
			cancel()
			// Before the off-path read finishes, the failed identity alone must
			// already have withdrawn the cached link and the published row.
			if c.claudeLinks.fresh(j.DispatchKey, run.NativeSessionID, time.Now()) != nil { // guard:identity-loss-forgets-now
				t.Error("a lost native identity kept its cached link")
			}
			if b, err := os.ReadFile(filepath.Join(filepath.Dir(r.file), "remote-control.json")); err == nil {
				var row remoteControlObservation
				if json.Unmarshal(b, &row) != nil || row.Link != nil {
					t.Error("a lost native identity published a link")
				}
			}
			if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			c.shadow.bg.Wait()
			if c.claudeLinks.fresh(j.DispatchKey, run.NativeSessionID, time.Now()) != nil {
				t.Fatal("control: failed read did not clear memory cache")
			}
			out, err := os.ReadFile(filepath.Join(filepath.Dir(r.file), "remote-control.json"))
			if os.IsNotExist(err) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var row remoteControlObservation
			if err := json.Unmarshal(out, &row); err != nil {
				t.Fatal(err)
			}
			until, err := time.Parse(time.RFC3339Nano, row.ValidUntil)
			if err != nil {
				t.Fatal(err)
			}
			if row.Link != nil && time.Now().Before(until) {
				t.Errorf("a %s native session retains a live link on disk: state=%s remaining-validity=%s (private identifiers omitted)", mode, row.State, time.Until(until).Round(time.Millisecond))
			}
		})
	}
}

func TestReviewObservationURLValidation(t *testing.T) {
	r := registrationReceipt(t, "claude", Overrides{})
	r.dispatch.Host = "fixture-host"
	if err := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); err != nil {
		t.Fatal(err)
	}
	run := syntheticRemoteRun(t)
	good := "https://claude.ai/code/" + fixtureBridge
	bad := []string{"", "null", "https://claude.ai/code/session_", "https://claude.ai/code/session_A/other", "https://claude.ai/code/session_A#fragment", "https://claude.ai/code/session_A?token=x", "https://claude.ai:443/code/session_A", "https://claude.ai.example.com/code/session_A", "https://user@claude.ai/code/session_A", "https://claude.ai/code/session_A\n", "https://claude.ai/code/session_%41", "https://claude.ai/code/session_" + strings.Repeat("A", 129), "http://claude.ai/code/session_A"}
	for i, target := range bad {
		t.Run(fmt.Sprintf("invalid-%02d", i), func(t *testing.T) {
			if err := writeRemoteObservation(context.Background(), r, "claude", run, "unconfirmed", "remote-control-unconfirmed", nil, &target); err == nil {
				t.Fatal("invalid target was accepted")
			}
		})
	}
	if err := writeRemoteObservation(context.Background(), r, "claude", run, "unconfirmed", "remote-control-unconfirmed", nil, &good); err != nil {
		t.Fatal("control: valid origin refused", err)
	}
	if err := writeRemoteObservation(context.Background(), r, "claude", run, "unconfirmed", "remote-control-unconfirmed", nil, nil); err != nil {
		t.Fatal("control: absent link refused", err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(r.file), "remote-control.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"link":null`) {
		t.Fatal("absent link is not null")
	}
}

// The structured read response itself must obey its cross-field checks.
func TestReviewBridgeResponseConsistency(t *testing.T) {
	pid := os.Getpid()
	url := "https://claude.ai/code/" + fixtureBridge
	resp := func(bridge, transcript, u string) string {
		return fmt.Sprintf(`{"matches":1,"malformed":false,"pid":%d,"start":"1","bridge":%q,"transcript":%q,"url":%q}`, pid, bridge, transcript, u)
	}
	for _, tc := range []struct {
		name, response string
		ok             bool
	}{
		{"url-without-bridge", resp("", "entry", url), false},
		{"entry-without-url", resp(fixtureBridge, "entry", ""), false},
		{"url-without-entry", resp(fixtureBridge, "none", url), false},
		{"unknown-transcript-state", resp(fixtureBridge, "maybe", ""), false},
		{"bridge-wrong-form", resp("bridge_x", "none", ""), false},
		{"unbound-pid", fmt.Sprintf(`{"matches":1,"malformed":false,"pid":0,"start":"","bridge":"","transcript":"none","url":""}`), false},
		{"unknown-field", `{"matches":1,"malformed":false,"pid":7,"start":"1","bridge":"","transcript":"none","url":"","present":true}`, false},
		{"consistent-present", resp(fixtureBridge, "entry", url), true},
		{"consistent-absent", resp("", "none", ""), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sid := syntheticRemoteRun(t).NativeSessionID
			h := claudeBridgeHost(t, []int{pid}, nil)
			bin := filepath.Join(h.Home, "response-bin")
			writeFixture(t, filepath.Join(bin, "python3"), "#!/bin/sh\nprintf '%s\\n' '"+tc.response+"'\n")
			if err := os.Chmod(filepath.Join(bin, "python3"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			read, reason := h.claudeBridgeSession(context.Background(), "w1:p1", sid, "")
			if tc.ok != (reason == "") || (!tc.ok && read != (claudeBridgeRead{})) {
				t.Fatalf("response accepted=%v, want %v", reason == "", tc.ok)
			}
		})
	}
}
