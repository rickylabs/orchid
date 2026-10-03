package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAGYUnreadableStateBlocksBeforeNativeSeatAndSurvivesRestart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission denial requires an unprivileged launch user")
	}
	for _, file := range []string{"settings.json", "cache/onboarding.json"} {
		t.Run(file, func(t *testing.T) {
			h, _ := registrationHost(t, "")
			root := h.Home
			cwd := filepath.Join(root, "issue-7")
			if err := exec.Command("git", "init", "--quiet", cwd).Run(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, ".gemini", "antigravity-cli", file)
			if err := os.Chmod(path, 0000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0600) })
			bin := filepath.Join(root, ".local", "bin")
			comment := filepath.Join(root, "comment")
			t.Setenv("AGY_BLOCK_COMMENT", comment)
			// Only the actual trust/staging shell runs. Auth and GitHub are synthetic.
			ssh := `#!/usr/bin/env python3
import os,subprocess,sys
s=sys.argv[-1]
if 'pwd -P' in s or s.startswith('if test -f ') or 'exec head -c' in s or 'command -v awk' in s or '__DIVYBOT_EOF__' in s or 'git rev-parse --git-path info/exclude' in s:
 p=subprocess.run(['/bin/bash','-c',s]);sys.exit(p.returncode)
if "'workspace' 'create'" in s or "'agent' 'start'" in s:
 with open(os.environ['REGISTRATION_CALLS'],'a') as f:f.write('seat attempted\n')
 sys.exit(91)
print('{}')
`
			gh := `#!/usr/bin/env python3
import os,pathlib,sys
a=sys.argv;body=pathlib.Path(a[a.index('--body-file')+1]).read_text()
with open(os.environ['AGY_BLOCK_COMMENT'],'a') as f:f.write(body)
`
			for name, script := range map[string]string{"ssh": ssh, "gh": gh} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			h.SSH, h.WorkdirRoot = "fixture-host", root
			cfg := &Config{Inbox: "fixture/inbox", BranchPrefix: "fixture/", Targets: []Target{{Label: "fixture-target", Repo: "fixture/project"}}}
			c := &Coord{cfg: cfg, auth: &AuthStore{}, st: loadState(filepath.Join(root, "state.json"))}
			is := Issue{Number: 7, Title: "Synthetic", Body: "PRIVATE-AGY-CANARY", Labels: []string{"fixture-target"}}
			r := registrationReceipt(t, "agy", Overrides{Model: "fixture-model", Effort: "low"})
			r.dispatch.Host = h.Name
			if r.writeDispatch("reserved", nil) != nil || os.WriteFile(filepath.Join(filepath.Dir(r.file), "binding.json"), []byte(`{"Repo":"fixture/project","BriefDigest":"synthetic-full-brief"}`), 0600) != nil {
				t.Fatal("fixture full launch binding")
			}
			err := c.spawn(context.Background(), 7, is, h, "agy", Overrides{Model: "fixture-model", Effort: "low"}, r)
			_, seatErr := os.Stat(os.Getenv("REGISTRATION_CALLS"))
			if !agySettingsBlocked(err) || registrationFailureKind(err) != string(agySettingsUnreadable) || c.st.Jobs[7] != nil || !os.IsNotExist(seatErr) {
				t.Fatal("unreadable state lost its no-seat blocked class")
			}
			if _, err := os.Stat(filepath.Join(cwd, ".divybot-agy")); !os.IsNotExist(err) {
				t.Fatal("unreadable state created scoped native settings")
			}
			body, err := os.ReadFile(comment)
			if err != nil || !strings.Contains(string(body), "AGY launch BLOCKED (`agy-settings-unreadable`)") || !strings.Contains(string(body), "No native workspace or agent was created") || strings.Contains(string(body), "inconclusive") || strings.Contains(string(body), "PRIVATE-AGY-CANARY") {
				t.Fatal("blocked notice lost meaning or exposed private data")
			}
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			c.st = loadState(c.st.path)
			if c.st.LaunchBlocks[7].Reason != string(agySettingsUnreadable) || c.st.reserveLaunch(7) || !c.reportBlockedLaunch(context.Background(), 7) {
				t.Fatal("repair or restart erased the one-attempt fence")
			}
			after, err := os.ReadFile(comment)
			if err != nil || string(after) != string(body) {
				t.Fatal("restart duplicated the already delivered notice")
			}
		})
	}
}

func TestAGYSettingsBlockRequiresExactPreWorkspaceEvidence(t *testing.T) {
	for _, err := range []error{
		matrixSite("spawn.agent-start", agySettingsUnreadable),
		matrixSite("spawn.agy-trust", matrixReason("agy-settings-unavailable")),
		matrixSite("spawn.agy-trust", matrixSite("command.timeout", agySettingsUnreadable)),
	} {
		if agySettingsBlocked(err) || registrationFailureKind(err) == string(agySettingsUnreadable) {
			t.Fatal("missing, ambiguous or post-start failure falsely denied an agent")
		}
	}
}

func TestMatrixAGYSettingsBlockDoesNotOverwriteSpecificNotice(t *testing.T) {
	root := privateTestRoot(t)
	cfg := &Config{Inbox: "example/inbox", UnmeteredTransports: UnmeteredTransportLimits{"agy": {1}}, Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: strings.Repeat("b", 40), TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)}}}
	c := &Coord{cfg: cfg, st: loadState(filepath.Join(root, "state.json"))}
	is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic", Body: "/swarm\nprofile: rfc\ntier: simple\nrole: deep_research\n\nSynthetic task"}
	reports := 0
	var lastRefusal matrixRefusal
	deps := matrixAttemptDeps{
		report: func(r matrixRefusal) { reports++; lastRefusal = r },
		read: func(context.Context, string, string, string) (string, error) {
			return "| `routing` | matrix `deep_research` row |", nil
		},
		resolve: func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) {
			r := syntheticRoute()
			r.Transport, r.Provider, r.Model = "agy", "gemini", "fixture-model"
			return r, nil
		},
		host:    func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, true },
		persist: persistMatrixReceipt,
		launch: func(context.Context, int, Issue, Host, string, Overrides, *durableMatrixReceipt) error {
			return matrixSite("launch.registration", matrixSite("spawn.agy-trust", agySettingsUnreadable))
		},
	}
	if _, ok := c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, map[string]int{"agy": 1}, deps); ok || reports != 0 {
		t.Fatalf("specific blocked launch became success or a generic inconclusive notice: %+v", lastRefusal)
	}
	files, _ := filepath.Glob(filepath.Join(root, "*", "record", "dispatch.json"))
	if len(files) != 1 {
		t.Fatal("one-attempt receipt was erased")
	}
	body, err := os.ReadFile(files[0])
	var dispatch dispatchBinding
	if err != nil || json.Unmarshal(body, &dispatch) != nil || dispatch.State != "reserved" || dispatch.Location != nil {
		t.Fatal("no-seat block was relabeled as a possible native effect")
	}
}
