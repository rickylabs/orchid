package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func terminalRetentionFixture(t *testing.T, count, limit int) *Coord {
	t.Helper()
	c := nativeRunFixture(t)
	j := c.st.Jobs[701]
	j.NativeSessionID = "fixture-701"
	template := *c.st.nativeRunRecordLocked(701, j, c.cfg.Inbox)
	c.st.NativeTerminals = map[string]nativeTerminalFact{}
	c.st.NativeTerminalKeys = map[int]string{}
	c.st.LaunchBlocks = map[int]launchBlock{}
	base := time.Now().Add(-24 * time.Hour)
	for i := 0; i < count; i++ {
		n := 701 + i
		r := template
		r.Key, r.Label = strconv.Itoa(n), "codex-"+strconv.Itoa(n)
		r.DispatchKey, r.NativeSessionID = fmt.Sprintf("%064x", n), fmt.Sprintf("fixture-%d", n)
		r.AgentID, r.AssignmentID = actionOpaque("agent", "orchid-"+r.DispatchKey), actionOpaque("assignment", "orchid-"+r.DispatchKey)
		r.SourceCommentID, r.Phase = int64(n), "completed"
		r.CompletedAt, r.ObservedAt = stamp(base.Add(time.Duration(i)*time.Second)), stamp(base.Add(time.Duration(i)*time.Second))
		c.st.NativeTerminals[r.DispatchKey] = nativeTerminalFact{Record: r, Reason: "ended_unobserved"}
		c.st.NativeTerminalKeys[n] = r.DispatchKey
		c.st.LaunchBlocks[n] = launchBlock{Reason: "assignment_ended", Notified: true}
	}
	// Seed an older on-disk state directly: its first dispatcher save must bound it.
	body, _ := json.Marshal(c.st)
	if err := os.WriteFile(c.st.path, body, 0600); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"inbox": c.cfg.Inbox, "state_file": c.st.path}
	if limit != 0 {
		config["native_terminal_limit"] = limit
	}
	body, _ = json.Marshal(config)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return newCoord(cfg)
}

func TestNativeTerminalRetentionBoundsOldestFactsAndBlocks(t *testing.T) {
	for _, tc := range []struct{ count, limit int }{{6, 3}, {20000, 1000}} {
		t.Run(fmt.Sprintf("%d/%d", tc.count, tc.limit), func(t *testing.T) {
			configured := tc.limit
			if tc.limit == 1000 {
				configured = 0 // exercise the default, not only a custom setting
			}
			c := terminalRetentionFixture(t, tc.count, configured)
			if tc.count == 6 {
				// An older assignment on the same coordinate is not the tracked job.
				key := fmt.Sprintf("%064x", 702)
				old := c.st.NativeTerminals[key]
				old.Record.Key, old.Record.Label, old.Record.SourceCommentID = "701", "codex-701", 701
				c.st.NativeTerminals[key] = old
				delete(c.st.NativeTerminalKeys, 702)
				delete(c.st.LaunchBlocks, 702)
			}
			c.st.LaunchBlocks[999] = launchBlock{Reason: "cockpit_stop", Notified: true}
			if err := c.st.save(); err != nil {
				t.Fatal(err)
			}
			s := loadState(c.st.path)
			if len(s.NativeTerminals) != tc.limit || len(s.NativeTerminalKeys) != tc.limit {
				t.Fatal("terminal facts or index exceeded the retention limit")
			}
			// Oldest 701 is pinned by its tracked job; retain the newest remaining facts.
			for n := 701; n < 701+tc.count; n++ {
				want := n == 701 || n >= 701+tc.count-(tc.limit-1)
				_, fact := s.NativeTerminals[fmt.Sprintf("%064x", n)]
				_, index := s.NativeTerminalKeys[n]
				if fact != want || index != want || (n != 999 && (s.LaunchBlocks[n].Reason == "assignment_ended") != want) {
					t.Fatalf("fact/index/block retention disagreed or evicted newest/tracked key %d", n)
				}
			}
			if s.LaunchBlocks[999].Reason != "cockpit_stop" {
				t.Fatal("terminal retention removed another launch fence")
			}
		})
	}
}

func TestNativeTerminalRetentionPinsTrackedJobsAboveLimit(t *testing.T) {
	c := terminalRetentionFixture(t, 4, 2)
	for n := 702; n <= 704; n++ {
		c.st.Jobs[n] = &Job{Issue: n, DispatchKey: fmt.Sprintf("%064x", n)}
	}
	if err := c.st.save(); err != nil || len(c.st.NativeTerminals) != 4 {
		t.Fatal("tracked terminal facts were pruned to force the limit", err)
	}
	delete(c.st.Jobs, 701)
	if err := c.st.save(); err != nil || len(c.st.NativeTerminals) != 3 || c.st.NativeTerminalKeys[701] != "" || c.st.LaunchBlocks[701].Reason != "" {
		t.Fatal("released tracked fact did not expire with its index and block", err)
	}
	delete(c.st.Jobs, 702)
	if err := c.st.save(); err != nil || len(c.st.NativeTerminals) != 2 {
		t.Fatal("retention did not converge after cleanup released jobs", err)
	}
}

func TestNativeTerminalRetentionSaveFence(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(strconv.FormatBool(afterRename), func(t *testing.T) {
			c := terminalRetentionFixture(t, 6, 3)
			fail := func(*os.File) error { return errors.New("fixture sync loss") }
			c.st.fs = &stateFS{syncFile: fail}
			want := 6
			if afterRename {
				c.st.fs = &stateFS{syncDir: fail}
				want = 3
			}
			if c.st.save() == nil || len(c.st.NativeTerminals) != want || len(c.st.NativeTerminalKeys) != want || len(c.st.LaunchBlocks) != want || len(loadState(c.st.path).NativeTerminals) != want {
				t.Fatal("failed retention commit left memory and the committed pathname inconsistent")
			}
			c.st.fs = nil
			if err := c.st.save(); err != nil || len(loadState(c.st.path).NativeTerminals) != 3 {
				t.Fatal("retention retry failed to persist the bound", err)
			}
		})
	}
}

func TestNativeTerminalRetentionConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, value := range []string{"0", "-1", "null", "1.5", `"2"`} {
		if err := os.WriteFile(path, []byte(`{"native_terminal_limit":`+value+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil {
			t.Fatal("invalid retention configuration accepted", value)
		}
	}
	_ = os.WriteFile(path, []byte(`{}`), 0600)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(cfg)
	var fields map[string]any
	_ = json.Unmarshal(body, &fields)
	if fields["native_terminal_limit"] != float64(1000) {
		t.Fatal("missing configuration did not resolve to the bounded default")
	}
}
