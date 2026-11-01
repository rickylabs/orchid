package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Uses the real canonical socket, private binding and Herdr workspace protocol.
func nativeCapacityFixture(t *testing.T, mode string) (*Coord, *Job, Host, agentRef, string, *atomic.Int32) {
	t.Helper()
	c, j, _, _, _ := completionFixture(t)
	r := syntheticRemoteRun(t)
	r.NativeSessionID, r.Cwd = "thread-fixture", completionAgent(j).Cwd
	r.ClientVersion = canonicalFixtureVersion
	r.Resume = &codexResumeProof{Producer: strings.Repeat("a", 64), Connection: "1"}
	j.RemoteControl, j.NativeSessionID = r, r.NativeSessionID
	j.NativeGoal = &dispatchGoal{ReceiptKey: j.DispatchKey, Intent: fixtureIntent(), Owned: true, PromptConfirmed: true}
	j.GoalDeliveryCommit = deliveryCommitMark
	j.SpawnedAt = time.Now().Add(-time.Hour)
	var loaded atomic.Bool
	loaded.Store(mode != "unloaded")
	archives := &atomic.Int32{}
	base := remoteIdleHandler(r, mode)
	h := canonicalFixtureHost(t, "multi", func(method string, params map[string]any) any {
		switch method {
		case "thread/read":
			v := base(method, params).(map[string]any)
			if !loaded.Load() {
				v["thread"].(map[string]any)["status"] = map[string]string{"type": "notLoaded"}
			}
			return v
		case "thread/goal/get":
			g := fixtureGoal("complete")
			g.ThreadID = r.NativeSessionID
			if mode == "active-goal" {
				g.Status = "active"
			}
			return map[string]any{"goal": g}
		case "thread/turns/list":
			if mode == "streaming" {
				return base(method, params)
			}
			return map[string]any{"data": []any{map[string]any{"id": "fixture-turn", "status": "completed", "itemsView": "full", "completedAt": time.Now().Unix() - 1,
				"items": []any{map[string]string{"type": "agentMessage", "phase": "final_answer", "text": "Synthetic final response"}}}}}
		case "thread/loaded/list":
			data := []string{}
			if loaded.Load() {
				data = append(data, r.NativeSessionID)
			}
			return map[string]any{"data": data, "nextCursor": nil}
		case "thread/archive":
			archives.Add(1)
			if mode != "archive-stuck" {
				loaded.Store(false)
			}
			return map[string]any{}
		}
		return base(method, params)
	})
	h.Name = j.Host
	h.Capacity = 1
	c.hosts[j.Host], c.cfg.Hosts = h, []Host{h}
	seat := filepath.Join(h.Home, "seats.json")
	if err := os.WriteFile(seat, []byte(`[]`), 0600); err != nil {
		t.Fatal(err)
	}
	a := completionAgent(j)
	payload, _ := json.Marshal(map[string]any{"result": map[string]any{"type": "agent_info", "agent": a}})
	if err := os.MkdirAll(filepath.Join(h.Home, ".local", "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/usr/bin/env python3\nimport json,sys\na=sys.argv[1:]\nif a[:2]==['workspace','list']:\n print(json.dumps({'result':{'type':'workspace_list','workspaces':json.load(open(" + strconvQuote(seat) + "))}}))\nelif a[:2]==['agent','get']:\n print(" + strconvQuote(string(payload)) + ")\nelse:sys.exit(1)\n"
	if err := os.WriteFile(filepath.Join(h.Home, ".local", "bin", "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c.actions.completionPR = func(context.Context, *Job) (bool, error) { return true, nil }
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	ref := completionRef(j)
	ref.StateChangeSeq = a.StateChangeSeq
	return c, j, h.forJob(j), ref, seat, archives
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func ageRemoteMiss(j *Job) { j.LastMissAt = time.Now().Add(-time.Minute) }

func TestRemoteRunReaperReleasesOnlyNativeInactive(t *testing.T) {
	for _, mode := range []string{"valid", "unloaded"} {
		t.Run(mode, func(t *testing.T) {
			c, j, h, _, _, archives := nativeCapacityFixture(t, mode)
			c.actions.stopProcess = func(context.Context, Host, string, string) (*actionStopProcess, error) {
				t.Fatal("reaper used process ancestry")
				return nil, nil
			}
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			for miss := 1; miss <= deadMissThreshold; miss++ {
				ageRemoteMiss(j)
				c.reapRemoteRuns(context.Background(), nil, map[string]bool{h.observerKey(): true}, map[int]bool{j.Issue: true})
				if miss < deadMissThreshold && c.st.Jobs[j.Issue] == nil {
					t.Fatal("absence debounce bypassed")
				}
			}
			if c.st.Jobs[j.Issue] != nil {
				t.Fatal("native-inactive vanished seat retained capacity")
			}
			restarted := loadState(c.st.path)
			if restarted.Jobs[j.Issue] != nil || restarted.LaunchBlocks[j.Issue].Reason != "agent_disappeared" || restarted.reserveLaunch(j.Issue) {
				t.Fatal("release was not durably fenced")
			}
			if got := logs.String(); strings.Count(got, "capacity released evidence=herdr-workspace-and-label-absent,codex-native-tree-unloaded") != 1 {
				t.Fatal("release evidence log missing or duplicated", got)
			}
			if mode == "valid" && archives.Load() != 1 || mode == "unloaded" && archives.Load() != 0 {
				t.Fatal("native resource release was not idempotent")
			}
		})
	}
}

func TestRemoteRunReaperKeepsActiveAndUnknown(t *testing.T) {
	for _, mode := range []string{"active", "active-goal", "queue", "background", "streaming", "paged", "runtime-unreachable", "workspace-present", "same-label", "workspace-unreadable", "host-down", "completed-owner", "stop-owner", "cleanup-owner", "recovery-owner", "save-fails"} {
		t.Run(mode, func(t *testing.T) {
			c, j, h, _, seats, archives := nativeCapacityFixture(t, mode)
			j.Misses = deadMissThreshold - 1
			up := map[string]bool{h.observerKey(): true}
			switch mode {
			case "runtime-unreachable":
				os.Remove(filepath.Join(h.Home, ".codex", "app-server-control", "app-server-control.sock"))
			case "workspace-present":
				os.WriteFile(seats, goalJSON([]any{map[string]string{"workspace_id": j.Workspace, "label": "other"}}), 0600)
			case "same-label":
				os.WriteFile(seats, goalJSON([]any{map[string]string{"workspace_id": "other", "label": j.Label}}), 0600)
			case "workspace-unreadable":
				os.WriteFile(seats, []byte(`null`), 0600)
			case "host-down":
				up[h.observerKey()] = false
			case "completed-owner":
				c.st.CompletedRuns = map[int]completedRun{j.Issue: {DispatchKey: j.DispatchKey}}
			case "stop-owner":
				j.RemoteStopOperation = "synthetic-stop"
			case "cleanup-owner":
				j.RemoteCleanup = "operator-timeout"
			case "recovery-owner":
				j.GoalRecovery = &goalRecovery{State: recoveryExhausted}
			case "save-fails":
				c.st.path = filepath.Join(t.TempDir(), "missing", "state.json")
			}
			ageRemoteMiss(j)
			c.reapRemoteRuns(context.Background(), nil, up, map[int]bool{j.Issue: true})
			if c.st.Jobs[j.Issue] != j || archives.Load() != 0 {
				t.Fatal("active, unconfirmed or foreign-owned work was released")
			}
			if c.activeByAccount()["codex"] != 1 {
				t.Fatal("retained native work stopped counting")
			}
		})
	}
}

func TestRemoteRunDoneReleasesAccountRetainsRecord(t *testing.T) {
	c, j, h, ref, _, _ := nativeCapacityFixture(t, "valid")
	c.st.SourceBindings = map[int]*sourceBinding{j.Issue: {Repo: "example/source", Issue: 9, Comment: 7}}
	c.refreshRunCapacity(map[int]agentRef{j.Issue: ref})
	if c.retireCompleted(context.Background(), j.Issue, j, ref, true) {
		t.Fatal("pending PR retired the warm run")
	}
	if !nativeCapacityDoneForRef(j, ref, true) || c.activeByAccount()["codex"] != 0 || c.admissionBudget(map[int]agentRef{j.Issue: ref})["codex"] != 1 {
		t.Fatal("native done did not release account capacity")
	}
	if c.st.Jobs[j.Issue] != j || c.st.SourceBindings[j.Issue] == nil || len(c.st.CompletedRuns) != 0 {
		t.Fatal("done lost the app record or source binding")
	}
	if _, ok := c.pickHost(Target{}, "codex"); ok {
		t.Fatal("warm resident seat was mistaken for free physical capacity")
	}
	if c.st.save() != nil {
		t.Fatal("save failed")
	}
	if restarted := loadState(c.st.path); restarted.Jobs[j.Issue] == nil || restarted.Jobs[j.Issue].CapacityObservation != nil || restarted.SourceBindings[j.Issue] == nil {
		t.Fatal("restart lost binding or trusted an old capacity sample")
	}
	_ = h
}

func TestRemoteRunDoneInvalidationAndInputReservation(t *testing.T) {
	for _, mode := range []string{"resumed", "missing", "changed-identity", "stale", "relay", "failed-relay", "full"} {
		t.Run(mode, func(t *testing.T) {
			c, j, h, ref, _, _ := nativeCapacityFixture(t, "valid")
			c.observeRemoteDone(context.Background(), h, j, ref)
			if !nativeCapacityDone(j, time.Now()) {
				t.Fatal("fixture not natively done")
			}
			status := map[int]agentRef{j.Issue: ref}
			sends := 0
			c.actions.send = func(context.Context, Host, string, string) error {
				sends++
				if c.activeByAccount()["codex"] == 0 {
					t.Fatal("input sent before reacquiring capacity")
				}
				if mode == "failed-relay" {
					return errors.New("synthetic delivery failure")
				}
				return nil
			}
			switch mode {
			case "resumed":
				ref.Status = "working"
				ref.StateChangeSeq++
				status[j.Issue] = ref
			case "missing":
				delete(status, j.Issue)
			case "changed-identity":
				ref.Pane = "other"
				status[j.Issue] = ref
			case "stale":
				j.CapacityObservation.At = time.Now().Add(-time.Hour)
			case "full":
				c.st.Jobs[8] = &Job{Issue: 8, Agent: "codex"}
			}
			if strings.Contains(mode, "relay") || mode == "full" {
				budget := c.admissionBudget(status)
				err := c.actionSend(context.Background(), h, j.Pane, "synthetic PR relay")
				if mode == "full" {
					if err != errRunCapacity || sends != 0 || !nativeCapacityDone(j, time.Now()) {
						t.Fatal("full account resumed done work")
					}
					return
				}
				if sends != 1 || nativeCapacityDone(j, time.Now()) {
					t.Fatal("relay did not reserve its slot")
				}
				c.reconcileRunCapacityBudget(budget)
				if budget["codex"] != 0 {
					t.Fatal("later same-tick admission could spend the reacquired slot")
				}
			} else {
				c.refreshRunCapacity(status)
			}
			if c.activeByAccount()["codex"] != 1 {
				t.Fatal("resumed or unconfirmed run not counted")
			}
		})
	}
}

func TestRemoteRunGovernorCategories(t *testing.T) {
	c, j, h, ref, _, _ := nativeCapacityFixture(t, "valid")
	c.observeRemoteDone(context.Background(), h, j, ref)
	c.st.Jobs[8] = &Job{Issue: 8, Agent: "codex", CapacityObservation: &runCapacityObservation{Kind: runCapacityLive, At: time.Now()}}
	c.st.Jobs[9] = &Job{Issue: 9, Agent: "codex"}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	c.curCaps()
	if c.activeByAccount()["codex"] != 2 || !strings.Contains(logs.String(), "active 2 live 1 done 1 unconfirmed 1") {
		t.Fatal("governor categories disagree with occupied capacity", logs.String())
	}
}

func TestRemoteRunClosedInboxTeardownWithoutTUIAnchor(t *testing.T) {
	c, j, _, _, _, _ := nativeCapacityFixture(t, "valid")
	c.actions.stopProcess = func(context.Context, Host, string, string) (*actionStopProcess, error) {
		t.Fatal("missing TUI required process anchor")
		return nil, nil
	}
	if !teardownEligible(context.Background(), j.Issue, nil, true, func(context.Context, int) string { return "CLOSED" }) ||
		!c.teardown(context.Background(), j.Issue, j, "teardown") {
		t.Fatal("closed inbox native-inactive seat was stranded")
	}
	if j.stopUnconfirmed() {
		t.Fatal("closure exhausted retries instead of proving native release")
	}
}
