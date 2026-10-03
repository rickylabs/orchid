package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeRegistrationPendingNativeProof(t *testing.T) {
	h, j, _ := openCodeHostFixture(t, "")
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	o := Overrides{Model: j.OpenCode.Route.qualifiedModel(), Effort: "high"}
	receipt := registrationReceipt(t, "opencode", o)
	if _, _, err := h.spawnAgent(context.Background(), j.Label, j.OpenCode.Cwd, nil, "opencode", o, receipt); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), string(nativeUnsupported)) || !strings.Contains(output.String(), "native identity pending OpenCode prompt proof") {
		t.Fatal("registration used the generic unsupported identity contract", output.String())
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(receipt.file), "binding.json"))
	if err != nil || bytes.Contains(raw, []byte("ses_fixture")) {
		t.Fatal("registration invented a native identity")
	}
}

func TestOpenCodePromptWaitDeadlineCannotConfirm(t *testing.T) {
	t.Run("expired before read", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		reads := 0
		err := waitOpenCodePrompt(ctx, func(context.Context) (bool, bool, error) { reads++; return true, true, nil })
		if err != matrixReason("opencode-output-unconfirmed") || reads != 0 {
			t.Fatal("expired confirmation budget read or accepted native proof", err, reads)
		}
	})
	t.Run("cancel during positive read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := waitOpenCodePrompt(ctx, func(context.Context) (bool, bool, error) { cancel(); return true, true, nil })
		if err != matrixReason("opencode-output-unconfirmed") {
			t.Fatal("late positive observation bypassed cancellation", err)
		}
	})
	t.Run("fresh session after empty read", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		reads := 0
		err := waitOpenCodePrompt(ctx, func(context.Context) (bool, bool, error) { reads++; return reads == 2, false, nil })
		if err != nil || reads != 2 || ctx.Err() != nil {
			t.Fatal("bounded re-read failed to certify a fresh streaming session", err, reads)
		}
	})
	t.Run("never appears", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		err := waitOpenCodePrompt(ctx, func(context.Context) (bool, bool, error) { return false, false, nil })
		if err != matrixReason("opencode-output-unconfirmed") || ctx.Err() == nil {
			t.Fatal("missing native session escaped its deadline", err)
		}
	})
}

func TestOpenCodeObservationExpiredCannotBindSession(t *testing.T) {
	for _, phase := range []string{"", "before", "sessions", "export", "after"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			route := openCodeRoute{"fixture-provider", "fixture-model", "high"}
			prompt := "Synthetic full goal and helper."
			run := &openCodeRun{Route: route, Cwd: "/fixture/checkout", NotBefore: 1000, ExpectedPromptDigest: shaText([]byte(prompt))}
			j := &Job{Agent: "opencode", Label: "fixture-agent", Pane: "fixture-pane", Workspace: "fixture-workspace", OpenCode: run}
			native := *run
			native.SessionID = "ses_fixture"
			raw := fixtureExportPrompt(t, &native, prompt)
			reads := 0
			calls := openCodeObservationCalls{
				agent: func(context.Context, string) (AgentInfo, error) {
					reads++
					if reads == 1 && phase == "before" || reads == 2 && phase == "after" {
						cancel()
					}
					return AgentInfo{Agent: j.Agent, Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, Cwd: run.Cwd, InteractiveReady: true, StateChangeSeq: 1}, nil
				},
				sessions: func(context.Context, *openCodeRun) ([]openCodeSession, error) {
					if phase == "sessions" {
						cancel()
					}
					return []openCodeSession{{"ses_fixture", run.Cwd, 2000}}, nil
				},
				export: func(context.Context, *openCodeRun, string) ([]byte, error) {
					if phase == "export" {
						cancel()
					}
					return raw, nil
				},
			}
			confirmed, completed, err := observeOpenCodeSession(ctx, j, calls)
			if phase == "" {
				if !confirmed || !completed || err != nil || run.SessionID != "ses_fixture" || run.CreatedAt != 2000 {
					t.Fatal("positive native store proof was lost", confirmed, completed, err)
				}
			} else if confirmed || completed || err != matrixReason("opencode-output-unconfirmed") || run.SessionID != "" || run.CreatedAt != 0 {
				t.Fatal("canceled proof accepted or wrote native identity", confirmed, completed, err, run.SessionID)
			}
		})
	}
}
