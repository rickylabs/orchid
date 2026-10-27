package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodexTrustReconcileKeepsLiveAndForeignProjects(t *testing.T) {
	projects := map[string]any{}
	for _, cwd := range []string{
		"/fixture/disk/issue-7", "/fixture/legacy/issue-8", // abandoned managed checkouts
		"/fixture/disk/issue-9", // retained dispatch
		"/fixture/disk", "/fixture/disk/repo", "/fixture/disk/issue-0",
		"/fixture/disk/issue-01", "/fixture/disk/nested/issue-7",
		"/fixture/disk-other/issue-7", "/foreign/issue-7",
	} {
		projects[cwd] = map[string]any{"trust_level": "trusted"}
	}
	projects["/fixture/disk/issue-10"] = map[string]any{"trust_level": "untrusted"}
	projects["/fixture/disk/issue-11"] = map[string]any{"other": "retained"}
	var reads int
	var removed []string
	p, _ := remoteFixturePort(func(method string, params map[string]any) any {
		switch method {
		case codexConfigRead:
			reads++
			if !reflect.DeepEqual(params, map[string]any{"includeLayers": false}) {
				t.Fatal("sweep read a thread or a project overlay")
			}
			return map[string]any{"config": map[string]any{"projects": projects}}
		case codexConfigValueWrite:
			if params["value"] != nil || params["mergeStrategy"] != "upsert" {
				t.Fatal("sweep wrote a grant or replaced the projects map")
			}
			removed = append(removed, params["keyPath"].(string))
			return trustWriteOK()
		default:
			t.Fatalf("unexpected sweep effect: %s", method)
			return nil
		}
	}, "")
	if err := p.reconcileCheckoutTrust([]string{"/fixture/disk", "/fixture/legacy"}, map[string]bool{"/fixture/disk/issue-9": true}); err != nil {
		t.Fatal(err)
	}
	want := []string{`projects."/fixture/disk/issue-7"`, `projects."/fixture/legacy/issue-8"`}
	if reads != 1 || !reflect.DeepEqual(removed, want) {
		t.Fatalf("sweep crossed its ownership boundary or retained an orphan: %v", removed)
	}
}

func TestCodexTrustReconcileRetriesFailedRemoval(t *testing.T) {
	remaining := map[string]any{
		"/fixture/disk/issue-7": map[string]any{"trust_level": "trusted"},
		"/fixture/disk/issue-8": map[string]any{"trust_level": "trusted"},
	}
	fail := true
	attempts := map[string]int{}
	p, _ := remoteFixturePort(func(method string, params map[string]any) any {
		if method == codexConfigRead {
			return map[string]any{"config": map[string]any{"projects": remaining}}
		}
		key := params["keyPath"].(string)
		attempts[key]++
		for cwd := range remaining {
			if key != codexCheckoutTrustKey(cwd) {
				continue
			}
			if strings.HasSuffix(cwd, "issue-7") && fail {
				return map[string]string{"status": "okOverridden", "version": "fixture-version"}
			}
			delete(remaining, cwd)
		}
		return trustWriteOK()
	}, "")
	if err := p.reconcileCheckoutTrust([]string{"/fixture/disk"}, nil); err == nil || len(remaining) != 1 || remaining["/fixture/disk/issue-7"] == nil {
		t.Fatal("failed revoke was forgotten, or prevented unrelated orphan cleanup")
	}
	fail = false
	if err := p.reconcileCheckoutTrust([]string{"/fixture/disk"}, nil); err != nil || len(remaining) != 0 {
		t.Fatal("a later tick did not retry the failed revoke")
	}
	if attempts[codexCheckoutTrustKey("/fixture/disk/issue-7")] != 2 || attempts[codexCheckoutTrustKey("/fixture/disk/issue-8")] != 1 {
		t.Fatal("reconciliation did not retry only the remaining entry")
	}
}

func TestCodexTrustReconcileRejectsUnconfirmedReadAndInvalidRoot(t *testing.T) {
	for _, result := range []any{nil, map[string]any{}, map[string]any{"config": nil}, map[string]any{"config": "invalid"}} {
		p, w := remoteFixturePort(func(string, map[string]any) any { return result }, "")
		if p.reconcileCheckoutTrust([]string{"/fixture/disk"}, nil) == nil || len(w.requests) != 1 {
			t.Fatal("an unavailable read was treated as an empty successful sweep")
		}
	}
	p, w := remoteFixturePort(func(string, map[string]any) any { t.Fatal("unsafe root reached native API"); return nil }, "")
	if p.reconcileCheckoutTrust([]string{"/"}, nil) == nil || len(w.requests) != 0 {
		t.Fatal("root-wide reconciliation was accepted")
	}
	for _, params := range []any{map[string]any{}, map[string]any{"cwd": "/fixture/issue-7"}, codexCheckoutTrustRead{IncludeLayers: true}} {
		if _, err := p.request(codexConfigRead, params); err == nil {
			t.Fatal("generic config reading gained the trust capability")
		}
	}
}

