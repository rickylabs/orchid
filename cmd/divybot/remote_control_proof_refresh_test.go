package main

import (
	"context"
	"testing"
	"time"
)

func TestRemoteControlProofRefreshCannotEraseConflict(t *testing.T) {
	for _, operation := range []string{"completion", "stop"} {
		for _, mode := range []string{"valid", "matching-completed", "matching-child", "late-root", "late-child", "late-child-then-old-complete", "late-root-then-old-complete", "late-root-finished-then-old-complete", "late-foreign"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				run := syntheticRemoteRun(t)
				child := "fixture-owned-child"
				base := remoteIdleHandler(run, "valid")
				rootFullReads := 0
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
						turnID := "old-native-turn"
						if params["threadId"] == child {
							turnID = "old-child-turn"
						}
						return map[string]any{"data": []any{map[string]any{"id": turnID, "status": "completed", "itemsView": "full", "completedAt": time.Now().Unix() - 1, "items": []any{map[string]string{"type": "agentMessage", "phase": "final_answer", "text": "synthetic final"}}}}}
					case "thread/goal/get":
						if operation == "completion" && params["threadId"] == run.NativeSessionID {
							g := fixtureGoal("complete")
							g.ThreadID = run.NativeSessionID
							return map[string]any{"goal": g}
						}
					}
					return base(method, params)
				}, func(method string, params map[string]any) []any {
					// Deliver during the last full terminal read, just before its old response.
					finalRead := false
					if method == "thread/turns/list" && params["threadId"] == run.NativeSessionID && params["itemsView"] == "full" {
						rootFullReads++
						finalRead = rootFullReads == 3
					}
					if !finalRead || mode == "valid" {
						return nil
					}
					thread := run.NativeSessionID
					if mode == "late-child" || mode == "late-child-then-old-complete" || mode == "matching-child" {
						thread = child
					} else if mode == "late-foreign" {
						thread = "foreign-native-thread"
					}
					turnID := "old-native-turn"
					if thread == child {
						turnID = "old-child-turn"
					}
					matched := map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]string{"id": turnID, "status": "completed"}}}
					if mode == "matching-completed" || mode == "matching-child" {
						return []any{matched}
					}
					if mode == "late-root-finished-then-old-complete" {
						finished := map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]string{"id": "new-native-turn", "status": "completed"}}}
						return []any{nativeStartedFixture(thread), finished, matched}
					}
					if mode == "late-child-then-old-complete" || mode == "late-root-then-old-complete" {
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
				t.Logf("operation=%s mode=%s confirmed=%v rootFullReads=%d", operation, mode, confirmed, rootFullReads)
				if confirmed != (mode == "valid" || mode == "matching-completed" || mode == "matching-child" || mode == "late-foreign") {
					t.Fatal("last scoped lifecycle read certified conflicting work")
				}
			})
		}
	}
}

func TestRemoteControlProofRefreshTransitions(t *testing.T) {
	for _, phase := range []string{"initial", "refresh"} {
		for _, mode := range []string{"valid", "matched", "foreign", "unresolved", "resolved-new", "unresolved-other"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				run := syntheticRemoteRun(t)
				base := remoteIdleHandler(run, "valid")
				reads, target := 0, 1
				if phase == "refresh" {
					target = 2
				}
				completed := func(thread, turn string) any {
					return map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]string{"id": turn, "status": "completed"}}}
				}
				h := canonicalFixtureHost(t, "valid", func(method string, params map[string]any) any {
					if method == "thread/turns/list" {
						reads++
						turn := "old-native-turn"
						if reads == target && mode == "resolved-new" {
							turn = "new-native-turn"
						} else if reads == target && mode == "unresolved-other" {
							turn = "other-native-turn"
						}
						return map[string]any{"data": []any{map[string]any{"id": turn, "status": "completed", "itemsView": "full", "items": []any{}}}}
					}
					return base(method, params)
				}, func(method string, params map[string]any) []any {
					if method != "thread/turns/list" || reads != target {
						return nil
					}
					root := run.NativeSessionID
					switch mode {
					case "matched":
						return []any{completed(root, "old-native-turn")}
					case "foreign":
						return []any{nativeStartedFixture("foreign-native-thread")}
					case "unresolved":
						return []any{nativeStartedFixture(root), completed(root, "old-native-turn")}
					case "resolved-new":
						return []any{nativeStartedFixture(root), completed(root, "new-native-turn")}
					case "unresolved-other":
						return []any{nativeStartedFixture(root), completed(root, "other-native-turn")}
					}
					return nil
				})
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				err := h.withCanonicalConnection(ctx, run.NativeSessionID, func(p *goalRPC) error {
					if e := p.remoteThreadIdle(false); e != nil {
						return e
					}
					if phase == "refresh" {
						if e := p.remoteThreadIdle(false); e != nil {
							return e
						}
					}
					return p.reconcileNativeLifecycle()
				})
				if (err == nil) != (mode != "unresolved" && mode != "unresolved-other") {
					t.Fatal("proof refresh accepted an unresolved turn or refused a coherent terminal transition")
				}
			})
		}
	}
}

func TestRemoteControlProofRefreshIsAtomic(t *testing.T) {
	p := &goalRPC{thread: "fixture-native-root"}
	if err := p.recordNativeTurnProof("old-native-turn", "completed"); err != nil {
		t.Fatal("initial proof unavailable")
	}
	before := p.turnProofs[p.thread]
	for _, event := range []remoteNativeTurn{{p.thread, "new-native-turn", "inProgress"}, {p.thread, "old-native-turn", "completed"}} {
		method := "turn/completed"
		if event.Status == "inProgress" {
			method = "turn/started"
		}
		if err := p.nativeTurnNotification(method, fixtureJSON(t, map[string]any{"threadId": event.ThreadID, "turn": map[string]string{"id": event.ID, "status": event.Status}})); err != nil {
			t.Fatal("native notice unavailable")
		}
	}
	if err := p.recordNativeTurnProof("old-native-turn", "completed"); err == nil || p.turnProofs[p.thread] != before {
		t.Fatal("rejected proof refresh changed the retained proof or cursor")
	}
}
