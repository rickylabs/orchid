package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSource is a fake GitHub for the source-binding surface: comments per
// repository, issues, deletions, every posted reply, ETags that answer 304 when
// nothing changed, and call counts.
type fakeSource struct {
	mu          sync.Mutex
	feed        map[string][]sourceComment
	issues      map[string]sourceIssueView
	deleted     map[int64]bool
	replies     []fakeReply
	failIssue   bool
	failList    bool
	failReplies int // the next N replies fail
	remaining   int // X-RateLimit-Remaining; -1 = absent
	reads       int // every GET, 304 included
	notModified int
}

type fakeReply struct {
	repo string
	n    int
	body string
}

func newFakeSource() *fakeSource {
	return &fakeSource{feed: map[string][]sourceComment{}, issues: map[string]sourceIssueView{}, deleted: map[int64]bool{}, remaining: -1}
}

func (f *fakeSource) answer(tag, current string) sourceMeta {
	f.reads++
	meta := sourceMeta{ETag: `W/"` + shaText([]byte(current))[:16] + `"`, Remaining: f.remaining}
	if tag != "" && tag == meta.ETag {
		f.notModified++
		meta.NotModified = true
	}
	return meta
}

func (f *fakeSource) comments(_ context.Context, repo, since string, page int, etag string) ([]sourceComment, sourceMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failList {
		f.reads++
		return nil, sourceMeta{Remaining: -1}, errors.New("list down")
	}
	from, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return nil, sourceMeta{Remaining: -1}, err
	}
	var all []sourceComment
	for _, c := range f.feed[repo] {
		if c.UpdatedAt.Truncate(time.Second).After(from) && !f.deleted[c.ID] { // GitHub: strictly after, whole seconds
			all = append(all, c)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].UpdatedAt.Before(all[j].UpdatedAt) })
	lo, hi := (page-1)*sourcePageSize, page*sourcePageSize
	if lo > len(all) {
		lo = len(all)
	}
	if hi > len(all) {
		hi = len(all)
	}
	out := all[lo:hi]
	raw, _ := json.Marshal(out)
	meta := f.answer(etag, string(raw))
	if meta.NotModified {
		return nil, meta, nil
	}
	return out, meta, nil
}

func (f *fakeSource) issue(_ context.Context, repo string, n int, etag string) (sourceIssueView, sourceMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failIssue {
		f.reads++
		return sourceIssueView{}, sourceMeta{Remaining: -1}, errors.New("issue down")
	}
	v, ok := f.issues[fmt.Sprintf("%s#%d", repo, n)]
	if !ok {
		v = sourceIssueView{State: "GONE"}
	}
	meta := f.answer(etag, fmt.Sprintf("%+v", v))
	if meta.NotModified {
		return sourceIssueView{}, meta, nil
	}
	return v, meta, nil
}

func (f *fakeSource) comment(_ context.Context, _ string, id int64, etag string) (bool, sourceMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleted[id] {
		f.reads++
		return false, sourceMeta{Remaining: f.remaining}, nil
	}
	meta := f.answer(etag, fmt.Sprint(id))
	return true, meta, nil
}

