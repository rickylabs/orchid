package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUnmeteredLimitsCannotDisableNativeMeters(t *testing.T) {
	for _, limits := range []UnmeteredTransportLimits{{"claude": {1}}, {"codex": {1}}, {"invented": {1}}, {"agy": {-1}}, {"agy": {257}}} {
		if limits.valid() || limits.cap("agy") != 0 {
			t.Fatal("invalid unmetered policy admitted")
		}
		path := filepath.Join(t.TempDir(), "config.json")
		data, _ := json.Marshal(map[string]any{"unmetered_transports": limits})
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil {
			t.Fatal("runtime accepted invalid unmetered policy")
		}
	}
	for _, data := range []string{`{"unmetered_transports":null}`, `{"unmetered_transports":{"agy":{"max_active":1,"unknown":true}}}`, `{"unmetered_transports":{"agy":{"max_active":1,"max_active":2}}}`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil {
			t.Fatal("unknown or duplicate static policy accepted")
		}
	}
	for _, cap := range []int{0, 1, 256} {
		if !(UnmeteredTransportLimits{"agy": {cap}}).valid() {
			t.Fatal("valid explicit cap refused")
		}
	}
}

func TestAGYCapNeverConsumesClaudeMeter(t *testing.T) {
	now := time.Now()
	native := quota{ok: true, at: now, seven: RateLimit{UsedPct: 95, ResetsAt: now.Add(time.Hour).Unix()}}
	cfg := &Config{Targets: []Target{{Agents: []string{"claude", "codex", "agy", "opencode"}}}, Governor: Gov{Enabled: true, WeeklyCeiling: 90, MaxActive: 4, MinActive: 1}, UnmeteredTransports: UnmeteredTransportLimits{"agy": {1}}}
	st := loadState(filepath.Join(t.TempDir(), "state.json"))
	c := &Coord{cfg: cfg, st: st}
	c.gov.q = map[string]quota{"claude": native, "codex": native, "agy": native}
	original := c.gov.q["claude"]
	caps := c.curCaps()
	if caps["agy"] != 1 || caps["claude"] != 0 || caps["codex"] != 0 {
		t.Fatal("static AGY cap borrowed quota or weakened native meters", caps)
	}
	st.Jobs[7] = &Job{Issue: 7, Agent: "agy"}
	budget := c.admissionBudget(nil)
	if budget["agy"] != 0 || budget["claude"] != 0 || c.gov.q["claude"] != original || len(st.QuotaSamples["agy"]) != 0 {
		t.Fatal("AGY launch changed Claude meter or quota ring")
	}
	delete(st.Jobs, 7)
	if c.admissionBudget(nil)["agy"] != 1 {
		t.Fatal("AGY explicit seat was not restored")
	}
	cfg.UnmeteredTransports = nil
	if c.curCaps()["agy"] != 0 {
		t.Fatal("AGY inherited global cap without explicit policy")
	}
	if _, _, _, ok := parseHostQuota("agy", "1782700000 "+claudeLine); ok {
		t.Fatal("Claude quota accepted as AGY evidence")
	}
}

