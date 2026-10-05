package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSource is a fake GitHub for the source-binding surface: comments per
// repository, issues, deletions and every posted reply.
type fakeSource struct {
	mu        sync.Mutex
	feed      map[string][]sourceComment
	issues    map[string]sourceIssueView
	deleted   map[int64]bool
	replies   []fakeReply
	failIssue bool
	failList  bool
	sinces    []string
}

type fakeReply struct {
	repo string
	n    int
	body string
}

func newFakeSource() *fakeSource {
	return &fakeSource{feed: map[string][]sourceComment{}, issues: map[string]sourceIssueView{}, deleted: map[int64]bool{}}
}

func (f *fakeSource) comments(_ context.Context, repo, since string, page int) ([]sourceComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sinces = append(f.sinces, repo+"@"+since)
	if f.failList {
		return nil, errors.New("list down")
	}
	from, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return nil, err
	}
	var out []sourceComment
	for _, c := range f.feed[repo] {
		if !c.UpdatedAt.Before(from) && !f.deleted[c.ID] {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	lo, hi := (page-1)*100, page*100
	if lo >= len(out) {
		return nil, nil
	}
	if hi > len(out) {
		hi = len(out)
	}
	return out[lo:hi], nil
}

func (f *fakeSource) issue(_ context.Context, repo string, n int) (sourceIssueView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failIssue {
		return sourceIssueView{}, errors.New("issue down")
	}
	v, ok := f.issues[fmt.Sprintf("%s#%d", repo, n)]
	if !ok {
		return sourceIssueView{}, errors.New("missing")
	}
	return v, nil
}

func (f *fakeSource) commentExists(_ context.Context, _ string, id int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.deleted[id], nil
}

func (f *fakeSource) reply(_ context.Context, repo string, n int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, fakeReply{repo, n, body})
	return nil
}

