package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteControlContinuousFooterIdentity(t *testing.T) {
	for _, mode := range []string{"valid", "native-context", "missing", "quoted", "truncated", "foreign", "late", "changed-occupant", "changed-sequence", "conflicting-late-hook"} {
		t.Run(mode, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			s := promptFixture()
			s.Agent.Cwd = run.Cwd
			s.Screen = "› Ask Codex to do anything\n  " + run.NativeSessionID
			switch mode {
			case "native-context":
				s.Screen += "     Auto (Shift+Tab to cycle)"
			case "missing":
				s.Screen = "› Ask Codex to do anything"
			case "quoted":
				s.Screen = "• Quoted native identity: " + run.NativeSessionID + "\n› Ask Codex to do anything"
			case "truncated":
				s.Screen = "› Ask Codex to do anything\n" + run.NativeSessionID[:30] + "…"
			case "foreign":
				s.Screen = "› Ask Codex to do anything\n" + syntheticRemoteRun(t).NativeSessionID
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads := 0
			err := verifyCodexFooterAttachment(ctx, s.Agent.Name, run, &dispatchLocation{PaneID: s.Agent.PaneID, WorkspaceID: s.Agent.WorkspaceID}, func() (promptSnapshot, error) {
				reads++
				v := s
				if reads > 1 {
					switch mode {
					case "late":
						cancel()
					case "changed-occupant":
						v.Agent.PaneID = "foreign-pane"
					case "changed-sequence":
						v.Agent.StateChangeSeq++
					case "conflicting-late-hook":
						v.Agent.AgentSession = fixtureJSON(t, map[string]string{"source": "herdr:codex", "agent": "codex", "kind": "id", "value": privateTestID(t)})
					}
				}
				return v, nil
			})
			if (err == nil) != (mode == "valid" || mode == "native-context") {
				t.Fatal("continuous footer binding guard failed")
			}
			if mode == "foreign" && err != goalError("remote-control-hook-mismatch") {
				t.Fatal("foreign attached native identity was not blocked")
			}
		})
	}
}

func TestRemoteControlPreparedGoalBindingRechecks(t *testing.T) {
	for _, mode := range []string{"valid", "absent-proof", "late", "changed-binding", "changed-dispatch", "wrong-private-id", "wrong-job"} {
		t.Run(mode, func(t *testing.T) {
			c, j, _, _, _ := completionFixture(t)
			run := syntheticRemoteRun(t)
			run.NativeSessionID = "thread-fixture"
			run.IdentitySource = "codex-native-status"
			j.RemoteControl = run
			j.NativeGoal = &dispatchGoal{ReceiptKey: j.DispatchKey}
			r, _, e := loadGoalReceipt(c.cfg.Matrix.ReceiptRoot, j, c.cfg.Inbox, nil)
			if e != nil {
				t.Fatal("fixture unavailable")
			}
			base := filepath.Dir(r.file)
			if mode == "wrong-private-id" {
				run.NativeSessionID = privateTestID(t)
			}
			if mode == "wrong-job" {
				j.Pane = "foreign-pane"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id, err := bindRemoteNativeGoal(ctx, r, j, func() error {
				switch mode {
				case "absent-proof":
					return goalError("remote-control-identity-unconfirmed")
				case "late":
					cancel()
				case "changed-binding":
					if os.WriteFile(filepath.Join(base, "binding.json"), []byte(`{"Repo":"foreign/project","NativeSessionID":"thread-fixture"}`), 0600) != nil {
						t.Fatal("fixture unavailable")
					}
				case "changed-dispatch":
					d := *r.dispatch
					d.Host = "foreign-host"
					body, _ := json.Marshal(d)
					if os.WriteFile(filepath.Join(base, "dispatch.json"), body, 0600) != nil {
						t.Fatal("fixture unavailable")
					}
				}
				return nil
			})
			if (err == nil) != (mode == "valid") || mode == "valid" && id != run.NativeSessionID {
				t.Fatal("prepared goal binding accepted unavailable or changed evidence")
			}
		})
	}
}

func TestRemoteControlNativeTurnSignals(t *testing.T) {
	for _, mode := range []string{"completed", "persisted-read", "foreign-notice", "started", "other-turn", "failed", "missing-native", "no-final", "malformed-own"} {
		t.Run(mode, func(t *testing.T) {
			root, turn := privateTestID(t), privateTestID(t)
			p, _ := remoteFixturePort(func(string, map[string]any) any {
				if mode == "missing-native" {
					return map[string]any{"data": []any{}}
				}
				phase := "final_answer"
				if mode == "no-final" {
					phase = "commentary"
				}
				return map[string]any{"data": []any{map[string]any{"id": turn, "status": "completed", "itemsView": "full", "completedAt": time.Now().Unix() - 1, "items": []any{map[string]string{"type": "agentMessage", "phase": phase, "text": "synthetic final"}}}}}
			}, root)
			if mode != "persisted-read" {
				thread, status, method, noticeTurn := root, "completed", "turn/completed", turn
				if mode == "foreign-notice" {
					thread = privateTestID(t)
				}
				if mode == "started" {
					status, method = "inProgress", "turn/started"
				}
				if mode == "failed" {
					status = "failed"
				}
				if mode == "other-turn" {
					noticeTurn = privateTestID(t)
				}
				if mode == "malformed-own" {
					noticeTurn = ""
				}
				params := fixtureJSON(t, map[string]any{"threadId": thread, "turn": map[string]string{"id": noticeTurn, "status": status}})
				err := p.notification(map[string]json.RawMessage{"method": fixtureJSON(t, method), "params": params})
				if mode == "malformed-own" {
					if err == nil {
						t.Fatal("malformed native turn notice accepted")
					}
					return
				}
				if err != nil {
					t.Fatal("native notification fixture failed")
				}
			}
			complete, err := p.lastTurnCompleted()
			want := mode == "completed" || mode == "persisted-read" || mode == "foreign-notice"
			if complete != want || want && err != nil {
				t.Fatal("native lifecycle signal accepted ambiguous completion")
			}
		})
	}
}

func TestRemoteControlInvocationFooterScope(t *testing.T) {
	run := syntheticRemoteRun(t)
	args, err := remoteCodexArgs(nil, run.Cwd, run.NativeSessionID)
	if err != nil || !strings.Contains(strings.Join(args, " "), `-c tui.status_line=["thread-id"]`) {
		t.Fatal("invocation-only native identity footer absent")
	}
}
