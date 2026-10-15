package main

import (
	"encoding/json"
	"strings"
)

// Server session selection is distinct from an agent's native conversation.
// Missing scope in old private records means the existing default session.
type herdrScope struct {
	Session string `json:"session"`
	Machine string `json:"machine,omitempty"`
}

// Role routing changes only the endpoint after physical host selection. A
// populated map must name dispatch, so it cannot fall back to a manual role.
func (h Host) forHerdrRole(role string) (Host, bool) {
	if len(h.HerdrRoles) == 0 {
		return h, role == "dispatch"
	}
	scope, ok := h.HerdrRoles[role]
	if !ok {
		return Host{}, false
	}
	h.HerdrSession, h.HerdrMachine = scope.Session, scope.Machine
	return h, true
}

func (h Host) validHerdrRoles() bool {
	if len(h.HerdrRoles) == 0 {
		return true
	}
	if _, ok := h.HerdrRoles["dispatch"]; !ok {
		return false
	}
	seen := map[string]bool{}
	for role, scope := range h.HerdrRoles {
		if role != "dispatch" && role != "operator" && role != "diagnostics" {
			return false
		}
		if strings.TrimSpace(scope.Session) == "" || strings.TrimSpace(scope.Session) != scope.Session ||
			strings.ContainsAny(scope.Session+scope.Machine, "\x00\r\n") || (scope.Machine != "" && h.SSH == "") {
			return false
		}
		// Saved-machine selectors can be aliases of the same account. Session
		// names must differ even when two role selectors appear different.
		if seen[scope.Session] {
			return false
		}
		seen[scope.Session] = true
	}
	return true
}

func herdrSession(name string) string {
	if name == "" {
		return "default"
	}
	return name
}
func (h Host) herdrScope() *herdrScope {
	return &herdrScope{Session: herdrSession(h.HerdrSession), Machine: h.HerdrMachine}
}
func sameHerdrScope(a, b *herdrScope) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil {
		return herdrSession(b.Session) == "default" && b.Machine == ""
	}
	if b == nil {
		return herdrSession(a.Session) == "default" && a.Machine == ""
	}
	return herdrSession(a.Session) == herdrSession(b.Session) && a.Machine == b.Machine
}
func jobOccupant(a AgentInfo, j *Job) bool {
	return j != nil && a.Agent == j.Agent && a.PaneID == j.Pane && a.WorkspaceID == j.Workspace && a.Name == j.Label &&
		(j.TerminalID == "" || a.TerminalID == j.TerminalID) && (j.Workdir == "" || a.Cwd == j.Workdir) && sameHerdrScope(a.HerdrScope, j.HerdrScope) && jobNativeOccupant(a, j)
}
func jobRefMatches(ref agentRef, j *Job) bool {
	return j != nil && ref.Host == j.Host && ref.Pane == j.Pane && ref.Workspace == j.Workspace && accountKey(ref.Agent) == accountKey(j.Agent) &&
		sameHerdrScope(ref.HerdrScope, j.HerdrScope) && (j.TerminalID == "" || ref.TerminalID == j.TerminalID)
}
func scopedAgentResponse(raw json.RawMessage, j *Job) bool {
	if j.TerminalID == "" {
		return true
	}
	var r struct {
		Agent AgentInfo `json:"agent"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return false
	}
	r.Agent.HerdrScope = j.HerdrScope
	return jobOccupant(r.Agent, j)
}

func (h Host) observerKey() string {
	if herdrSession(h.HerdrSession) == "default" && h.HerdrMachine == "" {
		return h.Name
	}
	return h.Name + "\x00" + herdrSession(h.HerdrSession) + "\x00" + h.HerdrMachine
}
func (c *Coord) herdrObserverHosts() map[string]Host {
	hosts := map[string]Host{}
	for _, h := range c.hosts {
		hosts[h.observerKey()] = h
		for role := range h.HerdrRoles {
			if selected, ok := h.forHerdrRole(role); ok {
				hosts[selected.observerKey()] = selected
			}
		}
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	for _, j := range c.st.Jobs {
		if h, ok := c.hosts[j.Host]; ok {
			h = h.forJob(j)
			hosts[h.observerKey()] = h
		}
	}
	return hosts
}

func jobNativeOccupant(a AgentInfo, j *Job) bool {
	if j.NativeSessionID == "" || (j.Agent != "claude" && j.Agent != "codex") {
		return true
	}
	// Before the first turn, the canonical TUI can have a certified stored
	// resume proof while its SessionStart hook is deferred. Reuse that native
	// path with the exact recorded scope/terminal and refuse conflicting hooks.
	if rcCodex(j) && j.RemoteControl.IdentitySource == "codex-native-status" && j.RemoteControl.NativeSessionID == j.NativeSessionID && codexResumeProven(j.RemoteControl) {
		raw, err := json.Marshal(map[string]any{"type": "agent_info", "agent": a})
		return err == nil && remoteStatusOccupant(raw, "codex", j.Label, j.RemoteControl.Cwd, j.NativeSessionID, &dispatchLocation{PaneID: j.Pane, WorkspaceID: j.Workspace})
	}
	var session struct{ Source, Agent, Kind, Value string }
	return json.Unmarshal(a.AgentSession, &session) == nil && lifecycleSource(session.Source, j.Agent) && session.Agent == j.Agent && session.Kind == "id" && session.Value == j.NativeSessionID
}
