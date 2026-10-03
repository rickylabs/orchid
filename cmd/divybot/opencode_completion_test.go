package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openCodeCompletionFixture(t *testing.T) (*Coord, *Job, *bool, *bool, *int, *durableMatrixReceipt) {
	t.Helper()
	c, j, seatGone, processGone, closes := completionFixture(t)
	root, valid, r := openCodeBindingFixture(t)
	valid.OpenCode.Cwd = "/fixture/issue-7"
	*j = *valid
	j.SpawnedAt = time.Now().Add(-time.Minute)
	j.Deadline = time.Now().Add(time.Minute)
	id := j.OpenCode.SessionID
	if r.writeNativeIdentity(&id) != nil {
		t.Fatal("native binding")
	}
	c.cfg.Inbox = "fixture/inbox"
	c.cfg.Matrix.ReceiptRoot = root
	a := AgentInfo{Agent: j.Agent, Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, Cwd: j.OpenCode.Cwd, InteractiveReady: true, AgentStatus: "done", StateChangeSeq: 42}
	c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
		if *seatGone {
			return nil, nil
		}
		return []AgentInfo{a}, nil
	}
	c.actions.completed = nil
	bin := t.TempDir()
	native := openCode11834SummaryFixture(t, j.OpenCode, map[string]any{"diffs": []any{}}, false)
	t.Setenv("OPENCODE_COMPLETION_EXPORT", string(native))
	agents, _ := json.Marshal(map[string]any{"result": map[string]any{"type": "agent_info", "agent": a}})
	sessions, _ := json.Marshal([]openCodeSession{{ID: id, Directory: j.OpenCode.Cwd, Created: j.OpenCode.CreatedAt}})
	t.Setenv("OPENCODE_COMPLETION_AGENT", string(agents))
	t.Setenv("OPENCODE_COMPLETION_SESSIONS", string(sessions))
	script := "#!/usr/bin/env python3\nimport os,sys\ns=sys.argv[-1]\nif \"'export'\" in s: print(os.environ['OPENCODE_COMPLETION_EXPORT'])\nelif \"'session' 'list'\" in s: print(os.environ['OPENCODE_COMPLETION_SESSIONS'])\nelse: print(os.environ['OPENCODE_COMPLETION_AGENT'])\n"
	if os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700) != nil {
		t.Fatal("fixture ssh")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c.hosts = map[string]Host{j.Host: {Name: j.Host, SSH: "fixture-host"}}
	if c.st.save() != nil {
		t.Fatal("state")
	}
	return c, j, seatGone, processGone, closes, r
}

func TestOpenCodeNativeFinalStopRetiresBeforeOperatorTimeout(t *testing.T) {
	c, j, seatGone, processGone, closes, _ := openCodeCompletionFixture(t)
	if !openCodeBindingEligible(j) {
		t.Fatal("fixture not eligible")
	}
	_, native, bindingErr := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, "opencode")
	if bindingErr != nil {
		t.Fatal("fixture binding", bindingErr)
	}
	a, _ := c.actions.list(context.Background(), c.hosts[j.Host])
	if _, ok := completionOccupant(a, j, native); !ok {
		t.Fatal("fixture occupant")
	}
	if complete, err := c.completionEvidence(context.Background(), c.hosts[j.Host], j, native); !complete || err != nil {
		t.Fatal("native stop unavailable", err)
	}
	retired := c.retireCompleted(context.Background(), j.Issue, j, completionRef(j), true)
	if !retired || *closes != 1 {
		t.Fatalf("certified OpenCode stop has no success retirement before timeout: retired=%v closes=%d fence=%+v", retired, *closes, c.st.CompletedRuns[j.Issue])
	}
	if c.st.Jobs[j.Issue] == nil || c.st.CompletedRuns[j.Issue].Phase != "close-sent" {
		t.Fatal("close delivery falsely proved cleanup")
	}
	*seatGone = true
	c.retireCompleted(context.Background(), j.Issue, j, completionRef(j), true)
	if c.st.Jobs[j.Issue] == nil {
		t.Fatal("seat absence alone released process")
	}
	*processGone = true
	c.retireCompleted(context.Background(), j.Issue, j, completionRef(j), true)
	if c.st.Jobs[j.Issue] != nil || c.st.CompletedRuns[j.Issue].Phase != "observed" || c.st.reserveLaunch(j.Issue) {
		t.Fatal("paired cleanup lost no-replay fence")
	}
}

