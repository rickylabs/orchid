package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An inbox binding as the Cockpit creates it: no target label of its own. The
// trigger label "harness" is also the inbox repository's target label, and it
// is applied only after the grant is installed.
func ownerPortInboxFixture(t *testing.T, title, body, target string) (*ownerNativeGrantStore, ownerNativeInstallRequest, *Issue, *[]string) {
	t.Helper()
	labels := []string{"type:chore", "status:plan"}
	is := &Issue{ID: "binding-node", Number: 604, Title: title, Body: body}
	uid := os.Getuid()
	options := ownerNativePortConfig{Socket: filepath.Join(privateTestRoot(t), "operator.sock"), StoreRoot: privateTestRoot(t), ApprovalRoot: privateTestRoot(t), OperatorUID: &uid}
	cfg := &Config{Inbox: "example/inbox", Targets: []Target{
		{Label: "harness", Repo: "example/inbox"},
		{Label: "netscript", Repo: "example/netscript"},
	}, Matrix: syntheticSource(t)}
	cfg.Matrix.ReceiptRoot = privateTestRoot(t)
	cfg.Matrix.TargetRevisions = map[string]string{"example/inbox": strings.Repeat("c", 40), "example/netscript": strings.Repeat("d", 40)}
	deps := configDeps()
	deps.command = func(context.Context, string, string, []byte, ...string) ([]byte, error) {
		var named []any
		for _, l := range labels {
			named = append(named, map[string]any{"name": l})
		}
		return json.Marshal(map[string]any{"id": is.ID, "number": is.Number, "title": is.Title, "body": is.Body, "state": "OPEN", "labels": named})
	}
	deps.read = func(context.Context, string, string, string) (string, error) {
		return "| `routing` | matrix `implementation` row |", nil
	}
	s, err := newOwnerNativeGrantStore(context.Background(), cfg, options, deps)
	if err != nil {
		t.Fatal("private fixture bootstrap", err)
	}
	r := ownerNativeInstallRequest{SchemaVersion: 1, OperationID: "22222222-2222-4222-8222-222222222222", ApprovalRef: "inbox-approval", IssueNumber: is.Number, ExpectedIssueID: is.ID, Target: target, ExpectedBriefDigest: briefDigest(*is), Tier: "feature", Role: "implementation", Profile: "leaf", NativeOverride: nativeOwnerFixture("claude")}
	ownerPortWriteApproval(t, s, r)
	return s, r, is, &labels
}

const inboxBrief = "/swarm\ntier: feature\nrole: implementation\nprofile: leaf\n"

func installLogged(t *testing.T, s *ownerNativeGrantStore, r ownerNativeInstallRequest) (ownerNativeAck, string) {
	t.Helper()
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(os.Stderr)
	ack := s.install(context.Background(), r)
	return ack, out.String()
}

// An inbox binding for the inbox repository installs although it carries no
// target label yet: the trigger it will receive selects that target, exactly as
// the launch resolves it. Before, every such install was refused.
func TestOwnerNativeInboxGrantResolvesTargetWithTheTrigger(t *testing.T) {
	s, r, _, labels := ownerPortInboxFixture(t, "[example/inbox#575] Picker test", inboxBrief+"\nWork here.", "example/inbox")
	ack, logged := installLogged(t, s, r)
	if ack.State != "LIVE" {
		t.Fatalf("inbox-target binding refused: %s %s: %s", ack.State, ack.Reason, logged)
	}
	// Once the owner's trigger is applied, the grant stays the launch's proof.
	*labels = append(*labels, "harness")
	if got := s.readStatus(context.Background(), r.OperationID); got.State != "LIVE" {
		t.Fatalf("triggered issue invalidated its grant: %s %s", got.State, got.Reason)
	}
}

