package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const policyMember int64 = 4242001

// policyGrantFixture is a comment grant the Cockpit installs under a saved launch
// policy for one existing member comment.
func policyGrantFixture(t *testing.T, comment int64, body string) (*ownerNativeGrantStore, ownerNativeInstallRequest, *Issue) {
	t.Helper()
	s, r, is := ownerPortFixture(t, "claude")
	_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
	r.Trigger = ownerNativeCommentTrigger
	r.ExpectedBriefDigest = shaText([]byte(body))
	r.NativeOverride.Route = ownerNativeRoute{Harness: "claude", Provider: "anthropic", Model: "claude-opus-5-5", Effort: "high"}
	r.CommentID, r.AuthorID = comment, policyMember
	r.Policy = &ownerNativePolicyAuthority{PolicyID: "policy-example-project", Revision: "7", Member: policyMember}
	return s, r, is
}

const policyBody = "/swarm\nharness: claude\nrepo: example/project\n\nFix it."

func TestPolicyGrantMatchesOnlyItsCommentAndMember(t *testing.T) {
	s, r, is := policyGrantFixture(t, 3100000070, policyBody)
	digest := r.ExpectedBriefDigest
	if s.commentGrantReady("example/project", 7, is.ID, digest, 3100000070, policyMember) || s.commentGrantNamed("example/project", 3100000070, policyMember) {
		t.Fatal("a policy grant was ready before installation")
	}
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("policy grant not LIVE: %s %s", ack.State, ack.Reason)
	}
	if !s.commentGrantNamed("example/project", 3100000070, policyMember) || !s.commentGrantReady("example/project", 7, is.ID, digest, 3100000070, policyMember) {
		t.Fatal("the member's own comment was not granted")
	}
	for name, ok := range map[string]bool{
		"another comment": s.commentGrantReady("example/project", 7, is.ID, digest, 3100000071, policyMember) || s.commentGrantNamed("example/project", 3100000071, policyMember),
		"another author":  s.commentGrantReady("example/project", 7, is.ID, digest, 3100000070, policyMember+1) || s.commentGrantNamed("example/project", 3100000070, policyMember+1),
		"the owner":       s.commentGrantReady("example/project", 7, is.ID, digest, 3100000070, ownerGitHubID),
		"an edited body":  s.commentGrantReady("example/project", 7, is.ID, shaText([]byte(policyBody+"\nMore.")), 3100000070, policyMember),
		"another repo":    s.commentGrantNamed("example/other", 3100000070, policyMember),
		"another issue":   s.commentGrantReady("example/project", 7, "another-node", digest, 3100000070, policyMember),
	} {
		if ok {
			t.Fatalf("a policy grant matched %s", name)
		}
	}
	binding := func(key int) Issue {
		return Issue{ID: is.ID, Number: key, Title: is.Title, Body: policyBody, Labels: []string{"fixture-target"}, Source: &issueSource{Repo: "example/project", Number: 7}}
	}
	// Admission is the twin fence: another comment with the same body is refused.
	if _, err := s.matrixForIssue(binding(3100000071), "example/project"); err == nil {
		t.Fatal("admission accepted a policy grant for another comment")
	}
	if _, err := s.matrixForIssue(binding(3100000070), "example/project"); err != nil {
		t.Fatalf("the granted comment was not admitted: %v", err)
	}
}

func TestOwnerGrantNeverMatchesAMemberComment(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
	r.Trigger = ownerNativeCommentTrigger
	r.ExpectedBriefDigest = shaText([]byte(policyBody))
	r.NativeOverride.Route = ownerNativeRoute{Harness: "claude", Provider: "anthropic", Model: "claude-opus-5-5", Effort: "high"}
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("owner grant not LIVE: %s %s", ack.State, ack.Reason)
	}
	if s.commentGrantReady("example/project", 7, is.ID, r.ExpectedBriefDigest, 3100000072, policyMember) || s.commentGrantNamed("example/project", 3100000072, policyMember) {
		t.Fatal("an owner grant matched a member's comment")
	}
	if !s.commentGrantReady("example/project", 7, is.ID, r.ExpectedBriefDigest, 3100000072, ownerGitHubID) {
		t.Fatal("the owner path changed")
	}
}

func TestPolicyGrantInstallRefusals(t *testing.T) {
	for _, name := range []string{"member-not-author", "no-comment", "no-trigger", "author-without-comment", "blank-policy"} {
		t.Run(name, func(t *testing.T) {
			s, r, _ := policyGrantFixture(t, 3100000073, policyBody)
			switch name {
			case "member-not-author":
				r.Policy.Member = policyMember + 1
			case "no-comment":
				r.CommentID, r.AuthorID = 0, 0
			case "no-trigger":
				r.Trigger = ""
			case "author-without-comment":
				r.Policy, r.CommentID = nil, 0
			case "blank-policy":
				r.Policy.Revision = ""
			}
			if ack := s.install(context.Background(), r); ack.State == "LIVE" {
				t.Fatal("a malformed policy grant was installed")
			}
			if records, _ := os.ReadDir(filepath.Join(s.options.StoreRoot, "records")); len(records) != 0 {
				t.Fatal("a refused policy grant published authority")
			}
		})
	}
}

