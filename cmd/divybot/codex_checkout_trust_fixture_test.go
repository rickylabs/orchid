package main

import (
	"path/filepath"
	"sync"
	"testing"
)

type checkoutRecoveryFixture struct {
	mu              sync.Mutex
	projects        map[string]any
	reads, removals int
	failRead        bool
	onRead          func()
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
	c.fleet = herdrFleetFixture(t, c.herdrObserverHosts())
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
