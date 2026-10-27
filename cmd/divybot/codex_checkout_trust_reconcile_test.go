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

// Real startup and tick wiring recover native entries left before Job save,
// retry a failed rollback and remove grants after bounded capacity release.
func TestCodexTrustReconcileStartupAndTickRecoverOrphanGrants(t *testing.T) {
	var mu sync.Mutex
	projects := map[string]any{}
	reads, removals := 0, 0
	h := canonicalFixtureHost(t, "multi", func(method string, params map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		if method == codexConfigRead {
			reads++
			snapshot := make(map[string]any, len(projects))
			for cwd, project := range projects {
				snapshot[cwd] = project
			}
			return map[string]any{"config": map[string]any{"projects": snapshot}}
		}
		if method != codexConfigValueWrite || params["value"] != nil {
			t.Fatal("recovery attempted a non-removal effect")
		}
		for cwd := range projects {
			if params["keyPath"] == codexCheckoutTrustKey(cwd) {
				delete(projects, cwd)
				removals++
			}
		}
		return trustWriteOK()
	})
	h.WorkdirRoot, h.DiskWorkdirRoot = filepath.Join(h.Home, "legacy"), filepath.Join(h.Home, "disk")
	off := false
	h.RemoteControl = &RemoteControlConfig{Codex: &off} // disabling launches must still clean old grants
	bin := filepath.Join(h.Home, ".local", "bin")
	if os.MkdirAll(bin, 0700) != nil || os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\nprintf '%s\\n' '{\"result\":{\"agents\":[]}}'\n"), 0700) != nil {
		t.Fatal("isolated fleet fixture unavailable")
	}
	cfg := &Config{Hosts: []Host{h}, StateFile: filepath.Join(t.TempDir(), "state.json")}
	c := newCoord(cfg)
	live := filepath.Join(h.DiskWorkdirRoot, "issue-7")
	orphan := filepath.Join(h.DiskWorkdirRoot, "issue-8")
	foreignHost := filepath.Join(h.DiskWorkdirRoot, "issue-9")
	c.st.Jobs[7] = &Job{Issue: 7, Agent: "codex", Host: h.Name, Workdir: live}
	c.st.Jobs[9] = &Job{Issue: 9, Agent: "codex", Host: "other-host", Workdir: foreignHost}
	if c.st.save() != nil {
		t.Fatal("recovery state unavailable")
	}
	c.st = loadState(c.st.path) // restart lost all unpersisted launch state
	mu.Lock()
	for _, cwd := range []string{live, orphan, foreignHost} {
		projects[cwd] = map[string]any{"trust_level": "trusted"}
	}
	mu.Unlock()
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	c.startOwnerGrantsWithTargets(ctx)
	mu.Lock()
	if len(projects) != 1 || projects[live] == nil || reads != 1 || removals != 2 {
		t.Fatal("startup did not remove unsaved grants or preserve its persisted job")
	}
	// A failed launch rollback leaves only native config evidence for the next tick.
	projects[orphan] = map[string]any{"trust_level": "trusted"}
	// A bounded release cannot keep trust even if its Job awaits state removal.
	c.st.Jobs[7].StopMisses = stopMissLimit
	mu.Unlock()
	c.tick(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(projects) != 0 || reads != 2 || removals != 4 {
		t.Fatal("tick failed to reconcile a failed launch or bounded release")
	}
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

func TestCodexTrustGrantMatchesReceiptIssue(t *testing.T) {
	h := canonicalFixtureHost(t, "valid", func(string, map[string]any) any { t.Fatal("mismatched checkout reached daemon"); return nil })
	on := true
	h.RemoteControl = &RemoteControlConfig{Codex: &on}
	ctx, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	_, _, err := h.spawnAgent(ctx, "fixture-agent", "/fixture/issue-8", nil, "codex", Overrides{}, registrationReceipt(t, "codex", Overrides{}), "Synthetic task")
	if err == nil || matrixCause(err) != "spawn.checkout-trust" {
		t.Fatal("a mismatched checkout was not refused before client selection")
	}
}
