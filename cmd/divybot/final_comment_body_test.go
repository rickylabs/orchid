package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func finalCommentTestScript(t *testing.T) string {
	t.Helper()
	script, err := finalCommentBodyScript(strings.Repeat("d", 64))
	if err != nil {
		t.Fatal("valid fixture identity refused")
	}
	path := filepath.Join(t.TempDir(), finalCommentBodyFile)
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("generated script syntax invalid: %v %s", err, out)
	}
	return path
}

func runFinalCommentBody(path, body string, args ...string) (string, string, error) {
	cmd := exec.Command("sh", append([]string{path}, args...)...)
	cmd.Stdin = strings.NewReader(body)
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err := cmd.Run()
	return out.String(), diagnostic.String(), err
}

func TestFinalCommentBodyKeepsVisibleReportAndExactPublicMarker(t *testing.T) {
	path := finalCommentTestScript(t)
	key := strings.Repeat("d", 64)
	marker := "<!-- orchid-run assignment_a1f5fcca4becf7d50bd0e31ad73fa92c02529e014327dc23b0aed46f3f5ef794 agent_01b2bf7785f015813e783817d39ed708678d7641631859805a0d30ddea71ce86 -->"
	for _, body := range []string{
		"- The launch observation is recorded (`src/read.ts:7`).\n- The source is cited (`src/write.ts:9`).\n- The conclusion is reviewed (`src/view.ts:11`).",
		"  - Résumé: the reviewed result is complete.\n\n  A citation: `src/read.ts:7`.\n",
		strings.Repeat("x", 59999),
	} {
		out, diagnostic, err := runFinalCommentBody(path, body)
		want := body
		if !strings.HasSuffix(want, "\n") {
			want += "\n"
		}
		want += "\n" + marker + "\n"
		if err != nil || diagnostic != "" || out != want || strings.Count(out, marker) != 1 || strings.Contains(out, key) {
			t.Fatal("visible body or paired public marker changed")
		}
		repeated, _, err := runFinalCommentBody(path, body)
		if err != nil || repeated != out {
			t.Fatal("same source report produced a different body")
		}
		now := time.Now().UTC().Truncate(time.Second)
		if !markedCompletionComment(out, "fixture-bot", "fixture-bot", now.Format(time.RFC3339), now.Add(-time.Minute), key) {
			t.Fatal("constructed final body cannot be attributed by the existing reader")
		}
	}
}

func TestFinalCommentBodyRejectsInvalidInputWithoutOutput(t *testing.T) {
	path := finalCommentTestScript(t)
	marker := finalCommentMarker(strings.Repeat("d", 64))
	for name, body := range map[string]string{
		"empty":              "",
		"whitespace":         " \t\n\n\r\n",
		"oversized":          strings.Repeat("x", 60000),
		"multiline-overflow": strings.Repeat("x\n", 30001),
		"already-marked":     "Final report.\n" + marker,
		"duplicate":          "Final report.\n" + marker + "\n" + marker,
		"foreign":            "Final report.\n" + finalCommentMarker(strings.Repeat("a", 64)),
		"malformed":          "Final report.\n<!-- orchid-run PRIVATE-FIXTURE -->",
	} {
		t.Run(name, func(t *testing.T) {
			out, diagnostic, err := runFinalCommentBody(path, body)
			if err == nil || out != "" || diagnostic != "final-comment-input-invalid\n" {
				t.Fatal("invalid report emitted an attributable comment body")
			}
		})
	}
	out, diagnostic, err := runFinalCommentBody(path, "Final report.", "untrusted-marker")
	if err == nil || out != "" || diagnostic != "final-comment-input-invalid\n" {
		t.Fatal("argument can replace the launch-bound identity")
	}
	for _, key := range []string{"", "bad", strings.Repeat("D", 64), strings.Repeat("d", 63), "native-fixture-id"} {
		if script, err := finalCommentBodyScript(key); err == nil || script != "" || finalCommentBodyInstruction(key) != "" {
			t.Fatal("invalid dispatch identity generated a public producer artifact")
		}
	}
}

