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
	if a == nil && b == nil {
		return true
	}
	if a == nil {
		return herdrSession(b.Session) == "default"
	}
	if b == nil {
		return herdrSession(a.Session) == "default"
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
	var session struct{ Source, Agent, Kind, Value string }
	return json.Unmarshal(a.AgentSession, &session) == nil && lifecycleSource(session.Source, j.Agent) && session.Agent == j.Agent && session.Kind == "id" && session.Value == j.NativeSessionID
}
