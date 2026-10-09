package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
		return Issue{ID: is.ID, Number: key, Title: is.Title, Body: policyBody, Labels: []string{"fixture-target"}, Source: &issueSource{Repo: "example/project", Number: 7, Author: policyMember}}
	}
	// Admission is the twin fence: another author on the granted comment is refused.
	owner := binding(3100000070)
	owner.Source.Author = ownerGitHubID
	if _, err := s.matrixForIssue(owner, "example/project"); err == nil {
		t.Fatal("admission accepted a policy grant for another author")
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

// The member edits the comment after Orchid kept it and before the grant for
// its first body arrives: the edited comment is read again and never launched.
func TestSourcePolicyRecheckReadsTheCurrentComment(t *testing.T) {
	s, r, is := policyGrantFixture(t, 3100000095, policyBody)
	c := &Coord{cfg: s.cfg, st: loadState(filepath.Join(t.TempDir(), "state.json")), ownerGrants: s}
	f := newFakeSource()
	c.source = f
	f.issues["example/project#7"] = sourceIssueView{NodeID: is.ID, Title: is.Title, State: "OPEN"}
	c.sourceTick(context.Background())
	at := c.st.SourceCursors["example/project"].Add(time.Second)
	f.post("example/project", 7, 3100000095, policyMember, policyBody, at)
	c.sourceTick(context.Background())
	f.mu.Lock()
	edited := f.feed["example/project"][0]
	edited.Body, edited.UpdatedAt = policyBody+"\nEdited.", at.Add(time.Minute)
	f.feed["example/project"][0] = edited
	f.mu.Unlock()
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("policy grant not LIVE: %s %s", ack.State, ack.Reason)
	}
	c.sourceTick(context.Background())
	c.sourceTick(context.Background())
	if b := c.st.SourceBindings[3100000095]; b != nil && b.Closed == "" {
		t.Fatal("an edited comment was bound for launch from its kept copy")
	}
}

// The test hook models owner grants: it never approves a member's comment, even
// when a policy grant names that comment for another body.
func TestSourceGrantHookNeverCombinesAuthorities(t *testing.T) {
	c := &Coord{}
	c.sourceGrant = func(string, int, string, string, int) bool { return true }
	c.sourceNamed = func(string, int64, int64) bool { return true }
	if c.sourceGrantReady("example/project", 7, "node", policyBody, 3100000096, policyMember) {
		t.Fatal("an owner grant and a policy grant combined to approve a member comment")
	}
	if !c.sourceGrantReady("example/project", 7, "node", policyBody, 3100000096, ownerGitHubID) {
		t.Fatal("the owner path changed")
	}
}

// reconsidered reads the comment again as a listing would, and reports whether
// its binding was reconsidered (the issue was read).
func reconsidered(c *Coord, f *fakeSource, id int64, author ...int64) bool {
	f.mu.Lock()
	cm := f.feed["example/project"][0]
	f.mu.Unlock()
	if len(author) > 0 {
		cm.AuthorID = author[0]
	}
	reads := 0
	lookup := func(n int) (sourceIssueView, error) {
		reads++
		v, _, err := f.issue(context.Background(), "example/project", n, "")
		return v, err
	}
	c.considerSourceComment(context.Background(), lookup, "example/project", cm, time.Now().UTC(), c.st.SourceStarts["example/project"])
	return reads > 0
}

// ownerPolicyGrant names the owner's own comment under a saved policy whose
// member is the owner.
func ownerPolicyGrant(t *testing.T, comment int64) (*Coord, *fakeSource, *ownerNativeGrantStore, ownerNativeInstallRequest, time.Time) {
	t.Helper()
	s, r, is := policyGrantFixture(t, comment, policyBody)
	r.AuthorID, r.Policy.Member = ownerGitHubID, ownerGitHubID
	c := &Coord{cfg: s.cfg, st: loadState(filepath.Join(t.TempDir(), "state.json")), ownerGrants: s}
	f := newFakeSource()
	c.source = f
	f.issues["example/project#7"] = sourceIssueView{NodeID: is.ID, Title: is.Title, State: "OPEN"}
	c.sourceTick(context.Background())
	return c, f, s, r, c.st.SourceCursors["example/project"].Add(time.Second)
}

func refusedMarkers(f *fakeSource) int {
	n := 0
	for _, m := range f.markers() {
		if strings.Contains(m, "state=refused") {
			n++
		}
	}
	return n
}

