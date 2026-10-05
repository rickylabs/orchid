package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// JSON keeps these behavioral controls runnable against the pre-fix record.
func fixtureExpectedPrompt(t *testing.T, run *openCodeRun, digest string) {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"expectedPromptDigest": digest})
	if json.Unmarshal(data, run) != nil {
		t.Fatal("private prompt binding fixture failed")
	}
}

func fixtureExportPrompt(t *testing.T, run *openCodeRun, prompt string) []byte {
	t.Helper()
	var record map[string]any
	if json.Unmarshal(openCodeExportFixture(run, "OK"), &record) != nil {
		t.Fatal("native export fixture failed")
	}
	user := record["messages"].([]any)[0].(map[string]any)
	user["parts"].([]any)[0].(map[string]any)["text"] = prompt
	raw, _ := json.Marshal(record)
	return raw
}

func TestOpenCodeFullGoalExportBoundToPrivateDigest(t *testing.T) {
	goal := "A complete synthetic brief.\n" + finalCommentBodyInstruction(strings.Repeat("d", 64))
	for _, tc := range []struct {
		name, prompt, digest string
		want                 bool
	}{
		{"full", goal, shaText([]byte(goal)), true},
		{"pointer", runPointer, shaText([]byte(goal)), false},
		{"truncated", goal[:30], shaText([]byte(goal)), false},
		{"foreign", goal + "different", shaText([]byte(goal)), false},
		{"missing digest", goal, "", false},
		{"legacy pointer without digest", runPointer, "", false},
		{"malformed digest", goal, "private-invalid", false},
		{"foreign digest", goal, shaText([]byte("different")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &openCodeRun{Route: openCodeRoute{"fixture-provider", "fixture-model", "high"}, Cwd: "/fixture/checkout", SessionID: "ses_fixture", NotBefore: 1000}
			fixtureExpectedPrompt(t, run, tc.digest)
			confirmed, complete, err := inspectOpenCodeExport(fixtureExportPrompt(t, run, tc.prompt), run)
			if tc.want {
				if !confirmed || !complete || err != nil {
					t.Fatal("complete first goal did not match its private binding", err)
				}
			} else if confirmed || complete || err != matrixReason("opencode-output-unconfirmed") {
				t.Fatal("unbound or changed first goal was accepted")
			}
		})
	}
}

func TestWorkerBlankGoalRejectedBeforeArtifacts(t *testing.T) {
	for _, staged := range []bool{false, true} {
		for _, goal := range []string{"", " \n\t\r"} {
			t.Run(strings.Join([]string{map[bool]string{true: "staged", false: "direct"}[staged], map[bool]string{true: "empty", false: "whitespace"}[goal == ""]}, "/"), func(t *testing.T) {
				root := t.TempDir()
				if exec.Command("git", "init", "--quiet", root).Run() != nil {
					t.Fatal("synthetic repository initialization failed")
				}
				exclude := filepath.Join(root, ".git", "info", "exclude")
				before, _ := os.ReadFile(exclude)
				if (Host{}).stageWorkerGoal(context.Background(), root, strings.Repeat("d", 64), goal, staged) == nil {
					t.Fatal("blank rendered goal passed pre-spawn staging")
				}
				after, _ := os.ReadFile(exclude)
				if string(after) != string(before) {
					t.Fatal("blank goal changed repository exclusions")
				}
				for _, name := range []string{finalCommentBodyFile, ".divybot-goal.md"} {
					if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
						t.Fatal("blank goal staged a launch artifact")
					}
				}
			})
		}
	}
}

