package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recoveryWriteFunc func([]byte) (int, error)

func (write recoveryWriteFunc) Write(b []byte) (int, error) { return write(b) }

func capacityFixture(t *testing.T) (*Coord, *Job) {
	t.Helper()
	receipt, j, root := fixtureGoalBinding(t)
	j.Host = "fixture"
	j.DispatchKey = j.NativeGoal.ReceiptKey
	j.NativeGoal.Owned = true
	j.NativeGoal.PromptConfirmed = true
	j.GoalDelivery = "confirmed"
	receipt.dispatch.Host = j.Host
	if receipt.writeDispatch("dispatched", receipt.dispatch.Location) != nil {
		t.Fatal("fixture dispatch")
	}
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, st: loadState(filepath.Join(t.TempDir(), "state.json"))}
	c.st.Jobs[j.Issue] = j
	return c, j
}

func capacityTurnResult(id, status, code string) any {
	return map[string]any{"data": []any{map[string]any{"id": id, "status": status, "error": map[string]any{"codexErrorInfo": code, "message": "untrusted text"}}}}
}
func capacityReadPort(goal *nativeGoal, id, status, code string) (*goalRPC, *strings.Builder) {
	// Strings are returned separately only for assertions on attempted protocol effects.
	p, input := goalPort(response(2, map[string]any{"goal": goal}), response(3, capacityTurnResult(id, status, code)))
	out := new(strings.Builder)
	p.input = out
	_ = input
	return p, out
}

func capacityResumePort(before, after *nativeGoal, id string, confirm bool) (*goalRPC, *strings.Builder) {
	frames := []any{response(2, map[string]any{"goal": before}), response(3, capacityTurnResult(id, "failed", "serverOverloaded")), response(4, map[string]any{"goal": before}), response(5, capacityTurnResult(id, "failed", "serverOverloaded")), response(6, map[string]any{"goal": after})}
	if confirm {
		frames = append(frames, updated(after), response(7, map[string]any{"goal": after}))
	}
	p, _ := goalPort(frames...)
	out := new(strings.Builder)
	p.input = out
	return p, out
}

func TestCapacityRecoveryBackoffBoundAndRestart(t *testing.T) {
	c, j := capacityFixture(t)
	now := time.Now()
	p, _ := capacityReadPort(fixtureGoal("blocked"), "capacity-turn", "failed", "serverOverloaded")
	owns, err := c.recoverCapacityWithPort(context.Background(), j, p, now)
	if !owns || err != nil || j.GoalRecovery.Attempts != 0 || !j.GoalRecovery.NextAttemptAt.Equal(now.Add(capacityRetryInitial)) {
		t.Fatal("capacity did not schedule a bounded initial backoff")
	}
	for attempt := 1; attempt <= capacityRetryLimit; attempt++ {
		due := j.GoalRecovery.NextAttemptAt
		p, input := capacityReadPort(fixtureGoal("blocked"), "capacity-turn", "failed", "serverOverloaded")
		if owns, err = c.recoverCapacityWithPort(context.Background(), j, p, due.Add(-time.Millisecond)); !owns || err != nil || strings.Contains(input.String(), "thread/goal/set") || j.GoalRecovery.Attempts != attempt-1 {
			t.Fatal("resumed before the persisted backoff")
		}
		c.st = loadState(c.st.path)
		j = c.st.Jobs[j.Issue]
		p, input = capacityResumePort(fixtureGoal("blocked"), fixtureGoal("active"), "capacity-turn", true)
		owns, err = c.recoverCapacityWithPort(context.Background(), j, p, due)
		if !owns || err != nil || j.GoalRecovery.Attempts != attempt {
			t.Fatalf("bounded native resume failed at attempt %d: %v", attempt, err)
		}
		if strings.Count(input.String(), `"method":"thread/goal/set"`) != 1 || strings.Contains(input.String(), `"objective"`) || strings.Contains(input.String(), `"tokenBudget"`) || !strings.Contains(input.String(), `"origin":"automatic"`) {
			t.Fatal("resume changed goal intent, budget, or sent multiple writes")
		}
		saved := loadState(c.st.path).Jobs[j.Issue]
		if saved.GoalRecovery.Attempts != attempt || !saved.GoalRecovery.NextAttemptAt.Equal(due.Add(capacityRetryDelay(attempt))) {
			t.Fatal("attempt reservation did not survive restart")
		}
		p, _ = capacityReadPort(fixtureGoal("blocked"), "capacity-turn", "failed", "serverOverloaded")
		if owns, err = c.recoverCapacityWithPort(context.Background(), j, p, due.Add(time.Millisecond)); !owns || err != nil {
			t.Fatal("repeat native capacity failure lost recovery")
		}
	}
	p, input := capacityReadPort(fixtureGoal("blocked"), "capacity-turn", "failed", "serverOverloaded")
	owns, err = c.recoverCapacityWithPort(context.Background(), j, p, j.GoalRecovery.NextAttemptAt)
	if !owns || err != nil || j.GoalRecovery.State != recoveryExhausted || j.GoalRecovery.Attempts != capacityRetryLimit || !j.GoalRecovery.NextAttemptAt.IsZero() || strings.Contains(input.String(), "thread/goal/set") {
		t.Fatal("capacity recovery exceeded its bound or silently went idle")
	}
	if !occupiesAdmissionSlot(j, agentRef{Host: j.Host, Agent: j.Agent, Pane: j.Pane, Workspace: j.Workspace, Status: "done"}, true) {
		t.Fatal("capacity recovery released its running slot")
	}
	if capacityRetryDelay(100) != capacityRetryMaximum {
		t.Fatal("backoff has no ceiling")
	}
}

