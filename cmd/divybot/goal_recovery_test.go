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

func TestCapacityRecoveryExhaustedNativeObservation(t *testing.T) {
	for _, mode := range []string{"unchanged-active", "manual-active", "new-turn", "blocked", "paused", "wrong-goal", "read-failed", "save-failed", "legacy-active"} {
		t.Run(mode, func(t *testing.T) {
			c, j := capacityFixture(t)
			goal := fixtureGoal("active")
			j.GoalRecovery = &goalRecovery{State: recoveryExhausted, Attempts: 1, TurnID: "capacity-turn", ExhaustedTurnID: "capacity-turn", Reason: "goal-capacity-resume-unproven", ObservedGoalStatus: "active", ObservedGoalUpdatedAt: goal.UpdatedAt}
			turn := "capacity-turn"
			switch mode {
			case "manual-active", "save-failed":
				goal.UpdatedAt++
			case "new-turn":
				turn = "next-turn"
			case "blocked", "paused":
				goal.Status = mode
			case "wrong-goal":
				goal.Objective = "different objective"
			case "legacy-active":
				j.GoalRecovery.ObservedGoalStatus = ""
				j.GoalRecovery.ObservedGoalUpdatedAt = 0
			}
			if c.st.save() != nil {
				t.Fatal("fixture state")
			}
			writes := 0
			c.st.fs = &stateFS{hold: func(stage string) {
				if stage == "state-file" {
					writes++
				}
			}}
			if mode == "save-failed" {
				c.st.fs.rename = func(string, string) error { return errors.New("fixture") }
			}
			p, input := capacityReadPort(goal, turn, "failed", "serverOverloaded")
			if mode == "read-failed" {
				p, _ = goalPort()
				p.input = input
				p.canonical = true
			}
			owns, err := c.recoverCapacityWithPort(context.Background(), j, p, time.Now())
			released := mode == "manual-active" || mode == "new-turn" || mode == "paused"
			wantError := mode == "wrong-goal" || mode == "read-failed" || mode == "save-failed"
			if owns == released || capacityRecoveryOwns(j) == released || (err != nil) != wantError {
				t.Fatal("exhausted recovery ignored native continuation, uncertainty or operator authority")
			}
			if !released && !wantError && writes != 0 {
				t.Fatal("unchanged exhausted recovery rewrote durable state")
			}
			if strings.Contains(input.String(), "thread/goal/set") {
				t.Fatal("exhausted observation wrote to the native goal")
			}
			saved := loadState(c.st.path).Jobs[j.Issue]
			if capacityRecoveryOwns(saved) == released || saved.GoalRecovery.Attempts != 1 {
				t.Fatal("exhausted observation lost its durable ownership or attempt bound")
			}
		})
	}
}

func TestCapacityRecoveryExhaustedNoticeOncePerTurn(t *testing.T) {
	c, j := capacityFixture(t)
	var notices bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&notices)
	t.Cleanup(func() { log.SetOutput(prior) })
	now := time.Now()
	j.GoalRecovery = &goalRecovery{State: recoveryWaiting, Attempts: capacityRetryLimit, TurnID: "capacity-turn", NextAttemptAt: now}
	p, _ := capacityReadPort(fixtureGoal("blocked"), "capacity-turn", "failed", "serverOverloaded")
	if owns, err := c.recoverCapacityWithPort(context.Background(), j, p, now); !owns || err != nil {
		t.Fatal("retry exhaustion failed")
	}
	for round := 0; round < 2; round++ {
		// Retry exhaustion was observed blocked; native active proves an external resume.
		p, _ = capacityReadPort(fixtureGoal("active"), "capacity-turn", "failed", "serverOverloaded")
		if owns, err := c.recoverCapacityWithPort(context.Background(), j, p, now); owns || err != nil {
			t.Fatal("manual resume did not release exhausted ownership")
		}
		c.st = loadState(c.st.path)
		j = c.st.Jobs[j.Issue]
		p, _ = capacityReadPort(fixtureGoal("blocked"), "capacity-turn", "failed", "serverOverloaded")
		if owns, err := c.recoverCapacityWithPort(context.Background(), j, p, now); !owns || err != nil || j.GoalRecovery.State != recoveryWaiting {
			t.Fatal("same capacity turn did not re-enter bounded backoff")
		}
		p, _ = capacityReadPort(fixtureGoal("blocked"), "capacity-turn", "failed", "serverOverloaded")
		if owns, err := c.recoverCapacityWithPort(context.Background(), j, p, j.GoalRecovery.NextAttemptAt); !owns || err != nil || j.GoalRecovery.State != recoveryExhausted {
			t.Fatal("same capacity turn lost its exhausted lifetime bound")
		}
	}
	if strings.Count(notices.String(), "native capacity recovery stopped") != 1 {
		t.Fatal("exhaustion notice repeated for the same failed turn after manual resume and restart")
	}
	p, _ = capacityReadPort(fixtureGoal("blocked"), "next-turn", "failed", "serverOverloaded")
	if owns, err := c.recoverCapacityWithPort(context.Background(), j, p, now); !owns || err != nil {
		t.Fatal("new capacity turn lost recovery")
	}
	p, _ = capacityReadPort(fixtureGoal("blocked"), "next-turn", "failed", "serverOverloaded")
	if owns, err := c.recoverCapacityWithPort(context.Background(), j, p, j.GoalRecovery.NextAttemptAt); !owns || err != nil {
		t.Fatal("new turn did not retain the exhausted attempt bound")
	}
	if strings.Count(notices.String(), "native capacity recovery stopped") != 2 {
		t.Fatal("a distinct failed turn did not get its own exhaustion notice")
	}
}

