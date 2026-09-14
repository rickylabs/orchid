package main

import (
	"testing"
	"time"
)

// Invented values only. No account identities or live meter observations.
var headroomTestNow = time.Unix(1800000000, 0)

const headroomTestCeiling = 92.0

func TestQuotaHasHeadroomNoPublishedWindows(t *testing.T) {
	if quotaHasHeadroom(quota{}, headroomTestNow, headroomTestCeiling) {
		t.Fatal("empty meter must refuse: no published window is not entitlement")
	}
}

func TestQuotaHasHeadroomPublishedWindowAtOrOverCeiling(t *testing.T) {
	for _, used := range []float64{headroomTestCeiling, headroomTestCeiling + 1} {
		window := RateLimit{UsedPct: used, ResetsAt: headroomTestNow.Add(time.Hour).Unix()}
		for _, q := range []quota{{five: window}, {seven: window}} {
			if quotaHasHeadroom(q, headroomTestNow, headroomTestCeiling) {
				t.Errorf("published window at or over ceiling must refuse (used=%g)", used)
			}
		}
	}
}

func TestQuotaHasHeadroomExpiredPublishedWindow(t *testing.T) {
	for _, reset := range []int64{headroomTestNow.Add(-time.Second).Unix(), headroomTestNow.Unix()} {
		window := RateLimit{UsedPct: 10, ResetsAt: reset}
		for _, q := range []quota{{five: window}, {seven: window}} {
			if quotaHasHeadroom(q, headroomTestNow, headroomTestCeiling) {
				t.Error("published window expired or resetting now must refuse")
			}
		}
	}
}

func TestQuotaHasHeadroomOnePublishedWindow(t *testing.T) {
	window := RateLimit{UsedPct: 10, ResetsAt: headroomTestNow.Add(time.Hour).Unix()}
	for _, q := range []quota{{five: window}, {seven: window}} {
		if !quotaHasHeadroom(q, headroomTestNow, headroomTestCeiling) {
			t.Error("one fresh published window under ceiling must admit")
		}
	}
}

func TestQuotaHasHeadroomAllPublishedWindowsMustHaveHeadroom(t *testing.T) {
	healthy := RateLimit{UsedPct: 10, ResetsAt: headroomTestNow.Add(time.Hour).Unix()}
	exhausted := RateLimit{UsedPct: headroomTestCeiling + 1, ResetsAt: headroomTestNow.Add(time.Hour).Unix()}
	for _, q := range []quota{{five: healthy, seven: exhausted}, {five: exhausted, seven: healthy}} {
		if quotaHasHeadroom(q, headroomTestNow, headroomTestCeiling) {
			t.Error("two published windows with one over ceiling must refuse in either order")
		}
	}
}
