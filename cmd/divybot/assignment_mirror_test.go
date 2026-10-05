package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assignmentFixture loads cfg through loadConfig and puts a fake gh on PATH that
// records every call and reports one open issue assigned to the bot in any
// repository it is asked about.
func assignmentFixture(t *testing.T, cfg string) (*Coord, func() []string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "divybot.json")
	if os.WriteFile(path, []byte(cfg), 0600) != nil {
		t.Fatal("fixture config")
	}
	loaded, err := loadConfig(path)
	if err != nil {
		t.Fatalf("fixture config rejected: %v", err)
	}
	bin := filepath.Join(root, "bin")
	calls := filepath.Join(root, "gh.calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$GH_CALLS"
case "$1 $2" in
"issue list") echo '[]' ;;
"issue create") echo 'https://github.invalid/fixture-owner/inbox/issues/1' ;;
api*) echo '{"items":[{"node_id":"I_fixture","number":7,"title":"fixture","body":"","html_url":"https://github.invalid/fixture","user":{"login":"fixture-author"}}]}' ;;
*) exit 1 ;;
esac
`
	if os.MkdirAll(bin, 0700) != nil || os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700) != nil {
		t.Fatal("fixture gh")
	}
	t.Setenv("GH_CALLS", calls)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &Coord{cfg: loaded}, func() []string {
		b, _ := os.ReadFile(calls)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

func TestAssignmentTickNeverMirrorsTargetWithoutOptIn(t *testing.T) {
	for name, field := range map[string]string{"absent": "", "false": `, "mirror_assignments": false`} {
		t.Run(name, func(t *testing.T) {
			c, calls := assignmentFixture(t, `{"inbox": "fixture-owner/inbox", "bot_login": "fixture-bot",
				"targets": [{"label": "work", "repo": "fixture-owner/work"`+field+`}]}`)
			if c.cfg.Targets[0].MirrorAssignments {
				t.Fatal("mirror_assignments must default to false")
			}
			c.assignmentTick(context.Background())
			if got := calls(); len(got) != 1 || got[0] != "" {
				t.Fatalf("target without mirror_assignments reached gh: %q", got)
			}
		})
	}
}

func TestAssignmentTickMirrorsOnlyOptedInTargets(t *testing.T) {
	c, calls := assignmentFixture(t, `{"inbox": "fixture-owner/inbox", "bot_login": "fixture-bot",
		"targets": [
			{"label": "quiet", "repo": "fixture-owner/quiet"},
			{"label": "work", "repo": "fixture-owner/work", "mirror_assignments": true}
		]}`)
	c.assignmentTick(context.Background())
	var searched, created []string
	for _, call := range calls() {
		switch {
		case strings.HasPrefix(call, "api "):
			searched = append(searched, call)
		case strings.HasPrefix(call, "issue create "):
			created = append(created, call)
		}
	}
	if len(searched) != 1 || !strings.Contains(searched[0], "repo:fixture-owner/work+") {
		t.Fatalf("searched %q, want only the opted-in target", searched)
	}
	if len(created) != 1 || !strings.Contains(created[0], "--repo fixture-owner/inbox --label work ") ||
		!strings.Contains(created[0], "[fixture-owner/work#7]") {
		t.Fatalf("created %q, want one inbox mirror for the opted-in target", created)
	}
}

// A supplied mirror_assignments must be a JSON boolean in both config readers.
// null is the case encoding/json lets through into a bool as false.
var nonBooleanMirrorAssignments = []string{`null`, `"yes"`, `1`, `{}`, `[]`}

func mirrorAssignmentsConfig(value string) string {
	return `{"inbox": "fixture-owner/inbox", "targets": [{"label": "ok", "repo": "fixture-owner/ok", "mirror_assignments": true},
		{"label": "work", "repo": "fixture-owner/work", "mirror_assignments": ` + value + `}]}`
}

func TestLoadConfigMirrorAssignmentsMustBeBoolean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "divybot.json")
	for _, value := range nonBooleanMirrorAssignments {
		if os.WriteFile(path, []byte(mirrorAssignmentsConfig(value)), 0600) != nil {
			t.Fatal("fixture config")
		}
		if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "mirror_assignments") {
			t.Fatalf("mirror_assignments %s accepted or not named: %v", value, err)
		}
	}
	if os.WriteFile(path, []byte(mirrorAssignmentsConfig(`null`)), 0600) != nil {
		t.Fatal("fixture config")
	}
	if _, err := loadConfig(path); err == nil || err.Error() != "targets[1].mirror_assignments must be a JSON boolean" {
		t.Fatalf("null mirror_assignments diagnostic = %v", err)
	}
	for _, value := range []string{`true`, `false`} {
		if os.WriteFile(path, []byte(mirrorAssignmentsConfig(value)), 0600) != nil {
			t.Fatal("fixture config")
		}
		if _, err := loadConfig(path); err != nil {
			t.Fatalf("mirror_assignments %s rejected: %v", value, err)
		}
	}
}

func TestMatrixConfigMirrorAssignmentsMustBeBoolean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "divybot.json")
	for _, value := range nonBooleanMirrorAssignments {
		if os.WriteFile(path, []byte(mirrorAssignmentsConfig(value)), 0600) != nil {
			t.Fatal("fixture config")
		}
		if _, _, p := readMatrixConfigFile(path); len(p) != 1 || p[0] != (matrixConfigProblem{"targets[1].mirror_assignments", "boolean-required"}) {
			t.Fatalf("matrix reader mirror_assignments %s: %v", value, p)
		}
		var out, diagnostic bytes.Buffer
		if code := invokeMatrixConfigCLI([]string{"validate", "-config", path}, &out, &diagnostic); code != 2 ||
			!strings.Contains(out.String()+diagnostic.String(), "targets[1].mirror_assignments") {
			t.Fatalf("matrix validate mirror_assignments %s: exit %d %s%s", value, code, out.String(), diagnostic.String())
		}
	}
	for _, value := range []string{`true`, `false`} {
		if os.WriteFile(path, []byte(mirrorAssignmentsConfig(value)), 0600) != nil {
			t.Fatal("fixture config")
		}
		if _, _, p := readMatrixConfigFile(path); len(p) != 0 {
			t.Fatalf("matrix reader rejected mirror_assignments %s: %v", value, p)
		}
	}
}
