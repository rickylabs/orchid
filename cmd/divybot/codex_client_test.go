package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodexServerVersionFromUserAgent(t *testing.T) {
	for ua, want := range map[string]string{
		"codex_app_server/9.1.0 (fixture-os; fixture-arch) unknown": "9.1.0",
		"codex-tui/9.2.0 (fixture-os; fixture-arch)":                "9.2.0",
		"codex_app_server/1.2.3":                                    "1.2.3",
		"codex_app_server/1.2.3-alpha.1 (x)":                        "1.2.3-alpha.1",
		"fixture":                                                   "",
		"codex_app_server/":                                         "",
		"codex_app_server/9.1 (x)":                                  "",
		"codex_app_server/9.1.0;rm (x)":                             "",
		"codex_app_server/../9.1.2":                                 "",
		"":                                                          "",
	} {
		if got := codexServerVersion(ua); got != want {
			t.Fatalf("userAgent %q parsed as %q, want %q", ua, got, want)
		}
	}
}

// A fake client: --version reports its version. Launched as the TUI, it models
// the native startup order: an update prompt that runs before any daemon attach
// unless check_for_update_on_startup=false is passed (codex tui lib.rs).
func fakeCodexScript(reports string) string {
	return "#!/bin/sh\n[ \"$1\" = --version ] && echo 'codex-cli " + reports + "' && exit 0\n" +
		// Native: a remote resume refuses permission overrides before attaching.
		"case \"$*\" in *--dangerously-bypass-approvals-and-sandbox*--remote*) echo permission-override-refused; exit 1;; esac\n" +
		"case \"$*\" in *check_for_update_on_startup=false*) echo attached;; *) echo update-prompt;; esac\n"
}

// A synthetic host home: version-named standalone releases plus a PATH codex.
// Each fake reports a version through --version, exactly as the real client does.
func codexClientHome(t *testing.T, releases map[string]string, pathVersion string) string {
	t.Helper()
	home := t.TempDir()
	for dir, reports := range releases {
		bin := filepath.Join(home, ".codex", "packages", "standalone", "releases", dir, "bin")
		writeFixture(t, filepath.Join(bin, "codex"), fakeCodexScript(reports))
		if os.Chmod(filepath.Join(bin, "codex"), 0700) != nil {
			t.Fatal("fixture client unavailable")
		}
	}
	if pathVersion != "" {
		writeFixture(t, filepath.Join(home, ".local", "bin", "codex"), fakeCodexScript(pathVersion))
		if os.Chmod(filepath.Join(home, ".local", "bin", "codex"), 0700) != nil {
			t.Fatal("fixture client unavailable")
		}
	}
	// No real client may answer: only the synthetic home and system tools are reachable.
	t.Setenv("PATH", toolShimDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	if real, err := exec.LookPath("codex"); err == nil {
		t.Fatalf("a real codex is reachable from the fixture PATH: %s", real)
	}
	return home
}

func TestResolveCodexClientMatchesDaemonVersion(t *testing.T) {
	releases := map[string]string{
		"9.1.0-x86_64-fixture": "9.1.0", "9.3.0-x86_64-fixture": "9.3.1", "9.5.0-x86_64-fixture": "9.5.0",
		"bogus-x86_64-fixture": "bogus", // a malformed version that a release would answer to
		"9.6.0-a:b":            "9.6.0", // a directory that would split the PATH
	}
	for _, tc := range []struct {
		name, version, pathVersion, wantDir string
	}{
		{"release-dir", "9.1.0", "9.2.0", ".codex/packages/standalone/releases/9.1.0-x86_64-fixture/bin"},
		{"path-client", "9.2.0", "9.2.0", ".local/bin"},
		{"release-preferred-over-path", "9.5.0", "9.5.0", ".codex/packages/standalone/releases/9.5.0-x86_64-fixture/bin"},
		{"release-reports-other-version", "9.3.0", "9.2.0", ""},
		{"no-client", "9.4.0", "9.2.0", ""},
		{"malformed-version-never-resolves", "bogus", "9.2.0", ""},
		{"path-splitting-dir-refused", "9.6.0", "9.2.0", ""},
		{"invalid-version", "9.1", "9.2.0", ""},
		{"empty-version", "", "9.2.0", ""},
		{"injection", "9.1.0'; touch pwned; '", "9.2.0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := codexClientHome(t, releases, tc.pathVersion)
			h := Host{Name: "fixture-host", Home: home}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			binary, err := h.resolveCodexClient(ctx, tc.version)
			if tc.wantDir == "" {
				if err != codexClientUnavailable || binary != "" {
					t.Fatalf("no exact client must refuse: binary=%q err=%v", binary, err)
				}
			} else if want, _ := filepath.EvalSymlinks(filepath.Join(home, tc.wantDir, "codex")); err != nil || binary != want {
				t.Fatalf("resolved %q (%v), want the real binary %q", binary, err, want)
			}
			if _, statErr := os.Stat(filepath.Join(home, "pwned")); statErr == nil {
				t.Fatal("version text reached the shell")
			}
		})
	}
}

