package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// Every way a transport can be unavailable, in admission's precedence order.
// The operator words in refusal details are unchanged; the 5h and weekly
// ceilings now have their own codes (atelier-cockpit#392 part 2).
func TestTransportAvailabilityReasons(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	sample := 90 * time.Second
	later := now.Add(time.Hour).Unix()
	fresh := func(five, seven RateLimit) quota {
		return quota{ok: true, at: now.Add(-time.Minute), five: five, seven: seven}
	}
	under := RateLimit{UsedPct: 10, ResetsAt: later}
	over := RateLimit{UsedPct: 95, ResetsAt: later}
	for _, tc := range []struct {
		name      string
		budget    int
		q         quota
		ceiling   float64
		reason    string
		condition string
	}{
		{"available", 1, fresh(under, under), 92, "", ""},
		{"a quota verdict names itself before a nonpositive budget", 0, quota{}, 0, "meter-unread", "absent"},
		{"a governor ceiling pause is not physical capacity", 0, fresh(under, over), 92, "weekly-ceiling", "over ceiling"},
		{"no seat with a fresh meter under the ceiling", 0, fresh(under, under), 92, "no-capacity", "blocked by capacity"},
		{"meter never read", 1, quota{}, 92, "meter-unread", "absent"},
		{"meter published no window", 1, fresh(RateLimit{}, RateLimit{}), 92, "meter-unread", "absent"},
		{"meter older than three samples", 1, quota{ok: true, at: now.Add(-4 * sample), five: under, seven: under}, 92, "meter-stale", "stale"},
		{"meter from the future", 1, quota{ok: true, at: now.Add(time.Minute), five: under, seven: under}, 92, "meter-stale", "stale"},
		{"ceiling misconfigured", 1, fresh(under, under), 0, "ceiling-misconfigured", "over ceiling"},
		{"window reset passed", 1, fresh(RateLimit{UsedPct: 10, ResetsAt: now.Unix()}, under), 92, "window-expired", "expired"},
		{"5h window over the ceiling", 1, fresh(over, under), 92, "5h-ceiling", "over ceiling"},
		{"weekly window over the ceiling", 1, fresh(under, over), 92, "weekly-ceiling", "over ceiling"},
		{"weekly only published, over", 1, fresh(RateLimit{}, over), 92, "weekly-ceiling", "over ceiling"},
		{"both over: the 5h window is checked first", 1, fresh(over, over), 92, "5h-ceiling", "over ceiling"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := transportAvailabilityReason(tc.budget, tc.q, now, sample, tc.ceiling); got != tc.reason {
				t.Fatalf("reason %q, want %q", got, tc.reason)
			}
			if got := transportQuotaCondition(tc.budget, tc.q, now, sample, tc.ceiling); got != tc.condition {
				t.Fatalf("admission condition %q, want %q", got, tc.condition)
			}
		})
	}
}

func TestTransportAvailabilitySnapshotMatchesAdmission(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 123456789, time.UTC)
	later := now.Add(time.Hour).Unix()
	quotas := map[string]quota{
		"claude": {ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: later}, seven: RateLimit{UsedPct: 95, ResetsAt: later}},
		"codex":  {ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: later}, seven: RateLimit{UsedPct: 10, ResetsAt: later}},
	}
	budget := map[string]int{"claude": 2, "codex": 1, "agy": 0}
	snapshot := buildTransportAvailability(budget, quotas, now, 90*time.Second, 92, time.Minute, nil, nil)
	// Millisecond UTC, whatever the clock's precision: the contract reads at most three digits.
	if snapshot.SchemaVersion != 1 || snapshot.ObservedAt != "2026-09-30T12:00:00.123Z" || snapshot.ValidUntil != "2026-09-30T12:01:00.123Z" {
		t.Fatalf("snapshot header %+v", snapshot)
	}
	want := map[string]string{"claude": "weekly-ceiling", "codex": "", "agy": "no-capacity", "opencode": "no-capacity"}
	var order []string
	for _, row := range snapshot.Transports {
		order = append(order, row.Transport)
		reason := ""
		if row.Reason != nil {
			reason = *row.Reason
		}
		if row.Available != (reason == "") || reason != want[row.Transport] {
			t.Fatalf("%s: available=%v reason=%q, want reason %q", row.Transport, row.Available, reason, want[row.Transport])
		}
		// The published answer is admission's answer.
		admitted := budget[row.Transport] > 0
		if row.Transport != "opencode" && row.Transport != "agy" {
			admitted = transportQuotaCondition(budget[row.Transport], quotas[row.Transport], now, 90*time.Second, 92) == ""
		}
		if admitted != row.Available {
			t.Fatalf("%s: snapshot says available=%v, admission says %v", row.Transport, row.Available, admitted)
		}
	}
	if !reflect.DeepEqual(order, matrixTransports) {
		t.Fatalf("transports %v, want every matrix transport in order %v", order, matrixTransports)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"observedAt", "openCodeProviderPools", "schemaVersion", "transports", "validUntil"}) {
		t.Fatalf("snapshot fields %v", keys)
	}
	var rows []map[string]any
	if err := json.Unmarshal(fields["transports"], &rows); err != nil || len(rows) != 4 || len(rows[0]) != 3 || rows[1]["reason"] != nil {
		t.Fatalf("rows %s (%v)", fields["transports"], err)
	}
}