func TestOpenCodeCompletionCannotUsePaneStatusOrCommentAsNativeStop(t *testing.T) {
	for _, name := range []string{"streaming", "tool-continuation", "route", "prompt", "parent", "clock", "empty", "compaction", "native-missing", "native-foreign", "binding-foreign", "receipt-changed", "occupant", "working", "changed-sequence", "open-pr", "pending", "run-mode", "canceled"} {
		t.Run(name, func(t *testing.T) {
			c, j, _, _, closes, r := openCodeCompletionFixture(t)
			raw := os.Getenv("OPENCODE_COMPLETION_EXPORT")
			switch name {
			case "streaming":
				raw = strings.Replace(raw, `"completed":2002`, `"completed":0`, 1)
			case "tool-continuation":
				raw = strings.Replace(raw, `"finish":"stop"`, `"finish":"tool-calls"`, 1)
			case "route":
				raw = strings.ReplaceAll(raw, `"providerID":"fixture-provider"`, `"providerID":"foreign"`)
			case "prompt":
				raw = strings.Replace(raw, runPointer, "different", 1)
			case "parent":
				raw = strings.Replace(raw, `"parentID":"fixture-user"`, `"parentID":"foreign"`, 1)
			case "clock":
				raw = strings.Replace(raw, `"completed":2002`, `"completed":2000`, 1)
			case "empty":
				raw = strings.Replace(raw, `"text":"fixture answer"`, `"text":""`, 1)
			case "compaction":
				raw = strings.Replace(raw, `"summary":false`, `"summary":true`, 1)
			case "native-missing":
				_ = r.writeNativeIdentity(nil)
			case "native-foreign":
				id := "ses_foreign"
				_ = r.writeNativeIdentity(&id)
			case "binding-foreign":
				j.OpenCode.Route.Model = "foreign"
			case "receipt-changed":
				r.dispatch.Model = "foreign/model"
				_ = r.writeDispatch("dispatched", r.dispatch.Location)
			case "open-pr":
				c.actions.completionPR = func(context.Context, *Job) (bool, error) { return true, nil }
			case "pending":
				j.GoalDelivery = "pending"
			case "run-mode":
				j.RunMode = true
			}
			t.Setenv("OPENCODE_COMPLETION_EXPORT", raw)
			original := c.actions.list
			reads := 0
			c.actions.list = func(ctx context.Context, h Host) ([]AgentInfo, error) {
				a, e := original(ctx, h)
				reads++
				if len(a) > 0 {
					switch name {
					case "occupant":
						a[0].Name = "foreign"
					case "working":
						a[0].AgentStatus = "working"
					case "changed-sequence":
						a[0].StateChangeSeq += uint64(reads)
					}
				}
				return a, e
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			if c.retireCompleted(ctx, j.Issue, j, completionRef(j), true) || *closes != 0 || len(c.st.CompletedRuns) != 0 || c.st.Jobs[j.Issue] != j {
				t.Fatal("unproven OpenCode native completion retired")
			}
		})
	}
}

func TestOpenCodeDonePRWorkerKeepsEventRelayWithoutGoalReplay(t *testing.T) {
	c, j, _, _, closes, _ := openCodeCompletionFixture(t)
	j.PR = 8
	j.LastPoke = time.Now()
	c.actions.completionPR = func(context.Context, *Job) (bool, error) { return true, nil }
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal("fixture ssh")
	}
	callLog := filepath.Join(filepath.Dir(ssh), "relay-log")
	t.Setenv("OPENCODE_RELAY_LOG", callLog)
	script, _ := os.ReadFile(ssh)
	script = []byte(strings.Replace(string(script), "s=sys.argv[-1]\n", "s=sys.argv[-1]\nwith open(os.environ['OPENCODE_RELAY_LOG'],'a') as f: f.write(s+'\\n')\n", 1))
	if os.WriteFile(ssh, script, 0700) != nil {
		t.Fatal("fixture log")
	}
	gh := "#!/bin/sh\nprintf '%s\\n' '{\"number\":8,\"state\":\"OPEN\",\"mergeable\":\"MERGEABLE\",\"statusCheckRollup\":[{\"name\":\"check\",\"conclusion\":\"FAILURE\"}]}'\n"
	if os.WriteFile(filepath.Join(filepath.Dir(ssh), "gh"), []byte(gh), 0700) != nil {
		t.Fatal("fixture gh")
	}
	c.supervise(context.Background(), j.Issue, j, map[int]agentRef{j.Issue: completionRef(j)}, Issue{Number: j.Issue})
	commands, err := os.ReadFile(callLog)
	if err != nil || strings.Count(string(commands), "'agent' 'prompt'") != 1 || !strings.Contains(string(commands), "New activity on your PR") || strings.Contains(string(commands), "continue — work the assigned issue") || *closes != 0 || len(c.st.CompletedRuns) != 0 {
		t.Fatal("OpenCode completion intercepted PR relay or replayed the goal", err)
	}
}

