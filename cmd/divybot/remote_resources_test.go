package main

import (
	"context"
	"testing"
	"time"
)

func TestRemoteResourcesNativeRelease(t *testing.T) {
	for _, mode := range []string{"valid", "active", "archive-failed", "still-loaded"} {
		t.Run(mode, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			child, neighbor := privateTestID(t), privateTestID(t)
			base := remoteIdleHandler(run, "valid")
			loaded, archives := true, 0
			p, _ := remoteFixturePort(func(method string, params map[string]any) any {
				id, _ := params["threadId"].(string)
				switch method {
				case "thread/read":
					row := remoteThreadFixture(run)
					thread := row["thread"].(map[string]any)
					thread["id"] = id
					if id == child {
						thread["parentThreadId"] = run.NativeSessionID
					}
					if id == neighbor {
						thread["sessionId"] = neighbor
					}
					if !loaded && id != neighbor {
						thread["status"] = map[string]string{"type": "notLoaded"}
					}
					if mode == "active" && id == child {
						thread["status"] = map[string]string{"type": "active"}
					}
					return row
				case "thread/loaded/list":
					data := []string{neighbor}
					if loaded {
						data = append(data, run.NativeSessionID, child)
					}
					return map[string]any{"data": data, "nextCursor": nil}
				case "thread/list":
					data := []any{}
					if params["archived"] == false {
						data = append(data, map[string]string{"id": child, "parentThreadId": run.NativeSessionID})
					}
					return map[string]any{"data": data, "nextCursor": nil}
				case "thread/archive":
					if id != run.NativeSessionID {
						t.Fatal("archive escaped exact native tree")
					}
					archives++
					if mode == "archive-failed" {
						return map[string]any{"unexpected": true}
					}
					if mode != "still-loaded" {
						loaded = false
					}
					return map[string]any{}
				}
				return base(method, params)
			}, run.NativeSessionID)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			p.ctx = ctx
			err := p.releaseRemoteResources()
			if (err == nil) != (mode == "valid") {
				t.Fatalf("release result: %v", err)
			}
			if mode == "active" && archives != 0 {
				t.Fatal("active descendant archived")
			}
			if mode == "valid" {
				if loaded || archives != 1 {
					t.Fatal("resources retained")
				}
				if err := p.releaseRemoteResources(); err != nil || archives != 1 {
					t.Fatal("release retry repeated native archive")
				}
				// Repeated completions on the shared daemon retain only the neighbor.
				for cycle := 0; cycle < 8; cycle++ {
					run.NativeSessionID, child = privateTestID(t), privateTestID(t)
					p.thread, loaded = run.NativeSessionID, true
					p.turnProofs = nil
					if err := p.releaseRemoteResources(); err != nil || loaded || archives != cycle+2 {
						t.Fatal("completed thread resources accumulated")
					}
				}
			}
		})
	}
}
