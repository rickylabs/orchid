package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"
)

func capacityText(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func capacityCount(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// Physical seats, configuration, meter age and pacing are distinct answers
// (issue #98): healthy quota can coexist with full seats, a governor cap is not
// a seat, and a seat budget the dispatcher never computed is unknown.
func TestTransportCapacitySeparatesSeatsFromPacing(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour).Unix()
	fresh := func(weekly float64) quota {
		return quota{ok: true, at: now.Add(-time.Minute),
			five: RateLimit{UsedPct: 10, ResetsAt: later}, seven: RateLimit{UsedPct: weekly, ResetsAt: later}}
	}
	gov := Gov{MaxActive: 2, WeeklyCeiling: 92, SampleInterval: "90s"}
	for _, tc := range []struct {
		name                     string
		transport                string
		caps, running            map[string]int
		q                        quota
		limits                   UnmeteredTransportLimits
		capacity, capacityReason string
		maxActive, active, cap   int // -1 is null
		pacing, pacingReason     string
	}{
		{"seat free, quota clear", "claude", map[string]int{"claude": 2}, map[string]int{"claude": 1}, fresh(37), nil,
			"free", "", 2, 1, 2, "clear", ""},
		{"healthy quota, every seat occupied", "codex", map[string]int{"codex": 2}, map[string]int{"codex": 2}, fresh(37), nil,
			"full", "", 2, 2, 2, "clear", ""},
		{"governor cap below free seats", "claude", map[string]int{"claude": 1}, map[string]int{"claude": 1}, fresh(37), nil,
			"free", "", 2, 1, 1, "limited", "governor-pacing"},
		{"weekly ceiling pause with free seats", "claude", map[string]int{"claude": 0}, map[string]int{}, fresh(95), nil,
			"free", "", 2, 0, 0, "limited", "weekly-ceiling"},
		{"stale meter is unknown pacing, not seats", "codex", map[string]int{"codex": 2}, map[string]int{},
			quota{ok: true, at: now.Add(-time.Hour), five: RateLimit{UsedPct: 10, ResetsAt: later}}, nil,
			"free", "", 2, 0, 2, "unknown", "meter-stale"},
		{"no target names the transport", "codex", map[string]int{"claude": 2}, map[string]int{"codex": 1}, quota{}, nil,
			"unknown", "seat-budget-missing", -1, 1, -1, "unknown", "meter-unread"},
		{"agy without configured seats", "agy", map[string]int{"agy": 0}, map[string]int{}, quota{}, nil,
			"disabled", "seats-not-configured", 0, 0, -1, "unmetered", ""},
		{"agy seat occupied", "agy", map[string]int{"agy": 1}, map[string]int{"agy": 1}, quota{},
			UnmeteredTransportLimits{"agy": {MaxActive: 1}}, "full", "", 1, 1, 1, "unmetered", ""},
		{"agy seats without a target", "agy", map[string]int{}, map[string]int{}, quota{},
			UnmeteredTransportLimits{"agy": {MaxActive: 1}}, "unknown", "seat-budget-missing", -1, 0, -1, "unmetered", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Governor: gov, UnmeteredTransports: tc.limits}
			rows := buildTransportCapacity(transportSeats{caps: tc.caps, running: tc.running}, cfg, nil,
				map[string]quota{tc.transport: tc.q}, now)
			var row transportCapacityRow
			for _, r := range rows {
				if r.Transport == tc.transport {
					row = r
				}
			}
			got := []any{row.Capacity, capacityText(row.CapacityReason), capacityCount(row.MaxActive), capacityCount(row.Active),
				capacityCount(row.AdmissionCap), row.Pacing, capacityText(row.PacingReason)}
			want := []any{tc.capacity, tc.capacityReason, tc.maxActive, tc.active, tc.cap, tc.pacing, tc.pacingReason}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("row %v, want %v", got, want)
			}
		})
	}
}

func TestOpenCodeCapacityFromProviderPools(t *testing.T) {
	for _, tc := range []struct {
		name, capacity, reason string
		cfg                    OpenCodeConfig
		pools                  []openCodeProviderPool
	}{
		{"no providers", "disabled", "seats-not-configured", OpenCodeConfig{}, []openCodeProviderPool{}},
		{"zero seats", "disabled", "seats-not-configured", OpenCodeConfig{Providers: map[string]OpenCodeProvider{"fixture": {0}}},
			[]openCodeProviderPool{{"fixture", 0, 0}}},
		{"one pool free", "free", "", OpenCodeConfig{Providers: map[string]OpenCodeProvider{"a": {1}, "b": {2}}},
			[]openCodeProviderPool{{"a", 1, 1}, {"b", 2, 1}}},
		{"every pool occupied", "full", "", OpenCodeConfig{Providers: map[string]OpenCodeProvider{"a": {1}}},
			[]openCodeProviderPool{{"a", 1, 1}}},
		{"unreadable configuration", "unknown", "seats-config-invalid",
			OpenCodeConfig{Providers: map[string]OpenCodeProvider{"Bad Provider": {1}}}, []openCodeProviderPool{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := openCodeCapacity(tc.cfg, tc.pools)
			if row.Capacity != tc.capacity || capacityText(row.CapacityReason) != tc.reason || row.MaxActive != nil || row.Active != nil || row.AdmissionCap != nil {
				t.Fatalf("row %+v, want %s/%s with per-pool counts only", row, tc.capacity, tc.reason)
			}
		})
	}
}

