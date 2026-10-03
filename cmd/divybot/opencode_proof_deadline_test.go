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
	h, j, calls := openCodeHostFixture(t, "")
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
	if strings.Contains(calls(), `"agent", "get"`) {
		t.Fatal("OpenCode registration re-read the unsupported generic contract")
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
	for _, phase := range []string{"", "entry", "before", "sessions", "export", "after"} {
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
			if phase == "entry" {
				cancel()
			}
			confirmed, completed, err := observeOpenCodeSession(ctx, j, calls)
			if phase == "" {
				if !confirmed || !completed || err != nil || run.SessionID != "ses_fixture" || run.CreatedAt != 2000 {
					t.Fatal("positive native store proof was lost", confirmed, completed, err)
				}
			} else if confirmed || completed || err != matrixReason("opencode-output-unconfirmed") || run.SessionID != "" || run.CreatedAt != 0 {
				t.Fatal("canceled proof accepted or wrote native identity", confirmed, completed, err, run.SessionID)
			}
			if phase == "entry" && reads != 0 {
				t.Fatal("expired observation performed native reads")
			}
		})
	}
}

func TestOpenCodePrivateBindingDeadlineCannotPublish(t *testing.T) {
	for _, phase := range []string{"", "entry", "before", "observe", "after"} {
		t.Run(phase, func(t *testing.T) {
			root, j, receipt := openCodeBindingFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "entry" {
				cancel()
			}
			reads, observations := 0, 0
			read := func(context.Context, string) (AgentInfo, error) {
				reads++
				if reads == 1 && phase == "before" || reads == 2 && phase == "after" {
					cancel()
				}
				return AgentInfo{Agent: j.Agent, Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, Cwd: j.OpenCode.Cwd, InteractiveReady: true, StateChangeSeq: 1}, nil
			}
			observe := func(context.Context, *Job) (bool, bool, error) {
				observations++
				if phase == "observe" {
					cancel()
				}
				return true, false, nil
			}
			bound := retryOpenCodeNativeBinding(ctx, root, "fixture/inbox", nil, j, read, observe)
			raw, err := os.ReadFile(filepath.Join(filepath.Dir(receipt.file), "binding.json"))
			if err != nil {
				t.Fatal(err)
			}
			if phase == "" {
				if !bound || !bytes.Contains(raw, []byte("ses_fixture")) {
					t.Fatal("live-context native identity missing")
				}
			} else {
				if bound || bytes.Contains(raw, []byte("ses_fixture")) {
					t.Fatal("expired native identity was published")
				}
				if phase == "entry" && reads != 0 || phase == "before" && observations != 0 || phase == "observe" && reads != 1 {
					t.Fatal("canceled proof kept reading native state", reads, observations)
				}
			}
		})
	}
}

func TestGoalDeliveryDeadlineCannotConfirm(t *testing.T) {
	for _, phase := range []string{"", "entry", "save"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "entry" {
				cancel()
			}
			calls := 0
			err := confirmGoalDeliveryBeforeDeadline(ctx, func() error {
				calls++
				if phase == "save" {
					cancel()
				}
				return nil
			})
			if phase == "" {
				if err != nil || calls != 1 {
					t.Fatal("live confirmation failed", err, calls)
				}
			} else if err != errPromptUnconfirmed || phase == "entry" && calls != 0 {
				t.Fatal("canceled save could confirm a launch", err, calls)
			}
		})
	}
}

func TestOpenCodeCanceledPersistenceNeverSendsPrompt(t *testing.T) {
	h, j, calls := openCodeHostFixture(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := h.injectOpenCodeGoal(ctx, j, runPointer, func() error { cancel(); return nil })
	if err != matrixReason("opencode-output-unconfirmed") || strings.Contains(calls(), `"agent", "prompt"`) {
		t.Fatal("canceling persistence allowed a native effect", err)
	}
}

func TestOpenCodePrivateIdentityWriteDeadline(t *testing.T) {
	for _, phase := range []string{"", "entry", "write", "error"} {
		t.Run(phase, func(t *testing.T) {
			_, _, receipt := openCodeBindingFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "entry" {
				cancel()
			}
			writes := 0
			bound := publishOpenCodeNativeIdentity(ctx, "ses_fixture", func(id *string) error {
				writes++
				if phase == "error" {
					return errMatrix
				}
				err := receipt.writeNativeIdentity(id)
				if id != nil && phase == "write" {
					cancel()
				}
				return err
			})
			raw, err := os.ReadFile(filepath.Join(filepath.Dir(receipt.file), "binding.json"))
			if err != nil {
				t.Fatal(err)
			}
			if phase == "" {
				if !bound || writes != 1 || !bytes.Contains(raw, []byte("ses_fixture")) {
					t.Fatal("active write failed to publish native identity")
				}
			} else if bound || bytes.Contains(raw, []byte("ses_fixture")) || phase == "entry" && writes != 0 || phase == "write" && writes != 2 {
				t.Fatal("expired or failed write retained identity or reported success", bound, writes)
			}
		})
	}
}
