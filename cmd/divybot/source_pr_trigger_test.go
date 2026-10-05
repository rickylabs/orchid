package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

const launchMarker = "<!-- cockpit:launch v1 2de1909798125e4262300ff385b6379f -->"

// A Cockpit "Fix this" comment: the brief, then its launch identity as the last line.
var markedTrigger = sourceTrigger + "\n\n" + launchMarker + "\n"

// prFixture adds pull request #2078 to the source fixture and grants its marked trigger.
func prFixture(t *testing.T) (*Coord, *fakeSource, time.Time) {
	t.Helper()
	c, f, _ := sourceFixture(t)
	f.issues[sourceRepo+"#2078"] = sourceIssueView{NodeID: "PR_2078", Title: "A pull request", State: "OPEN", PR: true}
	granted := c.sourceGrant
	c.sourceGrant = func(repo string, n int, issueID, body string, key int) bool {
		if n == 2078 {
			return repo == sourceRepo && issueID == "PR_2078" && body == markedTrigger
		}
		return granted(repo, n, issueID, body, key)
	}
	return c, f, startSource(t, c)
}

// postPR posts a comment on the pull request's conversation, as GitHub lists it.
func postPR(f *fakeSource, id, author int64, body string, at time.Time) {
	f.post(sourceRepo, 2078, id, author, body, at)
	f.mu.Lock()
	f.feed[sourceRepo][len(f.feed[sourceRepo])-1].HTMLURL = fmt.Sprintf("https://github.com/%s/pull/2078#issuecomment-%d", sourceRepo, id)
	f.mu.Unlock()
}

func captureSourceLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &out
}

// The owner's Cockpit launch comment on a pull request is a trigger: it binds
// to the pull request and is admitted on its grant.
func TestSourceTriggerOwnerLaunchOnPullRequestBinds(t *testing.T) {
	c, f, at := prFixture(t)
	postPR(f, 6001886192, ownerGitHubID, markedTrigger, at)
	c.sourceTick(context.Background())
	b := c.st.SourceBindings[6001886192]
	if b == nil || b.Repo != sourceRepo || b.Issue != 2078 || b.IssueID != "PR_2078" || b.Closed != "" || b.Body != markedTrigger {
		t.Fatalf("the owner's launch comment on a pull request did not bind: %+v", b)
	}
}

// Plain pull request conversation stays ignored, and each ignored /swarm comment
// says why exactly once; a comment that is not /swarm is not logged at all.
func TestSourceTriggerPullRequestCommentsWithoutTheMarkerAreIgnoredAndLogged(t *testing.T) {
	c, f, at := prFixture(t)
	out := captureSourceLog(t)
	cases := map[int64]string{
		6100000001: sourceTrigger,                                       // owner /swarm, no marker
		6100000002: markedTrigger + "\nOne more line.",                  // marker not the last line
		6100000003: strings.Replace(markedTrigger, "v1", "v2", 1),       // another marker version
		6100000004: strings.Replace(markedTrigger, "2de19", "2DE19", 1), // not lower-case hex
		6100000005: strings.Replace(markedTrigger, "2de19", "2de1", 1),  // 31 hex digits
	}
	for id, body := range cases {
		postPR(f, id, ownerGitHubID, body, at)
	}
	postPR(f, 6100000006, 4242, markedTrigger, at)                // marked, not the owner
	postPR(f, 6100000007, ownerGitHubID, "Looks good to me.", at) // conversation
	// None of them costs an issue read: an unreadable issue changes nothing.
	f.failIssue = true
	c.sourceTick(context.Background())
	c.sourceTick(context.Background()) // a re-read logs nothing new
	if len(c.st.SourceBindings) != 0 {
		t.Fatalf("an unmarked or foreign pull request comment bound: %v", c.st.SourceBindings)
	}
	logged := out.String()
	for id := range cases {
		line := fmt.Sprintf("comment %d ignored: a pull request comment without the Cockpit launch marker", id)
		if strings.Count(logged, line) != 1 {
			t.Fatalf("comment %d: want exactly one reason line, log:\n%s", id, logged)
		}
	}
	if strings.Count(logged, "comment 6100000006 ignored: not written by the owner") != 1 {
		t.Fatalf("a foreign /swarm was ignored silently or repeatedly:\n%s", logged)
	}
	if strings.Contains(logged, "6100000007") {
		t.Fatalf("ordinary conversation was logged:\n%s", logged)
	}
}