// harness#604's shape: an old binding for a netscript source with no repo key.
// It launches nowhere (its title names netscript, its label target is the inbox),
// so the install refuses with that reason, whichever target was approved.
func TestOwnerNativeRepoLessSourceBindingRefusesWithTheLaunchReason(t *testing.T) {
	for _, target := range []string{"example/netscript", "example/inbox"} {
		s, r, _, _ := ownerPortInboxFixture(t, "[example/netscript#2076] Witness", inboxBrief+"\nNo-op.", target)
		ack, logged := installLogged(t, s, r)
		if ack.State != "REFUSED" || ack.Reason != "override-invalid" {
			t.Fatalf("%s: repo-less source binding not refused: %s %s", target, ack.State, ack.Reason)
		}
		if !strings.Contains(logged, "does not launch in the approved target (source-repo-mismatch)") {
			t.Fatalf("%s: refusal does not name the launch reason: %s", target, logged)
		}
	}
}

// With its repo key the same binding launches in netscript: that target installs
// and stays valid once triggered, and the inbox target is refused by name.
func TestOwnerNativeRepoKeySelectsTheApprovedTarget(t *testing.T) {
	body := inboxBrief + "repo: example/netscript\n\nFix it."
	s, r, _, labels := ownerPortInboxFixture(t, "[example/netscript#2076] Fix", body, "example/netscript")
	if ack, logged := installLogged(t, s, r); ack.State != "LIVE" {
		t.Fatalf("repo-keyed binding refused: %s %s: %s", ack.State, ack.Reason, logged)
	}
	*labels = append(*labels, "harness")
	if got := s.readStatus(context.Background(), r.OperationID); got.State != "LIVE" {
		t.Fatalf("triggered repo-keyed grant invalidated: %s %s", got.State, got.Reason)
	}
	other, ro, _, _ := ownerPortInboxFixture(t, "[example/netscript#2076] Fix", body, "example/inbox")
	ack, logged := installLogged(t, other, ro)
	if ack.State != "REFUSED" || !strings.Contains(logged, "does not launch in the approved target (resolves to example/netscript)") {
		t.Fatalf("a grant for another target than the launch's was not refused by name: %s %s", ack.State, logged)
	}
}

// A disabled target never installs, even when the issue resolves to it.
func TestOwnerNativeDisabledResolvedTargetRefuses(t *testing.T) {
	s, r, _, _ := ownerPortInboxFixture(t, "[example/inbox#575] Picker test", inboxBrief+"\nWork here.", "example/inbox")
	s.cfg.Targets[0].Disabled = true
	if ack, _ := installLogged(t, s, r); ack.State == "LIVE" {
		t.Fatal("a disabled target installed")
	}
}

// Where no configured target label applies even with the trigger counted, the
// install refuses and says so.
func TestOwnerNativeNoTargetLabelRefuses(t *testing.T) {
	s, r, _, _ := ownerPortInboxFixture(t, "[example/inbox#575] Picker test", inboxBrief+"\nWork here.", "example/inbox")
	s.cfg.Targets[0].Label = "not-the-trigger"
	ack, logged := installLogged(t, s, r)
	if ack.State != "REFUSED" || !strings.Contains(logged, "(no-target-label)") {
		t.Fatalf("an unlabelled issue installed or was refused without its reason: %s %s", ack.State, logged)
	}
}

// targetMatches itself refuses a disabled target the issue resolves to, so no
// caller relies on its own enabled check.
func TestOwnerNativeTargetMatchesRefusesDisabledResolution(t *testing.T) {
	s, _, is, labels := ownerPortInboxFixture(t, "[example/inbox#575] Picker test", inboxBrief+"\nWork here.", "example/inbox")
	if !s.targetMatches(*is, *labels, "example/inbox") {
		t.Fatal("control: the enabled inbox target did not match")
	}
	s.cfg.Targets[0].Disabled = true
	if s.targetMatches(*is, *labels, "example/inbox") {
		t.Fatal("a disabled resolved target matched")
	}
}