func TestFinalCommentBodyStagingExcludesArtifactAndFailsOnPreparationErrors(t *testing.T) {
	for _, failure := range []string{"", "exclude", "write", "missing-awk", "symlink", "tracked"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			info := filepath.Join(root, ".git", "info")
			if err := os.MkdirAll(info, 0700); err != nil {
				t.Fatal(err)
			}
			if err := exec.Command("git", "init", "--quiet", root).Run(); err != nil {
				t.Fatal("owned Git fixture could not be initialized")
			}
			if failure == "exclude" {
				if err := os.Remove(filepath.Join(info, "exclude")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(info, "exclude"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "write" {
				if err := os.Mkdir(filepath.Join(root, finalCommentBodyFile), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "missing-awk" {
				bin := t.TempDir()
				for _, tool := range []string{"bash", "grep", "mkdir", "cat", "git"} {
					target, err := exec.LookPath(tool)
					if err != nil {
						t.Fatal("fixture prerequisite missing")
					}
					if err := os.Symlink(target, filepath.Join(bin, tool)); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("PATH", bin)
			}
			protected := filepath.Join(root, "protected-fixture")
			if failure == "symlink" {
				if err := os.WriteFile(protected, []byte("preserved fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(protected, filepath.Join(root, finalCommentBodyFile)); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "tracked" {
				if err := os.WriteFile(filepath.Join(root, finalCommentBodyFile), []byte("preserved fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := exec.Command("git", "-C", root, "add", finalCommentBodyFile).Run(); err != nil {
					t.Fatal(err)
				}
			}
			err := (Host{}).stageFinalCommentBody(context.Background(), root, strings.Repeat("d", 64))
			if failure != "" {
				if err == nil {
					t.Fatal("failed producer preparation claimed success")
				}
				if failure == "symlink" || failure == "tracked" {
					name := protected
					if failure == "tracked" {
						name = filepath.Join(root, finalCommentBodyFile)
					}
					b, err := os.ReadFile(name)
					if err != nil || string(b) != "preserved fixture" {
						t.Fatal("producer overwrote a foreign artifact")
					}
				}
				return
			}
			if err != nil {
				t.Fatal("valid producer preparation failed")
			}
			ignored, err := os.ReadFile(filepath.Join(info, "exclude"))
			if err != nil || strings.Count(string(ignored), finalCommentBodyFile+"\n") != 1 {
				t.Fatal("staged identity artifact is not excluded from Git")
			}
			check := exec.Command("git", "-C", root, "check-ignore", finalCommentBodyFile)
			if out, err := check.Output(); err != nil || strings.TrimSpace(string(out)) != finalCommentBodyFile {
				t.Fatal("Git would expose the launch artifact as an untracked file")
			}
			artifact := filepath.Join(root, finalCommentBodyFile)
			out, _, err := runFinalCommentBody(artifact, "A reviewed result.")
			if err != nil || !strings.Contains(out, finalCommentMarker(strings.Repeat("d", 64))) {
				t.Fatal("staged helper did not construct a marked comment")
			}
			if err := (Host{}).stageFinalCommentBody(context.Background(), root, strings.Repeat("d", 64)); err != nil {
				t.Fatal("repeat staging failed")
			}
			ignored, _ = os.ReadFile(filepath.Join(info, "exclude"))
			if strings.Count(string(ignored), finalCommentBodyFile) != 1 {
				t.Fatal("repeat staging duplicated the Git exclusion")
			}
		})
	}
}

func TestFinalCommentBodyInvalidLaunchIdentityMakesNoArtifact(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "init", "--quiet", root).Run(); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(root, ".git", "info", "exclude")
	before, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	if err := (Host{}).stageFinalCommentBody(context.Background(), root, "invalid-fixture"); err == nil {
		t.Fatal("invalid launch identity reached artifact staging")
	}
	after, _ := os.ReadFile(exclude)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid launch identity changed Git state")
	}
	if _, err := os.Lstat(filepath.Join(root, finalCommentBodyFile)); !os.IsNotExist(err) {
		t.Fatal("invalid launch identity wrote an artifact")
	}
}

func TestFinalCommentWorkerGoalPreservesDeliveryShape(t *testing.T) {
	for _, mode := range []string{"interactive", "staged", "goal-write-error"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := exec.Command("git", "init", "--quiet", root).Run(); err != nil {
				t.Fatal(err)
			}
			key := strings.Repeat("d", 64)
			goal := "A synthetic brief.\n" + finalCommentBodyInstruction(key)
			file := filepath.Join(root, ".divybot-goal.md")
			if mode == "goal-write-error" {
				if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := (Host{}).stageWorkerGoal(context.Background(), root, key, goal, mode != "interactive")
			if mode == "goal-write-error" {
				if err == nil {
					t.Fatal("failed goal preparation claimed success")
				}
				return
			}
			if err != nil {
				t.Fatal("worker artifacts were not prepared")
			}
			if _, err := os.Stat(filepath.Join(root, finalCommentBodyFile)); err != nil {
				t.Fatal("interactive or staged worker missed the comment constructor")
			}
			body, err := os.ReadFile(file)
			if mode == "interactive" {
				if !os.IsNotExist(err) {
					t.Fatal("interactive delivery unexpectedly staged a goal")
				}
			} else if err != nil || string(body) != goal+"\n" {
				t.Fatal("staged delivery lost the goal body")
			}
		})
	}
}

// Real spawn/staging orchestration with a synthetic SSH endpoint. No network,
// credentials, native CLI, GitHub write, or model turn can be reached here.
func TestFinalCommentBodyLaunchStagesBeforeAnySeatAndDeliversInstruction(t *testing.T) {
	for _, failure := range []string{"", "exclude", "write"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			workdir := filepath.Join(root, "issue-7")
			info := filepath.Join(workdir, ".git", "info")
			if err := os.MkdirAll(info, 0700); err != nil {
				t.Fatal(err)
			}
			if err := exec.Command("git", "init", "--quiet", workdir).Run(); err != nil {
				t.Fatal(err)
			}
			if failure == "exclude" {
				if err := os.Remove(filepath.Join(info, "exclude")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(info, "exclude"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "write" {
				if err := os.Mkdir(filepath.Join(workdir, finalCommentBodyFile), 0700); err != nil {
					t.Fatal(err)
				}
			}
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(root, "calls.jsonl")
			t.Setenv("FINAL_COMMENT_CALLS", calls)
			t.Setenv("FINAL_COMMENT_WORKDIR", workdir)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			fake := `#!/usr/bin/env python3
import json,os,pathlib,subprocess,sys
script=sys.argv[-1]
with open(os.environ['FINAL_COMMENT_CALLS'],'a') as f:f.write(json.dumps(script)+'\n')
root=pathlib.Path(os.environ['FINAL_COMMENT_WORKDIR'])
if 'command -v awk' in script or '__DIVYBOT_EOF__' in script:
 p=subprocess.run(['/bin/sh','-c',script]);sys.exit(p.returncode)
if "'workspace' 'create'" in script:
 # The real constructor and exclusion must already exist at the seat effect.
 try:
  body=(root/'.divybot-final-comment.sh').read_text()
  excluded=(root/'.git/info/exclude').read_text()
 except OSError:sys.exit(1)
 if '<!-- orchid-run assignment_' not in body or '.divybot-final-comment.sh' not in excluded:sys.exit(1)
 print(json.dumps({'result':{'workspace':{'workspace_id':'fixture-seat'},'root_pane':{'pane_id':'fixture-pane'}}}))
else:print(json.dumps({'result':{}}))
`
			if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			cfg := &Config{Inbox: "fixture/inbox", BranchPrefix: "fixture/", Targets: []Target{{Label: "fixture-target", Repo: "fixture/project"}}}
			c := &Coord{cfg: cfg, auth: &AuthStore{}, st: loadState(filepath.Join(root, "state.json"))}
			h := Host{Name: "fixture-host", SSH: "fixture-host", Home: root, WorkdirRoot: root}
			is := Issue{Number: 7, Title: "[fixture/source#42] Synthetic report", Body: "Post one final comment on fixture/source issue 42, with three bullets.", Labels: []string{"fixture-target"}}
			r := registrationReceipt(t, "codex-run", Overrides{})
			err := c.spawn(context.Background(), 7, is, h, "codex-run", Overrides{}, r)
			log, readErr := os.ReadFile(calls)
			if readErr != nil {
				t.Fatal("synthetic endpoint was not reached")
			}
			var effects []string
			for _, row := range strings.Split(strings.TrimSpace(string(log)), "\n") {
				var command string
				if json.Unmarshal([]byte(row), &command) != nil {
					t.Fatal("invalid fixture command log")
				}
				effects = append(effects, command)
			}
			seatEffects := 0
			for _, command := range effects {
				if strings.Contains(command, "'workspace' 'create'") || strings.Contains(command, "'agent' 'start'") {
					seatEffects++
				}
			}
			if failure != "" {
				if err == nil || matrixCause(err) != "launch.goal-file" || seatEffects != 0 || c.st.Jobs[7] != nil {
					t.Fatal("producer preparation failure reached a worker seat")
				}
				return
			}
			if err != nil || seatEffects != 1 || c.st.Jobs[7] == nil {
				t.Fatalf("synthetic launch failed: %v", err)
			}
			goal, err := os.ReadFile(filepath.Join(workdir, ".divybot-goal.md"))
			if err != nil || !strings.Contains(string(goal), "sh .divybot-final-comment.sh < REPORT.md > FINAL-COMMENT.md") ||
				!strings.Contains(string(goal), "--body-file FINAL-COMMENT.md") || !strings.Contains(string(goal), is.Body) ||
				strings.Count(string(goal), finalCommentMarker(strings.Repeat("d", 64))) != 1 {
				t.Fatal("delivered goal lost the source destination, helper, or exact public marker")
			}
		})
	}
}