// GitHub can list a pull request comment under an issue URL; the issue read still
// says it is a pull request, and only the marker makes it a trigger.
func TestSourceTriggerPullRequestReadNeedsTheMarker(t *testing.T) {
	c, f, at := prFixture(t)
	out := captureSourceLog(t)
	f.post(sourceRepo, 2078, 6200000001, ownerGitHubID, sourceTrigger, at)
	f.post(sourceRepo, 2078, 6200000002, ownerGitHubID, markedTrigger, at)
	c.sourceTick(context.Background())
	if c.st.SourceBindings[6200000001] != nil {
		t.Fatal("an unmarked comment on a pull request bound")
	}
	if !strings.Contains(out.String(), "comment 6200000001 ignored: a pull request comment without the Cockpit launch marker") {
		t.Fatalf("ignored silently:\n%s", out.String())
	}
	if b := c.st.SourceBindings[6200000002]; b == nil || b.Issue != 2078 || b.Closed != "" {
		t.Fatalf("the marked comment did not bind: %+v", b)
	}
}

// Every ignored owner /swarm comment says why once: edited, written before the
// first scan, or seen too late. Before, the pre-start case was silent and the
// others repeated on every re-read.
func TestSourceTriggerIgnoredOwnerSwarmLogsOnce(t *testing.T) {
	c, f, _ := sourceFixture(t)
	before := time.Now().Add(-time.Hour)
	at := startSource(t, c)
	out := captureSourceLog(t)
	f.post(sourceRepo, 2063, 6300000001, ownerGitHubID, sourceTrigger, at)
	f.feed[sourceRepo][0].UpdatedAt = at.Add(time.Minute)
	f.post(sourceRepo, 2063, 6300000002, ownerGitHubID, sourceTrigger, before)
	c.st.SourceCursors[sourceRepo] = before.Add(-time.Minute)
	stale := sourceComment{ID: 6300000003, Body: sourceTrigger, AuthorID: ownerGitHubID, HTMLURL: "https://github.com/" + sourceRepo + "/issues/2063",
		IssueURL: "https://api.github.com/repos/" + sourceRepo + "/issues/2063", CreatedAt: at, UpdatedAt: at}
	if !c.considerSourceComment(context.Background(), func(int) (sourceIssueView, error) { return sourceIssueView{}, nil }, sourceRepo, stale, at.Add(48*time.Hour), at.Add(-time.Second)) {
		t.Fatal("a stale comment asked to be read again")
	}
	c.sourceTick(context.Background())
	c.sourceTick(context.Background())
	logged := out.String()
	for _, want := range []string{
		"comment 6300000001 ignored: it was edited",
		"comment 6300000002 ignored: it was written before this repository's first scan",
		"comment 6300000003 ignored: it is older than 24h0m0s",
	} {
		if strings.Count(logged, want) != 1 {
			t.Fatalf("want exactly one %q, log:\n%s", want, logged)
		}
	}
	if len(c.st.SourceBindings) != 0 {
		t.Fatal("an ignored comment bound")
	}
}

func TestCockpitLaunchMarkerIsTheLastLine(t *testing.T) {
	for body, want := range map[string]bool{
		markedTrigger:               true,
		markedTrigger + "\n\n \t\n": true, // blank lines after it
		sourceTrigger + "\n```\ncode\n```\n" + launchMarker:    true, // a fence closed before it
		sourceTrigger + "\n~~~~\ncode\n~~~~~\n" + launchMarker: true,
		sourceTrigger + "\n``` x`y\n" + launchMarker:           true,  // not a fence: backtick in its info string
		sourceTrigger + "\n" + launchMarker + " ":              false, // trailing space on the marker line
		sourceTrigger + "\n" + launchMarker + "\t":             false, // trailing tab
		sourceTrigger + "\n" + launchMarker + "\r":             false, // stray CR
		sourceTrigger + "\n " + launchMarker:                   false, // indented
		sourceTrigger + "\n```\n" + launchMarker:               false, // inside an open backtick fence
		sourceTrigger + "\n  ~~~ text\n" + launchMarker:        false, // inside an open tilde fence
		sourceTrigger + "\n````\n```\n" + launchMarker:         false, // a shorter fence does not close it
		sourceTrigger + "\n```\n~~~\n" + launchMarker:          false, // the other fence char does not close it
		sourceTrigger:                                                   false,
		launchMarker + "\n" + sourceTrigger:                             false,
		sourceTrigger + "\n> " + launchMarker:                           false,
		sourceTrigger + "\n" + strings.TrimSuffix(launchMarker, " -->"): false,
		"": false,
	} {
		if hasCockpitLaunchMarker(body) != want {
			t.Fatalf("marker(%q) != %v", body, want)
		}
	}
}

