package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"testing"
)

// A registered job persisted unconfirmed, as the real caller has it before delivery.
func confirmFixture(t *testing.T, prior string, native *dispatchGoal) (*Coord, *Job, []byte) {
	t.Helper()
	c := &Coord{st: loadState(t.TempDir() + "/state.json")}
	j := &Job{Issue: 7, Agent: "claude", GoalDelivery: prior, NativeGoal: native}
	c.st.Jobs[7] = j
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(c.st.path)
	if err != nil {
		t.Fatal(err)
	}
	return c, j, before
}

func reloadedJob(c *Coord) *Job { return loadState(c.st.path).Jobs[7] }

func promptBit(j *Job) bool { return j.NativeGoal != nil && j.NativeGoal.PromptConfirmed }

var errInjected = errors.New("injected")

// The caller blocks only after a returned error; exercise every follow-up outcome.
func confirmFollowUps(t *testing.T, c *Coord, j *Job, follow string) {
	t.Helper()
	switch follow {
	case "block":
		c.st.fs = nil
		c.blockGoalDelivery(7, j)
	case "failed-block":
		// Both saves inside blocking (delivery and launch block) fail.
		c.st.fs = &stateFS{syncFile: func(*os.File) error { return errInjected }}
		c.blockGoalDelivery(7, j)
	}
}