func TestQuotaSamplingSkipsUnmeteredAccounts(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := map[string]any{"rate_limits": map[string]any{"five_hour": map[string]any{"used_percentage": 12, "resets_at": time.Now().Add(time.Hour).Unix()}}}
	bytes, _ := json.Marshal(data)
	if err := os.WriteFile(filepath.Join(dir, "statusline.jsonl"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	c := &Coord{cfg: &Config{Hosts: []Host{{SSH: "localhost", Home: root}}, Targets: []Target{{Agents: []string{"claude", "agy", "opencode"}}}}}
	qs := c.sampleQuota(context.Background())
	if !qs["claude"].ok || qs["claude"].five.UsedPct != 12 {
		t.Fatal("Claude native sampling changed")
	}
	if _, ok := qs["agy"]; ok {
		t.Fatal("AGY sampled the Claude meter")
	}
	if _, ok := qs["opencode"]; ok {
		t.Fatal("OpenCode sampled the Claude meter")
	}
}

func TestAGYAvailabilityIsExplicitUnmeteredCapacity(t *testing.T) {
	now := time.Now()
	bad := quota{ok: true, at: now.Add(-time.Hour), seven: RateLimit{UsedPct: 100, ResetsAt: now.Add(time.Hour).Unix()}}
	for _, cap := range []int{0, 1} {
		for _, remaining := range []int{-1, 0, 1} {
			limits := UnmeteredTransportLimits{"agy": {cap}}
			s := buildTransportAvailability(map[string]int{"agy": remaining}, map[string]quota{"agy": bad}, now, time.Minute, 90, time.Minute, limits, nil)
			if s.Transports[2].Available != (cap > 0 && remaining > 0) {
				t.Fatal("AGY meter or missing static policy decided availability")
			}
		}
	}
}

func TestOpenCodePoolSnapshotCountsExactAndUnboundSeats(t *testing.T) {
	cfg := OpenCodeConfig{Providers: map[string]OpenCodeProvider{"fixture-a": {1}, "fixture-b": {2}}}
	jobs := map[int]*Job{1: {Agent: "opencode", OpenCode: &openCodeRun{Route: openCodeRoute{Provider: "fixture-a"}}}, 2: {Agent: "opencode"}, 3: {Agent: "agy"}}
	pools := openCodeProviderPools(cfg, jobs)
	if !reflect.DeepEqual(pools, []openCodeProviderPool{{"fixture-a", 1, 2}, {"fixture-b", 2, 1}}) {
		t.Fatal("provider source snapshot invented free capacity", pools)
	}
	budget := openCodeCapacityBudget(cfg, jobs, nil)
	for _, pool := range pools {
		if budget["opencode:"+pool.Provider] != pool.MaxActive-pool.Active {
			t.Fatal("source pools contradict admission")
		}
	}
	if len(openCodeProviderPools(OpenCodeConfig{}, nil)) != 0 {
		t.Fatal("an unconfigured provider was invented")
	}
}

func TestAdmissionTickPublishesSourceProviderPools(t *testing.T) {
	root := privateTestRoot(t)
	cfg := &Config{Targets: []Target{{Agents: []string{"agy", "opencode"}}}, Matrix: MatrixConfig{ReceiptRoot: root}, UnmeteredTransports: UnmeteredTransportLimits{"agy": {1}}, OpenCode: OpenCodeConfig{Providers: map[string]OpenCodeProvider{"fixture-provider": {2}}}}
	st := &State{Jobs: map[int]*Job{1: {Agent: "opencode", OpenCode: &openCodeRun{Route: openCodeRoute{Provider: "fixture-provider"}}}}}
	c := &Coord{cfg: cfg, st: st}
	c.gov.q = map[string]quota{}
	budget := c.admissionBudget(nil)
	var first transportAvailabilitySnapshot
	if err := readPrivateActionJSON(transportAvailabilityPath(root), &first); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.OpenCodeProviderPools, []openCodeProviderPool{{"fixture-provider", 2, 1}}) || budget["opencode"] != 1 || !first.Transports[3].Available {
		t.Fatal("tick lost source pool or aggregate agreement", first, budget)
	}
	cfg.OpenCode.Providers["fixture-provider"] = OpenCodeProvider{1}
	c.admissionBudget(nil)
	var next transportAvailabilitySnapshot
	if err := readPrivateActionJSON(transportAvailabilityPath(root), &next); err != nil {
		t.Fatal(err)
	}
	if next.OpenCodeProviderPools[0].MaxActive != 1 || next.OpenCodeProviderPools[0].Active != 1 || next.Transports[3].Available {
		t.Fatal("next tick retained config mirror", next)
	}
}

func TestUnmeteredTransportNeverReadsNativeQuota(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "reads")
	fake := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\necho native-read >> " + shq(logfile) + "\necho '1782700000 " + claudeLine + "'\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	c := &Coord{cfg: &Config{Hosts: []Host{{SSH: "fixture-host"}}, Targets: []Target{{Agents: []string{"claude", "agy", "opencode"}}}}}
	c.sampleQuota(context.Background())
	data, err := os.ReadFile(logfile)
	if err != nil || strings.Count(string(data), "native-read") != 1 {
		t.Fatal("unmetered transport issued native quota read", string(data), err)
	}
}
