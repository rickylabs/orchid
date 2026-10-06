package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemoteControlLoadedDescendants(t *testing.T) {
	for _, mode := range []string{"ephemeral", "foreign-tree", "active-ephemeral", "missing-cursor", "duplicate", "missing-parent", "foreign-parent", "changed-tree", "late-child"} {
		t.Run(mode, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			child, foreign := privateTestID(t), privateTestID(t)
			base := remoteIdleHandler(run, "valid")
			reads, loaded := 0, 0
			p, w := remoteFixturePort(func(method string, params map[string]any) any {
				if method == "thread/loaded/list" {
					loaded++
					data := []string{run.NativeSessionID, child}
					if mode == "duplicate" {
						data = append(data, child)
					}
					if mode == "late-child" && loaded > 1 {
						data = append(data, foreign)
					}
					row := map[string]any{"data": data, "nextCursor": nil}
					if mode == "missing-cursor" {
						delete(row, "nextCursor")
					}
					return row
				}
				if method == "thread/read" && params["threadId"] != run.NativeSessionID {
					v := remoteThreadFixture(run)
					row := v["thread"].(map[string]any)
					row["id"], row["parentThreadId"] = params["threadId"], run.NativeSessionID
					if mode == "foreign-tree" {
						row["sessionId"] = foreign
					}
					if mode == "missing-parent" {
						delete(row, "parentThreadId")
					}
					if mode == "foreign-parent" {
						row["parentThreadId"] = foreign
					}
					if mode == "active-ephemeral" {
						row["status"] = map[string]string{"type": "active"}
					}
					if mode == "changed-tree" {
						reads++
						if reads > 1 {
							row["parentThreadId"] = foreign
						}
					}
					return v
				}
				return base(method, params)
			}, run.NativeSessionID)
			err := p.remoteWorkIdle(false)
			if (err == nil) != (mode == "ephemeral" || mode == "foreign-tree") {
				t.Fatal("loaded native descendant proof failed")
			}
			if mode == "foreign-tree" {
				for _, request := range w.requests {
					if request == "thread/goal/get" {
						t.Fatal("foreign loaded thread gained work authority")
					}
				}
			}
		})
	}
}

func TestRemoteControlArchivedChildNeverProvesShutdown(t *testing.T) {
	run := syntheticRemoteRun(t)
	child := privateTestID(t)
	base := remoteIdleHandler(run, "valid")
	p, _ := remoteFixturePort(func(method string, params map[string]any) any {
		if method == "thread/list" && params["archived"] == true {
			return map[string]any{"data": []any{map[string]string{"id": child, "parentThreadId": run.NativeSessionID}}, "nextCursor": nil}
		}
		if method == "thread/read" && params["threadId"] == child {
			v := remoteThreadFixture(run)
			v["thread"].(map[string]any)["id"] = child
			v["thread"].(map[string]any)["parentThreadId"] = run.NativeSessionID
			v["thread"].(map[string]any)["status"] = map[string]string{"type": "notLoaded"}
			return v
		}
		return base(method, params)
	}, run.NativeSessionID)
	if p.remoteWorkIdle(false) == nil {
		t.Fatal("archived child certified shutdown without native terminal work proof")
	}
}

func TestRemoteControlTeardownNeedsAttachedIdentity(t *testing.T) {
	c, _, j, _, _ := teardownFixture(t)
	j.Agent = "codex"
	j.RemoteControl = syntheticRemoteRun(t)
	j.RemoteControl.NativeSessionID = "thread-fixture"
	j.RemoteControl.Cwd = "/fixture/issue-7"
	expected := *j.RemoteControl
	h := canonicalFixtureHost(t, "valid", remoteIdleHandler(&expected, "valid"))
	h.Name = j.Host
	c.hosts[j.Host] = h
	closes := 0
	c.actions.close = func(context.Context, Host, string) error { closes++; return nil }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.teardown(ctx, j.Issue, j, "teardown")
	if closes != 0 || c.st.Jobs[j.Issue] == nil {
		t.Fatal("healthy daemon work proof licensed closing an unproven TUI occupant")
	}
}

func TestRemoteControlNeverAnswersTrustConsent(t *testing.T) {
	s := promptFixture()
	s.Screen = "Trust this folder?\n1. Trust and continue\n2. No, quit\n›"
	submits, accepted := 0, 0
	err := deliverCodexPrompt(context.Background(), s.Agent, promptFixtureGoal, promptCalls{
		observe: func(context.Context) (promptSnapshot, error) { return s, nil },
		submit:  func(context.Context, string) error { submits++; return nil },
		wait:    func(context.Context) bool { return false },
		native: func(ctx context.Context, _ string, submit func(context.Context) error) error {
			accepted++
			return submit(ctx)
		},
	})
	if err == nil || submits != 0 || accepted != 0 {
		t.Fatal("managed remote launch answered consent")
	}
}

// Reuse the actual completion fence and paired cleanup observations. Claude
// has no independent daemon work; Codex's separate native quiescence controls
// cover its additional stop gate.
func TestRemoteControlRetirementRetainsCapacityAcrossRestart(t *testing.T) {
	c, j, seatGone, processGone, closes := completionFixture(t)
	j.Agent, j.Label = "claude", "claude-fixture"
	j.RemoteControl = syntheticRemoteRun(t)
	j.RemoteControl.NativeSessionID = "thread-fixture"
	j.RemoteControl.Cwd = completionAgent(j).Cwd
	path := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record", "dispatch.json")
	var dispatch dispatchBinding
	if readPrivateActionJSON(path, &dispatch) != nil {
		t.Fatal("fixture unavailable")
	}
	dispatch.Source = j.Agent
	body, _ := json.Marshal(dispatch)
	if os.WriteFile(path, body, 0600) != nil {
		t.Fatal("fixture unavailable")
	}
	c.cfg.Targets = []Target{{Agent: j.Agent}}
	c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
		if *seatGone {
			return nil, nil
		}
		a := completionAgent(j)
		a.AgentSession = json.RawMessage(`{"agent":"claude","kind":"id","source":"herdr:claude","value":"thread-fixture"}`)
		return []AgentInfo{a}, nil
	}
	ref := completionRef(j)
	if !c.retireCompleted(context.Background(), j.Issue, j, ref, true) || *closes != 1 {
		t.Fatal("remote retirement did not fence and close")
	}
	if c.admissionBudget(map[int]agentRef{j.Issue: ref})[j.Agent] != 0 {
		t.Fatal("remote close delivery released capacity")
	}
	c.st = loadState(c.st.path)
	j = c.st.Jobs[j.Issue]
	*seatGone = true
	c.retireCompleted(context.Background(), j.Issue, j, ref, true)
	if c.st.Jobs[j.Issue] == nil {
		t.Fatal("remote seat absence alone released capacity")
	}
	*processGone = true
	c.retireCompleted(context.Background(), j.Issue, j, ref, true)
	if c.st.Jobs[j.Issue] != nil || c.st.CompletedRuns[j.Issue].Phase != "observed" || *closes != 1 {
		t.Fatal("remote paired absence did not release exactly once")
	}
	if c.admissionBudget(map[int]agentRef{})[j.Agent] != 1 {
		t.Fatal("remote paired cleanup did not restore capacity")
	}
	if c.st.reserveLaunch(j.Issue) {
		t.Fatal("remote completed run replayed after cleanup")
	}
}
