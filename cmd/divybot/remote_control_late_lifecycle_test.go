package main

import (
	"context"
	"testing"
	"time"
)

// Astra's independent regressions use the actual owned AF_UNIX/WebSocket
// production transport. Keep the valid and foreign-thread controls alongside
// the late exact-thread interleavings, without duplicating its fixture server.
func nativeStartedFixture(thread string) any {
	return map[string]any{"method": "turn/started", "params": map[string]any{"threadId": thread, "turn": map[string]string{"id": "new-native-turn", "status": "inProgress"}}}
}

func lateNativeGoalNotice(mode string) func(string, map[string]any) []any {
	return func(method string, params map[string]any) []any {
		if method != "thread/goal/get" || mode == "valid" {
			return nil
		}
		thread, _ := params["threadId"].(string)
		if mode == "late-foreign" {
			thread = "foreign-native-thread"
		}
		return []any{nativeStartedFixture(thread)}
	}
}

func TestRemoteControlLateNativeTurnCannotCertifyCompletion(t *testing.T) {
	for _, mode := range []string{"valid", "late-own", "late-foreign"} {
		t.Run(mode, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			base := remoteIdleHandler(run, "valid")
			h := canonicalFixtureHost(t, "valid", func(method string, params map[string]any) any {
				switch method {
				case "thread/turns/list":
					return map[string]any{"data": []any{map[string]any{"id": "old-native-turn", "status": "completed", "itemsView": "full", "completedAt": time.Now().Unix() - 1, "items": []any{map[string]string{"type": "agentMessage", "phase": "final_answer", "text": "synthetic final"}}}}}
				case "thread/goal/get":
					g := fixtureGoal("complete")
					g.ThreadID = run.NativeSessionID
					return map[string]any{"goal": g}
				default:
					return base(method, params)
				}
			}, lateNativeGoalNotice(mode))
			j := &Job{Agent: "codex", RemoteControl: run, NativeGoal: &dispatchGoal{Intent: fixtureIntent()}}
			c := &Coord{}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			complete, err := c.completionEvidence(ctx, h, j, run.NativeSessionID)
			t.Logf("complete=%v error=%v notice=%s", complete, err, mode)
			if mode == "late-own" && complete {
				t.Fatal("new exact-thread turn/started during final goal read certified Done")
			}
			if mode != "late-own" && (err != nil || !complete) {
				t.Fatal("positive/foreign-thread control failed")
			}
		})
	}
}

func TestRemoteControlLateNativeTurnCannotCertifyStop(t *testing.T) {
	for _, mode := range []string{"valid", "late-own", "late-foreign"} {
		t.Run(mode, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			h := canonicalFixtureHost(t, "valid", remoteIdleHandler(run, "valid"), lateNativeGoalNotice(mode))
			j := &Job{Agent: "codex", RemoteControl: run}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := h.stopRemoteRun(ctx, j)
			t.Logf("stopError=%v notice=%s", err, mode)
			if mode == "late-own" && err == nil {
				t.Fatal("exact-thread turn/started conflict certified native Stop")
			}
			if mode != "late-own" && err != nil {
				t.Fatal("positive/foreign-thread Stop control failed")
			}
		})
	}
}

func TestRemoteControlStopThreadFinalGoalNotice(t *testing.T) {
	for _, mode := range []string{"valid", "late-own", "late-foreign"} {
		t.Run(mode, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			reads := 0
			h := canonicalFixtureHost(t, "valid", remoteIdleHandler(run, "valid"), func(method string, params map[string]any) []any {
				if method != "thread/goal/get" {
					return nil
				}
				reads++
				if reads != 2 {
					return nil
				}
				return lateNativeGoalNotice(mode)(method, params)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := h.withCanonicalConnection(ctx, run.NativeSessionID, func(p *goalRPC) error {
				return p.stopRemoteThread(nil)
			})
			if (err == nil) != (mode != "late-own") {
				t.Fatal("final goal read accepted conflicting native Stop proof")
			}
		})
	}
}

// Inject after the last read of the whole proof, beyond earlier root/child idle
// checks. A verified child's notice remains relevant while the root is current;
// a foreign daemon tenant never enters either the work or lifecycle scope.
func TestRemoteControlFinalScopedLifecycle(t *testing.T) {
	for _, operation := range []string{"completion", "stop"} {
		for _, mode := range []string{"valid", "matching-completed", "late-root", "late-child", "late-child-then-old-complete", "late-foreign"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				run := syntheticRemoteRun(t)
				child := "fixture-owned-child"
				base := remoteIdleHandler(run, "valid")
				loadedReads := 0
				h := canonicalFixtureHost(t, "valid", func(method string, params map[string]any) any {
					switch method {
					case "thread/list":
						data := []any{}
						if params["archived"] == false {
							data = append(data, map[string]string{"id": child, "parentThreadId": run.NativeSessionID})
						}
						return map[string]any{"data": data, "nextCursor": nil}
					case "thread/read":
						row := remoteThreadFixture(run)
						if params["threadId"] == child {
							row["thread"].(map[string]any)["id"] = child
							row["thread"].(map[string]any)["parentThreadId"] = run.NativeSessionID
						}
						return row
					case "thread/turns/list":
						return map[string]any{"data": []any{map[string]any{"id": "old-native-turn", "status": "completed", "itemsView": "full", "completedAt": time.Now().Unix() - 1, "items": []any{map[string]string{"type": "agentMessage", "phase": "final_answer", "text": "synthetic final"}}}}}
					case "thread/goal/get":
						if operation == "completion" && params["threadId"] == run.NativeSessionID {
							g := fixtureGoal("complete")
							g.ThreadID = run.NativeSessionID
							return map[string]any{"goal": g}
						}
					}
					return base(method, params)
				}, func(method string, params map[string]any) []any {
					finalRead := operation == "completion" && method == "thread/goal/get" && params["threadId"] == run.NativeSessionID
					if method == "thread/loaded/list" {
						loadedReads++
						finalRead = operation == "stop" && loadedReads == 3
					}
					if !finalRead || mode == "valid" {
						return nil
					}
					thread := run.NativeSessionID
					if mode == "late-child" || mode == "late-child-then-old-complete" {
						thread = child
					} else if mode == "late-foreign" {
						thread = "foreign-native-thread"
					}
					matched := map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]string{"id": "old-native-turn", "status": "completed"}}}
					if mode == "matching-completed" {
						return []any{matched}
					}
					if mode == "late-child-then-old-complete" {
						return []any{nativeStartedFixture(thread), matched}
					}
					return []any{nativeStartedFixture(thread)}
				})
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				j := &Job{Agent: "codex", RemoteControl: run}
				confirmed := false
				if operation == "completion" {
					j.NativeGoal = &dispatchGoal{Intent: fixtureIntent()}
					c := &Coord{}
					complete, err := c.completionEvidence(ctx, h, j, run.NativeSessionID)
					confirmed = complete && err == nil
				} else {
					confirmed = h.stopRemoteRun(ctx, j) == nil
				}
				if confirmed != (mode == "valid" || mode == "matching-completed" || mode == "late-foreign") {
					t.Fatal("last scoped lifecycle read certified conflicting work")
				}
			})
		}
	}
}