func (f *fakeSource) post(repo string, n int, id int64, author int64, body string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.feed[repo] = append(f.feed[repo], sourceComment{ID: id, Body: body, AuthorID: author,
		HTMLURL:  fmt.Sprintf("https://github.com/%s/issues/%d#issuecomment-%d", repo, n, id),
		IssueURL: fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", repo, n), CreatedAt: at, UpdatedAt: at})
}

func (f *fakeSource) markers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.replies {
		first, _, _ := strings.Cut(r.body, "\n")
		out = append(out, fmt.Sprintf("%s#%d %s", r.repo, r.n, first))
	}
	return out
}

const sourceRepo = "example/netscript"

func sourceFixture(t *testing.T) (*Coord, *fakeSource, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	f := newFakeSource()
	f.issues[sourceRepo+"#2063"] = sourceIssueView{NodeID: "I_source_2063", Title: "Fix the parser", State: "OPEN"}
	cfg := &Config{Inbox: "example/inbox", Targets: []Target{
		{Label: "harness", Repo: "example/inbox"},
		{Label: "netscript", Repo: sourceRepo},
	}}
	return &Coord{cfg: cfg, st: loadState(path), source: f}, f, path
}

const sourceTrigger = "/swarm\nharness: codex\nmodel: gpt-6-sol\neffort: high\nrepo: example/netscript\n\nFix the parser."

// start runs the first tick, which only sets each cursor, and returns a time
// after it so later comments are new.
func startSource(t *testing.T, c *Coord) time.Time {
	t.Helper()
	c.sourceTick(context.Background())
	c.st.mu.Lock()
	cursor := c.st.SourceCursors[sourceRepo]
	c.st.mu.Unlock()
	if cursor.IsZero() {
		t.Fatal("first tick did not set the cursor")
	}
	return cursor.Add(time.Second)
}

func TestSourceTriggerFirstStartNeverBackfills(t *testing.T) {
	c, f, _ := sourceFixture(t)
	f.post(sourceRepo, 2063, 3100000001, ownerGitHubID, sourceTrigger, time.Now().Add(-time.Minute))
	c.sourceTick(context.Background())
	c.sourceTick(context.Background())
	if len(c.st.SourceBindings) != 0 || len(f.replies) != 0 {
		t.Fatal("a comment written before the first start was bound")
	}
}

func TestSourceTriggerBindsOnlyFreshOwnerSwarmComments(t *testing.T) {
	c, f, _ := sourceFixture(t)
	at := startSource(t, c)
	f.issues[sourceRepo+"#7"] = sourceIssueView{NodeID: "I_pr_7", Title: "A PR", State: "OPEN", PR: true}
	f.post(sourceRepo, 2063, 3100000001, ownerGitHubID, sourceTrigger, at)                                    // trigger
	f.post(sourceRepo, 2063, 3100000002, 4242, sourceTrigger, at)                                             // not the owner
	f.post(sourceRepo, 2063, 3100000003, ownerGitHubID, "Looks good.\n/swarm\nharness: codex", at)            // /swarm not first
	f.post(sourceRepo, 2063, 3100000004, ownerGitHubID, "> /swarm\nharness: codex", at)                       // quoted
	f.post(sourceRepo, 7, 3100000005, ownerGitHubID, sourceTrigger, at)                                       // a pull request
	f.post(sourceRepo, 2063, 3100000006, ownerGitHubID, sourceMarker(3100000001, "started", "", "")+"\n", at) // Orchid's own reply
	old := sourceComment{ID: 3100000007, Body: sourceTrigger, AuthorID: ownerGitHubID, HTMLURL: "https://github.com/" + sourceRepo + "/issues/2063", IssueURL: "https://api.github.com/repos/" + sourceRepo + "/issues/2063", CreatedAt: at.Add(-48 * time.Hour), UpdatedAt: at}
	f.feed[sourceRepo] = append(f.feed[sourceRepo], old) // an old comment edited now
	c.sourceTick(context.Background())
	if len(c.st.SourceBindings) != 1 {
		t.Fatalf("bound %d comments, want exactly the owner trigger", len(c.st.SourceBindings))
	}
	b := c.st.SourceBindings[3100000001]
	if b == nil || b.Repo != sourceRepo || b.Issue != 2063 || b.IssueID != "I_source_2063" || b.Closed != "" || b.Body != sourceTrigger {
		t.Fatalf("binding identity wrong: %+v", b)
	}
	if len(f.replies) != 0 {
		t.Fatalf("an admissible trigger replied before launch: %v", f.markers())
	}
	// An edit re-reads the same comment ID; it never binds or launches again.
	f.feed[sourceRepo][0].UpdatedAt = at.Add(time.Hour)
	f.feed[sourceRepo][0].Body = sourceTrigger + "\nEdited."
	c.sourceTick(context.Background())
	if len(c.st.SourceBindings) != 1 || c.st.SourceBindings[3100000001].Body != sourceTrigger {
		t.Fatal("an edited trigger was bound again or changed")
	}
}

func TestSourceTriggerFormatRefusalsReplyOnceAndClose(t *testing.T) {
	cases := map[string]string{
		"brief-encoding-invalid": "/swarm\nharness: codex\nmodel: a\rb\n\nGo.",
		"source-repo-mismatch":   "/swarm\nharness: codex\nrepo: example/inbox\n\nGo.",
		"source-repo-invalid":    "/swarm\nharness: codex\nrepo: example/netscript #1\n\nGo.",
	}
	for reason, body := range cases {
		t.Run(reason, func(t *testing.T) {
			c, f, _ := sourceFixture(t)
			at := startSource(t, c)
			f.post(sourceRepo, 2063, 3100000010, ownerGitHubID, body, at)
			c.sourceTick(context.Background())
			c.sourceTick(context.Background())
			b := c.st.SourceBindings[3100000010]
			want := sourceRepo + "#2063 " + sourceMarker(3100000010, "refused", reason, "")
			if b == nil || b.Closed != "refused" || len(f.replies) != 1 || f.markers()[0] != want {
				t.Fatalf("refusal not replied exactly once: %v", f.markers())
			}
			open, all := map[int]Issue{}, map[int]bool{}
			if !c.pollSourceBindings(context.Background(), open, all) || len(open) != 0 {
				t.Fatal("a refused trigger was admitted")
			}
		})
	}
	t.Run("closed-issue", func(t *testing.T) {
		c, f, _ := sourceFixture(t)
		at := startSource(t, c)
		f.issues[sourceRepo+"#2063"] = sourceIssueView{NodeID: "I_source_2063", Title: "Fix", State: "CLOSED"}
		f.post(sourceRepo, 2063, 3100000011, ownerGitHubID, sourceTrigger, at)
		c.sourceTick(context.Background())
		if b := c.st.SourceBindings[3100000011]; b == nil || b.Closed != "refused" || !strings.Contains(f.markers()[0], "reason=source-issue-closed") {
			t.Fatalf("closed issue not refused: %v", f.markers())
		}
	})
}

func TestSourceTriggerCRLFIsNormalized(t *testing.T) {
	c, f, _ := sourceFixture(t)
	at := startSource(t, c)
	f.post(sourceRepo, 2063, 3100000012, ownerGitHubID, strings.ReplaceAll(sourceTrigger, "\n", "\r\n"), at)
	c.sourceTick(context.Background())
	b := c.st.SourceBindings[3100000012]
	if b == nil || b.Closed != "" || b.Body != sourceTrigger || parseOverrides(b.Body).Model != "gpt-6-sol" {
		t.Fatalf("CRLF trigger not normalized: %+v", b)
	}
}

func TestSourceCursorAdvancesOnlyPastHandledComments(t *testing.T) {
	c, f, path := sourceFixture(t)
	at := startSource(t, c)
	before := c.st.SourceCursors[sourceRepo]
	f.post(sourceRepo, 2063, 3100000020, ownerGitHubID, sourceTrigger, at.Add(10*time.Minute))
	f.failIssue = true
	c.sourceTick(context.Background())
	if !c.st.SourceCursors[sourceRepo].Equal(before) || len(c.st.SourceBindings) != 0 {
		t.Fatal("cursor advanced past a comment that was not handled")
	}
	f.failIssue = false
	c.sourceTick(context.Background())
	if c.st.SourceBindings[3100000020] == nil {
		t.Fatal("the retried comment was not bound")
	}
	after := c.st.SourceCursors[sourceRepo]
	if !after.After(before) || !after.Equal(at.Add(10*time.Minute).Add(-sourceCursorOverlap)) {
		t.Fatalf("cursor %s, want newest minus overlap", after)
	}
	// Restart: bindings and cursors are durable.
	restarted := loadState(path)
	if restarted.SourceBindings[3100000020] == nil || !restarted.SourceCursors[sourceRepo].Equal(after) {
		t.Fatal("binding or cursor not persisted")
	}
	c2 := &Coord{cfg: c.cfg, st: restarted, source: f}
	c2.sourceTick(context.Background())
	if len(c2.st.SourceBindings) != 1 {
		t.Fatal("restart re-bound a consumed comment")
	}
	f.failList = true
	c2.sourceTick(context.Background())
	if !c2.st.SourceCursors[sourceRepo].Equal(after) {
		t.Fatal("a failed listing moved the cursor")
	}
}

func TestSourceBindingAdmissionHomeAndTarget(t *testing.T) {
	c, f, _ := sourceFixture(t)
	at := startSource(t, c)
	f.post(sourceRepo, 2063, 3100000030, ownerGitHubID, sourceTrigger, at)
	c.sourceTick(context.Background())
	open, all := map[int]Issue{}, map[int]bool{}
	if !c.pollSourceBindings(context.Background(), open, all) {
		t.Fatal("poll failed")
	}
	is, ok := open[3100000030]
	if !ok || !all[3100000030] || is.Source == nil || is.Source.Repo != sourceRepo || is.Source.Number != 2063 || is.Number != 3100000030 || is.ID != "I_source_2063" {
		t.Fatalf("binding not admitted as its own key: %+v", is)
	}
	if tgt, reason, ok := c.resolveTarget(is); !ok || reason != "" || tgt.Repo != sourceRepo {
		t.Fatalf("target %v %q %v, want the comment's repository", tgt.Repo, reason, ok)
	}
	if h := c.issueHome(3100000030); h != (dispatchIssue{Repo: sourceRepo, Number: 2063}) {
		t.Fatalf("home %+v", h)
	}
	if h := c.issueHome(17); h != (dispatchIssue{Repo: "example/inbox", Number: 17}) {
		t.Fatalf("inbox home changed: %+v", h)
	}
	if briefDigest(is) != shaText([]byte(sourceTrigger)) {
		t.Fatal("binding brief digest is not the sha256 of the comment body")
	}
	j := &Job{Issue: 3100000030, Home: is.home()}
	if jobHome("example/inbox", j) != (dispatchIssue{Repo: sourceRepo, Number: 2063}) || jobHome("example/inbox", &Job{Issue: 17}) != (dispatchIssue{Repo: "example/inbox", Number: 17}) {
		t.Fatal("job home wrong")
	}
	// A repo key naming another configured repository is refused, never followed.
	other := is
	other.Body = strings.Replace(sourceTrigger, "repo: example/netscript", "repo: example/inbox", 1)
	if _, reason, _ := c.resolveTarget(other); reason != "source-repo-mismatch" {
		t.Fatalf("mismatch reason %q", reason)
	}
	// An unconfigured source repository is refused.
	gone := is
	gone.Body = strings.Replace(sourceTrigger, "repo: example/netscript\n", "", 1)
	gone.Source = &issueSource{Repo: "example/elsewhere", Number: 1}
	if _, reason, _ := c.resolveTarget(gone); reason != "source-repo-unavailable" {
		t.Fatalf("unavailable reason %q", reason)
	}
	// The issue closed, or is no longer the same issue: no longer admitted.
	for _, view := range []sourceIssueView{
		{NodeID: "I_source_2063", Title: "Fix the parser", State: "CLOSED"},
		{NodeID: "I_replaced", Title: "Fix the parser", State: "OPEN"},
	} {
		f.issues[sourceRepo+"#2063"] = view
		open = map[int]Issue{}
		if !c.pollSourceBindings(context.Background(), open, map[int]bool{}) {
			t.Fatal("poll failed")
		}
		if _, still := open[3100000030]; still {
			t.Fatalf("binding admitted for %+v", view)
		}
	}
	f.issues[sourceRepo+"#2063"] = sourceIssueView{NodeID: "I_source_2063", Title: "Fix the parser", State: "OPEN"}
	// The trigger is gone: no longer admitted.
	f.deleted[3100000030] = true
	open = map[int]Issue{}
	c.pollSourceBindings(context.Background(), open, map[int]bool{})
	if _, still := open[3100000030]; still || c.issueState(context.Background(), 3100000030) != "CLOSED" {
		t.Fatal("deleted trigger still admitted")
	}
	f.failIssue = true
	delete(f.deleted, 3100000030)
	if c.pollSourceBindings(context.Background(), map[int]Issue{}, map[int]bool{}) {
		t.Fatal("a failed read was a reliable poll")
	}
}

func TestSourceRepliesAreMarkedOnceAndEndTheBinding(t *testing.T) {
	c, f, _ := sourceFixture(t)
	at := startSource(t, c)
	f.post(sourceRepo, 2063, 3100000040, ownerGitHubID, sourceTrigger, at)
	c.sourceTick(context.Background())
	key := 3100000040
	c.st.Jobs[key] = &Job{Issue: key, Host: "h1", Agent: "codex", Repo: sourceRepo, Overrides: Overrides{Model: "gpt-6-sol"}, Home: &dispatchIssue{Repo: sourceRepo, Number: 2063}}
	c.replySourceStarted(context.Background(), key)
	c.replySourceStarted(context.Background(), key)
	c.st.Jobs[key].PR = 99
	c.replySourcePR(context.Background(), key, c.st.Jobs[key])
	// Refusal notices for a binding become marked replies on the source issue.
	c.reportIssueMatrixRefusal(context.Background(), key, c.st.SourceBindings[key].issue(c), matrixRefusal{Status: "refused", ReasonCode: "quota-unavailable"}, func(context.Context, string, int, string) error {
		t.Fatal("a binding refusal was posted to the inbox")
		return nil
	})
	want := []string{
		sourceRepo + "#2063 " + sourceMarker(3100000040, "started", "", ""),
		sourceRepo + "#2063 " + sourceMarker(3100000040, "pr", "", sourceRepo+"#99"),
		sourceRepo + "#2063 " + sourceMarker(3100000040, "refused", "quota-unavailable", ""),
	}
	if got := f.markers(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("replies:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if c.st.SourceBindings[key].Closed != "" {
		t.Fatal("a non-terminal reply closed the binding")
	}
	// The issue closes after launch; with no job left, the binding replies done once.
	delete(c.st.Jobs, key)
	c.finishSource(context.Background(), map[int]Issue{}, true)
	c.finishSource(context.Background(), map[int]Issue{}, true)
	if got := f.markers(); len(got) != 4 || got[3] != sourceRepo+"#2063 "+sourceMarker(3100000040, "done", "source-closed", "") || c.st.SourceBindings[key].Closed != "done" {
		t.Fatalf("finish replies %v", got)
	}
	// An unreliable poll never finishes a binding.
	f.post(sourceRepo, 2063, 3100000041, ownerGitHubID, sourceTrigger, at)
	c.sourceTick(context.Background())
	c.finishSource(context.Background(), map[int]Issue{}, false)
	if c.st.SourceBindings[3100000041].Closed != "" {
		t.Fatal("finished on an unreliable poll")
	}
	c.finishSource(context.Background(), map[int]Issue{}, true)
	if c.st.SourceBindings[3100000041].Closed != "stopped" {
		t.Fatal("an unlaunched binding whose trigger is gone was not stopped")
	}
}

func TestSourceBlockedReplyKeepsTheBindingSupervised(t *testing.T) {
	c, f, _ := sourceFixture(t)
	at := startSource(t, c)
	f.post(sourceRepo, 2063, 3100000045, ownerGitHubID, sourceTrigger, at)
	c.sourceTick(context.Background())
	c.st.LaunchBlocks = map[int]launchBlock{3100000045: {Reason: "registration_failed"}}
	if !c.reportBlockedLaunch(context.Background(), 3100000045) {
		t.Fatal("blocked launch not reported")
	}
	got := f.markers()
	if len(got) != 1 || got[0] != sourceRepo+"#2063 "+sourceMarker(3100000045, "blocked", "registration_failed", "") {
		t.Fatalf("blocked reply %v", got)
	}
	if !strings.Contains(f.replies[0].body, "with a new /swarm comment") || strings.Contains(f.replies[0].body, "inbox issue") {
		t.Fatal("blocked reply still speaks of an inbox issue")
	}
	// A blocked launch may still hold a registered agent: the binding stays open.
	if c.st.SourceBindings[3100000045].Closed != "" {
		t.Fatal("a blocked reply ended the binding")
	}
}

func TestSourceCompletionRepliesDone(t *testing.T) {
	c, f, _ := sourceFixture(t)
	at := startSource(t, c)
	f.post(sourceRepo, 2063, 3100000050, ownerGitHubID, sourceTrigger, at)
	c.sourceTick(context.Background())
	key := 3100000050
	is := c.st.SourceBindings[key].issue(c)
	c.st.CompletedRuns = map[int]completedRun{key: {Phase: "observed"}}
	c.finishSource(context.Background(), map[int]Issue{key: is}, true)
	if got := f.markers(); len(got) != 1 || got[0] != sourceRepo+"#2063 "+sourceMarker(3100000050, "done", "", "") {
		t.Fatalf("completion reply %v", got)
	}
	// The completion fence is pruned once the binding is closed.
	if c.issueState(context.Background(), key) != "CLOSED" {
		t.Fatal("a done binding is not CLOSED")
	}
}

func TestSourceBindingsArePruned(t *testing.T) {
	c, _, _ := sourceFixture(t)
	now := time.Now()
	c.st.SourceBindings = map[int]*sourceBinding{
		1: {Closed: "done", CreatedAt: now.Add(-8 * 24 * time.Hour)},
		2: {Closed: "", CreatedAt: now.Add(-8 * 24 * time.Hour)},
		3: {Closed: "done", CreatedAt: now.Add(-time.Hour)},
	}
	c.pruneSourceBindings(now)
	if c.st.SourceBindings[1] != nil || c.st.SourceBindings[2] == nil || c.st.SourceBindings[3] == nil {
		t.Fatal("prune removed the wrong bindings")
	}
}

func TestSourceTriggerBodyGrammar(t *testing.T) {
	for raw, want := range map[string][3]bool{
		"/swarm\nharness: codex":    {true, true, true},
		"  /swarm  \nharness: x":    {true, true, true},
		"/swarm\r\nharness: codex":  {true, true, true},
		"/swarm\nmodel: a\rb":       {true, false, true},
		"Please /swarm":             {false, false, false},
		"\n/swarm\nharness: codex":  {false, false, false},
		"```\n/swarm\nharness: x\n": {false, false, false},
	} {
		body, trigger, ok := sourceTriggerBody(raw)
		if trigger != want[0] || (trigger && ok != want[1]) || (want[2] && strings.Contains(body, "\r\n")) {
			t.Fatalf("%q: trigger=%v ok=%v", raw, trigger, ok)
		}
	}
}

// Owner grants for comment triggers: bound before the comment exists, matched by
// the source issue and body digest, usable by one comment only.
func TestOwnerNativeCommentGrant(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
	body := "/swarm\nharness: claude\nrepo: example/project\n\nFix it."
	r.Trigger = ownerNativeCommentTrigger
	r.ExpectedBriefDigest = shaText([]byte(body))
	r.NativeOverride.Route = ownerNativeRoute{Harness: "claude", Provider: "anthropic", Model: "claude-opus-5-5", Effort: "high"}
	ack := s.install(context.Background(), r)
	if ack.State != "LIVE" || ack.BriefDigest != r.ExpectedBriefDigest || ack.IssueNumber != 7 {
		t.Fatalf("comment grant not LIVE: %s %s", ack.State, ack.Reason)
	}
	if s.readStatus(context.Background(), r.OperationID).State != "LIVE" {
		t.Fatal("comment grant status not LIVE")
	}
	binding := func(key int, b string) Issue {
		return Issue{ID: is.ID, Number: key, Title: is.Title, Body: b, Labels: []string{"fixture-target"}, Source: &issueSource{Repo: "example/project", Number: 7}}
	}
	cfg, err := s.matrixForIssue(binding(3100000060, body), "example/project")
	if err != nil {
		t.Fatalf("comment grant not admitted: %v", err)
	}
	req, err := prepareMatrixRequest(cfg, binding(3100000060, body), "example/project", parseOverrides(body))
	if err != nil || req.NativeOverride == nil || req.NativeOverride.Route.Model != "claude-opus-5-5" || !req.owner {
		t.Fatalf("owner route not carried: %v", err)
	}
	// Re-admitting the same binding is idempotent; a second comment cannot reuse it.
	if _, err := s.matrixForIssue(binding(3100000060, body), "example/project"); err != nil {
		t.Fatal("the same binding lost its claim")
	}
	if _, err := s.matrixForIssue(binding(3100000061, body), "example/project"); err == nil {
		t.Fatal("a second comment reused a one-launch grant")
	}
	// A comment with another body has no owner grant and routes through the matrix.
	cfg, err = s.matrixForIssue(binding(3100000062, body+"\nMore."), "example/project")
	if err != nil {
		t.Fatalf("an ungranted comment was refused: %v", err)
	}
	if req, _ := prepareMatrixRequest(cfg, binding(3100000062, body+"\nMore."), "example/project", parseOverrides(body)); req.NativeOverride != nil {
		t.Fatal("an ungranted comment inherited the owner route")
	}
	// The grant survives a restart, claim included.
	recovered, err := newOwnerNativeGrantStore(context.Background(), s.cfg, s.options, s.deps)
	if err != nil || recovered.readStatus(context.Background(), r.OperationID).State != "LIVE" {
		t.Fatal("comment grant not recovered")
	}
	if _, err := recovered.matrixForIssue(binding(3100000061, body), "example/project"); err == nil {
		t.Fatal("restart forgot the claim")
	}
	// Comment intents never mix with the same issue used as an inbox binding.
	if s.subjectKey(s.requestSubject(r)) == s.issueKey(7) {
		t.Fatal("comment and inbox subjects share a key")
	}
	inbox := *is
	if _, err := s.matrixForIssue(inbox, "example/project"); err != nil {
		t.Fatalf("an inbox issue saw the comment grant: %v", err)
	}
}

func TestOwnerNativeCommentGrantRefusals(t *testing.T) {
	for _, name := range []string{"closed", "other-issue", "unconfigured", "bad-trigger"} {
		t.Run(name, func(t *testing.T) {
			s, r, _ := ownerPortFixture(t, "claude")
			_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
			r.Trigger = ownerNativeCommentTrigger
			r.ExpectedBriefDigest = shaText([]byte("/swarm\nharness: claude"))
			state := "OPEN"
			switch name {
			case "closed":
				state = "CLOSED"
			case "other-issue":
				r.ExpectedIssueID = "another-node"
			case "unconfigured":
				s.cfg.Targets[0].Disabled = true
			case "bad-trigger":
				r.Trigger = "label"
			}
			s.deps.command = func(context.Context, string, string, []byte, ...string) ([]byte, error) {
				return json.Marshal(map[string]any{"id": "synthetic-node", "number": 7, "title": "T", "body": "B", "state": state, "labels": []any{}})
			}
			if ack := s.install(context.Background(), r); ack.State == "LIVE" {
				t.Fatal("comment grant installed")
			}
			records, _ := os.ReadDir(filepath.Join(s.options.StoreRoot, "records"))
			if len(records) != 0 {
				t.Fatal("a refused comment grant published authority")
			}
		})
	}
}