func TestCapacityRecoveryExhaustedObservationsAreDebounced(t *testing.T) {
	c, j := capacityFixture(t)
	run := syntheticRemoteRun(t)
	run.NativeSessionID = "fixture-thread"
	j.RemoteControl = run
	j.GoalDeliveryCommit = deliveryCommitMark
	j.GoalRecovery = &goalRecovery{State: recoveryExhausted, Attempts: capacityRetryLimit, TurnID: "capacity-turn", Reason: "goal-capacity-retry-exhausted"}
	var reads atomic.Int32
	base := remoteIdleHandler(run, "valid")
	h := canonicalFixtureHost(t, "multi", func(method string, _ map[string]any) any {
		if method == "thread/goal/get" {
			reads.Add(1)
			return map[string]any{"goal": fixtureGoal("active")}
		}
		return base(method, nil)
	}).forJob(j)
	ref := agentRef{Host: j.Host, Agent: j.Agent, Pane: j.Pane, Workspace: j.Workspace, Status: "idle", StateChangeSeq: 4, CompletionSeq: 2}
	if !c.markCapacityObservation(j, ref, time.Now()) {
		t.Fatal("fixture checkpoint")
	}
	writes := 0
	c.st.fs = &stateFS{hold: func(stage string) {
		if stage == "state-file" {
			writes++
		}
	}}
	for tick := 0; tick < 3; tick++ {
		if !c.recoverCapacityStall(context.Background(), h, j, ref, true) {
			t.Fatal("unchanged observation released recovery without a native read")
		}
	}
	if reads.Load() != 0 || writes != 0 {
		t.Fatal("unchanged exhausted ticks performed native reads or state writes")
	}
	// No fleet event is required: the existing bounded safety backstop still observes goals.
	j.GoalRecoveryObservation.At = time.Now().Add(-fleetRecoveryEvery)
	if c.recoverCapacityStall(context.Background(), h, j, ref, true) || reads.Load() != 1 || capacityRecoveryOwns(j) {
		t.Fatal("exhausted safety observation did not release native manual resume")
	}
}

func TestCapacityRecoveryAutomaticExhaustionKeepsNativeObservation(t *testing.T) {
	for _, mode := range []string{"readback", "lost-acknowledgement"} {
		t.Run(mode, func(t *testing.T) {
			c, j := capacityFixture(t)
			now := time.Now()
			j.GoalRecovery = &goalRecovery{State: recoveryWaiting, Attempts: 1, TurnID: "capacity-turn", NextAttemptAt: now, WatchUntil: now.Add(capacityResumeTimeout), ObservedGoalStatus: "blocked"}
			active := fixtureGoal("active")
			active.UpdatedAt++
			if mode == "readback" {
				p, _ := capacityResumePort(fixtureGoal("blocked"), active, "capacity-turn", true)
				if owns, err := c.recoverCapacityWithPort(context.Background(), j, p, now); !owns || err != nil {
					t.Fatal("automatic resume failed")
				}
				if j.GoalRecovery.ObservedGoalStatus != "active" || j.GoalRecovery.ObservedGoalUpdatedAt != active.UpdatedAt {
					t.Fatal("successful resume did not preserve the native active readback")
				}
			}
			// Even an acknowledgement lost while still recorded blocked must exhaust
			// against the fresh native active observation, never its stale snapshot.
			p, _ := capacityReadPort(active, "capacity-turn", "failed", "serverOverloaded")
			if owns, err := c.recoverCapacityWithPort(context.Background(), j, p, j.GoalRecovery.WatchUntil); !owns || err != nil || j.GoalRecovery.State != recoveryExhausted {
				t.Fatal("automatic start deadline did not exhaust")
			}
			if j.GoalRecovery.ObservedGoalStatus != "active" || j.GoalRecovery.ObservedGoalUpdatedAt != active.UpdatedAt {
				t.Fatal("exhaustion retained a stale goal observation")
			}
			c.st = loadState(c.st.path)
			j = c.st.Jobs[j.Issue]
			writes := 0
			c.st.fs = &stateFS{hold: func(stage string) {
				if stage == "state-file" {
					writes++
				}
			}}
			for tick := 0; tick < 2; tick++ {
				p, _ := capacityReadPort(active, "capacity-turn", "failed", "serverOverloaded")
				if owns, err := c.recoverCapacityWithPort(context.Background(), j, p, now.Add(time.Hour)); !owns || err != nil {
					t.Fatal("unchanged automatic exhaustion released ownership")
				}
			}
			if writes != 0 {
				t.Fatal("unchanged automatic exhaustion repeated its state write")
			}
		})
	}
}