// The owner posts /swarm, Orchid refuses it for want of a grant, and the
// Cockpit's policy then installs a grant naming that comment: it launches, once,
// and the refusal already posted is not repeated.
func TestSourceOwnerCommentLaunchesOnALatePolicyGrant(t *testing.T) {
	c, f, s, r, at := ownerPolicyGrant(t, 3100000100)
	f.post("example/project", 7, 3100000100, ownerGitHubID, policyBody, at)
	c.sourceTick(context.Background())
	if b := c.st.SourceBindings[3100000100]; b == nil || b.Closed != "refused" || b.Refusal != "source-grant-missing" {
		t.Fatalf("the ungranted owner comment was not refused for want of a grant: %+v", b)
	}
	if refusedMarkers(f) != 1 {
		t.Fatalf("refusal not posted once: %v", f.markers())
	}
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("policy grant not LIVE: %s %s", ack.State, ack.Reason)
	}
	c.sourceTick(context.Background())
	c.sourceTick(context.Background())
	b := c.st.SourceBindings[3100000100]
	if b == nil || b.Closed != "" || !b.Regranted || b.Author != ownerGitHubID {
		t.Fatalf("the owner comment was not bound for launch on the late policy grant: %+v", b)
	}
	if refusedMarkers(f) != 1 {
		t.Fatalf("the refusal was posted again: %v", f.markers())
	}
	// Admission accepts the binding the late grant opened.
	is := Issue{ID: b.IssueID, Number: 3100000100, Title: b.Title, Body: b.Body, Labels: []string{"fixture-target"}, Source: &issueSource{Repo: "example/project", Number: 7, Author: b.Author}}
	if _, err := s.matrixForIssue(is, "example/project"); err != nil {
		t.Fatalf("the regranted owner comment was not admitted: %v", err)
	}
}

// A late policy grant reconsiders a refusal once: a grant for another body is
// refused again without a second reply, and the comment is never kept again.
func TestSourceLatePolicyGrantReconsidersOnlyOnce(t *testing.T) {
	c, f, s, r, at := ownerPolicyGrant(t, 3100000101)
	f.post("example/project", 7, 3100000101, ownerGitHubID, policyBody+"\nAnother.", at)
	c.sourceTick(context.Background())
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("policy grant not LIVE: %s %s", ack.State, ack.Reason)
	}
	for range 3 {
		c.sourceTick(context.Background())
	}
	if b := c.st.SourceBindings[3100000101]; b == nil || b.Closed != "refused" || !b.Regranted {
		t.Fatalf("a grant for another body was not refused once more: %+v", b)
	}
	if refusedMarkers(f) != 1 {
		t.Fatalf("the refusal was posted again: %v", f.markers())
	}
	if reconsidered(c, f, 3100000101) {
		t.Fatal("a refusal was reconsidered a second time")
	}
	mem := c.sourceMem()
	mem.mu.Lock()
	kept := len(mem.pending)
	mem.mu.Unlock()
	if kept != 0 {
		t.Fatal("a reconsidered comment was kept for another grant")
	}
}

// Only a policy grant naming this comment and author reopens a refusal: the
// owner's unbound approval and a grant for another author never do, and an
// edit made before the grant is seen.
func TestSourceRefusalReopensOnlyForItsOwnPolicyGrant(t *testing.T) {
	for _, name := range []string{"owner-approval", "another-author", "edited", "closed-issue"} {
		t.Run(name, func(t *testing.T) {
			c, f, s, r, at := ownerPolicyGrant(t, 3100000102)
			if name == "closed-issue" {
				f.issues["example/project#7"] = sourceIssueView{NodeID: f.issues["example/project#7"].NodeID, Title: "t", State: "CLOSED"}
			}
			f.post("example/project", 7, 3100000102, ownerGitHubID, policyBody, at)
			c.sourceTick(context.Background())
			switch name {
			case "closed-issue":
				// Refused for another reason than a missing grant: never reconsidered,
				// even once the issue is open again.
				v := f.issues["example/project#7"]
				v.State = "OPEN"
				f.issues["example/project#7"] = v
			case "owner-approval":
				r.CommentID, r.AuthorID, r.Policy = 0, 0, nil
			case "another-author":
				r.AuthorID, r.Policy.Member = policyMember, policyMember
			case "edited":
				f.mu.Lock()
				edited := f.feed["example/project"][0]
				edited.Body, edited.UpdatedAt = policyBody+"\nEdited.", at.Add(time.Minute)
				f.feed["example/project"][0] = edited
				f.mu.Unlock()
			}
			if ack := s.install(context.Background(), r); ack.State != "LIVE" {
				t.Fatalf("grant not LIVE: %s %s", ack.State, ack.Reason)
			}
			c.sourceTick(context.Background())
			c.sourceTick(context.Background())
			// A later listing that reads the comment again reconsiders nothing either.
			again := []int64{}
			if name == "another-author" {
				again = append(again, policyMember) // a comment read as the grant's author
			}
			if name != "edited" && reconsidered(c, f, 3100000102, again...) {
				t.Fatalf("a listing reconsidered a refusal %s", name)
			}
			b := c.st.SourceBindings[3100000102]
			if b == nil || b.Closed != "refused" {
				t.Fatalf("a refusal was reopened by %s: %+v", name, b)
			}
			if name == "edited" && !b.Regranted {
				t.Fatal("an edited comment's reconsideration was not spent; it would be read every tick")
			}
		})
	}
}

