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

func goalTransportHost(t *testing.T, mode string) Host {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if os.MkdirAll(bin, 0700) != nil {
		t.Fatal("fixture bin failed")
	}
	t.Setenv("GOAL_RPC_FIXTURE", mode)
	script := `#!/usr/bin/env python3
import sys,json,os,time
mode=os.environ.get('GOAL_RPC_FIXTURE','')
goal=None
if mode=='existing':goal={'threadId':'fixture-thread','objective':'fixture/inbox#7: synthetic task','tokenBudget':100,'status':'active','tokensUsed':10,'timeUsedSeconds':2,'createdAt':1,'updatedAt':3}
def send(v): print(json.dumps(v),flush=True)
for line in sys.stdin:
 m=json.loads(line);method=m.get('method');i=m.get('id');p=m.get('params',{})
 if method=='initialized':continue
 if mode=='timeout':time.sleep(5);continue
 if method=='initialize':
  send({'id':i,'result':{'userAgent':'' if mode=='bad-initialize' else 'fixture'}});continue
 if method=='thread/goal/get':send({'id':i,'result':{'goal':goal}});continue
 if method=='thread/goal/set':
  if goal is None:goal={'threadId':p['threadId'],'objective':p['objective'],'tokenBudget':p['tokenBudget'],'status':p['status'],'tokensUsed':0,'timeUsedSeconds':0,'createdAt':1,'updatedAt':1}
  else:goal['status']=p['status']
  send({'id':i,'result':{'goal':goal}})
  if mode=='missing-notification':break
  send({'method':'thread/goal/updated','params':{'threadId':p['threadId'],'goal':goal}})
`
	if os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700) != nil {
		t.Fatal("fixture codex failed")
	}
	return Host{Home: root, Name: "fixture-host"}
}
func TestNativeGoalTransport(t *testing.T) {
	host := goalTransportHost(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	use := func(p *goalRPC) error { return createDispatchGoal(p, fixtureIntent()) }
	if e := host.withGoalConnection(ctx, "fixture-thread", use); e != nil {
		t.Fatal("valid transport failed", e)
	}
	if e := host.withGoalConnection(ctx, "", use); e == nil {
		t.Fatal("missing identity started transport")
	}
	host = goalTransportHost(t, "bad-initialize")
	if e := host.withGoalConnection(ctx, "fixture-thread", use); e == nil {
		t.Fatal("bad initialize accepted")
	}
	host = goalTransportHost(t, "missing-notification")
	if e := host.withGoalConnection(ctx, "fixture-thread", use); e == nil {
		t.Fatal("missing notification earned ownership")
	}
	host = goalTransportHost(t, "timeout")
	short, cancelShort := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelShort()
	if e := host.withGoalConnection(short, "fixture-thread", use); e == nil {
		t.Fatal("stalled daemon accepted")
	}
}
func TestNativeGoalStartOwnershipAndFailures(t *testing.T) {
	for _, mode := range []string{"success", "missing-notification", "identity-unavailable", "state-persistence-failure"} {
		t.Run(mode, func(t *testing.T) {
			r, j, root := fixtureGoalBinding(t)
			rpcMode := ""
			if mode == "missing-notification" {
				rpcMode = mode
			}
			host := goalTransportHost(t, rpcMode)
			report := nativeReport(j, mode != "identity-unavailable")
			script := "#!/bin/sh\nprintf '%s\\n' '" + strings.ReplaceAll(string(append(append([]byte(`{"result":`), report...), '}')), "'", "'\\''") + "'\n"
			if os.WriteFile(filepath.Join(host.Home, ".local", "bin", "herdr"), []byte(script), 0700) != nil {
				t.Fatal("fixture report failed")
			}
			statePath := filepath.Join(t.TempDir(), "state.json")
			if mode == "state-persistence-failure" {
				statePath = filepath.Join(t.TempDir(), "absent", "state.json")
			}
			c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, st: loadState(statePath), hosts: map[string]Host{host.Name: host}}
			j.Host = host.Name
			c.st.Jobs[j.Issue] = j
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if mode == "identity-unavailable" {
				cancel()
			}
			var logs bytes.Buffer
			original := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(original)
			c.startDispatchGoal(ctx, host, j, r)
			if mode == "success" {
				if !strings.Contains(logs.String(), "native goal active; updated notification observed") || strings.Contains(logs.String(), "INCONCLUSIVE") {
					t.Fatal("successful goal emitted a refusal")
				}
			} else if !strings.Contains(logs.String(), "INCONCLUSIVE reason=") || strings.Contains(logs.String(), "native goal active;") {
				t.Fatal("failed goal emitted success or lost its reason")
			}

			if mode == "success" {
				if !j.NativeGoal.Owned || !j.NativeGoal.UpdatedNotification || j.NativeGoal.LastStatus != "active" || j.NativeGoal.Reason != "" {
					t.Fatal("verified creation did not earn ownership")
				}
				saved := loadState(statePath).Jobs[j.Issue]
				if saved == nil || !saved.NativeGoal.Owned {
					t.Fatal("goal ownership not durable")
				}
			} else if j.NativeGoal.Owned || j.NativeGoal.Reason == "" {
				t.Fatal("uncertain goal creation earned ownership")
			}
		})
	}
}
func TestNativeGoalUnownedAndDryWritesRefused(t *testing.T) {
	for _, v := range []struct {
		dry  bool
		goal *dispatchGoal
	}{{true, &dispatchGoal{Owned: true}}, {false, nil}, {false, &dispatchGoal{Owned: false}}} {
		c := &Coord{dry: v.dry}
		j := &Job{NativeGoal: v.goal}
		// These intentionally omit config/hosts/state: reaching a write path would fail.
		c.transitionGoal(context.Background(), j, "complete")
		c.finishAssignmentGoal(context.Background(), j)
	}
	c := &Coord{}
	c.startDispatchGoal(context.Background(), Host{}, &Job{}, nil)
	if closedGoalReason(os.ErrPermission) != "goal-source-unavailable" {
		t.Fatal("native error escaped closed diagnostics")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if goalWait(ctx) {
		t.Fatal("cancelled identity wait continued")
	}
}

func TestNativeGoalWrappedTransition(t *testing.T) {
	for _, mode := range []string{"success", "binding-invalid", "host-unavailable", "daemon-refused"} {
		t.Run(mode, func(t *testing.T) {
			_, j, root := fixtureGoalBinding(t)
			j.NativeGoal.Owned = true
			rpcMode := "existing"
			if mode == "daemon-refused" {
				rpcMode = "bad-initialize"
			}
			host := goalTransportHost(t, rpcMode)
			j.Host = host.Name
			c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, st: loadState(filepath.Join(t.TempDir(), "state.json")), hosts: map[string]Host{host.Name: host}}
			c.st.Jobs[j.Issue] = j
			if mode == "binding-invalid" {
				j.NativeGoal.ReceiptKey = "invalid"
			}
			if mode == "host-unavailable" {
				c.hosts = map[string]Host{}
			}
			var logs bytes.Buffer
			original := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(original)
			c.transitionGoal(context.Background(), j, "complete")
			if mode == "success" {
				if j.NativeGoal.LastStatus != "complete" || !j.NativeGoal.UpdatedNotification {
					t.Fatal("verified transition not retained")
				}
				saved := loadState(c.st.path).Jobs[j.Issue]
				if saved == nil || saved.NativeGoal.LastStatus != "complete" {
					t.Fatal("transition not persisted")
				}
			} else if j.NativeGoal.LastStatus != "" || !strings.Contains(logs.String(), "INCONCLUSIVE reason=") {
				t.Fatal("failed transition lost refusal or claimed success")
			}
		})
	}
}

