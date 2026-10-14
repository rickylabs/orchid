package main

import "strings"

const promptFixtureGoal = "Read the staged fixture assignment in full and carry it out."
const promptFixtureEmpty = "OpenAI Codex (v0.159.3)\n› Anything interesting on the docket?\n"

func promptFixture() promptSnapshot {
	return promptSnapshot{Agent: AgentInfo{TerminalID: "fixture-terminal", NativeLifecycle: false, Agent: "codex", Name: "fixture-agent", PaneID: "w1:p1", WorkspaceID: "w1", Cwd: "/fixture/project", AgentStatus: "done", StateChangeSeq: 7, InteractiveReady: true}, Screen: promptFixtureEmpty, Stable: true}
}

func consumedPrompt(sent string) promptSnapshot {
	s := promptFixture()
	s.Agent.AgentStatus = "working"
	s.Agent.StateChangeSeq++
	s.Screen = "› " + sent + "\n• Reading the fixture assignment\n"
	return s
}

func realisticPromptGoal() string {
	body := strings.Repeat("Read the fixture implementation and its evidence before changing behavior.\n", 80)
	return renderGoal("fixture/inbox", "fixture/project", "fixture", "Fixture long goal", body, "/fixture/project", "fixture-branch", "", 42)
}