func TestTransportAvailabilityPublishedPrivately(t *testing.T) {
	root := privateTestRoot(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	var chowned []string
	owner := &receiptOwner{uid: 1000, gid: 1000,
		chown: func(path string, _, _ int) error { chowned = append(chowned, filepath.Base(path)); return nil },
		sync:  func(string) error { return nil }}
	first := buildTransportAvailability(map[string]int{"claude": 1}, nil, now, 90*time.Second, 92, time.Minute, nil, nil)
	if err := publishTransportAvailability(root, owner, first); err != nil {
		t.Fatal(err)
	}
	path := transportAvailabilityPath(root)
	if st, err := os.Stat(filepath.Dir(path)); err != nil || st.Mode().Perm() != 0700 {
		t.Fatal("governance directory is not owner-only", err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("snapshot is not owner-only", err)
	}
	if len(chowned) != 2 || chowned[0] != "governance" {
		t.Fatalf("owner transfer %v, want the new directory and the file", chowned)
	}
	second := buildTransportAvailability(map[string]int{"codex": 1}, nil, now.Add(30*time.Second), 90*time.Second, 92, time.Minute, nil, nil)
	if err := publishTransportAvailability(root, owner, second); err != nil {
		t.Fatal(err)
	}
	var got transportAvailabilitySnapshot
	if err := readPrivateActionJSON(path, &got); err != nil || !reflect.DeepEqual(got, second) {
		t.Fatalf("snapshot not replaced: %+v (%v)", got, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
	// A root that is not private is never written to.
	public := t.TempDir()
	if err := os.Chmod(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := publishTransportAvailability(public, nil, first); err == nil {
		t.Fatal("published into a non-private root")
	}
	if _, err := os.Stat(transportAvailabilityPath(public)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a non-private root received a snapshot")
	}
}

// A tick that cannot publish removes the previous snapshot rather than leave
// an old decision in place; a dry run never writes.
func TestTransportAvailabilityFailedPublishClears(t *testing.T) {
	root := privateTestRoot(t)
	path := transportAvailabilityPath(root)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	c := &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: root}}}
	c.gov.q = map[string]quota{}
	c.publishTransportAvailability(map[string]int{"claude": 1}, transportSeats{}, now, nil)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("snapshot not published", err)
	}
	uid := -1
	c.cfg.Matrix.ReceiptOwnerUID = &uid // An invalid owner makes the publish fail.
	c.publishTransportAvailability(map[string]int{"claude": 1}, transportSeats{}, now.Add(30*time.Second), nil)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed publish kept the previous snapshot")
	}
	c.cfg.Matrix.ReceiptOwnerUID = nil
	c.dry = true
	c.publishTransportAvailability(map[string]int{"claude": 1}, transportSeats{}, now.Add(time.Minute), nil)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a dry run wrote a snapshot")
	}
}

// The tick's admission budget is where the snapshot is published: every tick
// that computes a budget leaves the availability it will act on.
func TestAdmissionBudgetPublishesTransportAvailability(t *testing.T) {
	root := privateTestRoot(t)
	c := &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: root}}, st: &State{Jobs: map[int]*Job{}}}
	c.gov.q = map[string]quota{}
	budget := c.admissionBudget(map[int]agentRef{})
	var got transportAvailabilitySnapshot
	if err := readPrivateActionJSON(transportAvailabilityPath(root), &got); err != nil {
		t.Fatal("admission budget did not publish the snapshot", err)
	}
	if len(got.Transports) != len(matrixTransports) {
		t.Fatalf("snapshot rows %+v", got.Transports)
	}
	for _, row := range got.Transports {
		// No configured account has a budget, so admission would offer nothing. The
		// native meters were never read, which names itself before the budget.
		want := availabilityNoCapacity
		if row.Transport == "claude" || row.Transport == "codex" {
			want = availabilityMeterUnread
		}
		if budget[row.Transport] > 0 || row.Available || row.Reason == nil || *row.Reason != want {
			t.Fatalf("%s: %+v with budget %d", row.Transport, row, budget[row.Transport])
		}
	}
	if got.TransportCapacity != nil {
		t.Fatal("capacity rows published before the reader-first switch", got.TransportCapacity)
	}
}

func TestOpenCodeAvailabilityPublishedFromConfiguredProviderBudget(t *testing.T) {
	for _, mode := range []string{"unconfigured", "free", "occupied", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTestRoot(t)
			c := &Coord{cfg: &Config{Matrix: MatrixConfig{ReceiptRoot: root}}, st: &State{Jobs: map[int]*Job{}}}
			c.gov.q = map[string]quota{}
			if mode != "unconfigured" {
				limit := 1
				if mode == "disabled" {
					limit = 0
				}
				c.cfg.OpenCode = OpenCodeConfig{Providers: map[string]OpenCodeProvider{"fixture-provider": {MaxActive: limit}}}
			}
			if mode == "occupied" {
				// Unbound/unknown native seats consume each configured pool.
				c.st.Jobs[7] = &Job{Issue: 7, Agent: "opencode"}
			}
			budget := c.admissionBudget(nil)
			var got transportAvailabilitySnapshot
			if readPrivateActionJSON(transportAvailabilityPath(root), &got) != nil || len(got.Transports) != 4 {
				t.Fatal("tick did not publish the four-row contract")
			}
			row := got.Transports[3]
			want := mode == "free"
			if row.Transport != "opencode" || row.Available != want || row.Available != (budget["opencode"] > 0) || (want && row.Reason != nil) || (!want && (row.Reason == nil || *row.Reason != availabilityNoCapacity)) {
				t.Fatal("published OpenCode capacity differs from configured admission budget")
			}
		})
	}
}
