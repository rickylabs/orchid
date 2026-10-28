package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCheckoutBaseGrammarAndStartFence(t *testing.T) {
	for _, body := range []string{"/swarm\nbase:", "/swarm\nbase: main\nbase: other", "/swarm\nbase: main # ignored", "/swarm\nbase: -other", "/swarm\nbase: main~1", "/swarm\nbase: ../main", "/swarm\nbase: main.lock", "/swarm\nbase: refs//heads/main"} {
		if _, err := runBaseRef(parseOverrides(body)); err == nil {
			t.Fatal("invalid or ambiguous explicit base accepted")
		}
	}
	for _, base := range []string{"main", "topic/fix", "refs/heads/trunk", strings.Repeat("a", 40)} {
		if got, err := runBaseRef(parseOverrides("/swarm\nbase: " + base)); err != nil || got != base {
			t.Fatal("valid explicit base lost")
		}
	}
	r := registrationReceipt(t, "claude", Overrides{})
	path := filepath.Join(filepath.Dir(r.file), "binding.json")
	if os.WriteFile(path, goalJSON(map[string]any{"AdmissionRevision": strings.Repeat("b", 40)}), 0600) != nil {
		t.Fatal("fixture")
	}
	sha := strings.Repeat("c", 40)
	if r.recordRunStart(Overrides{}, sha) != nil {
		t.Fatal("verified start not durable")
	}
	if r.recordRunStart(Overrides{}, strings.Repeat("d", 40)) == nil {
		t.Fatal("one launch acquired two start commits")
	}
	if r.recordRunStart(Overrides{Base: "other"}, sha) == nil {
		t.Fatal("one launch acquired two base selections")
	}
	if r.recordRunStart(Overrides{}, "unknown") == nil {
		t.Fatal("unproven commit persisted")
	}
}