// A canonical daemon fixture records actual config reads/removals. The fleet is
// supplied independently so tests can distinguish raw occupants from adoption.
type checkoutRecoveryFixture struct {
	mu                    sync.Mutex
	projects              map[string]any
	reads, removals       int
	failRead, failRemoval bool
	onRead                func()
}

func newCheckoutRecoveryFixture(t *testing.T) (*Coord, *checkoutRecoveryFixture, Host) {
	t.Helper()
	f := &checkoutRecoveryFixture{projects: map[string]any{}}
	h := canonicalFixtureHost(t, "multi", func(method string, params map[string]any) any {
		f.mu.Lock()
		defer f.mu.Unlock()
		if method == codexConfigRead {
			f.reads++
			if f.onRead != nil {
				f.onRead()
			}
			if f.failRead {
				return map[string]any{}
			}
			snapshot := make(map[string]any, len(f.projects))
			for cwd, project := range f.projects {
				snapshot[cwd] = project
			}
			return map[string]any{"config": map[string]any{"projects": snapshot}}
		}
		if method != codexConfigValueWrite {
			return map[string]any{} // native stop intentionally remains unconfirmed
		}
		if params["value"] != nil {
			t.Error("recovery attempted a non-removal effect")
			return map[string]any{}
		}
		if f.failRemoval {
			return map[string]any{}
		}
		for cwd := range f.projects {
			if params["keyPath"] == codexCheckoutTrustKey(cwd) {
				delete(f.projects, cwd)
				f.removals++
			}
		}
		return trustWriteOK()
	})
	h.Name, h.WorkdirRoot, h.DiskWorkdirRoot = "fixture", "/fixture/legacy", "/fixture/disk"
	on := true
	h.RemoteControl = &RemoteControlConfig{Codex: &on}
	c := newCoord(&Config{Hosts: []Host{h}, StateFile: filepath.Join(t.TempDir(), "state.json")})
	c.fleet = &herdrFleet{hosts: c.herdrObserverHosts(), agents: map[string][]AgentInfo{}, up: map[string]bool{}, wake: make(chan struct{}, 1)}
	return c, f, h
}

func (f *checkoutRecoveryFixture) grant(cwds ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, cwd := range cwds {
		f.projects[cwd] = map[string]any{"trust_level": "trusted"}
	}
}

func (f *checkoutRecoveryFixture) check(t *testing.T, reads, removals int, retained ...string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reads != reads || f.removals != removals || len(f.projects) != len(retained) {
		t.Fatalf("recovery counts: reads=%d removals=%d remaining=%d; want %d/%d/%d", f.reads, f.removals, len(f.projects), reads, removals, len(retained))
	}
	for _, cwd := range retained {
		if f.projects[cwd] == nil {
			t.Fatal("recovery revoked a retained checkout")
		}
	}
}

func TestCodexTrustReconcileStartupAndTickRecoverOrphanGrants(t *testing.T) {
	c, f, h := newCheckoutRecoveryFixture(t)
	live, orphan := "/fixture/disk/issue-7", "/fixture/disk/issue-8"
	f.grant(live, orphan)
	c.fleet.publish(h.observerKey(), []AgentInfo{{Agent: "codex", Cwd: live}}, true)
	ctx := context.Background()
	c.startOwnerGrantsWithTargets(ctx)
	f.check(t, 0, 0, live, orphan) // startup must not sweep before observation/adoption
	c.tick(ctx)
	f.check(t, 1, 1, live) // raw occupant is not adoptable with no target
	c.fleet.publish(h.observerKey(), nil, true)
	c.tick(ctx)
	f.check(t, 1, 1, live) // event ticks must not read daemon config again
	c.trust.last[h.Name] = time.Now().Add(-fleetRecoveryEvery)
	c.tick(ctx)
	f.check(t, 2, 2)
}