func TestGoalConfirmationContract(t *testing.T) {
	priors := []struct {
		name     string
		delivery string
		native   func() *dispatchGoal
	}{
		{"no-native-goal", "pending", func() *dispatchGoal { return nil }},
		{"native-false", "pending", func() *dispatchGoal { return &dispatchGoal{} }},
		{"pending-native-true", "pending", func() *dispatchGoal { return &dispatchGoal{PromptConfirmed: true} }},
		{"blocked-native-true", "blocked", func() *dispatchGoal { return &dispatchGoal{PromptConfirmed: true} }},
	}
	refusals := []struct {
		name string
		fs   func() *stateFS
	}{
		{"file-sync-fails", func() *stateFS { return &stateFS{syncFile: func(*os.File) error { return errInjected }} }},
		{"rename-fails", func() *stateFS { return &stateFS{rename: func(string, string) error { return errInjected }} }},
	}
	for _, p := range priors {
		for _, r := range refusals {
			for _, follow := range []string{"none", "block", "failed-block"} {
				t.Run("refusal/"+p.name+"/"+r.name+"/"+follow, func(t *testing.T) {
					c, j, before := confirmFixture(t, p.delivery, p.native())
					priorBit := promptBit(j)
					c.st.fs = r.fs()
					if err := c.confirmGoalDelivery(context.Background(), j); err != errPromptUnconfirmed {
						t.Fatalf("failed save before the rename was accepted: %v", err)
					}
					if j.GoalDelivery != p.delivery || promptBit(j) != priorBit {
						t.Fatalf("memory lost its exact prior pair: %s/%v", j.GoalDelivery, promptBit(j))
					}
					if now, _ := os.ReadFile(c.st.path); !bytes.Equal(now, before) {
						t.Fatal("a refused confirmation changed the pathname")
					}
					// A later unrelated save cannot persist a partial confirmation.
					c.st.fs = nil
					if err := c.st.save(); err != nil {
						t.Fatal(err)
					}
					if loaded := reloadedJob(c); loaded.GoalDelivery != p.delivery || promptBit(loaded) != priorBit {
						t.Fatal("rolled-back memory persisted a different pair")
					}
					confirmFollowUps(t, c, j, follow)
					if deliveryConfirmed(reloadedJob(c)) {
						t.Fatal("a refused confirmation reloads as delivered")
					}
				})
			}
		}
	}
	for _, p := range priors[:2] {
		t.Run("expired/"+p.name, func(t *testing.T) {
			c, j, before := confirmFixture(t, p.delivery, p.native())
			saves := 0
			c.st.fs = &stateFS{hold: func(string) { saves++ }}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := c.confirmGoalDelivery(ctx, j); err != errPromptUnconfirmed || saves != 0 || j.GoalDelivery != p.delivery {
				t.Fatalf("expired decision wrote or confirmed: err=%v saves=%d", err, saves)
			}
			if now, _ := os.ReadFile(c.st.path); !bytes.Equal(now, before) {
				t.Fatal("expired decision changed the pathname")
			}
			for _, follow := range []string{"none", "block", "failed-block"} {
				confirmFollowUps(t, c, j, follow)
				if deliveryConfirmed(reloadedJob(c)) {
					t.Fatal("expired decision reloads as delivered")
				}
			}
		})
		t.Run("expired-while-waiting-for-state/"+p.name, func(t *testing.T) {
			c, j, _ := confirmFixture(t, p.delivery, p.native())
			saves := 0
			c.st.fs = &stateFS{hold: func(string) { saves++ }}
			ctx, cancel := context.WithCancel(context.Background())
			c.st.mu.Lock()
			done := make(chan error, 1)
			go func() { done <- c.confirmGoalDelivery(ctx, j) }()
			cancel()
			c.st.mu.Unlock()
			if err := <-done; err != errPromptUnconfirmed || saves != 0 || deliveryConfirmed(reloadedJob(c)) {
				t.Fatalf("expiry while waiting for the state confirmed: err=%v saves=%d", err, saves)
			}
		})
	}
	accepted := []struct {
		name string
		fs   func(cancel context.CancelFunc) *stateFS
	}{
		{"live", func(context.CancelFunc) *stateFS { return nil }},
		{"deadline-during-save", func(cancel context.CancelFunc) *stateFS { return &stateFS{hold: func(string) { cancel() }} }},
		{"dir-sync-fails-after-rename", func(context.CancelFunc) *stateFS {
			return &stateFS{syncDir: func(*os.File) error { return errInjected }}
		}},
	}
	for _, p := range priors[:2] {
		for _, a := range accepted {
			t.Run("accepted/"+p.name+"/"+a.name, func(t *testing.T) {
				c, j, before := confirmFixture(t, p.delivery, p.native())
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c.st.fs = a.fs(cancel)
				var logs bytes.Buffer
				prior := log.Writer()
				log.SetOutput(&logs)
				err := c.confirmGoalDelivery(ctx, j)
				log.SetOutput(prior)
				// The caller takes the no-block branch on nil.
				if err != nil || !deliveryConfirmed(j) || (j.NativeGoal != nil && !j.NativeGoal.PromptConfirmed) || len(c.st.LaunchBlocks) != 0 {
					t.Fatalf("an on-time decision was refused: %v", err)
				}
				loaded := loadState(c.st.path)
				if !deliveryConfirmed(loaded.Jobs[7]) || len(loaded.LaunchBlocks) != 0 {
					t.Fatal("an accepted confirmation did not reload as delivered")
				}
				if a.name == "dir-sync-fails-after-rename" {
					if !bytes.Contains(logs.Bytes(), []byte("goal-delivery-durability-uncertain")) {
						t.Fatal("uncertain durability was not recorded")
					}
					// Model a host crash that loses the unsynced rename.
					if err := os.WriteFile(c.st.path, before, 0600); err != nil {
						t.Fatal(err)
					}
					if deliveryConfirmed(reloadedJob(c)) {
						t.Fatal("a lost rename reloads as delivered")
					}
				}
			})
		}
	}
	// A process stop inside the call has no return value: the disk decides.
	for _, stop := range []string{"before-rename", "after-rename"} {
		t.Run("interrupted/"+stop, func(t *testing.T) {
			c, j, _ := confirmFixture(t, "pending", &dispatchGoal{})
			if stop == "before-rename" {
				c.st.fs = &stateFS{hold: func(string) { panic("stop") }}
			} else {
				c.st.fs = &stateFS{syncDir: func(*os.File) error { panic("stop") }}
			}
			func() {
				defer func() { _ = recover() }()
				_ = c.confirmGoalDelivery(context.Background(), j)
			}()
			if got := deliveryConfirmed(reloadedJob(c)); got != (stop == "after-rename") {
				t.Fatalf("interrupted %s reloaded delivered=%v", stop, got)
			}
		})
	}
}

// V8: the real non-RC caller (production spawn over an OpenCode fake host).
func TestGoalConfirmationOnRealCaller(t *testing.T) {
	for _, fault := range []string{"late-save", "dir-sync"} {
		t.Run(fault, func(t *testing.T) { openCodeFullGoalSpawn(t, "", fault) })
	}
}