func TestCapacityRecoveryLegacyExhaustedNoticeMigration(t *testing.T) {
	c, j := capacityFixture(t)
	j.GoalRecovery = &goalRecovery{State: recoveryExhausted, Attempts: capacityRetryLimit, TurnID: "capacity-turn", Reason: "goal-capacity-retry-exhausted"}
	p, _ := capacityReadPort(fixtureGoal("active"), "capacity-turn", "failed", "serverOverloaded")
	if owns, err := c.recoverCapacityWithPort(context.Background(), j, p, time.Now()); owns || err != nil || j.GoalRecovery.ExhaustedTurnID != "capacity-turn" {
		t.Fatal("release forgot that the legacy exhausted turn had already been reported")
	}
}

func capacityTurnResult(id, status, code string) any {
	return map[string]any{"data": []any{map[string]any{"id": id, "status": status, "error": map[string]any{"codexErrorInfo": code, "message": "untrusted text"}}}}
}
func capacityReadPort(goal *nativeGoal, id, status, code string) (*goalRPC, *strings.Builder) {
	// Strings are returned separately only for assertions on attempted protocol effects.
	p, input := goalPort(response(2, map[string]any{"goal": goal}), response(3, capacityTurnResult(id, status, code)))
	out := new(strings.Builder)
	p.input = out
	p.canonical = true
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
	p.canonical = true
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
		if saved.GoalRecovery.Attempts != attempt || !saved.GoalRecovery.NextAttemptAt.Equal(due.Add(capacityResumeTimeout)) {
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
			p.canonical = true
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
			_, err := resumeCapacityGoal(p, before, fixtureIntent())
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
	if c.nextGoalRecoveryDelay(now) != 0 {
		t.Fatal("no pending recovery retained a busy timer")
	}
	// A fresh attempt is an explicit run operation, never a side effect of an app read.
	src, err := os.ReadFile("main.go")
	if err != nil || !strings.Contains(string(src), "case <-recoveryWake:\n\t\t\tc.capacityRecoveryPass(ctx)") {
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

func TestCapacityRecoveryWatchHasOnePersistedDeadline(t *testing.T) {
	c, j := capacityFixture(t)
	now := time.Now()
	deadline := now.Add(capacityResumeTimeout)
	j.GoalRecovery = &goalRecovery{Attempts: 1, State: recoveryWatching, TurnID: "capacity-turn", WatchUntil: deadline, NextAttemptAt: deadline}
	for _, at := range []time.Time{now, deadline.Add(-time.Millisecond), deadline} {
		p, input := capacityReadPort(fixtureGoal("active"), "capacity-turn", "failed", "serverOverloaded")
		owns, err := c.recoverCapacityWithPort(context.Background(), j, p, at)
		if !owns || err != nil || strings.Contains(input.String(), "thread/goal/set") || !j.GoalRecovery.WatchUntil.Equal(deadline) {
			t.Fatal("unproven native start extended its deadline or repeated the write")
		}
		if at.Before(deadline) {
			if j.GoalRecovery.State != recoveryWatching || !j.GoalRecovery.NextAttemptAt.Equal(deadline) {
				t.Fatal("watching changed its persisted deadline")
			}
		} else if j.GoalRecovery.State != recoveryExhausted || j.GoalRecovery.Reason != "goal-capacity-resume-unproven" || c.nextGoalRecoveryDelay(at) != 0 {
			t.Fatal("unproven native start stayed in an unbounded watching loop")
		}
		c.st = loadState(c.st.path)
		j = c.st.Jobs[j.Issue]
	}
}

func TestCapacityRecoveryRequiresSharedCanonicalDaemon(t *testing.T) {
	c, j := capacityFixture(t)
	p, input := capacityReadPort(fixtureGoal("blocked"), "capacity-turn", "failed", "serverOverloaded")
	p.canonical = false
	owns, err := c.recoverCapacityWithPort(context.Background(), j, p, time.Now())
	if owns || err != goalError("goal-capacity-canonical-required") || j.GoalRecovery != nil || strings.Contains(input.String(), "thread/goal/set") {
		t.Fatal("temporary app-server connection gained resume authority")
	}
	h := goalTransportHost(t, "capacity-stall")
	j.Host = h.Name
	ref := agentRef{Host: j.Host, Agent: j.Agent, Pane: j.Pane, Workspace: j.Workspace, Status: "idle"}
	if c.recoverCapacityStall(context.Background(), h, j, ref, true) || j.NativeGoal.Reason != "goal-capacity-canonical-required" {
		t.Fatal("legacy transport did not refuse visibly")
	}
}

func TestCapacityRecoveryCanonicalTransportCapability(t *testing.T) {
	h := canonicalFixtureHost(t, "valid", func(string, map[string]any) any { return map[string]any{} })
	// Allow subprocess startup under shared CI load.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	called := false
	err := h.withCanonicalConnection(ctx, "fixture-thread", func(p *goalRPC) error {
		called = true
		if !p.canonical {
			t.Fatal("shared native connection lost its capability")
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("canonical app-server transport unavailable: %v", err)
	}
	legacy := goalTransportHost(t, "")
	if legacy.withGoalConnection(ctx, "fixture-thread", func(p *goalRPC) error {
		if p.canonical {
			t.Fatal("short-lived native daemon acquired shared transport capability")
		}
		return nil
	}) != nil {
		t.Fatal("legacy fixture unavailable")
	}
}

func TestCapacityRecoveryObservationsAreDebounced(t *testing.T) {
	c, j := capacityFixture(t)
	run := syntheticRemoteRun(t)
	run.NativeSessionID = "fixture-thread"
	j.RemoteControl = run
	j.GoalDeliveryCommit = deliveryCommitMark
	ref := agentRef{Host: j.Host, Agent: j.Agent, Pane: j.Pane, Workspace: j.Workspace, Status: "idle", StateChangeSeq: 4, CompletionSeq: 2}
	if !c.markCapacityObservation(j, ref, time.Now()) {
		t.Fatal("native observation checkpoint failed")
	}
	// A forbidden transport proves unchanged ticks return before any process/connection.
	h := goalTransportHost(t, "existing")
	prior := j.GoalRecoveryObservation.At
	if c.recoverCapacityStall(context.Background(), h, j, ref, true) {
		t.Fatal("idle non-recovery acquired ownership")
	}
	if !j.GoalRecoveryObservation.At.Equal(prior) {
		t.Fatal("unchanged tick opened another native observation")
	}
	if capacityObservationChanged(j, ref) {
		t.Fatal("unchanged observation became a new trigger")
	}
	c.st = loadState(c.st.path)
	j = c.st.Jobs[j.Issue]
	if capacityObservationChanged(j, ref) {
		t.Fatal("dispatcher restart lost observation debounce")
	}
	ref.StateChangeSeq++
	if !capacityObservationChanged(j, ref) {
		t.Fatal("native state transition did not trigger observation")
	}
	ref.StateChangeSeq--
	ref.CompletionSeq++
	if !capacityObservationChanged(j, ref) {
		t.Fatal("new native completion did not trigger observation")
	}
	j.GoalRecovery = &goalRecovery{State: recoveryWaiting, NextAttemptAt: time.Now().Add(-time.Second)}
	c.postponeCapacityObservation(j, time.Now())
	if c.nextGoalRecoveryDelay(time.Now()) < capacityRetryInitial-time.Second {
		t.Fatal("unavailable native source created a tight retry loop")
	}
}

func TestCapacityRecoveryStateFailureKeepsBoundedWake(t *testing.T) {
	c, j := capacityFixture(t)
	now := time.Now()
	j.GoalRecovery = &goalRecovery{State: recoveryWaiting, NextAttemptAt: now.Add(-time.Second)}
	c.st.fs = &stateFS{rename: func(string, string) error { return errors.New("fixture") }}
	if c.saveGoalRecovery(j, goalRecovery{State: recoveryWaiting, NextAttemptAt: now.Add(time.Minute)}) == nil {
		t.Fatal("fixture did not fail state persistence")
	}
	if c.nextGoalRecoveryDelay(now) < capacityRetryInitial {
		t.Fatal("state persistence failure created a hot recovery timer")
	}
}
