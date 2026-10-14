package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTickRequiresExplicitLaunchIntent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		labels  []string
		body    string
		refused bool
	}{
		{"plain", nil, "Filed work", false},
		{"routing-labels", []string{"orchid", "herdr"}, "Filed work", false},
		{"body-is-not-a-comment", []string{"orchid"}, "/swarm\ntier: feature\nrole: implementation", false},
		{"launch-label-keeps-validation", []string{"orchid", "harness"}, "Filed work", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTestRoot(t)
			bin := t.TempDir()
			calls := filepath.Join(bin, "calls")
			var labels []map[string]string
			for _, label := range tc.labels {
				labels = append(labels, map[string]string{"name": label})
			}
			rows, err := json.Marshal([]map[string]any{{
				"id": "fixture-issue", "number": 7, "title": "Filed work", "body": tc.body, "labels": labels,
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("INTENT_ISSUES", string(rows))
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shq(calls) + "\n" +
				"case \"$1 $2\" in\n'issue list') printf '%s\\n' \"$INTENT_ISSUES\";;\n*) printf '[]\\n';;\nesac\n"
			if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			c := &Coord{
				cfg: &Config{Inbox: "example/inbox", Targets: []Target{{Label: "orchid", Repo: "example/project"}},
					Matrix: MatrixConfig{ReceiptRoot: root}},
				st: loadState(filepath.Join(root, "state.json")),
			}
			c.tick(context.Background())
			c.tick(context.Background())
			commands, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			posted := strings.Count(string(commands), "issue comment 7 ")
			_, err = os.Stat(launchStatePath(root, c.cfg.Inbox, Issue{ID: "fixture-issue"}))
			if tc.refused {
				if posted != 1 || err != nil || len(c.st.MatrixNotices) != 1 {
					t.Fatal("explicit launch no longer runs the existing deduplicated refusal path")
				}
			} else if posted != 0 || !os.IsNotExist(err) || len(c.st.MatrixNotices) != 0 {
				t.Fatal("a plain issue produced a refusal, comment or reader-facing launch record")
			}
			if len(c.st.Jobs) != 0 {
				t.Fatal("fixture unexpectedly launched or adopted an agent")
			}
		})
	}
}

func TestTickKeepsVerifiedSwarmCommentWithoutLaunchLabel(t *testing.T) {
	c, f, _ := sourceFixture(t)
	c.cfg.Matrix.ReceiptRoot = privateTestRoot(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	at := startSource(t, c)
	const key = 3100000030
	f.post(sourceRepo, 2063, key, ownerGitHubID, sourceTrigger, at)
	c.tick(context.Background())
	if c.st.MatrixNotices[key] == "" {
		t.Fatal("verified comment did not enter existing launch validation")
	}
	binding, ok := c.sourceBinding(key)
	if !ok || contains(binding.issue(c).Labels, "harness") {
		t.Fatal("fixture did not exercise an unlabelled source comment")
	}
	for _, reply := range binding.Outbox {
		if strings.Contains(reply.Body, "source-grant-missing") {
			return // The source binding still requires the existing matrix grant.
		}
	}
	t.Fatal("existing source grant validation was bypassed")
}
