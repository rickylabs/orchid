package main

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

type ownerChoiceFixture struct {
	Choices        []struct{ Harness, Native, Provider, Model, Effort string }
	PlacementLabel string
}

func readOwnerChoices(t *testing.T) ownerChoiceFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/owner-launch-choice.json")
	var fixture ownerChoiceFixture
	if err != nil || json.Unmarshal(raw, &fixture) != nil || len(fixture.Choices) == 0 {
		t.Fatal("owner fixture unavailable", err)
	}
	return fixture
}

func TestOwnerArbitraryChoiceReachesLaunch(t *testing.T) {
	fixture := readOwnerChoices(t)
	for _, choice := range fixture.Choices {
		t.Run(choice.Harness, func(t *testing.T) {
			root := privateTestRoot(t)
			cfg := &Config{Inbox: "example/inbox", Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: strings.Repeat("b", 40), TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)}}}
			is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic owner work", Body: "/swarm\ntier: feature\nrole: implementation\nharness: " + choice.Harness + "\nmodel: " + choice.Model + "\neffort: " + choice.Effort + "\n\nSynthetic task"}
			grant := &ownerNativeOverride{Authorizer: "eric", Rationale: "Synthetic owner selection", Route: ownerNativeRoute{Harness: choice.Native, Provider: choice.Provider, Model: choice.Model, Effort: choice.Effort}}
			cfg.Matrix.Grants = []MatrixGrant{{IssueID: is.ID, Repo: "example/project", BriefDigest: briefDigest(is), NativeOverride: grant}}
			c := &Coord{cfg: cfg}
			launches := 0
			deps := matrixAttemptDeps{
				read: func(context.Context, string, string, string) (string, error) {
					return "| `routing` | matrix `implementation` row |", nil
				},
				resolve: func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) {
					t.Fatal("owner model consulted the matrix")
					return matrixRoute{}, errMatrix
				},
				host:    func(Target, string) (Host, bool) { return Host{Name: fixture.PlacementLabel}, true },
				persist: persistMatrixReceipt,
				launch: func(_ context.Context, _ int, _ Issue, _ Host, agent string, o Overrides, receipt *durableMatrixReceipt) error {
					launches++
					if agent != choice.Harness || o.Model != choice.Model || o.Effort != choice.Effort {
						t.Fatal("owner route changed", agent, o.Model, o.Effort)
					}
					command, err := buildAgentCmd(agent, o)
					if err != nil || (choice.Harness != "opencode" && !strings.Contains(command, choice.Effort)) || !receipt.claim(command) {
						t.Fatal("effort did not reach CLI argv", err)
					}
					if choice.Native == "codex" && !strings.Contains(command, shq("model_reasoning_effort="+strconv.Quote(choice.Effort))) {
						t.Fatal("Codex effort configuration missing")
					}
					if receipt.dispatch.MatrixSource != ownerNativeSource {
						t.Fatal("owner route lost provenance")
					}
					return nil
				},
			}
			if _, ok := c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, map[string]int{}, deps); !ok || launches != 1 {
				t.Fatal("owner choice did not reach native launch", launches)
			}
		})
	}
}

func TestOwnerOpenCodeCatalogIsAdvisoryBeforeSeat(t *testing.T) {
	for _, mode := range []string{"missing-model", "missing-variant", "fallback-agent"} {
		t.Run(mode, func(t *testing.T) {
			h, j, calls := openCodeHostFixture(t, mode)
			o := Overrides{Model: j.OpenCode.Route.qualifiedModel(), Effort: readOwnerChoices(t).Choices[3].Effort}
			receipt := registrationReceipt(t, "opencode", o)
			receipt.dispatch.MatrixSource = ownerNativeSource
			pane, workspace, err := h.spawnAgent(context.Background(), j.Label, j.OpenCode.Cwd, nil, "opencode", o, receipt)
			if err != nil || pane != j.Pane || workspace != j.Workspace {
				t.Fatal("catalog withheld owner seat", err)
			}
			if !strings.Contains(calls(), `"bin": "herdr"`) || strings.Contains(calls(), `"models"`) || strings.Contains(calls(), `"debug"`) {
				t.Fatal("owner launch still used catalog admission")
			}
			raw, err := os.ReadFile(j.OpenCode.Cwd + "/.divybot-opencode/state/opencode/model.json")
			var state struct{ Variant map[string]string }
			if err != nil || json.Unmarshal(raw, &state) != nil || state.Variant[o.Model] != o.Effort {
				t.Fatal("requested variant was not staged", err)
			}
		})
	}
}

func TestOwnerPlacementIgnoresDeclaredHarnessAndCapacity(t *testing.T) {
	fixture := readOwnerChoices(t)
	host := Host{Name: fixture.PlacementLabel, Capacity: 0, Agents: []string{"claude"}}
	c := &Coord{st: &State{Jobs: map[int]*Job{}}, hosts: map[string]Host{host.Name: host}}
	if _, ok := c.pickHost(Target{}, "codex"); ok {
		t.Fatal("autonomous placement policy changed")
	}
	selected, ok := c.pickOwnerHost(Target{}, "codex")
	if !ok || selected.Name != host.Name {
		t.Fatal("declared harness/capacity withheld owner attempt")
	}
}