// A fake client that reports its version, at an arbitrary path.
func fakeCodexAt(t *testing.T, path, reports string) {
	t.Helper()
	writeFixture(t, path, fakeCodexScript(reports))
	if os.Chmod(path, 0700) != nil {
		t.Fatal("fixture client unavailable")
	}
}

// The resolver pins the real executable behind every symlink, never an alias.
func TestResolveCodexClientPinsRealBinary(t *testing.T) {
	for _, tc := range []struct {
		name, target, reports, want string
	}{
		{"path-alias-resolves-to-real-binary", "opt/9.7.0/bin/codex", "9.7.0", "opt/9.7.0/bin/codex"},
		{"alias-target-not-named-codex", "opt/9.7.0/bin/codex.js", "9.7.0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := codexClientHome(t, nil, "")
			fakeCodexAt(t, filepath.Join(home, tc.target), tc.reports)
			alias := filepath.Join(home, ".local", "bin", "codex")
			if os.MkdirAll(filepath.Dir(alias), 0700) != nil || os.Symlink(filepath.Join(home, tc.target), alias) != nil {
				t.Fatal("alias fixture unavailable")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			binary, err := Host{Name: "fixture-host", Home: home}.resolveCodexClient(ctx, "9.7.0")
			if tc.want == "" {
				if err != codexClientUnavailable {
					t.Fatalf("a client not named codex was pinned: %q", binary)
				}
				return
			}
			if want, _ := filepath.EvalSymlinks(filepath.Join(home, tc.want)); err != nil || binary != want {
				t.Fatalf("pinned %q (%v), want the real binary %q", binary, err, want)
			}
		})
	}
}

