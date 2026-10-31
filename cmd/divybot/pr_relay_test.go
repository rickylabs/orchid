package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffConflictMergesBaseWithoutRewritingHistory(t *testing.T) {
	for _, base := range []string{"main", "release/stable", ""} {
		t.Run(base, func(t *testing.T) {
			view := &PRView{BaseRefName: base, HeadOID: "head-one", Mergeable: "CONFLICTING"}
			track := &tracker{HeadOID: view.HeadOID} // base advanced without a new PR head
			lines, changed := diff(track, view, "bot")
			if !changed || len(lines) != 1 {
				t.Fatalf("first conflict must relay once: changed=%v lines=%q", changed, lines)
			}
			line := strings.ToLower(lines[0])
			if !strings.Contains(line, "merge") || !strings.Contains(line, "push normally") ||
				strings.Contains(line, "rebase") || strings.Contains(line, "force-push") {
				t.Fatalf("conflict must request a merge and normal push: %q", lines[0])
			}
			if base != "" && !strings.Contains(lines[0], base) {
				t.Fatalf("conflict omits actual base %q: %q", base, lines[0])
			}
			if base == "" && strings.Contains(line, "origin/main") {
				t.Fatalf("missing base must not assume main: %q", lines[0])
			}
			if lines, changed := diff(track, view, "bot"); changed || len(lines) != 0 {
				t.Fatalf("unchanged conflict relayed again: %q", lines)
			}
			view.HeadOID = "head-two"
			if lines, changed := diff(track, view, "bot"); !changed || len(lines) != 1 {
				t.Fatalf("new conflicting head was not relayed: %q", lines)
			}
			view.Mergeable = "UNKNOWN"
			diff(track, view, "bot")
			view.Mergeable = "CONFLICTING"
			if lines, changed := diff(track, view, "bot"); changed || len(lines) != 0 {
				t.Fatalf("unknown mergeability reset conflict tracking: %q", lines)
			}
			view.Mergeable = "MERGEABLE"
			diff(track, view, "bot")
			view.Mergeable = "CONFLICTING"
			if lines, changed := diff(track, view, "bot"); !changed || len(lines) != 1 {
				t.Fatalf("resolved then recurring conflict was not relayed: %q", lines)
			}
		})
	}
}

func TestGHPRViewReadsBaseRefName(t *testing.T) {
	bin := t.TempDir()
	// Refuse a query that forgets the field: fixtures must cover the request too.
	script := `#!/bin/sh
case "$*" in
  "pr view 7 --repo example/project --json "*) ;;
  *) exit 1 ;;
esac
case ",$7," in
  *,baseRefName,*) ;;
  *) exit 1 ;;
esac
printf '%s\n' '{"number":7,"baseRefName":"release/stable","headRefOid":"head-one","mergeable":"CONFLICTING","state":"OPEN"}'
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	view, err := ghPRView(context.Background(), "example/project", 7)
	if err != nil {
		t.Fatal(err)
	}
	if view.BaseRefName != "release/stable" {
		t.Fatalf("baseRefName lost during conversion: %q", view.BaseRefName)
	}
	lines, changed := diff(&tracker{}, view, "bot")
	if !changed || len(lines) != 1 || !strings.Contains(lines[0], "release/stable") {
		t.Fatalf("GitHub base did not reach relay: %q", lines)
	}
}
