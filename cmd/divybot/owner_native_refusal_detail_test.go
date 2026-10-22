package main

import (
	"strings"
	"testing"
)

// A refused or unknown owner grant carries its plain why to the cockpit, bounded
// to one printable line, so the owner sees the cause and not only the reason.
func TestOwnerNativeRefusalCarriesItsWhy(t *testing.T) {
	s := &ownerNativeGrantStore{}
	ack := s.refuse(7, "op", "REFUSED", "override-invalid", "the issue cannot\nlaunch")
	if ack.Reason != "override-invalid" || ack.Detail != "the issue cannot launch" {
		t.Fatalf("ack = %+v", ack)
	}
	long := s.refuse(7, "op", "UNKNOWN", "override-invalid", strings.Repeat("x", 400))
	if len(long.Detail) != ownerNativeDetailLimit {
		t.Fatalf("detail not bounded: %d", len(long.Detail))
	}
	got := ownerNativeDetail("caf\u00e9 \t ok")
	for _, b := range []byte(got) {
		if b < 0x20 || b > 0x7e {
			t.Fatalf("non-printable byte kept: %q", got)
		}
	}
	if !strings.HasPrefix(got, "caf") || !strings.HasSuffix(got, "ok") {
		t.Fatalf("printable text lost: %q", got)
	}
}

// A cockpit target that does not allow the owner's agent names the agent, the
// repository and what the cockpit allows; anything else is the route's own conflict.
func TestOwnerNativeAgentWhyNamesTheRefusedAgent(t *testing.T) {
	targets := []Target{
		{Repo: "example/project", Agents: []string{"claude"}, cockpit: true},
		{Repo: "example/static", Agents: []string{"claude"}},
	}
	codex := &ownerNativeOverride{Route: ownerNativeRoute{Harness: "codex-run"}}
	want := "codex is not allowed on example/project (the cockpit allows: claude)"
	if got := ownerNativeAgentWhy(targets, "example/project", codex); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	claude := &ownerNativeOverride{Route: ownerNativeRoute{Harness: "claude"}}
	for _, tc := range []struct {
		repo     string
		override *ownerNativeOverride
	}{{"example/project", claude}, {"example/static", codex}, {"example/project", nil}} {
		if got := ownerNativeAgentWhy(targets, tc.repo, tc.override); got != "" {
			t.Fatalf("%s: got %q, want no agent why", tc.repo, got)
		}
	}
}