func TestCapacityRecoveryUsesOnlyNativeCapacity(t *testing.T) {
	for _, tc := range []struct{ goal, status, code string }{
		{"paused", "failed", "serverOverloaded"}, {"usageLimited", "failed", "serverOverloaded"}, {"budgetLimited", "failed", "serverOverloaded"}, {"complete", "failed", "serverOverloaded"},
		{"blocked", "failed", "usageLimitExceeded"}, {"blocked", "failed", "rateLimitExceeded"}, {"blocked", "failed", "other"}, {"blocked", "failed", ""}, {"blocked", "completed", "serverOverloaded"}, {"blocked", "interrupted", "serverOverloaded"}, {"blocked", "inProgress", "serverOverloaded"},
	} {
		t.Run(tc.goal+"/"+tc.status+"/"+tc.code, func(t *testing.T) {
			c, j := capacityFixture(t)
			p, input := capacityReadPort(fixtureGoal(tc.goal), "capacity-turn", tc.status, tc.code)
			owns, err := c.recoverCapacityWithPort(context.Background(), j, p, time.Now())
			if owns || err != nil || j.GoalRecovery != nil || strings.Contains(input.String(), "thread/goal/set") {
				t.Fatal("non-capacity, paused, limited, completed or in-progress goal was recovered")
			}
		})
	}
}

func TestCapacityRecoveryReservationPrecedesWriteAndConsumesAmbiguity(t *testing.T) {
	for _, mode := range []string{"persistence-failed", "manual-pause", "binding-changed", "goal-accounting-changed", "lost-notification", "valid"} {
		t.Run(mode, func(t *testing.T) {
			c, j := capacityFixture(t)
			now := time.Now()
			j.GoalRecovery = &goalRecovery{State: recoveryWaiting, TurnID: "capacity-turn", NextAttemptAt: now}
			before, after := fixtureGoal("blocked"), fixtureGoal("active")
			p, input := capacityResumePort(before, after, "capacity-turn", mode != "lost-notification")
			if mode == "manual-pause" || mode == "goal-accounting-changed" {
				changed := fixtureGoal("paused")
				if mode == "goal-accounting-changed" {
					changed = fixtureGoal("blocked")
					changed.CreatedAt++
				}
				p, _ = goalPort(response(2, map[string]any{"goal": before}), response(3, capacityTurnResult("capacity-turn", "failed", "serverOverloaded")), response(4, map[string]any{"goal": changed}), response(5, capacityTurnResult("capacity-turn", "failed", "serverOverloaded")))
				p.input = input
			}
			if mode == "persistence-failed" {
				c.st.fs = &stateFS{rename: func(string, string) error { return errors.New("fixture") }}
			}
			if mode == "binding-changed" {
				c.st.fs = &stateFS{hold: func(stage string) {
					if stage == "state-file" {
						path := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record", "binding.json")
						if os.WriteFile(path, goalJSON(map[string]any{"Repo": j.Repo, "NativeSessionID": "foreign-thread"}), 0600) != nil {
							t.Fatal("fixture binding change")
						}
					}
				}}
			}
			if mode == "valid" {
				p.input = recoveryWriteFunc(func(b []byte) (int, error) {
					if strings.Contains(string(b), `"method":"thread/goal/set"`) {
						saved := loadState(c.st.path).Jobs[j.Issue]
						if saved == nil || saved.GoalRecovery == nil || saved.GoalRecovery.Attempts != 1 {
							t.Fatal("native write preceded durable attempt reservation")
						}
					}
					return input.Write(b)
				})
			}
			reserved := false
			if mode == "valid" {
				c.st.fs = &stateFS{hold: func(stage string) {
					if stage == "state-file" {
						reserved = true
					}
				}}
			}
			owns, err := c.recoverCapacityWithPort(context.Background(), j, p, now)
			writes := strings.Count(input.String(), `"method":"thread/goal/set"`)
			if !owns || (err == nil) != (mode == "valid") {
				t.Fatal("reservation/identity/notification guard returned the wrong verdict")
			}
			if mode == "valid" || mode == "lost-notification" {
				if writes != 1 || loadState(c.st.path).Jobs[j.Issue].GoalRecovery.Attempts != 1 {
					t.Fatal("native effect did not consume its durable attempt")
				}
			} else if writes != 0 {
				t.Fatal("native resume bypassed durability, manual pause or private binding guard")
			}
			if mode == "valid" && !reserved {
				t.Fatal("write preceded its durable reservation")
			}
		})
	}
}