func TestPolicyAndOwnerApprovalsNeverShareADigest(t *testing.T) {
	s, r, _ := policyGrantFixture(t, 3100000074, policyBody)
	policy, err := s.approval(r)
	if err != nil {
		t.Fatal(err)
	}
	owner := r
	owner.Policy = nil
	plain, err := s.approval(owner)
	if err != nil || plain == policy {
		t.Fatalf("policy and owner approvals share a digest: %v", err)
	}
	// An operator file must name the policy as the authorizer of a policy grant;
	// the owner's name never stands in for it.
	write := func(authorizer string) {
		raw, _ := json.Marshal(ownerNativeApproval{SchemaVersion: 1, Authorizer: authorizer, Inbox: s.cfg.Inbox, MatrixRevision: s.cfg.Matrix.Revision, TargetRevision: s.cfg.Matrix.TargetRevisions[r.Target], Request: r})
		if os.WriteFile(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"), raw, 0600) != nil {
			t.Fatal("approval file")
		}
	}
	write("eric")
	if _, err := s.approval(r); err == nil {
		t.Fatal("an owner-authorized file approved a policy grant")
	}
	write("policy")
	if _, err := s.approval(r); err != nil {
		t.Fatalf("a policy-authorized file was refused: %v", err)
	}
}

// End to end through the source feed: no grant, ignored; a grant for another
// comment or an edited body, never launched; a policy grant for the member's own
// comment, bound for launch even though it arrives after the comment was read.
func TestSourcePolicyGrantTriggersOnlyTheGrantedMemberComment(t *testing.T) {
	s, r, is := policyGrantFixture(t, 3100000080, policyBody)
	c := &Coord{cfg: s.cfg, st: loadState(filepath.Join(t.TempDir(), "state.json")), ownerGrants: s}
	f := newFakeSource()
	c.source = f
	f.issues["example/project#7"] = sourceIssueView{NodeID: is.ID, Title: is.Title, State: "OPEN"}
	c.sourceTick(context.Background())
	at := c.st.SourceCursors["example/project"].Add(time.Second)
	f.post("example/project", 7, 3100000080, policyMember, policyBody, at)        // member, granted below
	f.post("example/project", 7, 3100000081, policyMember+9, policyBody, at)      // outsider, never granted
	f.post("example/project", 7, 3100000082, policyMember, policyBody, at)        // member, grant names another comment
	f.post("example/project", 7, 3100000083, policyMember, policyBody+"\nX.", at) // member, granted for another body
	c.sourceTick(context.Background())
	if len(c.st.SourceBindings) != 0 {
		t.Fatalf("an ungranted non-owner comment was bound: %d", len(c.st.SourceBindings))
	}
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("policy grant not LIVE: %s %s", ack.State, ack.Reason)
	}
	c.sourceTick(context.Background())
	c.sourceTick(context.Background())
	if b := c.st.SourceBindings[3100000080]; b == nil || b.Closed != "" {
		t.Fatalf("the granted member comment was not bound for launch: %+v", b)
	}
	if b := c.st.SourceBindings[3100000081]; b != nil {
		t.Fatal("an outsider's comment was bound")
	}
	if b := c.st.SourceBindings[3100000082]; b != nil {
		t.Fatal("a comment with no grant naming it was bound")
	}
	if b := c.st.SourceBindings[3100000083]; b != nil {
		t.Fatal("a comment with no grant naming it was bound")
	}
}

// A grant names the comment, but the body the member's comment carries is not
// the body the policy decision read: refused, never launched.
func TestSourcePolicyGrantRefusesAnotherBody(t *testing.T) {
	s, r, is := policyGrantFixture(t, 3100000090, policyBody)
	c := &Coord{cfg: s.cfg, st: loadState(filepath.Join(t.TempDir(), "state.json")), ownerGrants: s}
	f := newFakeSource()
	c.source = f
	f.issues["example/project#7"] = sourceIssueView{NodeID: is.ID, Title: is.Title, State: "OPEN"}
	c.sourceTick(context.Background())
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("policy grant not LIVE: %s %s", ack.State, ack.Reason)
	}
	f.post("example/project", 7, 3100000090, policyMember, policyBody+"\nEdited.", c.st.SourceCursors["example/project"].Add(time.Second))
	c.sourceTick(context.Background())
	if b := c.st.SourceBindings[3100000090]; b == nil || b.Closed != "refused" {
		t.Fatalf("a comment whose body differs from its grant was not refused: %+v", b)
	}
}