func (f *fakeSource) reply(_ context.Context, repo string, n int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failReplies > 0 {
		f.failReplies--
		return errors.New("reply down")
	}
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

// sourceFixture grants exactly the comments whose body is in granted.
func sourceFixture(t *testing.T) (*Coord, *fakeSource, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	f := newFakeSource()
	f.issues[sourceRepo+"#2063"] = sourceIssueView{NodeID: "I_source_2063", Title: "Fix the parser", State: "OPEN"}
	cfg := &Config{Inbox: "example/inbox", Targets: []Target{
		{Label: "harness", Repo: "example/inbox"},
		{Label: "netscript", Repo: sourceRepo},
	}}
	c := &Coord{cfg: cfg, st: loadState(path), source: f}
	c.sourceGrant = func(repo string, n int, issueID, body string, _ int) bool {
		return repo == sourceRepo && n == 2063 && issueID == "I_source_2063" && body == sourceTrigger
	}
	return c, f, path
}

const sourceTrigger = "/swarm\nharness: codex\nmodel: gpt-6-sol\neffort: high\nrepo: example/netscript\n\nFix the parser."

// startSource runs the first tick, which only records where each repository
// starts, and returns a time after it so later comments are new.
func startSource(t *testing.T, c *Coord) time.Time {
	t.Helper()
	c.sourceTick(context.Background())
	c.st.mu.Lock()
	cursor, start := c.st.SourceCursors[sourceRepo], c.st.SourceStarts[sourceRepo]
	c.st.mu.Unlock()
	if cursor.IsZero() || !start.Equal(cursor) {
		t.Fatal("first tick did not set the cursor and start")
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

// B2: an edited comment and a comment written before the first scan never launch,
// even when edited into a trigger after it.
func TestSourceTriggerRejectsEditedAndPreStartComments(t *testing.T) {
	c, f, _ := sourceFixture(t)
	before := time.Now().Add(-time.Hour)
	at := startSource(t, c)
	// An ordinary owner comment written after the start, edited into a trigger.
	f.post(sourceRepo, 2063, 3100000101, ownerGitHubID, sourceTrigger, at)
	f.feed[sourceRepo][0].UpdatedAt = at.Add(time.Minute)
	// A comment written an hour before the first scan, edited into a trigger now.
	f.post(sourceRepo, 2063, 3100000102, ownerGitHubID, sourceTrigger, before)
	f.feed[sourceRepo][1].UpdatedAt = at.Add(time.Minute)
	// The same pre-start comment, with created and updated equal (a feed replay).
	f.post(sourceRepo, 2063, 3100000103, ownerGitHubID, sourceTrigger, before)
	f.feed[sourceRepo][2].UpdatedAt = before
	f.feed[sourceRepo][2].CreatedAt = before
	c.st.SourceCursors[sourceRepo] = before.Add(-time.Minute) // let the listing reach it
	c.sourceTick(context.Background())
	if len(c.st.SourceBindings) != 0 {
		t.Fatalf("edited or pre-start comments bound: %v", c.st.SourceBindings)
	}
	// The start boundary survives the cursor moving and a restart.
	if !c.st.SourceStarts[sourceRepo].Equal(at.Add(-time.Second)) {
		t.Fatal("start boundary moved")
	}
}

// A comment first seen more than a day after it was written is not a trigger,
// even unedited and written after the start (for example after a long outage).
func TestSourceTriggerFirstSeenLateIsNotATrigger(t *testing.T) {
	c, f, _ := sourceFixture(t)
	startSource(t, c)
	longAgo := time.Now().UTC().Add(-72 * time.Hour)
	c.st.SourceCursors[sourceRepo], c.st.SourceStarts[sourceRepo] = longAgo, longAgo
	f.post(sourceRepo, 2063, 3100000105, ownerGitHubID, sourceTrigger, time.Now().UTC().Add(-48*time.Hour))
	c.sourceTick(context.Background())
	if len(c.st.SourceBindings) != 0 {
		t.Fatal("a comment first seen two days after it was written was bound")
	}
}

// B6: the first line is exactly /swarm.
func TestSourceTriggerFirstLineIsExact(t *testing.T) {
	for raw, trigger := range map[string]bool{
		"/swarm\nharness: codex":   true,
		"/swarm\r\nharness: codex": true,
		"/swarm":                   true,
		" /swarm\nharness: codex":  false,
		"/swarm \nharness: codex":  false,
		"\t/swarm\nharness: codex": false,
		"/swarm\t\nharness: codex": false,
		"\n/swarm\nharness: codex": false,
		"Please /swarm":            false,
		"/Swarm\nharness: codex":   false,
	} {
		if _, got, _ := sourceTriggerBody(raw); got != trigger {
			t.Fatalf("%q: trigger=%v, want %v", raw, got, trigger)
		}
	}
	if body, _, ok := sourceTriggerBody("/swarm\nmodel: a\rb"); ok || body != "/swarm\nmodel: a\rb" {
		t.Fatal("a stray CR was accepted")
	}
	if body, _, ok := sourceTriggerBody("/swarm\r\nmodel: a\r\n"); !ok || body != "/swarm\nmodel: a\n" {
		t.Fatal("CRLF not normalized")
	}
}

// B3: an owner comment launches only on its installed grant.
func TestSourceTriggerRequiresItsGrant(t *testing.T) {
	c, f, _ := sourceFixture(t)
	at := startSource(t, c)
	ungranted := strings.Replace(sourceTrigger, "Fix the parser.", "Something else.", 1)
	f.post(sourceRepo, 2063, 3100000110, ownerGitHubID, ungranted, at)
	f.post(sourceRepo, 2063, 3100000111, ownerGitHubID, sourceTrigger, at)
	c.sourceTick(context.Background())
	if b := c.st.SourceBindings[3100000110]; b == nil || b.Closed != "refused" {
		t.Fatal("an ungranted comment was not refused")
	}
	if b := c.st.SourceBindings[3100000111]; b == nil || b.Closed != "" {
		t.Fatal("a granted comment was not bound")
	}
	if got := f.markers(); len(got) != 1 || got[0] != sourceRepo+"#2063 "+sourceMarker(3100000110, "refused", "source-grant-missing", "") {
		t.Fatalf("replies %v", got)
	}
	open := map[int]Issue{}
	c.pollSourceBindings(context.Background(), open, map[int]bool{})
	if _, admitted := open[3100000110]; admitted {
		t.Fatal("an ungranted comment was admitted")
	}
	// Admission refuses a binding without the owner endpoint at all.
	is := c.st.SourceBindings[3100000111].issue(c)
	if _, err := c.matrixConfigForIssue(context.Background(), is, sourceRepo); err == nil || err.Error() != "source-grant-missing" {
		t.Fatalf("admission without an owner endpoint: %v", err)
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

// B5: a reply that cannot be posted stays decided and is delivered later; the
// refusal and the started acknowledgement are never lost.
func TestSourceRepliesSurviveTransientFailures(t *testing.T) {
	c, f, path := sourceFixture(t)
	at := startSource(t, c)
	f.failReplies = 2
	ungranted := strings.Replace(sourceTrigger, "Fix the parser.", "Other.", 1)
	f.post(sourceRepo, 2063, 3100000120, ownerGitHubID, ungranted, at)
	c.sourceTick(context.Background())
	b := c.st.SourceBindings[3100000120]
	if b == nil || b.Closed != "refused" || len(b.Outbox) != 1 || len(f.replies) != 0 {
		t.Fatalf("refusal decision not persisted while its reply failed: %+v", b)
	}
	// Restart before delivery: the decision is durable.
	c2 := &Coord{cfg: c.cfg, st: loadState(path), source: f, sourceGrant: c.sourceGrant}
	c2.sourceTick(context.Background()) // second failure
	c2.sourceTick(context.Background())
	if got := f.markers(); len(got) != 1 || got[0] != sourceRepo+"#2063 "+sourceMarker(3100000120, "refused", "source-grant-missing", "") {
		t.Fatalf("refusal not delivered after the outage: %v", got)
	}
	if len(c2.st.SourceBindings[3100000120].Outbox) != 0 {
		t.Fatal("delivered reply still queued")
	}
	c2.sourceTick(context.Background())
	if len(f.replies) != 1 {
		t.Fatal("a delivered reply was posted twice")
	}
	// The started acknowledgement after a launch, with GitHub down at that moment.
	f.post(sourceRepo, 2063, 3100000121, ownerGitHubID, sourceTrigger, at)
	c2.sourceTick(context.Background())
	key := 3100000121
	c2.st.Jobs[key] = &Job{Issue: key, Host: "h1", Agent: "codex", Repo: sourceRepo, Home: &dispatchIssue{Repo: sourceRepo, Number: 2063}}
	f.failReplies = 1
	c2.replySourceStarted(context.Background(), key)
	if !c2.st.SourceBindings[key].Launched || len(c2.st.SourceBindings[key].Outbox) != 1 {
		t.Fatal("started not decided durably")
	}
	c2.sourceTick(context.Background()) // the POST fails; the reply stays queued
	if len(c2.st.SourceBindings[key].Outbox) != 1 {
		t.Fatal("a failed reply left the queue")
	}
	c2.sourceTick(context.Background())
	if got := f.markers(); got[len(got)-1] != sourceRepo+"#2063 "+sourceMarker(3100000121, "started", "", "") {
		t.Fatalf("started not delivered later: %v", got)
	}
	// A launched job whose started decision was never saved gets one on catch-up.
	f.post(sourceRepo, 2063, 3100000122, ownerGitHubID, sourceTrigger, at)
	c2.sourceTick(context.Background())
	c2.st.Jobs[3100000122] = &Job{Issue: 3100000122, Host: "h1", Agent: "codex", Repo: sourceRepo, PR: 5, Home: &dispatchIssue{Repo: sourceRepo, Number: 2063}}
	c2.sourceTick(context.Background())
	got := strings.Join(f.markers(), "\n")
	if !strings.Contains(got, sourceMarker(3100000122, "started", "", "")) || !strings.Contains(got, sourceMarker(3100000122, "pr", "", sourceRepo+"#5")) {
		t.Fatalf("catch-up replies missing:\n%s", got)
	}
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
	startSource(t, c)
	// Started an hour ago; the trigger was written 30 minutes ago.
	before := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	c.st.SourceCursors[sourceRepo], c.st.SourceStarts[sourceRepo] = before, before
	written := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	f.post(sourceRepo, 2063, 3100000020, ownerGitHubID, sourceTrigger, written)
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
	if !after.Equal(written.Add(-time.Second)) {
		t.Fatalf("cursor %s, want one second before the newest comment read (%s): since is exclusive", after, written)
	}
	restarted := loadState(path)
	if restarted.SourceBindings[3100000020] == nil || !restarted.SourceCursors[sourceRepo].Equal(after) || !restarted.SourceStarts[sourceRepo].Equal(before) {
		t.Fatal("binding, cursor or start not persisted")
	}
	// The cursor is inclusive, so the next read lists the same unedited trigger
	// again; a closed binding must stay closed, with its replies.
	restarted.SourceBindings[3100000020].Closed = "stopped"
	restarted.SourceBindings[3100000020].Replies = map[string]bool{"stopped\x00source-closed\x00": true}
	c2 := &Coord{cfg: c.cfg, st: restarted, source: f, sourceGrant: c.sourceGrant}
	c2.sourceTick(context.Background())
	if b := c2.st.SourceBindings[3100000020]; len(c2.st.SourceBindings) != 1 || b.Closed != "stopped" || !b.Replies["stopped\x00source-closed\x00"] {
		t.Fatal("restart re-bound a consumed comment")
	}
	f.failList = true
	c2.sourceTick(context.Background())
	if !c2.st.SourceCursors[sourceRepo].Equal(after) {
		t.Fatal("a failed listing moved the cursor")
	}
}

// B4: a burst longer than one tick's pages, all inside the overlap window, is
// read to its end on later ticks; the trigger on page six is bound.
func TestSourceBurstIsReadToItsEnd(t *testing.T) {
	c, f, path := sourceFixture(t)
	at := startSource(t, c)
	for i := 0; i < 550; i++ {
		f.post(sourceRepo, 2063, int64(3200000000+i), 4242, "ordinary comment", at.Add(time.Duration(i)*100*time.Millisecond))
	}
	f.post(sourceRepo, 2063, 3100000130, ownerGitHubID, sourceTrigger, at.Add(56*time.Second))
	for tick := 0; tick < 4 && c.st.SourceBindings[3100000130] == nil; tick++ {
		c.sourceTick(context.Background())
	}
	if c.st.SourceBindings[3100000130] == nil {
		t.Fatal("the trigger after a 550-comment burst was never read")
	}
	// The scan position is durable across a restart mid-burst.
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// B4: unchanged reads are conditional (304) and every tick stays inside its
// read budget, however many repositories and bindings there are.
func TestSourceReadsAreConditionalAndBudgeted(t *testing.T) {
	c, f, _ := sourceFixture(t)
	for i := 0; i < 30; i++ {
		repo := fmt.Sprintf("example/repo%02d", i)
		c.cfg.Targets = append(c.cfg.Targets, Target{Label: fmt.Sprintf("r%02d", i), Repo: repo})
	}
	at := startSource(t, c) // first ticks only set cursors
	for tick := 0; tick < 3; tick++ {
		c.sourceTick(context.Background())
	}
	for i := 0; i < 20; i++ {
		f.post(sourceRepo, 2063, int64(3100000200+i), ownerGitHubID, sourceTrigger, at)
	}
	c.sourceTick(context.Background())
	c.sourceTick(context.Background())
	f.mu.Lock()
	f.reads, f.notModified = 0, 0
	f.mu.Unlock()
	for tick := 0; tick < 10; tick++ {
		f.mu.Lock()
		start := f.reads
		f.mu.Unlock()
		c.sourceTick(context.Background())
		open, all := map[int]Issue{}, map[int]bool{}
		c.pollSourceBindings(context.Background(), open, all)
		f.mu.Lock()
		spent := f.reads - start
		f.mu.Unlock()
		if spent > sourceListRequestsPerTick+sourceCheckRequestsPerTick {
			t.Fatalf("tick %d spent %d reads", tick, spent)
		}
		// Unchecked bindings stay admitted: a skipped check never drops one.
		for key, b := range c.st.SourceBindings {
			if b.Closed == "" && !all[key] {
				t.Fatalf("binding %d dropped while unchecked", key)
			}
		}
	}
	f.mu.Lock()
	reads, cached := f.reads, f.notModified
	f.mu.Unlock()
	if cached*10 < reads*8 {
		t.Fatalf("only %d of %d steady-state reads were conditional 304s", cached, reads)
	}
	// Every repository and every binding gets its turn.
	c.st.mu.Lock()
	for _, repo := range c.sourceRepos() {
		if _, ok := c.st.SourceCursors[repo]; !ok {
			t.Fatalf("repository %s never started", repo)
		}
	}
	c.st.mu.Unlock()
	mem := c.sourceMem()
	mem.mu.Lock()
	checked := len(mem.open)
	mem.mu.Unlock()
	if checked != 20 {
		t.Fatalf("%d of 20 bindings were ever checked", checked)
	}
	// Re-checks take turns: a trigger deleted on the last binding in order is
	// noticed within ceil(20 / 6) + 1 ticks.
	f.mu.Lock()
	f.deleted[3100000219] = true
	f.mu.Unlock()
	mem.mu.Lock()
	mem.checkTurn = 0 // start from the first binding: the deleted one is checked last
	mem.mu.Unlock()
	noticed := false
	for tick := 0; tick < 5 && !noticed; tick++ {
		all := map[int]bool{}
		c.pollSourceBindings(context.Background(), map[int]Issue{}, all)
		noticed = !all[3100000219]
	}
	if !noticed {
		t.Fatal("a deleted trigger was not noticed within five ticks")
	}
}

// B4: issue reads for new triggers on many issues share the listing budget;
// triggers past it are read again on later ticks, none is lost.
func TestSourceBindReadsShareTheBudget(t *testing.T) {
	c, f, _ := sourceFixture(t)
	at := startSource(t, c)
	for i := 0; i < 20; i++ {
		n := 3000 + i
		f.issues[fmt.Sprintf("%s#%d", sourceRepo, n)] = sourceIssueView{NodeID: fmt.Sprintf("I_%d", n), Title: "T", State: "OPEN"}
		f.post(sourceRepo, n, int64(3100000300+i), ownerGitHubID, sourceTrigger, at)
	}
	for tick := 0; tick < 5; tick++ {
		f.mu.Lock()
		start := f.reads
		f.mu.Unlock()
		c.sourceTick(context.Background())
		f.mu.Lock()
		spent := f.reads - start
		f.mu.Unlock()
		if spent > sourceListRequestsPerTick {
			t.Fatalf("tick %d spent %d listing and bind reads", tick, spent)
		}
	}
	if len(c.st.SourceBindings) != 20 {
		t.Fatalf("%d of 20 triggers were read", len(c.st.SourceBindings))
	}
}

// B4: near the end of the core budget, source reads pause until the reset.
func TestSourceReadsPauseNearTheRateFloor(t *testing.T) {
	c, f, _ := sourceFixture(t)
	startSource(t, c)
	f.remaining = sourceRateFloor - 1
	c.sourceTick(context.Background())
	f.mu.Lock()
	before := f.reads
	f.mu.Unlock()
	c.sourceTick(context.Background())
	c.pollSourceBindings(context.Background(), map[int]Issue{}, map[int]bool{})
	f.mu.Lock()
	after := f.reads
	f.mu.Unlock()
	if after != before {
		t.Fatalf("read %d more times while paused", after-before)
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
	other := is
	other.Body = strings.Replace(sourceTrigger, "repo: example/netscript", "repo: example/inbox", 1)
	if _, reason, _ := c.resolveTarget(other); reason != "source-repo-mismatch" {
		t.Fatalf("mismatch reason %q", reason)
	}
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
		if c.issueState(context.Background(), 3100000030) != "CLOSED" {
			t.Fatal("binding state not CLOSED")
		}
	}
	f.issues[sourceRepo+"#2063"] = sourceIssueView{NodeID: "I_source_2063", Title: "Fix the parser", State: "OPEN"}
	f.deleted[3100000030] = true
	open = map[int]Issue{}
	c.pollSourceBindings(context.Background(), open, map[int]bool{})
	if _, still := open[3100000030]; still || c.issueState(context.Background(), 3100000030) != "CLOSED" {
		t.Fatal("deleted trigger still admitted")
	}
	// A failed read keeps the last known state and makes the poll unreliable.
	delete(f.deleted, 3100000030)
	f.failIssue = true
	open = map[int]Issue{}
	if c.pollSourceBindings(context.Background(), open, map[int]bool{}) {
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
	c.reportIssueMatrixRefusal(context.Background(), key, c.st.SourceBindings[key].issue(c), matrixRefusal{Status: "refused", ReasonCode: "quota-unavailable"}, func(context.Context, string, int, string) error {
		t.Fatal("a binding refusal was posted to the inbox")
		return nil
	})
	c.deliverSourceReplies(context.Background())
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
	delete(c.st.Jobs, key)
	c.finishSource(context.Background(), map[int]Issue{}, true)
	c.finishSource(context.Background(), map[int]Issue{}, true)
	c.deliverSourceReplies(context.Background())
	if got := f.markers(); len(got) != 4 || got[3] != sourceRepo+"#2063 "+sourceMarker(3100000040, "done", "source-closed", "") || c.st.SourceBindings[key].Closed != "done" {
		t.Fatalf("finish replies %v", got)
	}
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
	c.deliverSourceReplies(context.Background())
	got := f.markers()
	if len(got) != 1 || got[0] != sourceRepo+"#2063 "+sourceMarker(3100000045, "blocked", "registration_failed", "") {
		t.Fatalf("blocked reply %v", got)
	}
	if !strings.Contains(f.replies[0].body, "with a new /swarm comment") || strings.Contains(f.replies[0].body, "inbox issue") {
		t.Fatal("blocked reply still speaks of an inbox issue")
	}
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
	c.deliverSourceReplies(context.Background())
	if got := f.markers(); len(got) != 1 || got[0] != sourceRepo+"#2063 "+sourceMarker(3100000050, "done", "", "") {
		t.Fatalf("completion reply %v", got)
	}
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
		4: {Closed: "done", CreatedAt: now.Add(-8 * 24 * time.Hour), Outbox: []sourceReply{{ID: "x"}}},
	}
	c.pruneSourceBindings(now)
	if c.st.SourceBindings[1] != nil || c.st.SourceBindings[2] == nil || c.st.SourceBindings[3] == nil || c.st.SourceBindings[4] == nil {
		t.Fatal("prune removed the wrong bindings")
	}
}

// Owner grants for comment triggers: bound before the comment exists, matched by
// the source issue and body digest, usable by one comment only, and the only way
// a comment launches.
func TestOwnerNativeCommentGrant(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
	body := "/swarm\nharness: claude\nrepo: example/project\n\nFix it."
	r.Trigger = ownerNativeCommentTrigger
	r.ExpectedBriefDigest = shaText([]byte(body))
	r.NativeOverride.Route = ownerNativeRoute{Harness: "claude", Provider: "anthropic", Model: "claude-opus-5-5", Effort: "high"}
	binding := func(key int, b string) Issue {
		return Issue{ID: is.ID, Number: key, Title: is.Title, Body: b, Labels: []string{"fixture-target"}, Source: &issueSource{Repo: "example/project", Number: 7}}
	}
	// Before installation nothing launches.
	if s.commentGrantReady("example/project", 7, is.ID, r.ExpectedBriefDigest, 3100000060) {
		t.Fatal("a grant was ready before installation")
	}
	if _, err := s.matrixForIssue(binding(3100000060, body), "example/project"); err == nil || err.Error() != "source-grant-missing" {
		t.Fatalf("an ungranted comment was admitted: %v", err)
	}
	ack := s.install(context.Background(), r)
	if ack.State != "LIVE" || ack.BriefDigest != r.ExpectedBriefDigest || ack.IssueNumber != 7 {
		t.Fatalf("comment grant not LIVE: %s %s", ack.State, ack.Reason)
	}
	if s.readStatus(context.Background(), r.OperationID).State != "LIVE" {
		t.Fatal("comment grant status not LIVE")
	}
	if !s.commentGrantReady("example/project", 7, is.ID, r.ExpectedBriefDigest, 3100000060) {
		t.Fatal("an installed grant was not ready")
	}
	if s.commentGrantReady("example/project", 7, is.ID, shaText([]byte(body+"x")), 3100000060) || s.commentGrantReady("example/project", 7, "another", r.ExpectedBriefDigest, 3100000060) {
		t.Fatal("a grant matched another body or issue")
	}
	cfg, err := s.matrixForIssue(binding(3100000060, body), "example/project")
	if err != nil {
		t.Fatalf("comment grant not admitted: %v", err)
	}
	req, err := prepareMatrixRequest(cfg, binding(3100000060, body), "example/project", parseOverrides(body))
	if err != nil || req.NativeOverride == nil || req.NativeOverride.Route.Model != "claude-opus-5-5" || !req.owner {
		t.Fatalf("owner route not carried: %v", err)
	}
	if _, err := s.matrixForIssue(binding(3100000060, body), "example/project"); err != nil {
		t.Fatal("the same binding lost its claim")
	}
	if s.commentGrantReady("example/project", 7, is.ID, r.ExpectedBriefDigest, 3100000061) {
		t.Fatal("a claimed grant was ready for another comment")
	}
	if _, err := s.matrixForIssue(binding(3100000061, body), "example/project"); err == nil {
		t.Fatal("a second comment reused a one-launch grant")
	}
	// A comment with another body has no grant: refused, never the matrix.
	if _, err := s.matrixForIssue(binding(3100000062, body+"\nMore."), "example/project"); err == nil || err.Error() != "source-grant-missing" {
		t.Fatalf("an ungranted comment was admitted: %v", err)
	}
	recovered, err := newOwnerNativeGrantStore(context.Background(), s.cfg, s.options, s.deps)
	if err != nil || recovered.readStatus(context.Background(), r.OperationID).State != "LIVE" {
		t.Fatal("comment grant not recovered")
	}
	if _, err := recovered.matrixForIssue(binding(3100000061, body), "example/project"); err == nil {
		t.Fatal("restart forgot the claim")
	}
	if s.subjectKey(s.requestSubject(r)) == s.issueKey(7) {
		t.Fatal("comment and inbox subjects share a key")
	}
	inbox := *is
	if _, err := s.matrixForIssue(inbox, "example/project"); err != nil {
		t.Fatalf("an inbox issue saw the comment grant: %v", err)
	}
	// End to end: the feeder binds the granted comment and refuses another.
	c := &Coord{cfg: s.cfg, st: loadState(filepath.Join(t.TempDir(), "state.json")), ownerGrants: s}
	f := newFakeSource()
	c.source = f
	f.issues["example/project#7"] = sourceIssueView{NodeID: is.ID, Title: is.Title, State: "OPEN"}
	c.sourceTick(context.Background())
	at := c.st.SourceCursors["example/project"].Add(time.Second)
	f.post("example/project", 7, 3100000063, ownerGitHubID, body, at)
	f.post("example/project", 7, 3100000064, ownerGitHubID, body+"\nOther.", at)
	c.sourceTick(context.Background())
	if b := c.st.SourceBindings[3100000063]; b == nil || b.Closed != "refused" {
		t.Fatal("a comment reusing a claimed grant was not refused at binding")
	}
	if b := c.st.SourceBindings[3100000064]; b == nil || b.Closed != "refused" {
		t.Fatal("an ungranted comment was not refused at binding")
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

// A granted comment end to end through the production matrix attempt: it
// launches once across another poll and a restart, and its started reply
// survives a transient outage.
func TestSourceGrantedCommentLaunchesOnceAndAcknowledges(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
	body := "/swarm\ntier: feature\nrole: implementation\nrepo: example/project\n\nSynthetic task"
	r.Trigger = ownerNativeCommentTrigger
	r.ExpectedBriefDigest = shaText([]byte(body))
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatalf("grant not LIVE: %s", ack.Reason)
	}
	f := newFakeSource()
	f.issues["example/project#7"] = sourceIssueView{NodeID: is.ID, Title: is.Title, State: "OPEN"}
	cfg := s.cfg
	cfg.OwnerNativeGrantPort = &s.options
	cfg.Governor.WeeklyCeiling = 92
	c := &Coord{cfg: cfg, st: loadState(filepath.Join(t.TempDir(), "state.json")), ownerGrants: s, source: f}
	now := time.Now()
	c.gov.q = map[string]quota{"claude": {ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()}}}
	c.sourceTick(context.Background())
	f.post("example/project", 7, 3100000400, ownerGitHubID, body, c.st.SourceCursors["example/project"].Add(time.Second))
	c.sourceTick(context.Background())
	if b := c.st.SourceBindings[3100000400]; b == nil || b.Closed != "" {
		t.Fatalf("granted comment not bound: %+v", b)
	}
	launches := 0
	d := matrixAttemptDeps{
		report: func(matrixRefusal) {},
		read: func(context.Context, string, string, string) (string, error) {
			return "| `routing` | matrix `implementation` row |", nil
		},
		resolve: func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) { return syntheticRoute(), nil },
		host:    func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, true },
		persist: persistMatrixReceipt,
		launch: func(_ context.Context, _ int, _ Issue, _ Host, agent string, o Overrides, receipt *durableMatrixReceipt) error {
			if !receipt.claim(mustBuildAgentCmd(t, agent, o)) {
				t.Fatal("launch did not claim its durable receipt")
			}
			launches++
			return nil
		},
	}
	target := c.cfg.Targets[0]
	open := map[int]Issue{}
	c.pollSourceBindings(context.Background(), open, map[int]bool{})
	if _, ok := c.matrixAttempt(context.Background(), 3100000400, open[3100000400], target, map[string]int{"claude": 1}, d); !ok {
		t.Fatal("granted comment did not launch")
	}
	f.failReplies = 1
	c.replySourceStarted(context.Background(), 3100000400)
	c.sourceTick(context.Background()) // the POST fails; the reply stays queued
	c.sourceTick(context.Background())
	if got := f.markers(); len(got) != 1 || got[0] != "example/project#7 "+sourceMarker(3100000400, "started", "", "") {
		t.Fatalf("started reply after an outage: %v", got)
	}
	if _, ok := c.matrixAttempt(context.Background(), 3100000400, open[3100000400], target, map[string]int{"claude": 1}, d); ok {
		t.Fatal("same comment launched twice on the next poll")
	}
	c.st = loadState(c.st.path)
	open = map[int]Issue{}
	c.pollSourceBindings(context.Background(), open, map[int]bool{})
	if _, ok := c.matrixAttempt(context.Background(), 3100000400, open[3100000400], target, map[string]int{"claude": 1}, d); ok {
		t.Fatal("same comment launched again after restart")
	}
	if launches != 1 {
		t.Fatalf("launches=%d, want 1", launches)
	}
}

// B4 (re-review): GitHub's since is exclusive and whole-second. A granted
// trigger sharing its second with a full page of other comments is still read.
func TestSourceEqualSecondGroupIsReadWhole(t *testing.T) {
	c, f, path := sourceFixture(t)
	startSource(t, c)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	c.st.SourceCursors[sourceRepo], c.st.SourceStarts[sourceRepo] = base, base
	group := base.Add(30 * time.Minute)
	for i := 0; i < 100; i++ {
		f.post(sourceRepo, 2063, int64(3300000000+i), 4242, "ordinary comment", group)
	}
	f.post(sourceRepo, 2063, 3100000500, ownerGitHubID, sourceTrigger, group)
	f.post(sourceRepo, 2063, 3300000999, 4242, "later comment", group.Add(time.Second))
	_ = c.st.save()
	for tick := 0; tick < 6 && c.st.SourceBindings[3100000500] == nil; tick++ {
		c = &Coord{cfg: c.cfg, st: loadState(path), source: f, sourceGrant: c.sourceGrant}
		c.sourceTick(context.Background())
	}
	if c.st.SourceBindings[3100000500] == nil {
		t.Fatal("a granted trigger sharing its second with a full page was skipped")
	}
}

// B4 (re-review): every reply POST goes through one per-tick budget, and every
// binding's reply is delivered over the following ticks.
func TestSourceRepliesShareOneBudget(t *testing.T) {
	c, f, _ := sourceFixture(t)
	at := startSource(t, c)
	for i := 0; i < 20; i++ {
		key := 3100000600 + i
		f.post(sourceRepo, 2063, int64(key), ownerGitHubID, sourceTrigger, at)
	}
	c.sourceTick(context.Background())
	for i := 0; i < 20; i++ {
		key := 3100000600 + i
		c.st.Jobs[key] = &Job{Issue: key, Host: "h1", Agent: "codex", Repo: sourceRepo, PR: 7, Home: &dispatchIssue{Repo: sourceRepo, Number: 2063}}
	}
	for tick := 0; tick < 8; tick++ {
		before := len(f.replies)
		c.sourceTick(context.Background())
		if posted := len(f.replies) - before; posted > sourceRepliesPerTick {
			t.Fatalf("tick %d posted %d replies", tick, posted)
		}
	}
	if len(f.replies) != 40 {
		t.Fatalf("%d of 40 started and pr replies delivered", len(f.replies))
	}
}

// B4 (re-review): admitting a granted comment reads nothing from GitHub; the
// source feed's budgeted check is the only liveness read.
func TestSourceGrantAdmissionMakesNoRead(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
	body := "/swarm\nharness: claude\nrepo: example/project\n\nFix it."
	r.Trigger = ownerNativeCommentTrigger
	r.ExpectedBriefDigest = shaText([]byte(body))
	if s.install(context.Background(), r).State != "LIVE" {
		t.Fatal("grant not LIVE")
	}
	reads := 0
	command := s.deps.command
	s.deps.command = func(ctx context.Context, dir, name string, in []byte, args ...string) ([]byte, error) {
		reads++
		return command(ctx, dir, name, in, args...)
	}
	binding := Issue{ID: is.ID, Number: 3100000700, Title: is.Title, Body: body, Labels: []string{"fixture-target"}, Source: &issueSource{Repo: "example/project", Number: 7}}
	for i := 0; i < 44; i++ {
		if _, err := s.matrixForIssue(binding, "example/project"); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 0 {
		t.Fatalf("admission made %d GitHub reads", reads)
	}
}

// After a restart a binding launches only once a budgeted check confirms it;
// never-checked bindings are checked first, and an unchecked one is never torn
// down.
func TestSourceUnconfirmedBindingIsHeldNotDropped(t *testing.T) {
	c, f, path := sourceFixture(t)
	at := startSource(t, c)
	f.post(sourceRepo, 2063, 3100000800, ownerGitHubID, sourceTrigger, at)
	c.sourceTick(context.Background())
	restarted := &Coord{cfg: c.cfg, st: loadState(path), source: f, sourceGrant: c.sourceGrant}
	restarted.sourceMem().pauseTill = time.Now().Add(time.Hour) // no reads at all
	open, all := map[int]Issue{}, map[int]bool{}
	restarted.pollSourceBindings(context.Background(), open, all)
	if _, admitted := open[3100000800]; admitted || !all[3100000800] {
		t.Fatal("an unconfirmed binding was admitted, or dropped")
	}
	restarted.sourceMem().pauseTill = time.Time{}
	open, all = map[int]Issue{}, map[int]bool{}
	restarted.pollSourceBindings(context.Background(), open, all)
	if _, admitted := open[3100000800]; !admitted {
		t.Fatal("a confirmed binding was not admitted")
	}
}

// Owner rule: a refused trigger logs one plain reason, once.
func TestSourceRefusalLogsOneReason(t *testing.T) {
	c, f, _ := sourceFixture(t)
	at := startSource(t, c)
	var logs bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prior)
	f.failReplies = 2
	f.post(sourceRepo, 2063, 3100000900, ownerGitHubID, strings.Replace(sourceTrigger, "Fix the parser.", "Other.", 1), at)
	for tick := 0; tick < 4; tick++ {
		c.sourceTick(context.Background())
	}
	if n := strings.Count(logs.String(), "comment 3100000900 refused (source-grant-missing): "+matrixReasons["source-grant-missing"].hint); n != 1 {
		t.Fatalf("refusal logged %d times:\n%s", n, logs.String())
	}
	if len(f.replies) != 1 {
		t.Fatal("refusal reply not delivered after the outage")
	}
}
