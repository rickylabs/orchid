package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// A new receipt behind unchanged Herdr counters is still a new native event:
// the fleet's receipt sequence moves the cursor, and the backstop covers a
// receipt the fleet did not read.
func TestVendorAuthNewReceiptBehindUnchangedCounters(t *testing.T) {
	f := newAuthFixture(t)
	f.hook(t, map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "Bash"})
	fleet := func() uint64 {
		a, err := f.host.agentInfoOf(context.Background(), f.j.Pane)
		if err != nil {
			t.Fatal(err)
		}
		return agentVendorSeq(a)
	}
	f.ref.VendorSeq = fleet()
	if f.ref.VendorSeq == 0 {
		t.Fatal("fixture receipt not bound")
	}
	f.c.superviseVendorAuth(context.Background(), f.host, f.j, f.ref, true, false)
	if f.j.AuthSignal != "" || authObservationDue(f.j, f.ref, time.Now()) {
		t.Fatal("a non-auth receipt was acted on or left pending")
	}
	f.hook(t, map[string]any{"hook_event_name": "StopFailure", "error": "authentication_failed"})
	f.c.superviseVendorAuth(context.Background(), f.host, f.j, f.ref, true, false)
	if f.j.AuthSignal != "" {
		t.Fatal("fixture: the unchanged cursor should defer to the fleet or backstop")
	}
	f.ref.VendorSeq = fleet() // the next fleet read binds the new receipt
	f.c.superviseVendorAuth(context.Background(), f.host, f.j, f.ref, true, false)
	if !digestPattern.MatchString(f.j.AuthSignal) {
		t.Fatal("a new native receipt behind unchanged Herdr counters was missed")
	}
	// The fleet does not read a receipt for a working occupant; the backstop does.
	g := newAuthFixture(t)
	g.hook(t, map[string]any{"hook_event_name": "UserPromptSubmit"})
	g.c.superviseVendorAuth(context.Background(), g.host, g.j, g.ref, true, false)
	g.hook(t, map[string]any{"hook_event_name": "StopFailure", "error": "authentication_failed"})
	g.j.authObservedAt = time.Now().Add(-authObservationBackstop)
	g.c.superviseVendorAuth(context.Background(), g.host, g.j, g.ref, true, false)
	if !digestPattern.MatchString(g.j.AuthSignal) {
		t.Fatal("the backstop did not read a receipt the fleet never bound")
	}
}

// The fleet pass carries the bound receipt sequence into the supervision ref.
func TestFleetStatusCarriesVendorCursor(t *testing.T) {
	c, j, _, _, _ := completionFixture(t)
	a := completionAgent(j)
	a.TerminalID, a.Cwd = "fixture-terminal", "/fixture/issue-7"
	a.AgentSession = json.RawMessage(`{"agent":"codex","kind":"id","source":"herdr:codex","value":"thread-fixture"}`)
	a.VendorEvent = &vendorLifecycleEvent{Agent: a.Agent, Name: a.Name, Pane: a.PaneID, Workspace: a.WorkspaceID, Cwd: a.Cwd,
		Terminal: a.TerminalID, Session: "thread-fixture", Event: "Stop", Seq: 77}
	observed := c.herdrObserverHosts()
	c.fleet = herdrFleetFixture(t, observed)
	publish := func() map[int]agentRef {
		for key := range observed {
			c.fleet.publish(key, []AgentInfo{a}, true)
		}
		status, _ := c.fleetStatus(context.Background())
		return status
	}
	if ref := publish()[j.Issue]; !jobRefMatches(ref, j) || ref.VendorSeq != 77 {
		t.Fatalf("fleet ref lost the native receipt cursor: %+v", ref)
	}
	a.VendorEvent.Terminal = "foreign-terminal"
	if ref := publish()[j.Issue]; !jobRefMatches(ref, j) || ref.VendorSeq != 0 {
		t.Fatal("an unbound receipt moved the native cursor")
	}
}