func TestOpenCodePromptBindingRejectsBeforeNativeEffects(t *testing.T) {
	goal := "Synthetic complete first goal.\n" + finalCommentBodyInstruction(strings.Repeat("d", 64))
	for _, mode := range []string{"missing", "malformed", "foreign", "truncated", "empty", "whitespace", "nil-run", "nil-job"} {
		t.Run(mode, func(t *testing.T) {
			h, j, calls := openCodeHostFixture(t, "")
			prompt := goal
			j.OpenCode.ExpectedPromptDigest = shaText([]byte(goal))
			switch mode {
			case "missing":
				j.OpenCode.ExpectedPromptDigest = ""
			case "malformed":
				j.OpenCode.ExpectedPromptDigest = "private-invalid"
			case "foreign":
				j.OpenCode.ExpectedPromptDigest = shaText([]byte("different"))
			case "truncated":
				prompt = goal[:30]
			case "empty", "whitespace":
				prompt = map[string]string{"empty": "", "whitespace": " \n\t\r"}[mode]
				// Even a matching digest cannot authorize a blank prompt.
				j.OpenCode.ExpectedPromptDigest = shaText([]byte(prompt))
			case "nil-run":
				j.OpenCode = nil
			case "nil-job":
				j = nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			saves := 0
			err := h.injectOpenCodeGoal(ctx, j, prompt, func() error { saves++; return nil })
			if err != matrixReason("opencode-output-unconfirmed") || calls() != "" || saves != 0 {
				t.Fatal("invalid prompt binding reached a native read, save or prompt")
			}
		})
	}
}

// Exercise production spawn with local fake SSH, Herdr and OpenCode endpoints.
// Preparation never fetches a repository or calls a provider/native agent.
func TestOpenCodeSpawnDeliversFullRenderedGoalOnce(t *testing.T) {
	testOpenCodeFullGoalSpawn(t, "")
}

func testOpenCodeFullGoalSpawn(t *testing.T, mode string) {
	t.Helper()
	openCodeFullGoalSpawn(t, mode, "")
}

// confirmFault faults only the goal confirmation save on the real spawn path:
// "late-save" cancels the launch context inside it, "dir-sync" fails it after
// the rename. Either way the on-time decision stands and the launch continues.
func openCodeFullGoalSpawn(t *testing.T, mode, confirmFault string) {
	t.Helper()
	h, _, calls := openCodeHostFixture(t, mode)
	h.SSH, h.WorkdirRoot = "fixture-host", h.Home
	cwd := filepath.Join(h.Home, "issue-7")
	if exec.Command("git", "init", "--quiet", cwd).Run() != nil {
		t.Fatal("synthetic repository initialization failed")
	}
	t.Setenv("OC_FIXTURE_CWD", cwd)
	t.Setenv("OC_FIXTURE_LABEL", "opencode-7")
	statePath := filepath.Join(h.Home, "state.json")
	t.Setenv("OC_FIXTURE_STATE", statePath)
	fakeSSH := `#!/usr/bin/env python3
import subprocess,sys
script=sys.argv[-1]
if 'git fetch --depth=1' in script or '.credentials.json' in script or 'SJ="$HOME/.claude/settings.json"' in script:sys.exit(0)
sys.exit(subprocess.run(['/bin/sh','-c',script]).returncode)
`
	writeFixture(t, filepath.Join(h.Home, ".local", "bin", "ssh"), fakeSSH)
	if os.Chmod(filepath.Join(h.Home, ".local", "bin", "ssh"), 0700) != nil {
		t.Fatal("synthetic SSH endpoint unavailable")
	}
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox", BranchPrefix: "fixture/", Targets: []Target{{Label: "fixture-target", Repo: "fixture/project"}}}, auth: &AuthStore{}, st: loadState(statePath)}
	// Longer than Job.Goal's public summary: that summary cannot supply the prompt.
	body := "Full brief beginning.\n" + strings.Repeat("synthetic task detail; ", 90) + "\nFull brief end."
	is := Issue{Number: 7, Title: "Synthetic goal", Body: body, Labels: []string{"fixture-target"}}
	o := Overrides{Model: "fixture-provider/fixture-model", Router: "fixture-provider", Effort: "high", Prompt: "Synthetic owner instruction: report only; no PR.", Profile: "fix"}
	r := registrationReceipt(t, "opencode", o)
	r.dispatch.Host = h.Name
	// The dispatcher's pinned Harness profile; the target checkout carries a conflicting decoy.
	r.profile = &workerProfile{Name: "fix", Revision: strings.Repeat("e", 40), Text: "Synthetic Harness fix process.\n"}
	writeFixture(t, filepath.Join(cwd, "profiles", "fix.md"), "Synthetic target decoy process.\n")
	if err := r.writeDispatch("reserved", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.file), "binding.json"), []byte(`{"Repo":"fixture/project","BriefDigest":"synthetic-full-brief"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	fired := false
	if confirmFault != "" {
		// The confirmation save is the first one whose job already says confirmed.
		confirming := func() bool {
			if j := c.st.Jobs[7]; !fired && j != nil && j.GoalDelivery == "confirmed" {
				fired = true
				return true
			}
			return false
		}
		c.st.fs = &stateFS{}
		if confirmFault == "late-save" {
			c.st.fs.hold = func(string) {
				if confirming() {
					cancel()
				}
			}
		}
		if confirmFault == "dir-sync" {
			c.st.fs.syncDir = func(d *os.File) error {
				if confirming() {
					return errors.New("injected")
				}
				return d.Sync()
			}
		}
	}
	r.attempt, _ = fixtureAttempt(t, false, false, nil)
	r.attempt.begin()
	err := c.spawn(ctx, 7, is, h, "opencode", o, r)
	// Registration is a native fact; this legacy confirmation is not native
	// goal acceptance, so it never becomes the tracker's success fact.
	if confirmFault == "" && err == nil && (!r.attempt.p.Facts.Registered || r.attempt.p.Facts.GoalCommitted || r.attempt.p.Stage != "goal-confirmation") {
		t.Fatalf("production spawn recorded the wrong facts: %+v", r.attempt.p)
	}
	if confirmFault != "" {
		reloaded := loadState(statePath)
		saved := reloaded.Jobs[7]
		if !fired || err != nil || saved == nil || !deliveryConfirmed(saved) || len(reloaded.LaunchBlocks) != 0 || len(c.st.LaunchBlocks) != 0 {
			t.Fatalf("an on-time confirmation was refused on the real caller: fault=%s fired=%v err=%v", confirmFault, fired, err)
		}
		return
	}
	if err != nil {
		t.Fatal("full rendered goal launch did not confirm", err)
	}
	goal := o.goalPreamble(r.profile) + "\n" + renderGoal(c.cfg.Inbox, "fixture/project", "fixture-target", is.Title, is.Body, cwd, "fixture/7", "", 7) + finalReportInstruction(r.dispatch.Issue, strings.Repeat("d", 64))
	actual, err := os.ReadFile(filepath.Join(h.Home, "submitted-prompt"))
	if err != nil || string(actual) != openCodeFirstPrompt(goal) || !strings.Contains(string(actual), body) || !strings.Contains(string(actual), finalReportFile) {
		t.Fatal("first prompt lost the complete brief, directives or report handoff")
	}
	if !strings.Contains(string(actual), "Synthetic Harness fix process.") || !strings.Contains(string(actual), matrixSourceRepository+" at "+strings.Repeat("e", 40)) ||
		strings.Contains(string(actual), "Synthetic target decoy") || strings.Contains(string(actual), "in the repo root") {
		t.Fatal("first prompt did not carry the pinned Harness profile, or pointed at the target checkout")
	}
	staged, err := os.ReadFile(filepath.Join(cwd, ".divybot-goal.md"))
	if err != nil || string(staged) != goal+"\n" {
		t.Fatal("existing staged goal changed or disappeared")
	}
	if strings.Count(calls(), `"agent", "prompt"`) != 1 || strings.Contains(calls(), "send-keys") {
		t.Fatal("goal was repeated or nudged")
	}
	if _, err := os.Stat(filepath.Join(h.Home, "persisted-before-prompt")); err != nil {
		t.Fatal("private digest/freshness was not durable before the effect")
	}
	saved := loadState(statePath).Jobs[7]
	private, _ := json.Marshal(saved.OpenCode)
	var binding map[string]any
	_ = json.Unmarshal(private, &binding)
	if binding["expectedPromptDigest"] != shaText([]byte(openCodeFirstPrompt(goal))) || strings.Contains(string(private), body) || saved.GoalDelivery != "confirmed" || !saved.FinalReportManaged {
		t.Fatal("private binding lost the first goal or persisted its body")
	}
	if _, scope, err := c.finalScope(saved); err != nil || scope.Destination != r.dispatch.Issue || scope.Cwd != cwd {
		t.Fatal("actual launch lost its producer publication scope", err)
	}
}
