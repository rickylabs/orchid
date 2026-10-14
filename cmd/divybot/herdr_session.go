package main

import "encoding/json"

// Server session selection is distinct from an agent's native conversation.
// Missing scope in old private records means the existing default session.
type herdrScope struct {
	Session string `json:"session"`
	Machine string `json:"machine,omitempty"`
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
	if a == nil {
		a = &herdrScope{}
	}
	if b == nil {
		b = &herdrScope{}
	}
	return herdrSession(a.Session) == herdrSession(b.Session) && a.Machine == b.Machine
}
func jobOccupant(a AgentInfo, j *Job) bool {
	return j != nil && a.Agent == j.Agent && a.PaneID == j.Pane && a.WorkspaceID == j.Workspace && a.Name == j.Label &&
		(j.TerminalID == "" || a.TerminalID == j.TerminalID) && (j.Workdir == "" || a.Cwd == j.Workdir) && sameHerdrScope(a.HerdrScope, j.HerdrScope)
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

func hScope(h Host) *herdrScope { return h.herdrScope() }
