package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func roleConfig(t *testing.T, roles string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"hosts":[{"name":"fixture","capacity":3`+roles+`}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	return loadConfig(path)
}

const roleConfigFields = `,"herdr_roles":{"dispatch":{"session":"dispatch"},"operator":{"session":"operator"},"diagnostics":{"session":"diagnostics"}}`

func TestHerdrRolesRouteBothDispatchPaths(t *testing.T) {
	cfg, err := roleConfig(t, roleConfigFields)
	if err != nil {
		t.Fatal(err)
	}
	h := cfg.Hosts[0]
	h.HerdrSession, h.HerdrMachine = "operator", "fixture"
	c := &Coord{st: &State{Jobs: map[int]*Job{1: {Host: h.Name}}}, hosts: map[string]Host{h.Name: h}}
	for name, pick := range map[string]func(Target, string) (Host, bool){"ordinary": c.pickHost, "owner": c.pickOwnerHost} {
		t.Run(name, func(t *testing.T) {
			selected, ok := pick(Target{}, "codex")
			if !ok || selected.Name != h.Name || selected.Capacity != h.Capacity || selected.herdrScope().Session != "dispatch" || selected.HerdrMachine != "" {
				t.Fatal("dispatch did not retain placement and select the dispatch role")
			}
			raw, err := json.Marshal(&Job{Host: selected.Name, HerdrScope: selected.herdrScope()})
			var restored Job
			if err != nil || json.Unmarshal(raw, &restored) != nil || !sameHerdrScope(restored.HerdrScope, &herdrScope{Session: "dispatch"}) {
				t.Fatal("dispatch scope did not survive persistence")
			}
		})
	}
	// Agent eligibility and capability still gate ordinary dispatch; the owner
	// retains its existing capacity/agent override and capability requirement.
	h.Capacity = 1
	h.Agents = []string{"claude"}
	c.hosts[h.Name] = h
	if _, ok := c.pickHost(Target{}, "codex"); ok {
		t.Fatal("ordinary eligibility changed")
	}
	if selected, ok := c.pickOwnerHost(Target{}, "codex"); !ok || selected.herdrScope().Session != "dispatch" {
		t.Fatal("owner override lost dispatch scope")
	}
	for _, pick := range []func(Target, string) (Host, bool){c.pickHost, c.pickOwnerHost} {
		if _, ok := pick(Target{NeedCap: "missing"}, "claude"); ok {
			t.Fatal("capability gate changed")
		}
	}
}

func TestHerdrRolesAbsentPreservesPlacementAndScope(t *testing.T) {
	for _, fields := range []string{"", `,"herdr_session":"operator"`} {
		cfg, err := roleConfig(t, fields)
		if err != nil {
			t.Fatal(err)
		}
		h := cfg.Hosts[0]
		c := &Coord{st: &State{Jobs: map[int]*Job{}}, hosts: map[string]Host{h.Name: h}}
		for _, pick := range []func(Target, string) (Host, bool){c.pickHost, c.pickOwnerHost} {
			selected, ok := pick(Target{}, "codex")
			if !ok || !reflect.DeepEqual(selected, h) {
				t.Fatal("configuration without roles changed")
			}
		}
		observers := c.herdrObserverHosts()
		if len(observers) != 1 || !reflect.DeepEqual(observers[h.observerKey()], h) {
			t.Fatal("legacy observer changed")
		}
	}
}

func TestHerdrRolesRejectUnsafeConfiguration(t *testing.T) {
	for _, roles := range []string{
		`{"operator":{"session":"operator"}}`,
		`{"dispatch":{"session":""}}`,
		`{"dispatch":{"session":"dispatch\n"}}`,
		`{"dispatch":{"session":"dispatch","machine":"bad\n"}}`,
		`{"dispatch":{"session":"dispatch","machine":"fixture"}}`,
		`{"dispatch":{"session":"dispatch"},"unknown":{"session":"operator"}}`,
		`{"dispatch":{"session":"dispatch"},"operator":{"session":"dispatch"}}`,
		`{"dispatch":{"session":"dispatch"},"operator":{"session":"operator"},"diagnostics":{"session":"operator"}}`,
	} {
		if _, err := roleConfig(t, `,"herdr_roles":`+roles); err == nil {
			t.Error("unsafe role configuration accepted")
		}
	}
}

func TestHerdrRolesObserveExplicitAndRecordedScopes(t *testing.T) {
	cfg, err := roleConfig(t, roleConfigFields)
	if err != nil {
		t.Fatal(err)
	}
	h := cfg.Hosts[0]
	job := &Job{Host: h.Name, HerdrScope: &herdrScope{Session: "recorded", Machine: "fixture"}}
	c := &Coord{st: &State{Jobs: map[int]*Job{1: job, 2: {Host: h.Name}}}, hosts: map[string]Host{h.Name: h}}
	observers := c.herdrObserverHosts()
	if len(observers) != 5 {
		t.Fatal("configured or recorded observer missing")
	}
	for _, scope := range []*herdrScope{{Session: "default"}, {Session: "dispatch"}, {Session: "operator"}, {Session: "diagnostics"}, job.HerdrScope} {
		expected := h.forJob(&Job{HerdrScope: scope})
		got, ok := observers[expected.observerKey()]
		if !ok || !sameHerdrScope(got.herdrScope(), scope) {
			t.Fatal("observer lost its explicit scope")
		}
	}
	if !sameHerdrScope(h.forJob(job).herdrScope(), job.HerdrScope) || !sameHerdrScope(h.forJob(&Job{}).herdrScope(), nil) {
		t.Fatal("role configuration replaced recorded scope")
	}
}

func TestHerdrRolesRetryUsesDispatchScope(t *testing.T) {
	c, _, _, req, is, record := retryActionFixture(t)
	var dispatch dispatchBinding
	name := filepath.Join(record, "dispatch.json")
	raw, err := os.ReadFile(name)
	if err != nil || json.Unmarshal(raw, &dispatch) != nil {
		t.Fatal("fixture dispatch unavailable")
	}
	dispatch.HerdrScope = &herdrScope{Session: "dispatch"}
	raw, err = json.Marshal(dispatch)
	if err != nil || os.WriteFile(name, raw, 0600) != nil {
		t.Fatal("fixture dispatch write failed")
	}
	h := c.hosts[dispatch.Host]
	if err := json.Unmarshal([]byte(`{"herdr_roles":{"dispatch":{"session":"dispatch"},"operator":{"session":"operator"}}}`), &h); err != nil {
		t.Fatal(err)
	}
	c.hosts[h.Name] = h
	c.actions.retryIssue = func(context.Context, int) (Issue, string, error) { return is, "OPEN", nil }
	c.actions.retryNativeFailed = func(_ context.Context, got Host, _ string) (bool, error) {
		if got.herdrScope().Session != "dispatch" {
			t.Fatal("retry inspected a manual role")
		}
		return true, nil
	}
	c.actions.retryAttempt = func(context.Context, Issue, Target, retryExpectation, bool) (bool, matrixRefusal) {
		return true, matrixRefusal{}
	}
	var result actionReceipt
	c.deliverRetryAction(context.Background(), req, &result)
	if result.Outcome != "accepted" {
		t.Fatalf("dispatch-role retry rejected: %s", result.Reason)
	}
}

func TestHerdrRolesAdoptionSeparatesManualAndDispatchOccupants(t *testing.T) {
	for _, role := range []string{"operator", "diagnostics"} {
		t.Run(role, func(t *testing.T) {
			cfg, err := roleConfig(t, roleConfigFields)
			if err != nil {
				t.Fatal(err)
			}
			h := cfg.Hosts[0]
			c := &Coord{cfg: &Config{Targets: []Target{{Label: "fixture", Repo: "example/work"}}}, st: &State{Jobs: map[int]*Job{}}, hosts: map[string]Host{h.Name: h}}
			c.fleet = &herdrFleet{hosts: c.herdrObserverHosts(), agents: map[string][]AgentInfo{}, up: map[string]bool{}, wake: make(chan struct{}, 1)}
			manual, _ := h.forHerdrRole(role)
			dispatch, _ := h.forHerdrRole("dispatch")
			a := AgentInfo{Agent: "claude", Name: "claude-7", PaneID: "fixture-pane", WorkspaceID: "fixture-workspace", Cwd: "issue-7"}
			is := Issue{Number: 7, Labels: []string{"fixture"}}
			c.fleet.publish(manual.observerKey(), []AgentInfo{a}, true)
			status, up := c.fleetStatus(context.Background())
			if !up[manual.observerKey()] {
				t.Fatal("explicit role observation was lost")
			}
			if _, found := status[7]; found {
				t.Fatal("manual occupant entered dispatch admission")
			}
			if _, ok := c.adopt(7, is, status); ok {
				t.Fatal("manual role was automatically adopted")
			}
			c.fleet.publish(dispatch.observerKey(), []AgentInfo{a}, true)
			status, _ = c.fleetStatus(context.Background())
			job, ok := c.adopt(7, is, status)
			if !ok || !sameHerdrScope(job.HerdrScope, dispatch.herdrScope()) {
				t.Fatal("manual occupant suppressed dispatch recovery")
			}
			// Existing recorded manual work remains scoped and supervised after restore.
			job.HerdrScope = manual.herdrScope()
			status, _ = c.fleetStatus(context.Background())
			if !jobRefMatches(status[7], job) {
				t.Fatal("recorded manual job lost supervision")
			}
		})
	}
}

func TestHerdrRolesAdoptionMatchesMachineAndPreservesLegacy(t *testing.T) {
	cfg, err := roleConfig(t, roleConfigFields)
	if err != nil {
		t.Fatal(err)
	}
	h := cfg.Hosts[0]
	c := &Coord{st: &State{Jobs: map[int]*Job{}}, hosts: map[string]Host{h.Name: h}}
	c.fleet = &herdrFleet{hosts: c.herdrObserverHosts(), agents: map[string][]AgentInfo{}, up: map[string]bool{}, wake: make(chan struct{}, 1)}
	a := AgentInfo{Agent: "claude", Cwd: "issue-7"}
	wrong, _ := h.forHerdrRole("dispatch")
	wrong.HerdrMachine = "fixture"
	c.fleet.hosts[wrong.observerKey()] = wrong
	c.fleet.publish(wrong.observerKey(), []AgentInfo{a}, true)
	status, _ := c.fleetStatus(context.Background())
	if _, found := status[7]; found {
		t.Fatal("another machine supplied dispatch adoption")
	}
	legacy := h
	legacy.HerdrRoles = nil
	legacy.HerdrSession = "operator"
	c.fleet.hosts = map[string]Host{legacy.observerKey(): legacy}
	c.fleet.publish(legacy.observerKey(), []AgentInfo{a}, true)
	status, _ = c.fleetStatus(context.Background())
	if ref, found := status[7]; !found || !sameHerdrScope(ref.HerdrScope, legacy.herdrScope()) {
		t.Fatal("legacy adoption changed without a role map")
	}
}