// Through the trigger path: a marker with trailing whitespace or inside an open
// fence is not a launch. The comment binds nothing and costs no issue read.
func TestSourceTriggerMalformedOrFencedPRMarkerIsIgnoredBeforeIssueRead(t *testing.T) {
	cases := map[string]string{
		"trailing space":      sourceTrigger + "\n\n" + launchMarker + " \n",
		"trailing tab":        sourceTrigger + "\n\n" + launchMarker + "\t\n",
		"open backtick fence": sourceTrigger + "\n```\n" + launchMarker + "\n",
		"open tilde fence":    sourceTrigger + "\n~~~\n" + launchMarker + "\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c, _, at := prFixture(t)
			reads := 0
			lookup := func(int) (sourceIssueView, error) {
				reads++
				return sourceIssueView{NodeID: "PR_2078", State: "OPEN", PR: true}, nil
			}
			cm := sourceComment{ID: 6400000001, Body: body, AuthorID: ownerGitHubID, CreatedAt: at, UpdatedAt: at,
				HTMLURL:  fmt.Sprintf("https://github.com/%s/pull/2078#issuecomment-6400000001", sourceRepo),
				IssueURL: fmt.Sprintf("https://api.github.com/repos/%s/issues/2078", sourceRepo)}
			if !c.considerSourceComment(context.Background(), lookup, sourceRepo, cm, at, at.Add(-time.Second)) {
				t.Fatal("asked to be read again")
			}
			if reads != 0 || len(c.st.SourceBindings) != 0 {
				t.Fatalf("%s: %d issue reads, %d bindings; want none", name, reads, len(c.st.SourceBindings))
			}
		})
	}
	// Control: the same comment with the exact marker, CRLF line ends, binds.
	c, _, at := prFixture(t)
	cm := sourceComment{ID: 6400000002, Body: strings.ReplaceAll(markedTrigger, "\n", "\r\n"), AuthorID: ownerGitHubID, CreatedAt: at, UpdatedAt: at,
		HTMLURL:  fmt.Sprintf("https://github.com/%s/pull/2078#issuecomment-6400000002", sourceRepo),
		IssueURL: fmt.Sprintf("https://api.github.com/repos/%s/issues/2078", sourceRepo)}
	lookup := func(int) (sourceIssueView, error) {
		return sourceIssueView{NodeID: "PR_2078", State: "OPEN", PR: true}, nil
	}
	c.considerSourceComment(context.Background(), lookup, sourceRepo, cm, at, at.Add(-time.Second))
	if c.st.SourceBindings[6400000002] == nil {
		t.Fatal("control: an exact marker with CRLF line ends did not bind")
	}
}

// At capacity the ignored-comment memory keeps what it has: a remembered comment
// re-read stays quiet, a new one is not logged one by one, and one line says so.
func TestSourceTriggerIgnoredLogStaysQuietAtCapacity(t *testing.T) {
	c, _, at := prFixture(t)
	out := captureSourceLog(t)
	none := func(int) (sourceIssueView, error) {
		t.Fatal("an unmarked PR comment read its issue")
		return sourceIssueView{}, nil
	}
	ignore := func(id int64) {
		cm := sourceComment{ID: id, Body: sourceTrigger, AuthorID: ownerGitHubID, CreatedAt: at, UpdatedAt: at,
			HTMLURL:  fmt.Sprintf("https://github.com/%s/pull/2078#issuecomment-%d", sourceRepo, id),
			IssueURL: fmt.Sprintf("https://api.github.com/repos/%s/issues/2078", sourceRepo)}
		c.considerSourceComment(context.Background(), none, sourceRepo, cm, at, at.Add(-time.Second))
	}
	first := int64(6500000000)
	for i := int64(0); i < sourceIgnoredLogLimit; i++ {
		ignore(first + i)
	}
	if got := strings.Count(out.String(), " ignored: "); got != sourceIgnoredLogLimit {
		t.Fatalf("logged %d of %d distinct ignored comments", got, sourceIgnoredLogLimit)
	}
	ignore(first)                         // a re-read at the boundary
	ignore(first + sourceIgnoredLogLimit) // one past it
	ignore(first + sourceIgnoredLogLimit + 1)
	ignore(first) // and a re-read after
	logged := out.String()
	if strings.Count(logged, fmt.Sprintf("comment %d ignored:", first)) != 1 {
		t.Fatal("a remembered comment was logged again")
	}
	if strings.Count(logged, " ignored: ") != sourceIgnoredLogLimit {
		t.Fatal("a comment past the bound was logged one by one")
	}
	if strings.Count(logged, "further ignored comments are not logged one by one") != 1 {
		t.Fatalf("want exactly one saturation line:\n%s", logged[len(logged)-400:])
	}
}
