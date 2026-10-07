package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func registrationReceipt(t *testing.T, agent string, o Overrides) *durableMatrixReceipt {
	t.Helper()
	key := strings.Repeat("d", 64)
	r, err := persistMatrixReceipt(privateTestRoot(t), key, mustBuildAgentCmd(t, agent, o), receiptFor(MatrixConfig{}, syntheticRoute()), nil)
	if err != nil {
		t.Fatal("fixture matrix receipt failed")
	}
	r.dispatch = &dispatchBinding{SchemaVersion: 1, RunID: "orchid-" + key, Issue: dispatchIssue{Repo: "fixture/inbox", Number: 7}, Source: strings.TrimSuffix(agent, "-run")}
	if r.writeDispatch("reserved", nil) != nil {
		t.Fatal("fixture dispatch binding failed")
	}
	return r
}

// Only synthetic handles and argv. This fixture never invokes a native agent.
func registrationHost(t *testing.T, fail string) (Host, func() [][]string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "calls.jsonl")
	t.Setenv("REGISTRATION_CALLS", logPath)
	t.Setenv("REGISTRATION_FAILURE", fail)
	settings := filepath.Join(root, ".gemini", "antigravity-cli", "settings.json")
	if os.MkdirAll(filepath.Dir(settings), 0700) != nil || os.WriteFile(settings, []byte(`{"model":"fixture-default","toolPermission":"always-proceed","trustedWorkspaces":["/fixture/prior"]}`), 0600) != nil {
		t.Fatal("synthetic AGY settings unavailable")
	}
	if os.WriteFile(filepath.Join(filepath.Dir(settings), "antigravity-oauth-token"), []byte("SYNTHETIC-AUTH-REFERENCE"), 0600) != nil {
		t.Fatal("synthetic native credential reference unavailable")
	}
	writeFixture(t, filepath.Join(filepath.Dir(settings), "cache", "onboarding.json"), `{"consumerOnboardingComplete":true,"enterpriseOnboardingComplete":false,"onboardingComplete":true}`)
	script := `#!/usr/bin/env python3
import json,os,sys,time,tomllib,pathlib
args=sys.argv[1:]
with open(os.environ['REGISTRATION_CALLS'],'a') as f:f.write(json.dumps(args)+'\n')
mode=os.environ.get('REGISTRATION_FAILURE','')
def recorded():
 return [json.loads(line) for line in open(os.environ['REGISTRATION_CALLS'])]
def cwd():
 a=recorded()[0];return a[a.index('--cwd')+1]
if args[:2]==['workspace','create']:
 print(json.dumps({'result':{'workspace':{'workspace_id':'w1'},'root_pane':{'pane_id':'w1:p1'}}}))
elif args[:2]==['agent','start'] and '--kind' in args and args[args.index('--kind')+1]=='agy':
 native=args[args.index('--')+1:]
 if '--gemini_dir' not in native or '--app_data_dir' not in native:
  print(json.dumps({'error':{'code':'timeout','message':'synthetic missing scoped trust'}}));sys.exit(1)
 settings=pathlib.Path(native[native.index('--gemini_dir')+1])/native[native.index('--app_data_dir')+1]/'settings.json'
 if json.loads(settings.read_text()).get('trustedWorkspaces')!=[cwd()]:
  print(json.dumps({'error':{'code':'timeout','message':'synthetic wrong scoped trust'}}));sys.exit(1)
 credential=settings.parent/'antigravity-oauth-token'
 expected=pathlib.Path(native[native.index('--gemini_dir')+1])/'antigravity-cli'/'antigravity-oauth-token'
 if not credential.is_symlink() or credential.resolve()!=expected.resolve() or credential.read_text()!='SYNTHETIC-AUTH-REFERENCE':
  print(json.dumps({'error':{'code':'timeout','message':'synthetic auth reference missing'}}));sys.exit(1)
 onboarding=settings.parent/'cache'/'onboarding.json'
 expectedOnboarding=expected.parent/'cache'/'onboarding.json'
 if not onboarding.is_symlink() or onboarding.resolve()!=expectedOnboarding.resolve() or json.loads(onboarding.read_text()).get('consumerOnboardingComplete')!=True:
  print(json.dumps({'error':{'code':'timeout','message':'synthetic onboarding reference missing'}}));sys.exit(1)
 if mode in ['agy-timeout','agy-timeout-unknown','agy-timeout-name-changed']:
  print(json.dumps({'error':{'code':'timeout','message':'PRIVATE-AGY-CANARY'}}));sys.exit(1)
 print(json.dumps({'result':{}}))
elif args[:2]==['agent','get'] and any(a[:2]==['agent','start'] and '--kind' in a and a[a.index('--kind')+1]=='agy' for a in recorded()):
 a={'agent':'agy','name':'fixture-agent','pane_id':'w1:p1','workspace_id':'w1','cwd':cwd(),'agent_status':'idle','interactive_ready':True,'state_change_seq':1}
 if mode in ['agy-timeout','agy-timeout-unknown','agy-timeout-name-changed']:a.update(name='',agent_status='unknown')
 if mode=='agy-timeout-name-changed' and sum(x[:2]==['agent','get'] for x in recorded())>1:a['name']='fixture-agent'
 if mode=='agy-foreign':a['agent']='claude'
 if mode=='agy-name':a['name']='foreign-agent'
 if mode=='agy-pane':a['pane_id']='w2:p1'
 if mode=='agy-workspace':a['workspace_id']='w2'
 if mode=='agy-cwd':a['cwd']='/fixture/foreign'
 if mode=='agy-working':a['agent_status']='working'
 if mode=='agy-unknown':a['agent_status']='unknown'
 if mode=='agy-native-blocked':a['agent_status']='blocked'
 if mode=='agy-not-ready':a['interactive_ready']=False
 if mode=='agy-changing':a['state_change_seq']=sum(x[:2]==['agent','get'] for x in recorded())
 print(json.dumps({'result':{'agent':a}}))
elif args[:2]==['pane','read'] and any(a[:2]==['agent','start'] and '--kind' in a and a[a.index('--kind')+1]=='agy' for a in recorded()):
 if mode=='agy-unreadable':sys.exit(1)
 if mode=='agy-malformed':print(json.dumps({'error':{'code':'PRIVATE-AGY-CANARY'}}))
 elif mode=='agy-oversized':print('PRIVATE-AGY-CANARY'*10000)
 elif mode=='agy-empty':print('')
 elif mode=='agy-utf8':sys.stdout.buffer.write(b'\xffPRIVATE-AGY-CANARY')
 elif mode=='agy-onboarding':print('Welcome to Antigravity CLI!\nChoose your color scheme:\n > terminal\n light\n solarized light\n colorblind-friendly light\n dark')
 elif mode=='agy-login':print('Welcome to the Antigravity CLI. You are currently\nnot signed in.\nSelect login method:\n > 1. Google OAuth\n 2. Use a Google Cloud project\n ↑/↓ Navigate · enter Select')
 elif mode in ['agy-trust','agy-timeout','agy-timeout-name-changed']:print('Accessing workspace:\n/fixture/PRIVATE-AGY-CANARY\nDo you trust the contents of this project?\nAntigravity requires permission to read, edit, and execute files here.\nConfirm · n / esc')
 elif mode=='agy-unknown-screen':print('PRIVATE-AGY-CANARY unfamiliar startup dialog')
 else:print('Antigravity\n > \n ? for shortcuts')
elif args[:2]==['agent','start'] and mode=='needs-trust':
 native=args[args.index('--')+1:]
 configs=[native[i+1] for i,x in enumerate(native[:-1]) if x=='-c' and native[i+1].startswith('projects=')]
 if len(configs)!=1 or tomllib.loads(configs[0]).get('projects')!={cwd():{'trust_level':'trusted'}}:
  print(json.dumps({'error':{'code':'timeout','message':'synthetic folder consent'}}));sys.exit(1)
 print(json.dumps({'result':{}}))
elif args[:2]==['agent','start'] and mode.startswith('trust-'):
 print(json.dumps({'error':{'code':'timeout','message':'PRIVATE-TRUST-CANARY'}}));sys.exit(1)
elif args[:2]==['agent','get'] and mode.startswith('trust-'):
 a={'agent':'codex','name':'','pane_id':'w1:p1','workspace_id':'w1','cwd':cwd(),'agent_status':'unknown','state_change_seq':0}
 if mode=='trust-foreign':a['agent']='claude'
 if mode=='trust-name':a['name']='foreign-agent'
 if mode=='trust-pane':a['pane_id']='w2:p1'
 if mode=='trust-workspace':a['workspace_id']='w2'
 if mode=='trust-cwd':a['cwd']='/fixture/foreign'
 if mode=='trust-working':a['agent_status']='working'
 if mode=='trust-changed':a['state_change_seq']=sum(x[:2]==['agent','get'] for x in recorded())
 print(json.dumps({'result':{'agent':a}}))
elif args[:2]==['pane','read'] and mode.startswith('trust-'):
 if mode=='trust-unreadable':sys.exit(1)
 if mode=='trust-malformed':print(json.dumps({'error':{'code':'PRIVATE-TRUST-CANARY'}}))
 elif mode=='trust-oversized':print('PRIVATE-TRUST-CANARY'*10000)
 elif mode=='trust-quoted':print('The brief mentions Trust this folder? and Trust and continue.')
 else:print('  Folder access\n  /fixture/PRIVATE-TRUST-CANARY\n\n  Trust this folder? Codex can read, edit, and run\n  files here, subject to your permission settings.\n\n› 1. Trust and\n  continue\n  2. Quit\n\n  enter continue · esc quit\n')
elif args[:2]==['agent','start'] and os.environ.get('REGISTRATION_FAILURE')=='busy':
 print(json.dumps({'error':{'code':'agent_pane_busy','message':'synthetic busy pane'}}));sys.exit(1)
elif args[:2]==['agent','start'] and os.environ.get('REGISTRATION_FAILURE')=='envelope':
 print(json.dumps({'error':{'code':'agent_not_ready','message':'synthetic not ready'}}))
elif args[:2]==['agent','start'] and os.environ.get('REGISTRATION_FAILURE')=='malformed':
 print('not a response')
elif args[:2]==['agent','start'] and os.environ.get('REGISTRATION_FAILURE')=='timeout':
 print(json.dumps({'error':{'code':'timeout','message':'PRIVATE-STARTUP-CANARY'}}));sys.exit(1)
elif args[:2]==['agent','start'] and os.environ.get('REGISTRATION_FAILURE')=='delayed':
 # Scale a cold startup: the old budget expires before readiness, without a live agent or 30s sleep.
 if int(args[args.index('--timeout')+1]) < 120000:
  print(json.dumps({'error':{'code':'timeout','message':'synthetic cold startup'}}));sys.exit(1)
 time.sleep(.05)
 print(json.dumps({'result':{}}))
else:print(json.dumps({'result':{}}))
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	calls := func() [][]string {
		b, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		var result [][]string
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var args []string
			if err := json.Unmarshal([]byte(line), &args); err != nil {
				t.Fatal(err)
			}
			result = append(result, args)
		}
		return result
	}
	return Host{Home: root}, calls
}

func TestRegistrationBeforeGoalUsesExactPaneAndConfiguredArgv(t *testing.T) {
	expectedArgs := map[string][]string{
		"codex":  {"--dangerously-bypass-approvals-and-sandbox", "-m", "fixture model 'quoted'", "-c", `model_reasoning_effort="high"`},
		"claude": {"--dangerously-skip-permissions", "--model", "fixture model 'quoted'", "--effort", "fixture-effort"},
		"agy":    {"--dangerously-skip-permissions", "--model", "fixture model 'quoted'", "--effort", "fixture-effort"},
	}
	for kind, configuredArgs := range expectedArgs {
		t.Run(kind, func(t *testing.T) {
			h, calls := registrationHost(t, "")
			o := Overrides{Model: "fixture model 'quoted'", Effort: "fixture-effort", Router: "fixture-provider"}
			if kind == "codex" {
				o.Effort = "high"
			}
			cwd := t.TempDir()
			if kind == "agy" {
				writeFixture(t, filepath.Join(cwd, ".git", "info", "exclude"), "")
			}
			pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", cwd, map[string]string{"FIXTURE_ENV": "fixture-value"}, kind, o, registrationReceipt(t, kind, o))
			if err != nil || pane != "w1:p1" || ws != "w1" {
				t.Fatal("registration did not return the created handles")
			}
			got := calls()
			wantCalls := 3
			if kind == "agy" {
				wantCalls = 6 // Exact occupant/screen/occupant before goal delivery.
			}
			if len(got) != wantCalls {
				t.Fatalf("wanted workspace, environment, registration; got %d calls", len(got))
			}
			if strings.Contains(got[1][3], "exec ") || !strings.Contains(got[1][3], "export FIXTURE_ENV=") {
				t.Fatal("environment preparation must leave the shell available")
			}
			expected := append([]string{"agent", "start", "fixture-agent", "--kind", kind, "--pane", "w1:p1", "--timeout", "120000", "--"}, configuredArgs...)
			if kind == "codex" {
				quoted, _ := json.Marshal(cwd)
				expected = append(expected, "-c", "projects={"+string(quoted)+`={trust_level="trusted"}}`, "-c", "check_for_update_on_startup=false")
			}
			if kind == "agy" {
				gemini := filepath.Join(h.Home, ".gemini")
				store, _ := agyStoreDirectory(cwd, "orchid-"+strings.Repeat("d", 64))
				relative, _ := filepath.Rel(gemini, store)
				expected = append(expected, "--gemini_dir", gemini, "--app_data_dir", relative)
			}
			if !reflect.DeepEqual(got[2], expected) {
				t.Fatalf("argv lost identity or quoting: %q", got[2])
			}
		})
	}
}

func TestRegistrationRefusesBusyNotReadyAndMalformedResponses(t *testing.T) {
	for _, failure := range []string{"busy", "envelope", "malformed", "timeout"} {
		t.Run(failure, func(t *testing.T) {
			h, calls := registrationHost(t, failure)
			pane, ws, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil, "codex", Overrides{}, registrationReceipt(t, "codex", Overrides{}))
			if !errors.Is(err, errAgentRegistration) {
				t.Fatal("failed registration was accepted")
			}
			if pane != "w1:p1" || ws != "w1" {
				t.Fatal("failure must retain owned cleanup handles")
			}
			wantCalls := 3
			if failure == "timeout" {
				wantCalls++ // exact-pane diagnostic read; never an input or retry
			}
			if len(calls()) != wantCalls {
				t.Fatal("registration failure retried or delivered a goal")
			}
			if failure == "timeout" && registrationFailureKind(err) != "startup_timeout" {
				t.Fatal("startup timeout lost its safe classification")
			}
		})
	}
}

func TestNonInteractiveRunKeepsDeadlineSupervisedLaunch(t *testing.T) {
	h, calls := registrationHost(t, "")
	if _, _, err := h.spawnAgent(context.Background(), "fixture-agent", t.TempDir(), nil, "codex-run", Overrides{}, registrationReceipt(t, "codex-run", Overrides{})); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 2 || !strings.Contains(got[1][3], "exec bash") {
		t.Fatal("run mode must not pretend to register an interactive agent")
	}
}

func TestRegistrationFenceSurvivesRestartAndRefusesRepeatedAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st := loadState(path)
	if !st.reserveLaunch(7) {
		t.Fatal("positive reservation failed")
	}
	for i := 0; i < 20; i++ {
		st = loadState(path)
		if st.reserveLaunch(7) {
			t.Fatal("restart allowed a duplicate launch")
		}
	}
	st.blockLaunch(7, "registration_failed")
	st = loadState(path)
	if st.LaunchBlocks[7].Reason != "registration_failed" || st.reserveLaunch(7) {
		t.Fatal("abandonment did not persist")
	}
	if !st.reserveLaunch(8) {
		t.Fatal("one abandoned issue blocked an unrelated assignment")
	}
}

func TestRegistrationCannotLaunchWithoutDurableState(t *testing.T) {
	st := loadState(filepath.Join(t.TempDir(), "missing", "state.json"))
	if st.reserveLaunch(7) {
		t.Fatal("failed persistence licensed a launch")
	}
}

func TestAbandonmentCommentRetriesOnlyNotification(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "comments")
	t.Setenv("REGISTRATION_COMMENT_CALLS", calls)
	t.Setenv("REGISTRATION_COMMENT_FAIL", "1")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
printf 'comment\n' >> "$REGISTRATION_COMMENT_CALLS"
[ "$REGISTRATION_COMMENT_FAIL" = 0 ]
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	st := loadState(filepath.Join(root, "state.json"))
	if !st.reserveLaunch(7) {
		t.Fatal("reservation failed")
	}
	c := &Coord{st: st, cfg: &Config{Inbox: "fixture/inbox"}}
	if !c.reportBlockedLaunch(context.Background(), 7) || st.LaunchBlocks[7].Notified {
		t.Fatal("failed comment lost launch fence")
	}
	t.Setenv("REGISTRATION_COMMENT_FAIL", "0")
	if !c.reportBlockedLaunch(context.Background(), 7) || !st.LaunchBlocks[7].Notified {
		t.Fatal("comment retry failed")
	}
	if !c.reportBlockedLaunch(context.Background(), 7) {
		t.Fatal("notified fence disappeared")
	}
	b, err := os.ReadFile(calls)
	if err != nil || string(b) != "comment\ncomment\n" {
		t.Fatal("comment duplicated after success")
	}
	if loadState(st.path).reserveLaunch(7) {
		t.Fatal("comment success allowed a launch")
	}
}