func TestOpenCodeCompletionProofKeepsAuthorityAndDeadline(t *testing.T) {
	for _, name := range []string{"normal", "streaming", "unconfirmed", "error", "pending", "job-foreign", "binding-foreign", "changed-session", "changed-failure", "changed-binding", "changed-dispatch", "canceled-before", "canceled-after"} {
		t.Run(name, func(t *testing.T) {
			c, j, _, _, _, r := openCodeCompletionFixture(t)
			native := j.OpenCode.SessionID
			switch name {
			case "pending":
				j.GoalDelivery = "pending"
			case "job-foreign":
				j.OpenCode.SessionID = "ses_foreign"
			case "binding-foreign":
				foreign := "ses_foreign"
				_ = r.writeNativeIdentity(&foreign)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "canceled-before" {
				cancel()
			}
			observe := func(context.Context, *Job) (bool, bool, error) {
				switch name {
				case "streaming":
					return true, false, nil
				case "unconfirmed":
					return false, true, nil
				case "error":
					return true, true, errors.New("fixture")
				case "changed-session":
					j.OpenCode.SessionID = "ses_foreign"
				case "changed-failure":
					j.OpenCode.Failure = "opencode-output-unconfirmed"
				case "changed-binding":
					path := filepath.Join(filepath.Dir(r.file), "binding.json")
					var b map[string]any
					data, _ := os.ReadFile(path)
					_ = json.Unmarshal(data, &b)
					b["FutureProof"] = "changed"
					data, _ = json.Marshal(b)
					_ = os.WriteFile(path, data, 0600)
				case "changed-dispatch":
					r.dispatch.Profile = "foreign"
					_ = r.writeDispatch("dispatched", r.dispatch.Location)
				case "canceled-after":
					cancel()
				}
				return true, true, nil
			}
			complete, err := c.openCodeCompletionWithObserver(ctx, j, native, observe)
			if name == "normal" {
				if !complete || err != nil {
					t.Fatal("valid native completion unavailable", err)
				}
			} else if complete || err == nil {
				t.Fatal("changed native proof certified terminal completion")
			}
		})
	}
}

func TestOpenCodePositiveBindingLogFollowsDurableProofOnly(t *testing.T) {
	for _, name := range []string{"normal", "wrong-prompt", "canceled"} {
		t.Run(name, func(t *testing.T) {
			c, j, _, _, _, r := openCodeCompletionFixture(t)
			if r.writeNativeIdentity(nil) != nil {
				t.Fatal("unbound fixture")
			}
			if name == "wrong-prompt" {
				t.Setenv("OPENCODE_COMPLETION_EXPORT", strings.Replace(os.Getenv("OPENCODE_COMPLETION_EXPORT"), runPointer, "foreign prompt", 1))
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			var output bytes.Buffer
			prior := log.Writer()
			log.SetOutput(&output)
			defer log.SetOutput(prior)
			c.bindOpenCodeLiveIdentity(ctx, c.hosts[j.Host], j)
			_, id, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, "opencode")
			if err != nil {
				t.Fatal("binding readback")
			}
			if name == "normal" {
				if id != j.OpenCode.SessionID || strings.Count(output.String(), "OpenCode native identity bound") != 1 {
					t.Fatal("durable binding has no positive proof log")
				}
				c.bindOpenCodeLiveIdentity(ctx, c.hosts[j.Host], j)
				if strings.Count(output.String(), "OpenCode native identity bound") != 1 {
					t.Fatal("existing binding was claimed as another write")
				}
			} else if id != "" || strings.Contains(output.String(), "OpenCode native identity bound") {
				t.Fatal("unproven binding emitted positive proof")
			}
		})
	}
}
