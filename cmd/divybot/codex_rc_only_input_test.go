package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// REVIEW-o94 (the reviewer's probe): a trust dialog is terminal for the attempt,
// even if a later observation would be ready.
func TestCodexPromptTrustDialogRefusalIsTerminal(t *testing.T) {
	reads, waits, accepts, submits := 0, 0, 0, 0
	err := deliverCodexPrompt(context.Background(), promptFixture().Agent, promptFixtureGoal, promptCalls{
		observe: func(context.Context) (promptSnapshot, error) {
			reads++
			s := promptFixture()
			if reads == 1 {
				s.Screen = "Trust this folder?\n1. Trust and continue\n2. No, quit\n›"
			}
			return s, nil
		},
		wait:   func(context.Context) bool { waits++; return waits < 5 },
		submit: func(context.Context, string) error { submits++; return nil },
		native: func(ctx context.Context, _ string, submit func(context.Context) error) error {
			accepts++
			return submit(ctx)
		},
	})
	if err == nil || reads != 1 || waits != 0 || accepts != 0 || submits != 0 {
		t.Fatalf("trust refusal not terminal: err=%v reads=%d waits=%d accepts=%d submits=%d", err, reads, waits, accepts, submits)
	}
}

// REVIEW-o94 P2 (the reviewer's probe): the real action spool refuses a send to
// a retained Codex job without Remote Control, before any herdr effect.
func TestNonRCCodexActionIsRefusedBeforeAnyEffect(t *testing.T) {
	c, spool, receipts := actionFixture(t)
	req := boundActionFixture(t, c, receipts)
	j := c.st.Jobs[7]
	j.Agent = "codex"
	j.RemoteControl = nil
	j.GoalDelivery = "confirmed"
	j.NativeGoal = &dispatchGoal{PromptConfirmed: true}
	c.cfg.RemoteControl.defaults()
	req.Action = "send"
	req.Payload.Text = "Read the fixture assignment."
	record := filepath.Join(receipts, j.DispatchKey, "record")
	d := dispatchBinding{SchemaVersion: 1, RunID: "orchid-" + j.DispatchKey,
		Issue: dispatchIssue{Repo: "example/repo", Number: 7}, Source: "codex",
		State: "dispatched", Host: j.Host,
		Location: &dispatchLocation{PaneID: j.Pane, WorkspaceID: j.Workspace}}
	b, _ := json.Marshal(d)
	if err := os.WriteFile(filepath.Join(record, "dispatch.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record, "binding.json"), []byte(`{"Repo":"example/repo","NativeSessionID":"fixture-thread"}`), 0600); err != nil {
		t.Fatal(err)
	}
	c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
		return []AgentInfo{{Agent: "codex", AgentStatus: "idle", Cwd: "/fixture/issue-7", PaneID: j.Pane, WorkspaceID: j.Workspace}}, nil
	}
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "calls")
	t.Setenv("RC_ONLY_CALLS", calls)
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RC_ONLY_CALLS\"\nprintf '{\"result\":{}}\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c.hosts[j.Host] = Host{Name: j.Host, Home: root, RemoteControl: &c.cfg.RemoteControl}
	actionDrop(t, spool, req)
	c.actionTick(context.Background())
	r := actionResult(t, receipts)
	raw, _ := os.ReadFile(calls)
	if r.Outcome == "accepted" || r.Reason != "native_identity_unavailable" || strings.Contains(string(raw), "agent prompt") || strings.Contains(string(raw), "send-keys") {
		t.Fatalf("non-RC Codex action was delivered: outcome=%s reason=%s calls=%q", r.Outcome, r.Reason, raw)
	}
}

// Supervision sends no PR relay, nudge or continuation to a Codex job without
// Remote Control; the same job under Remote Control gets the relay (control).
func TestNonRCCodexJobGetsNoSupervisionInput(t *testing.T) {
	for _, rc := range []bool{false, true} {
		c, j, _, _, _, _ := agyCompletionFixture(t, "")
		j.Agent, j.PR, j.LastPoke = "codex", 8, time.Now()
		j.RemoteControl = nil
		if rc {
			j.RemoteControl = syntheticRemoteRun(t)
		}
		ssh, err := exec.LookPath("ssh")
		if err != nil {
			t.Fatal("fixture ssh")
		}
		callLog := filepath.Join(filepath.Dir(ssh), "relay-log")
		t.Setenv("AGY_RELAY_LOG", callLog)
		script, _ := os.ReadFile(ssh)
		script = []byte(strings.Replace(string(script), "s=sys.argv[-1]\n", "s=sys.argv[-1]\nwith open(os.environ['AGY_RELAY_LOG'],'a') as f:f.write(s+'\\n')\n", 1))
		if os.WriteFile(ssh, script, 0700) != nil {
			t.Fatal("fixture log")
		}
		gh := "#!/bin/sh\nprintf '%s\\n' '{\"number\":8,\"state\":\"OPEN\",\"mergeable\":\"MERGEABLE\",\"statusCheckRollup\":[{\"name\":\"check\",\"conclusion\":\"FAILURE\"}]}'\n"
		if os.WriteFile(filepath.Join(filepath.Dir(ssh), "gh"), []byte(gh), 0700) != nil {
			t.Fatal("fixture gh")
		}
		var native []string
		c.actions.codexInput = func(_ context.Context, _ Host, _ *Job, text string) error { native = append(native, text); return nil }
		c.pollPR(context.Background(), j.Issue, j, c.hosts[j.Host], "idle")
		commands, _ := os.ReadFile(callLog)
		// Never through the pane; under Remote Control, through the native owner.
		if strings.Contains(string(commands), "'agent' 'prompt'") || (len(native) == 1) != rc {
			t.Fatalf("remote control %v: native=%v pane=%s", rc, native, commands)
		}
	}
}