// Production spawnAgent against a canonical daemon fixture and a Herdr fixture.
// Herdr starts the kind's canonical executable from the pane shell's PATH, so the
// fake runs exactly the exported pane environment and records which client ran.
func TestRemoteCodexSpawnPinsDaemonClient(t *testing.T) {
	for _, tc := range []struct {
		name        string
		releases    map[string]string
		pathVersion string
		alias       bool   // ~/.local/bin/codex is a symlink to opt/9.1.0/bin/codex; opt/9.2.0 also exists
		onWorkspace string // runs (with HOME) when Herdr creates the workspace, after selection
		wantClient  string // "" = refused before spawn; "recheck" = refused before agent start
	}{
		{"matching-release-beats-newer-path-client", map[string]string{"9.1.0-x86_64-fixture": "9.1.0"}, "9.2.0", false, "", "codex-cli 9.1.0"},
		{"no-matching-client-refuses-before-spawn", map[string]string{"9.0.0-x86_64-fixture": "9.0.0"}, "9.2.0", false, "", ""},
		// An updater re-points the PATH alias after selection: the verified binary still runs.
		{"path-alias-retargeted-after-selection", nil, "", true, `ln -sfn "$HOME/opt/9.2.0/bin/codex" "$HOME/.local/bin/codex"`, "codex-cli 9.1.0"},
		// The pinned binary itself changes before start: no agent is started.
		{"pinned-binary-replaced-before-start", map[string]string{"9.1.0-x86_64-fixture": "9.1.0"}, "9.2.0", false,
			`printf '#!/bin/sh\necho codex-cli 9.2.0\n' > "$HOME/.codex/packages/standalone/releases/9.1.0-x86_64-fixture/bin/codex"`, "recheck"},
		// Swapped for a different binary that reports the same version: still not the verified one.
		{"pinned-binary-relinked-same-version", map[string]string{"9.1.0-x86_64-fixture": "9.1.0"}, "", true,
			`ln -sfn "$HOME/opt/9.1.0/bin/codex" "$HOME/.codex/packages/standalone/releases/9.1.0-x86_64-fixture/bin/codex"`, "recheck"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var methods []string
			cwd := t.TempDir()
			o := Overrides{Model: "fixture-model", Effort: "low"}
			r := registrationReceipt(t, "codex", o)
			name, err := remoteSessionName(r.dispatch.Issue.Number, "Synthetic task")
			if err != nil {
				t.Fatal(err)
			}
			run := &remoteControlRun{NativeSessionID: "", Cwd: cwd, Name: name, Model: o.Model, Effort: o.Effort}
			id := syntheticRemoteRun(t).NativeSessionID
			h := canonicalFixtureHost(t, "multi", func(method string, params map[string]any) any {
				mu.Lock()
				methods = append(methods, method)
				mu.Unlock()
				switch method {
				case "remoteControl/status/read":
					return map[string]string{"status": "connected"}
				case "thread/start", "thread/read":
					run.NativeSessionID = id
					return remoteThreadFixture(run)
				}
				return map[string]any{}
			})
			// The fixture daemon's own home doubles as the host home for clients and Herdr.
			fixture := codexClientHome(t, tc.releases, tc.pathVersion)
			if tc.alias {
				fakeCodexAt(t, filepath.Join(fixture, "opt", "9.1.0", "bin", "codex"), "9.1.0")
				fakeCodexAt(t, filepath.Join(fixture, "opt", "9.2.0", "bin", "codex"), "9.2.0")
				if os.MkdirAll(filepath.Join(fixture, ".local", "bin"), 0700) != nil ||
					os.Symlink(filepath.Join(h.Home, "opt", "9.1.0", "bin", "codex"), filepath.Join(fixture, ".local", "bin", "codex")) != nil {
					t.Fatal("alias fixture unavailable")
				}
			}
			for _, sub := range []string{".codex/packages", ".local", "opt"} {
				if _, err := os.Stat(filepath.Join(fixture, sub)); err != nil {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(filepath.Join(h.Home, sub)), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(fixture, sub), filepath.Join(h.Home, sub)); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("RC_CLIENT_ON_WORKSPACE", tc.onWorkspace)
			calls := filepath.Join(h.Home, "herdr-calls.jsonl")
			started := filepath.Join(h.Home, "started-client")
			t.Setenv("RC_CLIENT_CALLS", calls)
			t.Setenv("RC_CLIENT_PANE", filepath.Join(h.Home, "pane-shell"))
			t.Setenv("RC_CLIENT_STARTED", started)
			t.Setenv("RC_CLIENT_HOME", h.Home)
			writeFixture(t, filepath.Join(h.Home, ".local", "bin", "herdr"), `#!/usr/bin/env python3
import json,os,subprocess,sys
args=sys.argv[1:]
with open(os.environ['RC_CLIENT_CALLS'],'a') as f:f.write(json.dumps(args)+'\n')
if args[:2]==['workspace','create']:
 if os.environ['RC_CLIENT_ON_WORKSPACE']:subprocess.run(['bash','-c',os.environ['RC_CLIENT_ON_WORKSPACE']],env=dict(os.environ,HOME=os.environ['RC_CLIENT_HOME']),check=True)
 print(json.dumps({'result':{'workspace':{'workspace_id':'w1'},'root_pane':{'pane_id':'w1:p1'}}}))
elif args[:2]==['pane','run']:
 open(os.environ['RC_CLIENT_PANE'],'a').write(args[3]+'\n');print(json.dumps({'result':{}}))
elif args[:2]==['agent','start']:
 env=dict(os.environ,HOME=os.environ['RC_CLIENT_HOME'])
 pane=open(os.environ['RC_CLIENT_PANE']).read()
 out=subprocess.run(['bash','-c',pane+'codex --version\n'],env=env,capture_output=True,text=True).stdout.strip()
 open(os.environ['RC_CLIENT_STARTED'],'w').write(out)
 # Herdr starts the canonical codex with the agent-start argv after "--".
 attach=subprocess.run(['bash','-c',pane+'codex "$@"\n','herdr']+args[args.index('--')+1:],env=env,capture_output=True,text=True).stdout.strip()
 open(os.environ['RC_CLIENT_STARTED']+'.attach','w').write(attach)
 print(json.dumps({'error':{'code':'agent_not_ready','message':'fixture stops after start'}}));sys.exit(1)
else:print(json.dumps({'result':{}}))
`)
			if os.Chmod(filepath.Join(h.Home, ".local", "bin", "herdr"), 0700) != nil {
				t.Fatal("herdr fixture unavailable")
			}
			on := true
			h.RemoteControl = &RemoteControlConfig{Codex: &on}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _, err = h.spawnAgent(ctx, "fixture-agent", cwd, nil, "codex", o, r, "Synthetic task")
			log, _ := os.ReadFile(calls)
			mu.Lock()
			seen := strings.Join(methods, ",")
			mu.Unlock()
			if tc.wantClient == "" {
				if !codexClientBlocked(err) || registrationFailureKind(err) != string(codexClientUnavailable) {
					t.Fatalf("missing client was not a plain pre-spawn block: %v", err)
				}
				if len(log) != 0 || strings.Contains(seen, "thread/start") {
					t.Fatalf("a refused launch created effects: herdr=%q daemon=%q", log, seen)
				}
				return
			}
			if tc.wantClient == "recheck" {
				if matrixCause(err) != "spawn.codex-client-recheck" || codexClientBlocked(err) || strings.Contains(string(log), `"agent", "start"`) {
					t.Fatalf("a changed pinned binary reached agent start or was served as a clean block: err=%v herdr=%s", err, log)
				}
				return
			}
			attach, _ := os.ReadFile(started + ".attach")
			if string(attach) != "attached" {
				t.Fatalf("the launched TUI stops at its startup update prompt before attaching: %q", attach)
			}
			got, _ := os.ReadFile(started)
			if string(got) != tc.wantClient {
				t.Fatalf("herdr started client %q, want the daemon's %q; err=%v daemon=%q herdr=%s", got, tc.wantClient, err, seen, log)
			}
			var first []string
			if line, _, _ := strings.Cut(string(log), "\n"); json.Unmarshal([]byte(line), &first) != nil || strings.Join(first[:2], " ") != "workspace create" {
				t.Fatal("client selection did not precede the workspace")
			}
			if !strings.Contains(seen, "thread/start") || codexClientBlocked(err) {
				t.Fatalf("matching client did not reach the prepared thread: daemon=%q err=%v", seen, err)
			}
		})
	}
}