func TestNativeGoalFinishAssignment(t *testing.T) {
	for _, v := range []struct {
		state, reason, want string
		failure             bool
	}{{"CLOSED", "COMPLETED", "complete", false}, {"CLOSED", "NOT_PLANNED", "paused", false}, {"OPEN", "COMPLETED", "", false}, {"CLOSED", "unknown", "", false}, {"CLOSED", "COMPLETED", "", true}} {
		t.Run(v.state+v.reason, func(t *testing.T) {
			_, j, root := fixtureGoalBinding(t)
			j.NativeGoal.Owned = true
			host := goalTransportHost(t, "existing")
			j.Host = host.Name
			c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: root}}, st: loadState(filepath.Join(t.TempDir(), "state.json")), hosts: map[string]Host{host.Name: host}}
			c.st.Jobs[j.Issue] = j
			bin := filepath.Join(host.Home, ".local", "bin")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			script := "#!/bin/sh\nprintf '%s' '{\"state\":\"" + v.state + "\",\"stateReason\":\"" + v.reason + "\"}'\n"
			if v.failure {
				script += "exit 1\n"
			}
			if os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700) != nil {
				t.Fatal("fixture issue reader failed")
			}
			var logs bytes.Buffer
			original := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(original)
			c.finishAssignmentGoal(context.Background(), j)
			if j.NativeGoal.LastStatus != v.want {
				t.Fatal("assignment status lost or fabricated")
			}
			if v.failure && !strings.Contains(logs.String(), "goal-inbox-unavailable") {
				t.Fatal("failed issue read lost its reason")
			}
		})
	}
}
func TestNativeGoalWaitAndClosedReason(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !goalWait(ctx) {
		t.Fatal("live bounded wait prematurely refused")
	}
	if closedGoalReason(goalError("goal-budget-invalid")) != "goal-budget-invalid" {
		t.Fatal("closed reason lost its meaning")
	}
}