func TestCodexTrustFirstSweepFollowsAdoption(t *testing.T) {
	c, f, h := newCheckoutRecoveryFixture(t)
	c.cfg.Targets = []Target{{Label: "work", Repo: "fixture/repo", Disabled: true}}
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1 $2" = "issue list" ]; then
  printf '%s\n' '[{"number":7,"title":"fixture","labels":[{"name":"work"},{"name":"harness"}]}]'
else
  printf '%s\n' '[]'
fi
`
	if os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700) != nil {
		t.Fatal("issue fixture unavailable")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cwd := "/fixture/disk/issue-7"
	f.grant(cwd, "/fixture/disk/issue-8")
	f.onRead = func() {
		c.st.mu.Lock()
		defer c.st.mu.Unlock()
		j := c.st.Jobs[7]
		if j == nil || j.Workdir != cwd {
			t.Error("first sweep preceded adoption of the observed cwd")
		}
	}
	c.fleet.publish(h.observerKey(), []AgentInfo{{Agent: "codex", Cwd: cwd, AgentStatus: "unknown"}}, true)
	c.tick(context.Background())
	f.check(t, 1, 1, cwd)
}

func TestCodexTrustRecoveryRequiresFirstSnapshot(t *testing.T) {
	c, f, h := newCheckoutRecoveryFixture(t)
	orphan := "/fixture/disk/issue-7"
	f.grant(orphan)
	// A recovery request may precede fleet observation. Even if host reachability
	// is known, it cannot authorize removal without the working-folder snapshot.
	c.trust.up = map[string]bool{h.Name: true}
	c.recoverCheckoutTrust(context.Background())
	f.check(t, 0, 0, orphan)
	c.fleet.publish(h.observerKey(), nil, true)
	c.fleetStatus(context.Background())
	c.reconcileCheckoutTrust(context.Background())
	f.check(t, 1, 1)
}

func TestCodexTrustRetainsRawFleetFoldersAndGlobalJobs(t *testing.T) {
	c, f, h := newCheckoutRecoveryFixture(t)
	alias := h
	alias.Name = "alias"
	c.hosts[alias.Name] = alias
	manual := h
	manual.HerdrSession = "manual"
	c.fleet.hosts[manual.observerKey()] = manual
	paths := []string{"/fixture/disk/issue-7", "/fixture/disk/issue-8", "/fixture/disk/issue-9", "/fixture/disk/issue-10", "/fixture/legacy/issue-7"}
	orphan := "/fixture/disk/issue-11"
	f.grant(append(paths, orphan)...)
	// Alias/unknown host jobs must protect the same daemon's paths globally.
	c.st.Jobs[7] = &Job{Issue: 7, Host: alias.Name, Agent: "codex", Workdir: paths[0]}
	c.st.Jobs[8] = &Job{Issue: 8, Host: "unconfigured", Agent: "claude", Workdir: paths[1]}
	// Conflicting issue keys, manual scopes and jobs failing occupant matching
	// disappear from adoption, but every raw working folder still owns trust.
	c.fleet.publish(h.observerKey(), []AgentInfo{{Agent: "claude", Cwd: paths[4]}, {Agent: "codex", Cwd: paths[2]}}, true)
	c.fleet.publish(manual.observerKey(), []AgentInfo{{Agent: "claude", Cwd: paths[3]}}, true)
	c.fleet.hosts[alias.observerKey()] = alias
	c.fleet.publish(alias.observerKey(), nil, true)
	c.fleetStatus(context.Background())
	c.reconcileCheckoutTrust(context.Background())
	f.check(t, 2, 1, paths...)
}

func TestCodexTrustAdoptionRecordsObservedWorkdir(t *testing.T) {
	c, f, h := newCheckoutRecoveryFixture(t)
	c.cfg.Targets = []Target{{Label: "work", Repo: "fixture/repo"}}
	cwd := "/fixture/disk/issue-7"
	f.grant(cwd)
	c.fleet.publish(h.observerKey(), []AgentInfo{{Agent: "codex", Cwd: cwd}}, true)
	status, _ := c.fleetStatus(context.Background())
	j, ok := c.adopt(7, Issue{Number: 7, Labels: []string{"work"}}, status)
	if !ok || j.Workdir != cwd {
		t.Fatal("adoption lost its observed working folder")
	}
	if c.st.save() != nil {
		t.Fatal("adoption state unavailable")
	}
	c.st = loadState(c.st.path)
	if c.st.Jobs[7].Workdir != cwd {
		t.Fatal("adopted Workdir did not persist")
	}
	// A later observation omission must not redirect this job to the legacy root.
	c.fleet.publish(h.observerKey(), nil, true)
	c.fleetStatus(context.Background())
	c.reconcileCheckoutTrust(context.Background())
	f.check(t, 1, 0, cwd)
}

func TestCodexTrustReleasedJobNoLongerRetainsCheckout(t *testing.T) {
	c, f, h := newCheckoutRecoveryFixture(t)
	cwd := "/fixture/disk/issue-7"
	f.grant(cwd)
	c.st.Jobs[7] = &Job{Issue: 7, Host: h.Name, Agent: "codex", Workdir: cwd, StopMisses: stopMissLimit}
	c.fleet.publish(h.observerKey(), nil, true)
	c.fleetStatus(context.Background())
	c.reconcileCheckoutTrust(context.Background())
	f.check(t, 1, 1)
}

func TestCodexTrustRecoverySkipsDownAndNonCodexHosts(t *testing.T) {
	for _, row := range []struct {
		name                                      string
		up, enabled, allowed, job, occupant, want bool
	}{
		{"down", false, true, true, true, false, false},
		{"disabled", true, false, true, false, false, false},
		{"other-agents", true, true, false, false, false, false},
		{"enabled", true, true, true, false, false, true},
		{"retained-job", true, false, false, true, false, true},
		{"fleet-job", true, false, false, false, true, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			c, f, h := newCheckoutRecoveryFixture(t)
			h.RemoteControl.Codex = &row.enabled
			if !row.allowed {
				h.Agents = []string{"claude"}
			}
			c.hosts[h.Name] = h
			if row.job {
				c.st.Jobs[9] = &Job{Issue: 9, Host: h.Name, Agent: "codex", Workdir: "/fixture/disk/issue-9"}
			}
			var agents []AgentInfo
			if row.occupant {
				agents = []AgentInfo{{Agent: "codex", Cwd: "/fixture/disk/issue-9"}}
			}
			c.fleet.publish(h.observerKey(), agents, row.up)
			c.fleetStatus(context.Background())
			c.reconcileCheckoutTrust(context.Background())
			want := 0
			if row.want {
				want = 1
			}
			f.check(t, want, 0)
		})
	}
}

func TestCodexTrustRecoveryPacesFailedReads(t *testing.T) {
	c, f, h := newCheckoutRecoveryFixture(t)
	f.failRead = true
	c.fleet.publish(h.observerKey(), nil, true)
	c.fleetStatus(context.Background())
	c.reconcileCheckoutTrust(context.Background())
	c.reconcileCheckoutTrust(context.Background())
	f.check(t, 1, 0)
	c.trust.last[h.Name] = time.Now().Add(-fleetRecoveryEvery)
	c.reconcileCheckoutTrust(context.Background())
	f.check(t, 2, 0)
}

// Failed lifecycle effects bypass the ordinary interval on the same loop turn.
func TestCodexTrustFailedLaunchRecoversImmediately(t *testing.T) {
	c, f, h := newCheckoutRecoveryFixture(t)
	f.grant("/fixture/disk/issue-8")
	c.fleet.publish(h.observerKey(), nil, true)
	c.fleetStatus(context.Background())
	c.trust.last = map[string]time.Time{h.Name: time.Now()}
	// A refused target aborts the production launch before any external effects.
	if c.spawn(context.Background(), 7, Issue{Number: 7}, h, "codex", Overrides{}, nil) == nil {
		t.Fatal("invalid launch succeeded")
	}
	f.check(t, 1, 1)
}

func TestCodexTrustFailedRevokeRecoversImmediately(t *testing.T) {
	c, f, h := newCheckoutRecoveryFixture(t)
	cwd, orphan := "/fixture/disk/issue-7", "/fixture/disk/issue-8"
	f.grant(cwd, orphan)
	c.fleet.publish(h.observerKey(), nil, true)
	c.fleetStatus(context.Background())
	c.trust.last = map[string]time.Time{h.Name: time.Now()}
	// A mismatched native binding refuses the direct revoke without a directory
	// deletion, but still schedules native recovery for unrelated orphan grants.
	j := &Job{Issue: 7, Host: h.Name, Agent: "codex", Workdir: cwd, RemoteControl: syntheticRemoteRun(t)}
	c.st.Jobs[7] = j
	if !errors.Is(c.cleanupCheckout(context.Background(), h, j), errCodexTrustRemoval) {
		t.Fatal("failed revoke lost its cleanup refusal")
	}
	f.check(t, 1, 1, cwd)
}

func TestCodexTrustBoundedReleaseRecoversImmediately(t *testing.T) {
	c, _, j, _, _ := teardownFixture(t)
	_, f, h := newCheckoutRecoveryFixture(t)
	h.Name = j.Host
	c.hosts = map[string]Host{h.Name: h}
	j.Agent, j.Label = "codex", "codex-7"
	j.RemoteControl = syntheticRemoteRun(t)
	j.Workdir, j.RemoteControl.Cwd = "/fixture/disk/issue-7", "/fixture/disk/issue-7"
	j.RemoteCleanup, j.StopMisses = "teardown", stopMissLimit-1
	f.grant(j.Workdir, "/fixture/disk/issue-8")
	c.trust = checkoutTrustRecovery{live: map[string]bool{}, up: map[string]bool{h.Name: true}, last: map[string]time.Time{h.Name: time.Now()}}
	if !c.teardown(context.Background(), 7, j, "teardown") {
		t.Fatal("bounded release did not end the run")
	}
	f.check(t, 1, 2)
}

func TestCodexTrustGrantUsesCleanupCheckoutValidation(t *testing.T) {
	for _, cwd := range []string{"/fixture/repo", "/fixture/issue-0", "/fixture/issue-01", "/fixture/issue-7/child"} {
		run := syntheticRemoteRun(t)
		run.Cwd, run.ClientVersion = cwd, canonicalFixtureVersion
		h := canonicalFixtureHost(t, "valid", func(string, map[string]any) any { t.Fatal("invalid grant reached daemon"); return nil })
		ctx, done := context.WithTimeout(context.Background(), time.Second)
		if err := h.prepareRemoteCodex(ctx, run, nil); err == nil {
			t.Fatal("grant accepted a checkout cleanup cannot revoke")
		}
		done()
	}
}

func TestCheckoutCleanupCompatibilityAndTrustOnlyRefusal(t *testing.T) {
	for _, agent := range []string{"claude", "agy", "opencode", "codex"} {
		t.Run(agent, func(t *testing.T) {
			h := Host{Home: t.TempDir()}
			// Invalid paths were skipped before this change and must stay harmless.
			j := &Job{Issue: 7, Agent: agent, Workdir: t.TempDir(), RemoteControl: syntheticRemoteRun(t)}
			if err := h.cleanupCheckout(context.Background(), j); err != nil {
				t.Fatal("invalid checkout changed the pre-existing skip contract")
			}
			// Force rm failure without a real privileged directory or service.
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "rm"), []byte("#!/bin/sh\nexit 71\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			j.RemoteControl = nil // plain cleanup owns no daemon trust
			j.Workdir = filepath.Join(t.TempDir(), "issue-7")
			if err := h.cleanupCheckout(context.Background(), j); err == nil || errors.Is(err, errCodexTrustRemoval) {
				t.Fatal("plain deletion failure was served as a trust-removal failure")
			}
			// Exercise the production teardown caller, including its continuation point.
			c := &Coord{cfg: &Config{}, st: &State{Jobs: map[int]*Job{7: j}}, hosts: map[string]Host{h.Name: h}}
			j.Host = h.Name
			if !c.teardownConfirmed(context.Background(), 7, j, "teardown") {
				t.Fatal("ordinary rm failure blocked teardown confirmation")
			}
		})
	}
}

func TestCodexTrustStopLimitReleaseAttemptsRevoke(t *testing.T) {
	c, _, j, _, _ := teardownFixture(t)
	j.Agent, j.Label = "codex", "codex-7"
	j.RemoteControl = syntheticRemoteRun(t)
	j.Workdir = j.RemoteControl.Cwd
	j.RemoteCleanup = "teardown"
	j.StopMisses = stopMissLimit - 1
	writes := make(chan bool, 2)
	h := canonicalFixtureHost(t, "multi", func(method string, params map[string]any) any {
		if method == codexConfigValueWrite {
			writes <- params["value"] == nil && params["keyPath"] == codexCheckoutTrustKey(j.Workdir)
			return trustWriteOK()
		}
		return map[string]any{} // native stop is deliberately unconfirmed
	})
	h.Name = j.Host
	c.hosts[j.Host] = h
	ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	if !c.teardown(ctx, j.Issue, j, "teardown") || len(writes) != 1 || !<-writes {
		t.Fatal("bounded release did not attempt exact-checkout trust removal")
	}
}

func TestTrustCheckoutMustHavePositiveCanonicalIssue(t *testing.T) {
	for _, row := range []struct {
		path string
		n    int
		want bool
	}{
		{"/fixture/issue-7", 7, true},
		{"/fixture/issue-0", 0, false},
		{"/fixture/issue--1", -1, false},
		{"/fixture/issue-07", 7, false},
		{"/fixture/issue-8", 7, false},
	} {
		if validJobCheckout(row.path, row.n) != row.want {
			t.Fatal("grant/revoke checkout validation admitted a noncanonical or mismatched issue")
		}
	}
}
