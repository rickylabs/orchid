package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// RUN-453 (harness #504, 2026-09-30): the stranded poke fired on the first idle
// tick after launch and told a read-only brief to "open a PR".
func TestStrandedPokeWaitsFromSpawnAndKeepsTheBrief(t *testing.T) {
	now := time.Date(2026, 9, 30, 7, 18, 34, 0, time.UTC)
	goal := "/swarm\nharness: codex\n\nRead-only proof. Do not open a branch or pull request."
	job := func(spawned, poked time.Time) *Job {
		return &Job{Issue: 504, Pane: "36", Goal: goal, SpawnedAt: spawned, LastPoke: poked}
	}

	// The RUN-453 tick: spawned 70 s ago, never poked, idle. No poke.
	if got := strandedPoke(job(now.Add(-70*time.Second), time.Time{}), "idle", now); got != "" {
		t.Fatalf("poked %q one minute after spawn", got)
	}
	// Idle for more than the interval since spawn: the brief is re-delivered as is.
	got := strandedPoke(job(now.Add(-11*time.Minute), time.Time{}), "idle", now)
	if !strings.HasSuffix(got, goal) {
		t.Fatalf("poke %q does not carry the brief", got)
	}
	if strings.Contains(strings.TrimSuffix(got, goal), "PR") {
		t.Fatalf("poke %q adds a deliverable the brief did not ask for", got)
	}
	// Debounce from the last poke as well.
	if got := strandedPoke(job(now.Add(-time.Hour), now.Add(-5*time.Minute)), "done", now); got != "" {
		t.Fatalf("poked %q five minutes after the last poke", got)
	}
	if got := strandedPoke(job(now.Add(-time.Hour), now.Add(-11*time.Minute)), "done", now); got != "" {
		t.Fatal("completed turn was re-poked")
	}
	// Never while working or blocked, with a PR, or without a pane.
	for _, status := range []string{"working", "blocked", "unknown", "done"} {
		if got := strandedPoke(job(now.Add(-time.Hour), time.Time{}), status, now); got != "" {
			t.Fatalf("poked %q while %s", got, status)
		}
	}
	withPR := job(now.Add(-time.Hour), time.Time{})
	withPR.PR = 12
	noPane := job(now.Add(-time.Hour), time.Time{})
	noPane.Pane = ""
	if strandedPoke(withPR, "idle", now) != "" || strandedPoke(noPane, "idle", now) != "" {
		t.Fatal("poked a job with a PR or without a pane")
	}
}

func TestStrandedPokeIsTheOnlyStrandedSend(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "if poke := strandedPoke(j, ref.Status, time.Now()); !suppressInput && poke != \"\" {") {
		t.Fatal("the stranded tick does not use strandedPoke")
	}
	if strings.Contains(text, "implement the assigned issue fully, then open a PR") {
		t.Fatal("the fixed open-a-PR poke text is back")
	}
}