func TestCodexClientBlockRequiresExactPreWorkspaceEvidence(t *testing.T) {
	blocked := matrixSite("launch.registration", matrixSite("spawn.codex-client", codexClientUnavailable))
	if !codexClientBlocked(blocked) || launchBlockReason(blocked) != string(codexClientUnavailable) || registrationFailureKind(blocked) != string(codexClientUnavailable) {
		t.Fatal("pre-workspace client refusal lost its plain block class")
	}
	for _, err := range []error{
		matrixSite("spawn.remote-control-prepare", codexClientUnavailable), // after the workspace exists
		matrixSite("spawn.agent-start", codexClientUnavailable),
		matrixSite("spawn.codex-client", matrixReason("codex-client-other")),
		matrixSite("spawn.agy-trust", agySettingsUnreadable),
	} {
		if codexClientBlocked(err) || launchBlockReason(err) == string(codexClientUnavailable) {
			t.Fatal("a post-workspace or unrelated failure was served as a clean client block")
		}
	}
}

// The daemon may change between selection and preparation; never start a thread
// for a client pinned to another daemon version.
func TestRemoteCodexPreparationRefusesChangedDaemon(t *testing.T) {
	for _, pinned := range []string{"9.0.0", ""} {
		run := syntheticRemoteRun(t)
		run.ClientVersion = pinned
		started := make(chan string, 8)
		h := canonicalFixtureHost(t, "valid", func(method string, _ map[string]any) any {
			started <- method
			if method == "remoteControl/status/read" {
				return map[string]string{"status": "connected"}
			}
			return remoteThreadFixture(run)
		})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := h.prepareRemoteCodex(ctx, run, nil)
		cancel()
		close(started)
		for m := range started {
			if m == "thread/start" {
				t.Fatal("a thread was started for a client pinned to another daemon")
			}
		}
		if err != codexClientUnavailable {
			t.Fatalf("changed daemon accepted: pinned=%q err=%v", pinned, err)
		}
	}
}

