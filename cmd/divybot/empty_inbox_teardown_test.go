package main

import (
	"context"
	"testing"
)

func TestTeardownEligibleWhenLastLabelledIssueCloses(t *testing.T) {
	ctx := context.Background()
	checks := 0
	state := func(_ context.Context, n int) string {
		checks++
		if n != 395 {
			t.Fatalf("looked up issue %d, want 395", n)
		}
		return "CLOSED"
	}
	if !teardownEligible(ctx, 395, map[int]bool{}, true, state) {
		t.Fatal("confirmed closed issue stranded when the open set is empty")
	}
	if checks != 1 {
		t.Fatalf("issue state checks = %d, want 1", checks)
	}
	for _, tc := range []struct {
		name   string
		open   map[int]bool
		pollOK bool
		state  string
		want   bool
		checks int
	}{
		{"failed-poll", map[int]bool{}, false, "CLOSED", false, 0},
		{"transient-empty", map[int]bool{}, true, "OPEN", false, 1},
		{"state-unavailable", map[int]bool{}, true, "", false, 1},
		{"still-listed", map[int]bool{395: true}, true, "CLOSED", false, 0},
		{"other-open-issue", map[int]bool{396: true}, true, "OPEN", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := 0
			got := teardownEligible(ctx, 395, tc.open, tc.pollOK, func(context.Context, int) string {
				checks++
				return tc.state
			})
			if got != tc.want || checks != tc.checks {
				t.Fatalf("eligible=%v checks=%d, want %v/%d", got, checks, tc.want, tc.checks)
			}
		})
	}
}
