package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeNativePasteSummaryGoalOnce(t *testing.T) {
	testOpenCodeFullGoalSpawn(t, "native-summary")
}

func TestOpenCodeNativeTerminalGoalOnce(t *testing.T) {
	testOpenCodeFullGoalSpawn(t, "native-terminal")
}

func TestOpenCodeStoreAppearsAfterFirstObservation(t *testing.T) {
	h, j, calls := openCodeHostFixture(t, "delayed-session")
	prompt := "Synthetic complete brief and helper; no trailing line ending"
	j.OpenCode.ExpectedPromptDigest = shaText([]byte(prompt))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := h.injectOpenCodeGoal(ctx, j, prompt, func() error { return nil }); err != nil {
		t.Fatal("late first native session was not confirmed", err)
	}
	reads, err := os.ReadFile(filepath.Join(h.Home, "session-read-count"))
	if err != nil || string(reads) != "2" || strings.Count(calls(), `"agent", "prompt"`) != 1 || strings.Contains(calls(), "send-keys") {
		t.Fatal("native creation was not re-read once without replay")
	}
}

// Installed 1.18.34 stores the full user text, omits the default variant,
// and emits tool turns before a final stop. Use synthetic identities only.
func TestOpenCode11834StoreContract(t *testing.T) {
	prompt := "Synthetic full brief.\nSynthetic deterministic final-comment helper."
	run := &openCodeRun{Route: openCodeRoute{"fixture-provider", "fixture-model", ""}, Cwd: "/fixture/checkout", SessionID: "ses_fixture", NotBefore: 1000, ExpectedPromptDigest: shaText([]byte(prompt))}
	var record map[string]any
	if json.Unmarshal(fixtureExportPrompt(t, run, prompt), &record) != nil {
		t.Fatal("native store fixture malformed")
	}
	info := record["info"].(map[string]any)
	info["version"] = "1.18.34"
	messages := record["messages"].([]any)
	user := messages[0].(map[string]any)
	delete(user["info"].(map[string]any)["model"].(map[string]any), "variant")
	assistant := messages[1].(map[string]any)
	delete(assistant["info"].(map[string]any), "variant")
	// The generic startup integration does not support OpenCode; the separate
	// stored-message contract can still certify exactly the launched goal.
	startup := nativeStartFixture(t, "ses_fixture")
	startup["agent"].(map[string]any)["agent"] = "opencode"
	startup["agent"].(map[string]any)["agent_session"].(map[string]any)["agent"] = "opencode"
	startup["agent"].(map[string]any)["agent_session"].(map[string]any)["source"] = "herdr:opencode"
	if id, reason := nativeSessionFromStart(fixtureJSON(t, startup), "opencode", "fixture-agent", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); id != "" || reason != nativeUnsupported {
		t.Fatal("unsupported startup contract unexpectedly supplied an identity")
	}
	// Preserve the native multi-assistant tool-turn shape without interpreting
	// shell/API outputs as goal acceptance or GitHub completion.
	toolRaw, _ := json.Marshal(assistant)
	var toolTurn map[string]any
	_ = json.Unmarshal(toolRaw, &toolTurn)
	toolInfo := toolTurn["info"].(map[string]any)
	toolInfo["id"] = "fixture-tool-turn"
	toolInfo["finish"] = "tool-calls"
	for _, p := range toolTurn["parts"].([]any) {
		p.(map[string]any)["messageID"] = "fixture-tool-turn"
	}
	toolTurn["parts"] = append(toolTurn["parts"].([]any), map[string]any{"type": "tool", "sessionID": run.SessionID, "messageID": "fixture-tool-turn", "tool": "read", "state": map[string]any{"status": "completed", "output": "1: # fixture"}})
	record["messages"] = []any{user, toolTurn, assistant}
	raw, _ := json.Marshal(record)
	if confirmed, completed, err := inspectOpenCodeExport(raw, run); !confirmed || !completed || err != nil {
		t.Fatal("supported native store contract rejected", err)
	}
	for _, change := range []string{"content", "terminal-space", "provider", "parent", "stale", "occupant-session"} {
		t.Run(change, func(t *testing.T) {
			var altered map[string]any
			_ = json.Unmarshal(raw, &altered)
			list := altered["messages"].([]any)
			u := list[0].(map[string]any)
			a := list[len(list)-1].(map[string]any)
			copy := *run
			switch change {
			case "content":
				u["parts"].([]any)[0].(map[string]any)["text"] = strings.Replace(prompt, "full", "foreign", 1)
			case "terminal-space":
				u["parts"].([]any)[0].(map[string]any)["text"] = prompt + " "
			case "provider":
				a["info"].(map[string]any)["providerID"] = "foreign"
			case "parent":
				a["info"].(map[string]any)["parentID"] = "foreign"
			case "stale":
				copy.NotBefore = 3000
			case "occupant-session":
				copy.SessionID = "ses_foreign"
			}
			changed, _ := json.Marshal(altered)
			if confirmed, complete, err := inspectOpenCodeExport(changed, &copy); confirmed || complete || err == nil {
				t.Fatal("changed native proof passed exact certification")
			}
		})
	}
}

func TestOpenCodeTerminalCanonicalizationKeepsExactContent(t *testing.T) {
	// Leading/embedded/trailing non-line-ending whitespace belongs to the goal.
	payload := " \tSynthetic exact brief\r\nSynthetic helper and protected whitespace\t "
	for _, ending := range []string{"", "\n", "\r", "\r\n", "\n\n", "\r\n\r\n"} {
		t.Run(strings.ReplaceAll(strings.ReplaceAll(ending, "\n", "LF"), "\r", "CR"), func(t *testing.T) {
			prompt := openCodeFirstPrompt(payload + ending)
			if prompt != strings.ReplaceAll(payload, "\r\n", "\n") {
				t.Fatal("terminal canonicalization changed goal content")
			}
			run := &openCodeRun{Route: openCodeRoute{"fixture-provider", "fixture-model", "high"}, Cwd: "/fixture/checkout", SessionID: "ses_fixture", NotBefore: 1000, ExpectedPromptDigest: shaText([]byte(prompt))}
			if confirmed, completed, err := inspectOpenCodeExport(fixtureExportPrompt(t, run, strings.ReplaceAll(payload, "\r\n", "\n")), run); !confirmed || !completed || err != nil {
				t.Fatal("canonical native terminal text failed exact binding", err)
			}
			for _, foreign := range []string{strings.TrimSpace(payload), strings.Replace(payload, "brief", "foreign", 1), payload + " ", payload + "\n"} {
				if confirmed, completed, err := inspectOpenCodeExport(fixtureExportPrompt(t, run, foreign), run); confirmed || completed || err == nil {
					t.Fatal("changed native content was normalized into acceptance")
				}
			}
		})
	}
}