func TestCapacityRecoveryResumePreservesAccounting(t *testing.T) {
	for _, mode := range []string{"valid", "created", "tokens", "time", "objective", "budget", "readback"} {
		t.Run(mode, func(t *testing.T) {
			before := fixtureGoal("blocked")
			after := fixtureGoal("active")
			read := fixtureGoal("active")
			switch mode {
			case "created":
				after.CreatedAt++
			case "tokens":
				after.TokensUsed--
			case "time":
				after.SecondsUsed--
			case "objective":
				after.Objective = "different goal"
			case "budget":
				after.TokenBudget = goalInt(101)
			case "readback":
				read.Status = "paused"
			}
			p, input := goalPort(response(2, map[string]any{"goal": after}), updated(after), response(3, map[string]any{"goal": read}))
			err := resumeCapacityGoal(p, before, fixtureIntent())
			if (err == nil) != (mode == "valid") || strings.Count(input.String(), `"method":"thread/goal/set"`) != 1 {
				t.Fatal("resume did not require matching native accounting and readback")
			}
		})
	}
}

func TestCapacityRecoveryProgressAndTimer(t *testing.T) {
	c, j := capacityFixture(t)
	now := time.Now()
	j.GoalRecovery = &goalRecovery{Attempts: 2, State: recoveryWaiting, TurnID: "capacity-turn", NextAttemptAt: now.Add(15 * time.Second)}
	if c.nextGoalRecoveryDelay(now) != 15*time.Second {
		t.Fatal("daemon wake ignored the pending native recovery")
	}
	p, _ := capacityReadPort(fixtureGoal("active"), "next-turn", "inProgress", "")
	if owns, err := c.recoverCapacityWithPort(context.Background(), j, p, now); owns || err != nil || capacityRecoveryOwns(j) || j.GoalRecovery.Attempts != 2 {
		t.Fatal("native progress failed to release recovery ownership or reset the lifetime bound")
	}
	if c.nextGoalRecoveryDelay(now) != fleetRecoveryEvery {
		t.Fatal("no pending recovery retained a busy timer")
	}
	// A fresh attempt is an explicit run operation, never a side effect of an app read.
	src, err := os.ReadFile("main.go")
	if err != nil || !strings.Contains(string(src), "case <-recoveryTimer.C:") {
		t.Fatal("recovery has no daemon-owned wake")
	}
	raw, _ := json.Marshal(j.GoalRecovery)
	if strings.Contains(string(raw), "objective") {
		t.Fatal("recovery duplicated the goal intent")
	}
}

func TestCapacityRecoveryOwnsIdleAndBlockedSupervision(t *testing.T) {
	for _, state := range []string{recoveryWaiting, recoveryExhausted} {
		for _, status := range []string{"idle", "blocked", "done"} {
			t.Run(state+"/"+status, func(t *testing.T) {
				c, j := capacityFixture(t)
				h := goalTransportHost(t, "existing")
				j.Host = h.Name
				j.SpawnedAt = time.Now().Add(-time.Hour)
				c.hosts = map[string]Host{h.Name: h}
				j.GoalRecovery = &goalRecovery{State: state, NextAttemptAt: time.Now().Add(time.Minute), TurnID: "capacity-turn"}
				log := filepath.Join(t.TempDir(), "effects")
				writeFixture(t, filepath.Join(h.Home, ".local", "bin", "herdr"), "#!/bin/sh\nprintf unexpected >> "+shq(log)+"\nexit 1\n")
				bin := t.TempDir()
				writeFixture(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '[]'\n")
				_ = os.Chmod(filepath.Join(h.Home, ".local", "bin", "herdr"), 0700)
				_ = os.Chmod(filepath.Join(bin, "gh"), 0700)
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				ref := agentRef{Host: j.Host, Agent: j.Agent, Pane: j.Pane, Workspace: j.Workspace, Status: status}
				c.superviseActive(context.Background(), j.Issue, j, map[int]agentRef{j.Issue: ref}, Issue{Number: j.Issue})
				if data, _ := os.ReadFile(log); len(data) != 0 || !j.LastPoke.IsZero() || j.NativeGoal.LastStatus != "" {
					t.Fatal("ordinary supervision fought the recovery owner")
				}
				if c.st.Jobs[j.Issue] != j || !occupiesAdmissionSlot(j, ref, true) {
					t.Fatal("pending/exhausted native recovery lost managed running capacity")
				}
			})
		}
	}
}