// A restart between the refusal and the late policy grant forgets the kept
// comment; the saved refusal is kept again, so the grant still launches it.
func TestSourceLatePolicyGrantSurvivesARestart(t *testing.T) {
	c, f, s, r, at := ownerPolicyGrant(t, 3100000110)
	f.post("example/project", 7, 3100000110, ownerGitHubID, policyBody, at)
	c.sourceTick(context.Background())
	restarted := &Coord{cfg: c.cfg, st: loadState(c.st.path), ownerGrants: s, source: f}
	restarted.sourceTick(context.Background()) // the listing reads the refused comment again before any grant
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("policy grant not LIVE: %s %s", ack.State, ack.Reason)
	}
	restarted.sourceTick(context.Background())
	restarted.sourceTick(context.Background())
	if b := restarted.st.SourceBindings[3100000110]; b == nil || b.Closed != "" || !b.Regranted {
		t.Fatalf("the late grant was lost across a restart: %+v", b)
	}
	if refusedMarkers(f) != 1 {
		t.Fatalf("the refusal was posted again: %v", f.markers())
	}
}

// A refusal saved before its reason was recorded is read from its one refusal
// reply, delivered or queued; any other or ambiguous refusal stays terminal.
func TestSourceLegacyRefusalReopensOnlyForAMissingGrant(t *testing.T) {
	missing := "refused\x00source-grant-missing\x00"
	for name, want := range map[string]bool{"delivered": true, "queued": true, "closed-issue": false, "not-a-refusal": false, "two-replies": false} {
		t.Run(name, func(t *testing.T) {
			c, f, s, r, at := ownerPolicyGrant(t, 3100000111)
			f.post("example/project", 7, 3100000111, ownerGitHubID, policyBody, at)
			c.sourceTick(context.Background())
			c.st.mu.Lock()
			b := c.st.SourceBindings[3100000111]
			b.Refusal, b.Author, b.Outbox = "", 0, nil
			switch name {
			case "delivered":
				b.Replies = map[string]bool{missing: true}
			case "queued":
				b.Replies, b.Outbox = nil, []sourceReply{{ID: missing, Body: "x"}}
			case "closed-issue":
				b.Replies = map[string]bool{"refused\x00source-issue-closed\x00": true}
			case "not-a-refusal":
				b.Replies = map[string]bool{"stopped\x00source-grant-missing\x00": true}
			case "two-replies":
				b.Replies, b.Outbox = map[string]bool{missing: true}, []sourceReply{{ID: "stopped\x00source-closed\x00", Body: "x"}}
			}
			_ = c.st.saveLocked()
			c.st.mu.Unlock()
			restarted := &Coord{cfg: c.cfg, st: loadState(c.st.path), ownerGrants: s, source: f}
			if ack := s.install(context.Background(), r); ack.State != "LIVE" {
				t.Fatalf("policy grant not LIVE: %s %s", ack.State, ack.Reason)
			}
			restarted.sourceTick(context.Background())
			restarted.sourceTick(context.Background())
			if got := restarted.st.SourceBindings[3100000111]; (got != nil && got.Closed == "") != want {
				t.Fatalf("legacy refusal %s: reopened=%v, want %v: %+v", name, got != nil && got.Closed == "", want, got)
			}
		})
	}
}

// The reconsideration is saved, but the directory sync after the rename fails:
// memory keeps what the state file holds, so the refusal is not reopened again.
func TestSourceRegrantKeptWhenTheSaveFailsAfterRename(t *testing.T) {
	c, f, s, r, at := ownerPolicyGrant(t, 3100000112)
	f.post("example/project", 7, 3100000112, ownerGitHubID, policyBody+"\nAnother.", at)
	c.sourceTick(context.Background())
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("policy grant not LIVE: %s %s", ack.State, ack.Reason)
	}
	c.st.fs = &stateFS{syncDir: func(*os.File) error { return errors.New("injected") }}
	c.sourceTick(context.Background())
	c.st.fs = nil
	if b := c.st.SourceBindings[3100000112]; b == nil || !b.Regranted {
		t.Fatalf("memory rewound a reconsideration the state file holds: %+v", b)
	}
	if reconsidered(c, f, 3100000112) {
		t.Fatal("a refusal was reconsidered a second time after a save failed past its rename")
	}
}
