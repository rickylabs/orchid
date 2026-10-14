package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Delivery is presentation only. Alerts never establish lifecycle, alter the
// native state or replace existing external notifications. Two private keys per
// job bound deduplication; reserve before sending to avoid crash-time replays.
func (c *Coord) emitHerdrAlert(ctx context.Context, j *Job, kind, reason string) {
	if c.dry || j == nil || (kind != "native-block" && kind != "policy") {
		return
	}
	if kind == "policy" {
		if _, ok := matrixReasons[reason]; !ok && reason != "completion-unproven" {
			return
		}
	}
	h, ok := c.hosts[j.Host]
	if !ok {
		return
	}
	h = h.forJob(j)
	check, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	receipt, id, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, j.Agent)
	if err != nil || id == "" || receipt.dispatch.Host != j.Host || receipt.dispatch.Location == nil || receipt.dispatch.Location.PaneID != j.Pane || receipt.dispatch.Location.WorkspaceID != j.Workspace {
		return
	}
	agents, err := c.actionList(check, h)
	if err != nil {
		return
	}
	var a AgentInfo
	count := 0
	for _, current := range agents {
		if current.PaneID == j.Pane || current.WorkspaceID == j.Workspace {
			count++
			if !jobOccupant(current, j) {
				return
			}
			a = current
		}
	}
	if count != 1 || a.TerminalID == "" {
		return
	}
	var native struct{ Source, Agent, Kind, Value string }
	if json.Unmarshal(a.AgentSession, &native) != nil || !lifecycleSource(native.Source, j.Agent) || native.Agent != j.Agent || native.Kind != "id" || native.Value != id {
		return
	}
	if kind == "native-block" {
		if a.AgentStatus != "blocked" {
			return
		}
		if a.Agent == "claude" || a.Agent == "codex" {
			if !a.VendorEvent.matches(a) || vendorEventState(a.VendorEvent.Event) != "blocked" {
				return
			}
		} else if !a.NativeLifecycle {
			return
		}
	}
	key := shaText([]byte(fmt.Sprintf("%s/%s/%s/%d/%s", j.DispatchKey, id, a.TerminalID, a.StateChangeSeq, reason)))
	if kind == "native-block" && a.VendorEvent != nil {
		key = shaText([]byte(fmt.Sprintf("%s/%s/%d", key, a.VendorEvent.Session, a.VendorEvent.Seq)))
	}
	c.st.mu.Lock()
	if j.HerdrAlerts[kind] == key {
		c.st.mu.Unlock()
		return
	}
	if j.HerdrAlerts == nil {
		j.HerdrAlerts = map[string]string{}
	}
	previous := j.HerdrAlerts[kind]
	j.HerdrAlerts[kind] = key
	if c.st.saveLocked() != nil {
		j.HerdrAlerts[kind] = previous
		c.st.mu.Unlock()
		return
	}
	c.st.mu.Unlock()
	title, body := "Agent needs input", "A verified native run is blocked. Inspect its assigned workspace."
	if kind == "policy" {
		title, body = "Orchid policy needs attention", "A verified run requires policy inspection: "+reason
	}
	if c.actions.notify != nil {
		_ = c.actions.notify(check, h, title, body)
		return
	}
	_, _ = h.herdr(check, "notification", "show", title, "--body", body, "--sound", "request")
}