func TestMatrixCodexClientBlockDoesNotOverwriteSpecificNotice(t *testing.T) {
	root := privateTestRoot(t)
	cfg := &Config{Inbox: "example/inbox", Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: strings.Repeat("b", 40), TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)}}, Governor: Gov{WeeklyCeiling: 92}}
	c := &Coord{cfg: cfg, st: loadState(filepath.Join(root, "state.json"))}
	now := time.Now()
	c.gov.q = map[string]quota{"codex": {ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()}}}
	is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic", Body: "/swarm\ntier: feature\nrole: implementation\n\nSynthetic task"}
	reports := 0
	var lastRefusal matrixRefusal
	deps := matrixAttemptDeps{
		report: func(r matrixRefusal) { reports++; lastRefusal = r },
		read: func(context.Context, string, string, string) (string, error) {
			return "| `routing` | matrix `implementation` row |", nil
		},
		resolve: func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) {
			r := syntheticRoute()
			r.Transport, r.Provider, r.Model, r.Effort = "codex", "openai", "fixture-model", "high"
			return r, nil
		},
		host:    func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, true },
		persist: persistMatrixReceipt,
		launch: func(context.Context, int, Issue, Host, string, Overrides, *durableMatrixReceipt) error {
			return matrixSite("launch.registration", matrixSite("spawn.codex-client", codexClientUnavailable))
		},
	}
	if _, ok := c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, map[string]int{"codex": 1}, deps); ok || reports != 0 {
		t.Fatalf("plain client block became success or a generic inconclusive notice: %+v", lastRefusal)
	}
	files, _ := filepath.Glob(filepath.Join(root, "*", "record", "dispatch.json"))
	if len(files) != 1 {
		t.Fatal("one-attempt receipt was erased")
	}
	body, err := os.ReadFile(files[0])
	var dispatch dispatchBinding
	if err != nil || json.Unmarshal(body, &dispatch) != nil || dispatch.State != "reserved" || dispatch.Location != nil {
		t.Fatal("pre-spawn block was relabeled as a possible native effect")
	}
}

func TestCodexClientBlockServesPlainNotice(t *testing.T) {
	root := t.TempDir()
	comment := filepath.Join(root, "comment")
	t.Setenv("CODEX_BLOCK_COMMENT", comment)
	bin := filepath.Join(root, "bin")
	writeFixture(t, filepath.Join(bin, "gh"), `#!/usr/bin/env python3
import os,pathlib,sys
a=sys.argv;body=pathlib.Path(a[a.index('--body-file')+1]).read_text()
with open(os.environ['CODEX_BLOCK_COMMENT'],'a') as f:f.write(body)
`)
	if os.Chmod(filepath.Join(bin, "gh"), 0700) != nil {
		t.Fatal("gh fixture unavailable")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox"}, st: loadState(filepath.Join(root, "state.json"))}
	c.st.blockLaunch(7, launchBlockReason(matrixSite("launch.registration", matrixSite("spawn.codex-client", codexClientUnavailable))))
	if !c.reportBlockedLaunch(context.Background(), 7) {
		t.Fatal("block not reported")
	}
	body, err := os.ReadFile(comment)
	if err != nil || !strings.Contains(string(body), "Codex launch BLOCKED (`codex-client-unavailable`)") ||
		!strings.Contains(string(body), "no workspace, thread or agent was created") || strings.Contains(string(body), "inconclusive") {
		t.Fatalf("plain client block notice lost meaning: %q", body)
	}
}