func TestCodexWithoutRemoteControlIsOnlyCodex(t *testing.T) {
	for agent, want := range map[string]bool{"codex": true, "claude": false, "agy": false, "opencode": false, "opencode-run": false, "codex-run": false} {
		if got := codexWithoutRemoteControl(&Job{Agent: agent}); got != want {
			t.Fatalf("%s: %v, want %v", agent, got, want)
		}
	}
	if codexWithoutRemoteControl(&Job{Agent: "codex", RemoteControl: &remoteControlRun{}}) || codexWithoutRemoteControl(nil) {
		t.Fatal("a Remote Control or absent job was treated as non-RC Codex")
	}
}

func relayLogFixture(t *testing.T, gh string) string {
	t.Helper()
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal("fixture ssh")
	}
	callLog := filepath.Join(filepath.Dir(ssh), "relay-log")
	t.Setenv("AGY_RELAY_LOG", callLog)
	script, _ := os.ReadFile(ssh)
	script = []byte(strings.Replace(string(script), "s=sys.argv[-1]\n", "s=sys.argv[-1]\nwith open(os.environ['AGY_RELAY_LOG'],'a') as f:f.write(s+'\\n')\n", 1))
	if os.WriteFile(ssh, script, 0700) != nil || os.WriteFile(filepath.Join(filepath.Dir(ssh), "gh"), []byte(gh), 0700) != nil {
		t.Fatal("fixture log")
	}
	return callLog
}

// The partial-merge fan-out nudge is never sent to a Codex job without Remote
// Control; the same job under Remote Control is nudged (control).
func TestNonRCCodexJobGetsNoFanoutNudge(t *testing.T) {
	for _, rc := range []bool{false, true} {
		c, j, _, _, _, _ := agyCompletionFixture(t, "")
		j.Agent, j.PR, j.Home, j.FanoutNudgedAt = "codex", 8, nil, time.Time{}
		j.Repo, j.Title = "example/repo", "[example/repo#5] fixture subtask"
		j.RemoteControl = nil
		if rc {
			j.RemoteControl = syntheticRemoteRun(t)
		}
		callLog := relayLogFixture(t, "#!/bin/sh\ncase \"$1\" in pr) echo '{\"state\":\"MERGED\"}';; *) echo '{\"state\":\"OPEN\"}';; esac\n")
		var native []string
		c.actions.codexInput = func(_ context.Context, _ Host, _ *Job, text string) error { native = append(native, text); return nil }
		deferred := c.fanoutGrace(context.Background(), j.Issue, j)
		commands, _ := os.ReadFile(callLog)
		if strings.Contains(string(commands), "'agent' 'prompt'") || (len(native) == 1) != rc || deferred != rc {
			t.Fatalf("remote control %v: native=%v deferred=%v pane=%s", rc, native, deferred, commands)
		}
	}
}

// A stranded, idle Codex job without Remote Control is not poked by the supervisor.
func TestNonRCCodexJobIsNotPokedWhenStranded(t *testing.T) {
	c, j, _, _, _, _ := agyCompletionFixture(t, "")
	j.Agent, j.PR, j.RemoteControl = "codex", 0, nil
	j.SpawnedAt, j.LastPoke = time.Now().Add(-24*time.Hour), time.Time{}
	callLog := relayLogFixture(t, "#!/bin/sh\necho '[]'\n")
	ref := completionRef(j)
	ref.Status = "idle"
	native := 0
	c.actions.codexInput = func(context.Context, Host, *Job, string) error { native++; return nil }
	c.supervise(context.Background(), j.Issue, j, map[int]agentRef{j.Issue: ref}, Issue{Number: j.Issue})
	commands, _ := os.ReadFile(callLog)
	// Not through the pane, and not even offered to the native owner.
	if native != 0 || strings.Contains(string(commands), "'agent' 'prompt'") || strings.Contains(string(commands), "continue — work the assigned issue") {
		t.Fatalf("a non-RC Codex job was poked: native=%d pane=%s", native, commands)
	}
	if strandedPoke(j, "idle", time.Now()) == "" {
		t.Fatal("control: the job is stranded and would be poked")
	}
}