// The tick publishes seats from the same accounting admission uses: occupying
// the last seat flips both the availability row and the capacity row, and
// freeing it flips both back. The fresh 37% meter never stands in for a seat.
func TestAdmissionBudgetPublishesCapacityBesideAvailability(t *testing.T) {
	root := privateTestRoot(t)
	now := time.Now()
	cfg := &Config{Matrix: MatrixConfig{ReceiptRoot: root, TransportCapacity: true},
		Governor: Gov{Enabled: true, MaxActive: 1, WeeklyCeiling: 92, SampleInterval: "90s"},
		Targets:  []Target{{Label: "fixture", Repo: "example/fixture", Agents: []string{"codex"}}}}
	c := &Coord{cfg: cfg, st: &State{Jobs: map[int]*Job{}}}
	c.gov.q = map[string]quota{"codex": {ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()},
		seven: RateLimit{UsedPct: 37, ResetsAt: now.Add(time.Hour).Unix()}}}
	read := func() (transportAvailabilitySnapshot, []byte) {
		t.Helper()
		var got transportAvailabilitySnapshot
		if err := readPrivateActionJSON(transportAvailabilityPath(root), &got); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(transportAvailabilityPath(root))
		if err != nil {
			t.Fatal(err)
		}
		return got, raw
	}
	codex := func(s transportAvailabilitySnapshot) (transportAvailabilityRow, transportCapacityRow) {
		return s.Transports[1], s.TransportCapacity[1]
	}

	c.st.Jobs[7] = &Job{Issue: 7, Agent: "codex"}
	c.admissionBudget(map[int]agentRef{})
	full, raw := read()
	avail, seat := codex(full)
	if avail.Available || capacityText(avail.Reason) != availabilityNoCapacity || seat.Capacity != capacityFull ||
		capacityCount(seat.Active) != 1 || capacityCount(seat.MaxActive) != 1 || seat.Pacing != pacingClear {
		t.Fatalf("occupied seat: availability %+v capacity %+v", avail, seat)
	}
	// Claude has no target: unknown, never zero seats or a borrowed quota.
	if claude := full.TransportCapacity[0]; claude.Capacity != capacityUnknown || capacityText(claude.CapacityReason) != capacitySeatBudgetMissing || claude.MaxActive != nil {
		t.Fatalf("claude %+v", claude)
	}
	var fields struct {
		TransportCapacity []map[string]json.RawMessage `json:"transportCapacity"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields.TransportCapacity) != len(matrixTransports) {
		t.Fatalf("published capacity %s (%v)", raw, err)
	}
	for i, row := range fields.TransportCapacity {
		keys := make([]string, 0, len(row))
		for key := range row {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		want := []string{"active", "admissionCap", "capacity", "capacityReason", "maxActive", "pacing", "pacingReason", "transport"}
		if !reflect.DeepEqual(keys, want) || string(row["transport"]) != `"`+matrixTransports[i]+`"` {
			t.Fatalf("row %d fields %v", i, keys) // nullable fields are explicit nulls, never omitted
		}
	}

	delete(c.st.Jobs, 7)
	c.admissionBudget(map[int]agentRef{})
	freed, _ := read()
	avail, seat = codex(freed)
	if !avail.Available || avail.Reason != nil || seat.Capacity != capacityFree || capacityCount(seat.Active) != 0 {
		t.Fatalf("freed seat: availability %+v capacity %+v", avail, seat)
	}

	c.cfg.Matrix.TransportCapacity = false
	c.admissionBudget(map[int]agentRef{})
	if legacy, raw := read(); legacy.TransportCapacity != nil || bytes.Contains(raw, []byte("transportCapacity")) {
		t.Fatalf("reader-first switch off still published capacity: %s", raw)
	}
}

// Whatever the cause, a transport with no free seat is never offered, and a
// ceiling pause keeps its quota reason on the availability row.
func TestTransportCapacityAgreesWithAvailability(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour).Unix()
	cfg := &Config{Governor: Gov{MaxActive: 2, WeeklyCeiling: 92, SampleInterval: "90s"},
		UnmeteredTransports: UnmeteredTransportLimits{"agy": {MaxActive: 1}}}
	for _, weekly := range []float64{10, 95} {
		for _, running := range []int{0, 1, 2} {
			for _, cap := range []int{0, 1, 2} {
				q := quota{ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: later}, seven: RateLimit{UsedPct: weekly, ResetsAt: later}}
				quotas := map[string]quota{"claude": q, "codex": q}
				caps := map[string]int{"claude": cap, "codex": cap, "agy": 1}
				run := map[string]int{"claude": running, "codex": running, "agy": running}
				budget := map[string]int{}
				for a, c := range caps {
					budget[a] = c - run[a]
				}
				snapshot := buildTransportAvailability(budget, quotas, now, 90*time.Second, 92, time.Minute, cfg.UnmeteredTransports, nil)
				seats := buildTransportCapacity(transportSeats{caps: caps, running: run}, cfg, nil, quotas, now)
				for i, row := range snapshot.Transports[:3] {
					seat := seats[i]
					if seat.Capacity != capacityFree && row.Available {
						t.Fatalf("weekly %v running %d cap %d: %s offered with %s seats", weekly, running, cap, row.Transport, seat.Capacity)
					}
					if seat.Pacing == pacingLimited && capacityText(seat.PacingReason) == availabilityWeeklyCeiling && capacityText(row.Reason) != availabilityWeeklyCeiling {
						t.Fatalf("weekly %v running %d cap %d: %s ceiling reads as %q", weekly, running, cap, row.Transport, capacityText(row.Reason))
					}
				}
			}
		}
	}
}
