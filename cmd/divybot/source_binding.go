package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A source binding is a launch triggered by a /swarm comment on the source issue
// itself, with no inbox copy. Its identity is repository + issue + comment ID; the
// comment's REST ID is also its job key, which is far above any inbox issue
// number, so a call site that still addresses the inbox fails on a missing issue
// instead of touching another one. Every reply goes to the source issue.
//
// A comment launches only when it matches a comment grant the owner installed
// through the owner endpoint before posting it (owner rule, 2026-10-05). The
// author ID only filters the feed: Orchid, the Cockpit and agents share it.

// ownerGitHubID is the only author whose comment can be a trigger. Checked by
// numeric ID, never by login.
const ownerGitHubID int64 = 129366361

const (
	// Comment pages read per repository per tick, and the GitHub reads one tick may
	// spend listing comments and checking open bindings. 304 Not Modified answers
	// to conditional reads do not count against GitHub's primary rate limit, but
	// they count here, so the hourly worst case stays bounded.
	sourceCommentPages         = 5
	sourceListRequestsPerTick  = 12
	sourceCheckRequestsPerTick = 12
	sourceRepliesPerTick       = 6
	// Below this many remaining core requests, source reads pause until the reset.
	sourceRateFloor = 300
	// Re-read window behind the newest comment seen, for listing lag. Comment IDs
	// already bound are never acted on twice.
	sourceCursorOverlap = 2 * time.Minute
	// A comment first seen more than this long after it was created is never a
	// trigger.
	sourceTriggerFreshness = 24 * time.Hour
	// Closed bindings are kept this long to deduplicate re-read comments.
	sourceBindingRetention = 7 * 24 * time.Hour
	sourcePageSize         = 100
)

const sourceMarkerPrefix = "<!-- orchid:binding v1 "

type sourceBinding struct {
	Repo      string          `json:"repo"`
	Issue     int             `json:"issue"`
	Comment   int64           `json:"comment"`
	IssueID   string          `json:"issue_id,omitempty"` // the source issue's node ID
	Title     string          `json:"title,omitempty"`
	Body      string          `json:"body,omitempty"` // the trigger comment, LF only
	URL       string          `json:"url,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	Closed    string          `json:"closed,omitempty"` // terminal decision; "" while open
	Launched  bool            `json:"launched,omitempty"`
	Replies   map[string]bool `json:"replies,omitempty"` // delivered
	Outbox    []sourceReply   `json:"outbox,omitempty"`  // decided, not yet delivered
}

// A reply is decided and persisted before it is posted, and posted until GitHub
// accepts it: a transient failure delays the acknowledgement, never loses it.
type sourceReply struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// sourceScan is a listing in progress across ticks. Since moves to the last
// comment of each full page (or Page advances when a page shares one time), so a
// burst longer than one tick's pages is read to its end instead of re-read.
type sourceScan struct {
	Since  time.Time `json:"since"`
	Page   int       `json:"page"`
	Newest time.Time `json:"newest"`
}

// issueSource marks an Issue as a source binding and names its home issue.
type issueSource struct {
	Repo   string
	Number int
}

type sourceComment struct {
	ID        int64
	Body      string
	HTMLURL   string
	IssueURL  string
	AuthorID  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type sourceIssueView struct {
	NodeID string
	Title  string
	State  string // OPEN | CLOSED | GONE
	PR     bool
}

// sourceMeta is what a conditional read reports besides its body.
type sourceMeta struct {
	ETag        string
	NotModified bool
	Remaining   int // -1 when unknown
	Reset       time.Time
}

// sourceGitHub is the binding's whole GitHub surface, so a fake can stand in for
// it. Reads are conditional on a previous ETag.
type sourceGitHub interface {
	comments(ctx context.Context, repo, since string, page int, etag string) ([]sourceComment, sourceMeta, error)
	issue(ctx context.Context, repo string, n int, etag string) (sourceIssueView, sourceMeta, error)
	comment(ctx context.Context, repo string, id int64, etag string) (bool, sourceMeta, error)
	reply(ctx context.Context, repo string, n int, body string) error
}

type ghSource struct{}

var errSourceRead = errors.New("source read failed")

// ghConditional performs one conditional GET and returns its status, body and
// headers. gh exits non-zero for 304 and 404, which are answers, not failures.
func ghConditional(ctx context.Context, path, etag string) (int, []byte, sourceMeta, error) {
	defer children.hold()()
	meta := sourceMeta{Remaining: -1}
	args := []string{"api", "-i"}
	if etag != "" {
		args = append(args, "-H", "If-None-Match: "+etag)
	}
	cmd := exec.CommandContext(ctx, "gh", append(args, path)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	runErr := cmd.Run()
	raw := out.Bytes()
	head, body, found := bytes.Cut(raw, []byte("\r\n\r\n"))
	if !found {
		head, body, found = bytes.Cut(raw, []byte("\n\n"))
	}
	lines := strings.Split(strings.ReplaceAll(string(head), "\r\n", "\n"), "\n")
	fields := strings.Fields(lines[0])
	if !found || len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return 0, nil, meta, errSourceRead
	}
	status, _ := strconv.Atoi(fields[1])
	for _, line := range lines[1:] {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(name) {
		case "etag":
			meta.ETag = value
		case "x-ratelimit-remaining":
			if v, e := strconv.Atoi(value); e == nil {
				meta.Remaining = v
			}
		case "x-ratelimit-reset":
			if v, e := strconv.ParseInt(value, 10, 64); e == nil {
				meta.Reset = time.Unix(v, 0)
			}
		}
	}
	switch {
	case status == 304:
		meta.NotModified = true
		return status, nil, meta, nil
	case status == 404 || (status >= 200 && status < 300):
		return status, body, meta, nil
	}
	if runErr == nil {
		runErr = errSourceRead
	}
	return status, nil, meta, runErr
}

func (ghSource) comments(ctx context.Context, repo, since string, page int, etag string) ([]sourceComment, sourceMeta, error) {
	status, body, meta, err := ghConditional(ctx, sourceCommentsPath(repo, since, page), etag)
	if err != nil || meta.NotModified {
		return nil, meta, err
	}
	if status != 200 {
		return nil, meta, errSourceRead
	}
	var raw []struct {
		ID        int64     `json:"id"`
		Body      string    `json:"body"`
		HTMLURL   string    `json:"html_url"`
		IssueURL  string    `json:"issue_url"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		User      struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return nil, meta, errSourceRead
	}
	out := make([]sourceComment, 0, len(raw))
	for _, r := range raw {
		out = append(out, sourceComment{ID: r.ID, Body: r.Body, HTMLURL: r.HTMLURL, IssueURL: r.IssueURL, AuthorID: r.User.ID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	return out, meta, nil
}

func (ghSource) issue(ctx context.Context, repo string, n int, etag string) (sourceIssueView, sourceMeta, error) {
	status, body, meta, err := ghConditional(ctx, fmt.Sprintf("repos/%s/issues/%d", repo, n), etag)
	if err != nil || meta.NotModified {
		return sourceIssueView{}, meta, err
	}
	if status == 404 {
		return sourceIssueView{State: "GONE"}, meta, nil
	}
	var raw struct {
		NodeID      string          `json:"node_id"`
		Title       string          `json:"title"`
		State       string          `json:"state"`
		PullRequest *map[string]any `json:"pull_request"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return sourceIssueView{}, meta, errSourceRead
	}
	return sourceIssueView{NodeID: raw.NodeID, Title: raw.Title, State: strings.ToUpper(raw.State), PR: raw.PullRequest != nil}, meta, nil
}

func (ghSource) comment(ctx context.Context, repo string, id int64, etag string) (bool, sourceMeta, error) {
	status, _, meta, err := ghConditional(ctx, fmt.Sprintf("repos/%s/issues/comments/%d", repo, id), etag)
	if err != nil {
		return false, meta, err
	}
	return meta.NotModified || status != 404, meta, nil
}

func (ghSource) reply(ctx context.Context, repo string, n int, body string) error {
	f, err := os.CreateTemp("", "source-reply-*.md")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(body)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	_, err = run(ctx, "gh", "issue", "comment", strconv.Itoa(n), "--repo", repo, "--body-file", f.Name())
	return err
}

func sourceCommentsPath(repo, since string, page int) string {
	return fmt.Sprintf("repos/%s/issues/comments?since=%s&sort=updated&direction=asc&per_page=%d&page=%d", repo, since, sourcePageSize, page)
}

// sourceMemory is in-memory read state: ETags and what they answered, the last
// known liveness of each binding, fairness offsets and the rate-limit pause. A
// restart loses it and costs one unconditional read each.
type sourceMemory struct {
	mu        sync.Mutex
	etags     map[string]string
	pages     map[string]sourcePageSummary
	issues    map[string]sourceIssueView
	open      map[int]bool
	repoTurn  int
	checkTurn int
	pauseTill time.Time
}

type sourcePageSummary struct {
	Count  int
	Last   time.Time
	Newest time.Time
}

var sourceMemoryInit sync.Mutex

func (c *Coord) sourceMem() *sourceMemory {
	sourceMemoryInit.Lock()
	defer sourceMemoryInit.Unlock()
	if c.srcMem == nil {
		c.srcMem = &sourceMemory{etags: map[string]string{}, pages: map[string]sourcePageSummary{}, issues: map[string]sourceIssueView{}, open: map[int]bool{}}
	}
	return c.srcMem
}

// noteRate pauses source reads near the end of the core budget, until its reset.
func (m *sourceMemory) noteRate(meta sourceMeta, now time.Time) {
	if meta.Remaining < 0 || meta.Remaining >= sourceRateFloor { // guard:source-rate-floor
		return
	}
	until := meta.Reset
	if !until.After(now) {
		until = now.Add(15 * time.Minute)
	}
	m.mu.Lock()
	if until.After(m.pauseTill) {
		m.pauseTill = until
	}
	m.mu.Unlock()
	log.Printf("source triggers: GitHub core budget at %d; source reads paused until %s", meta.Remaining, until.UTC().Format(time.RFC3339))
}

func (m *sourceMemory) paused(now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return now.Before(m.pauseTill)
}

func (c *Coord) sourceAPI() sourceGitHub {
	if c.source != nil {
		return c.source
	}
	return ghSource{}
}

// sourceTriggerBody returns the trigger body with CRLF line ends made LF, and
// whether the comment is a trigger at all: its first line is exactly /swarm.
// A carriage return left after that is an encoding refusal, never a value.
func sourceTriggerBody(raw string) (body string, trigger bool, encodingOK bool) {
	body = strings.ReplaceAll(raw, "\r\n", "\n")
	first, _, _ := strings.Cut(body, "\n")
	if first != "/swarm" { // guard:source-first-line
		return body, false, false
	}
	return body, true, !strings.Contains(body, "\r")
}

func sourceIssueNumber(issueURL string) int {
	i := strings.LastIndex(issueURL, "/")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(issueURL[i+1:])
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// sourceRepos are the configured target repositories, each once.
func (c *Coord) sourceRepos() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range c.cfg.Targets {
		key := strings.ToLower(t.Repo)
		if !repositoryName.MatchString(t.Repo) || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t.Repo)
	}
	return out
}

// sourceTick delivers pending replies, then reads new comments on the configured
// target repositories, in turn, within one tick's read budget. The first run on
// a repository only records where it started: nothing created before that ever
// launches.
func (c *Coord) sourceTick(ctx context.Context) {
	now := time.Now().UTC()
	c.catchUpSourceReplies(ctx)
	defer c.deliverSourceReplies(ctx) // after this tick's decisions, within its reply budget
	mem := c.sourceMem()
	if mem.paused(now) {
		return
	}
	repos := c.sourceRepos()
	mem.mu.Lock()
	turn := mem.repoTurn
	mem.mu.Unlock()
	budget, scanned := sourceListRequestsPerTick, 0
	for i := range repos {
		if budget <= 0 {
			break
		}
		repo := repos[(turn+i)%len(repos)]
		budget = c.scanSourceRepo(ctx, repo, now, budget)
		scanned++
	}
	// The next tick starts where this one stopped, so every repository gets turns.
	mem.mu.Lock()
	mem.repoTurn = turn + scanned // guard:source-repo-turns
	mem.mu.Unlock()
	c.pruneSourceBindings(now)
}

// scanSourceRepo reads one repository's comments from its scan position and
// returns the budget left. Progress is persisted page by page; a failed page is
// read again next tick, and the cursor moves only when a scan reaches its end.
func (c *Coord) scanSourceRepo(ctx context.Context, repo string, now time.Time, budget int) int {
	api, mem := c.sourceAPI(), c.sourceMem()
	c.st.mu.Lock()
	if c.st.SourceCursors == nil {
		c.st.SourceCursors = map[string]time.Time{}
	}
	if c.st.SourceStarts == nil {
		c.st.SourceStarts = map[string]time.Time{}
	}
	if c.st.SourceScans == nil {
		c.st.SourceScans = map[string]sourceScan{}
	}
	cursor, started := c.st.SourceCursors[repo]
	if !started { // guard:source-first-start
		c.st.SourceCursors[repo] = now
		c.st.SourceStarts[repo] = now
		_ = c.st.saveLocked()
		c.st.mu.Unlock()
		return budget
	}
	if _, ok := c.st.SourceStarts[repo]; !ok {
		c.st.SourceStarts[repo] = cursor
	}
	start := c.st.SourceStarts[repo]
	scan, scanning := c.st.SourceScans[repo]
	c.st.mu.Unlock()
	if !scanning || scan.Since.IsZero() {
		scan = sourceScan{Since: cursor, Page: 1, Newest: cursor}
	}
	completed := false
	// Issue reads for new triggers share the listing budget; one read per issue
	// per scan, however many triggers it carries.
	views := map[int]sourceIssueView{}
	lookup := func(n int) (sourceIssueView, error) {
		if v, ok := views[n]; ok {
			return v, nil
		}
		if budget <= 0 { // guard:source-bind-budget
			return sourceIssueView{}, errSourceRead
		}
		budget--
		v, meta, err := api.issue(ctx, repo, n, "")
		mem.noteRate(meta, now)
		if err == nil {
			views[n] = v
		}
		return v, err
	}
	for read := 0; read < sourceCommentPages && budget > 0; read++ {
		budget--
		since := scan.Since.UTC().Format(time.RFC3339)
		path := sourceCommentsPath(repo, since, scan.Page)
		mem.mu.Lock()
		etag := mem.etags[path]
		mem.mu.Unlock()
		list, meta, err := api.comments(ctx, repo, since, scan.Page, etag)
		mem.noteRate(meta, now)
		if err != nil {
			log.Printf("source triggers: list %s failed: %v", repo, err)
			break
		}
		var summary sourcePageSummary
		if meta.NotModified {
			mem.mu.Lock()
			cached, ok := mem.pages[path]
			mem.mu.Unlock()
			if !ok {
				break // a 304 for a page this process never read; read it unconditionally next tick
			}
			summary = cached
		} else {
			handled := true
			for _, cm := range list {
				if !c.considerSourceComment(ctx, lookup, repo, cm, now, start) {
					handled = false
				}
				if cm.UpdatedAt.After(summary.Newest) {
					summary.Newest = cm.UpdatedAt
				}
			}
			summary.Count = len(list)
			if len(list) > 0 {
				summary.Last = list[len(list)-1].UpdatedAt
			}
			if !handled {
				break // read this page again; nothing on it is lost
			}
			mem.mu.Lock()
			if meta.ETag != "" {
				mem.etags[path], mem.pages[path] = meta.ETag, summary
			} else {
				delete(mem.etags, path)
			}
			mem.mu.Unlock()
		}
		if summary.Newest.After(scan.Newest) {
			scan.Newest = summary.Newest
		}
		if summary.Count < sourcePageSize {
			completed = true
			break
		}
		// GitHub's since is exclusive and whole-second: continue one second before
		// the page's last update, so the rest of that second is read too, and page
		// within it when that makes no progress.
		if next := summary.Last.Add(-time.Second); next.After(scan.Since) { // guard:source-scan-advance
			scan.Since, scan.Page = next, 1
		} else {
			scan.Page++
		}
	}
	c.st.mu.Lock()
	if completed {
		// The overlap covers listing lag behind now: the cursor never passes the
		// newest comment read, nor now minus the overlap, so a quiet repository's
		// re-read window ages out instead of being listed again every tick.
		next := scan.Newest.Add(-time.Second) // exclusive since: re-read the newest second
		if lag := now.Add(-sourceCursorOverlap); lag.Before(next) {
			next = lag
		}
		if next.After(c.st.SourceCursors[repo]) { // guard:source-cursor-advance
			c.st.SourceCursors[repo] = next
		}
		delete(c.st.SourceScans, repo)
	} else {
		c.st.SourceScans[repo] = scan
	}
	_ = c.st.saveLocked()
	c.st.mu.Unlock()
	return budget
}

// considerSourceComment binds one comment if it is a fresh, unedited owner
// trigger created after this repository's first scan. It returns false only
// when the comment must be read again (a transient failure).
func (c *Coord) considerSourceComment(ctx context.Context, lookup func(int) (sourceIssueView, error), repo string, cm sourceComment, now, start time.Time) bool {
	if cm.ID <= 0 || strings.Contains(cm.HTMLURL, "/pull/") {
		return true // PR conversation comments share the feed; never a trigger
	}
	key := int(cm.ID)
	c.st.mu.Lock()
	_, bound := c.st.SourceBindings[key]
	c.st.mu.Unlock()
	if bound { // guard:source-once
		return true // edits and re-reads never relaunch
	}
	if cm.AuthorID != ownerGitHubID { // guard:source-owner
		return true
	}
	body, trigger, encodingOK := sourceTriggerBody(cm.Body)
	if !trigger {
		return true
	}
	// A trigger is a new comment: never one written before the first scan, never
	// an edited one, and never one first seen long after it was written.
	if !cm.UpdatedAt.Equal(cm.CreatedAt) { // guard:source-unedited
		log.Printf("source triggers: %s comment %d was edited; an edited comment is never a trigger", repo, cm.ID)
		return true
	}
	if cm.CreatedAt.Before(start) { // guard:source-after-start
		return true
	}
	if now.Sub(cm.CreatedAt) > sourceTriggerFreshness { // guard:source-fresh
		log.Printf("source triggers: %s comment %d is older than %s; not a trigger", repo, cm.ID, sourceTriggerFreshness)
		return true
	}
	n := sourceIssueNumber(cm.IssueURL)
	if n == 0 {
		return true
	}
	view, err := lookup(n)
	if err != nil {
		log.Printf("source triggers: %s#%d unreadable: %v", repo, n, err)
		return false
	}
	if view.PR {
		return true
	}
	b := &sourceBinding{Repo: repo, Issue: n, Comment: cm.ID, IssueID: view.NodeID, Title: view.Title, Body: body, URL: cm.HTMLURL, CreatedAt: cm.CreatedAt}
	refusal := ""
	switch {
	case !encodingOK:
		refusal = "brief-encoding-invalid"
	case view.State != "OPEN":
		refusal = "source-issue-closed"
	case !cleanText(view.NodeID):
		refusal = "issue-identity-missing"
	default:
		if o := parseOverrides(body); o.RepoInvalid {
			refusal = "source-repo-invalid"
		} else if o.Repo != "" && !strings.EqualFold(o.Repo, repo) { // guard:source-repo-match
			refusal = "source-repo-mismatch"
		} else if !c.sourceGrantReady(repo, n, view.NodeID, body, key) { // guard:source-grant-required
			refusal = "source-grant-missing"
		}
	}
	if refusal != "" {
		// The decision and its reply are persisted together, then delivered.
		b.Closed = "refused"
		b.Outbox = []sourceReply{sourceReplyFor(b, "refused", refusal, "", fmt.Sprintf("divybot: launch refused. Reason: `%s`. %s Nothing was launched. Post a new /swarm comment to try again.", refusal, matrixReasons[refusal].hint))}
	}
	c.st.mu.Lock()
	if c.st.SourceBindings == nil {
		c.st.SourceBindings = map[int]*sourceBinding{}
	}
	c.st.SourceBindings[key] = b
	err = c.st.saveLocked()
	if err != nil {
		delete(c.st.SourceBindings, key)
	}
	c.st.mu.Unlock()
	if err != nil {
		return false
	}
	if refusal == "" {
		// The comment was just listed and its issue just read: confirmed live.
		mem := c.sourceMem()
		mem.mu.Lock()
		mem.open[key] = true
		mem.mu.Unlock()
	}
	if refusal != "" {
		// Logged once, after the decision is saved; the binding is never reconsidered.
		log.Printf("source triggers: %s#%d comment %d refused (%s): %s", repo, n, cm.ID, refusal, matrixReasons[refusal].hint) // guard:source-refusal-log
	} else {
		log.Printf("source triggers: bound %s#%d comment %d", repo, n, cm.ID)
	}
	return true
}

// sourceGrantReady is true when an installed comment grant matches this exact
// comment and no other comment has claimed it.
func (c *Coord) sourceGrantReady(repo string, n int, issueID, body string, key int) bool {
	if c.sourceGrant != nil {
		return c.sourceGrant(repo, n, issueID, body, key)
	}
	if c.ownerGrants == nil {
		return false
	}
	return c.ownerGrants.commentGrantReady(repo, n, issueID, shaText([]byte(body)), key)
}

// sourceMarker is the first line of every reply, read back by the Cockpit.
func sourceMarker(comment int64, state, reason, pr string) string {
	m := fmt.Sprintf("%scomment=%d state=%s", sourceMarkerPrefix, comment, state)
	if reason != "" {
		m += " reason=" + reason
	}
	if pr != "" {
		m += " pr=" + pr
	}
	return m + " -->"
}

func sourceReplyFor(b *sourceBinding, state, reason, pr, text string) sourceReply {
	return sourceReply{ID: state + "\x00" + reason + "\x00" + pr, Body: sourceMarker(b.Comment, state, reason, pr) + "\n" + text}
}

// replySource decides one reply per binding, state and reason: it persists the
// reply (and a terminal state's end of the binding). Every reply is posted only
// by deliverSourceReplies. It reports whether the decision is durable.
func (c *Coord) replySource(ctx context.Context, key int, state, reason, pr, text string) bool {
	c.st.mu.Lock()
	b := c.st.SourceBindings[key]
	if b == nil {
		c.st.mu.Unlock()
		return false
	}
	reply := sourceReplyFor(b, state, reason, pr, text)
	if b.Replies[reply.ID] || sourceQueued(b, reply.ID) {
		c.st.mu.Unlock()
		return true
	}
	priorClosed, priorLaunched := b.Closed, b.Launched
	b.Outbox = append(b.Outbox, reply)
	if state == "started" {
		b.Launched = true
	}
	if sourceTerminal(state) && b.Closed == "" {
		b.Closed = state
	}
	if c.st.saveLocked() != nil {
		b.Outbox, b.Closed, b.Launched = b.Outbox[:len(b.Outbox)-1], priorClosed, priorLaunched
		c.st.mu.Unlock()
		return false
	}
	c.st.mu.Unlock()
	return true // delivered by deliverSourceReplies, within the per-tick reply budget
}

func sourceQueued(b *sourceBinding, id string) bool {
	for _, r := range b.Outbox {
		if r.ID == id {
			return true
		}
	}
	return false
}

// deliverSourceReplies posts queued replies, at most sourceRepliesPerTick POSTs
// per tick across every binding: one reply per binding per round, oldest first
// within a binding, so a long outbox never starves the others. A failed POST
// keeps its reply queued.
func (c *Coord) deliverSourceReplies(ctx context.Context) {
	if c.dry || c.st == nil {
		return
	}
	left := sourceRepliesPerTick
	failed := map[int]bool{}
	for left > 0 {
		c.st.mu.Lock()
		var keys []int
		for key, b := range c.st.SourceBindings {
			if len(b.Outbox) > 0 && !failed[key] {
				keys = append(keys, key)
			}
		}
		c.st.mu.Unlock()
		if len(keys) == 0 {
			return
		}
		sort.Ints(keys)
		for _, key := range keys {
			if left <= 0 {
				return
			}
			left-- // guard:source-reply-budget
			if !c.postSourceHead(ctx, key) {
				failed[key] = true
			}
		}
	}
}

// postSourceHead posts a binding's oldest queued reply.
func (c *Coord) postSourceHead(ctx context.Context, key int) bool {
	c.st.mu.Lock()
	b := c.st.SourceBindings[key]
	if b == nil || len(b.Outbox) == 0 {
		c.st.mu.Unlock()
		return true
	}
	head, repo, n := b.Outbox[0], b.Repo, b.Issue
	c.st.mu.Unlock()
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := c.sourceAPI().reply(rctx, repo, n, head.Body)
	cancel()
	if err != nil {
		log.Printf("source triggers: reply on %s#%d failed; it stays queued", repo, n)
		return false
	}
	c.st.mu.Lock()
	if len(b.Outbox) > 0 && b.Outbox[0].ID == head.ID {
		b.Outbox = b.Outbox[1:]
	}
	if b.Replies == nil {
		b.Replies = map[string]bool{}
	}
	b.Replies[head.ID] = true
	_ = c.st.saveLocked()
	c.st.mu.Unlock()
	return true
}

// catchUpSourceReplies decides any started or pr reply a live job is owed, so an
// acknowledgement whose first decision could not be saved is decided again.
func (c *Coord) catchUpSourceReplies(ctx context.Context) {
	c.st.mu.Lock()
	var started, prs []int
	for key, b := range c.st.SourceBindings {
		j := c.st.Jobs[key]
		if b.Closed != "" || j == nil {
			continue
		}
		if !b.Launched {
			started = append(started, key)
		}
		if j.PR != 0 {
			prs = append(prs, key)
		}
	}
	c.st.mu.Unlock()
	sort.Ints(started)
	sort.Ints(prs)
	for _, key := range started {
		c.replySourceStarted(ctx, key)
	}
	for _, key := range prs {
		c.st.mu.Lock()
		j := c.st.Jobs[key]
		c.st.mu.Unlock()
		c.replySourcePR(ctx, key, j)
	}
}

// Only done and stopped end a binding. A blocked launch keeps its binding open,
// like an inbox issue, so a registered agent stays supervised; its launch fence
// prevents any relaunch.
func sourceTerminal(state string) bool {
	return state == "done" || state == "stopped"
}

// postIssueNotice posts a notice to an inbox issue, or decides it as a marked
// reply on a binding's source issue.
func (c *Coord) postIssueNotice(ctx context.Context, n int, state, reason, body string, post func(context.Context, string, int, string) error) error {
	if _, ok := c.sourceBinding(n); ok {
		if !c.replySource(ctx, n, state, reason, "", body) {
			return errMatrix
		}
		return nil
	}
	return post(ctx, c.cfg.Inbox, n, body)
}

// closeSource ends an open binding whose reply was already decided.
func (c *Coord) closeSource(key int, state string) {
	c.st.mu.Lock()
	if b := c.st.SourceBindings[key]; b != nil && b.Closed == "" {
		b.Closed = state
		_ = c.st.saveLocked()
	}
	c.st.mu.Unlock()
}

func (c *Coord) sourceBinding(n int) (sourceBinding, bool) {
	if c.st == nil {
		return sourceBinding{}, false
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	b := c.st.SourceBindings[n]
	if b == nil {
		return sourceBinding{}, false
	}
	return *b, true
}

// issueHome is the GitHub issue a key's replies, receipts and goal refer to.
func (c *Coord) issueHome(n int) dispatchIssue {
	if b, ok := c.sourceBinding(n); ok {
		return dispatchIssue{Repo: b.Repo, Number: b.Issue}
	}
	return dispatchIssue{Repo: c.cfg.Inbox, Number: n}
}

// jobHome is the same for code that holds a job but no coordinator.
func jobHome(inbox string, j *Job) dispatchIssue {
	if j != nil && j.Home != nil {
		return *j.Home
	}
	if j == nil {
		return dispatchIssue{Repo: inbox}
	}
	return dispatchIssue{Repo: inbox, Number: j.Issue}
}

func (b sourceBinding) issue(c *Coord) Issue {
	is := Issue{ID: b.IssueID, Number: int(b.Comment), Title: b.Title, Body: b.Body, Source: &issueSource{Repo: b.Repo, Number: b.Issue}}
	for _, t := range c.cfg.Targets {
		if strings.EqualFold(t.Repo, b.Repo) {
			is.Labels = []string{t.Label}
			break
		}
	}
	return is
}

// pollSourceBindings adds every open binding whose trigger and issue still exist.
// Bindings are checked in turn within a per-tick read budget, with conditional
// reads; one not checked this tick keeps its last known state, and an unknown
// one counts as open, so a skipped check never tears anything down. ok=false on
// any read failure, so the teardown pass is skipped this tick.
func (c *Coord) pollSourceBindings(ctx context.Context, open map[int]Issue, all map[int]bool) bool {
	api, mem, now := c.sourceAPI(), c.sourceMem(), time.Now().UTC()
	c.st.mu.Lock()
	var keys []int
	for key, b := range c.st.SourceBindings {
		if b.Closed == "" {
			keys = append(keys, key)
		}
	}
	c.st.mu.Unlock()
	sort.Ints(keys)
	mem.mu.Lock()
	turn := mem.checkTurn
	// Bindings never checked since this process started go first, in turn order,
	// so new granted work is confirmed (and can launch) before re-checks.
	if len(keys) > 0 {
		keys = append(keys[turn%len(keys):], keys[:turn%len(keys)]...)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		_, ki := mem.open[keys[i]]
		_, kj := mem.open[keys[j]]
		return !ki && kj
	})
	mem.mu.Unlock()
	budget, checkedKeys := sourceCheckRequestsPerTick, 0
	if mem.paused(now) {
		budget = 0
	}
	ok := true
	for _, key := range keys {
		b, found := c.sourceBinding(key)
		if !found {
			continue
		}
		mem.mu.Lock()
		live, known := mem.open[key]
		mem.mu.Unlock()
		if budget >= 2 { // guard:source-check-budget
			budget -= 2
			checkedKeys++
			checked, err := c.checkSourceBinding(ctx, api, mem, b, now)
			if err != nil {
				ok = false
			} else {
				live, known = checked, true
				mem.mu.Lock()
				mem.open[key] = live
				mem.mu.Unlock()
			}
		}
		if live || !known { // guard:source-still-open
			all[key] = true // never torn down for lack of a read
			c.st.mu.Lock()
			job := c.st.Jobs[key] != nil
			c.st.mu.Unlock()
			if known || job { // guard:source-admit-confirmed
				open[key] = b.issue(c) // launched only once confirmed live
			}
		}
	}
	mem.mu.Lock()
	mem.checkTurn = turn + checkedKeys // the next tick checks the next bindings
	mem.mu.Unlock()
	return ok
}

// checkSourceBinding reads whether the trigger comment and the same open issue
// still exist, conditionally on the last answers.
func (c *Coord) checkSourceBinding(ctx context.Context, api sourceGitHub, mem *sourceMemory, b sourceBinding, now time.Time) (bool, error) {
	commentPath := fmt.Sprintf("repos/%s/issues/comments/%d", b.Repo, b.Comment)
	issuePath := fmt.Sprintf("repos/%s/issues/%d", b.Repo, b.Issue)
	mem.mu.Lock()
	commentTag, issueTag := mem.etags[commentPath], mem.etags[issuePath]
	mem.mu.Unlock()
	exists, commentMeta, err := api.comment(ctx, b.Repo, b.Comment, commentTag)
	mem.noteRate(commentMeta, now)
	if err != nil {
		return false, err
	}
	view, issueMeta, err := api.issue(ctx, b.Repo, b.Issue, issueTag)
	mem.noteRate(issueMeta, now)
	if err != nil {
		return false, err
	}
	mem.mu.Lock()
	defer mem.mu.Unlock()
	if !exists {
		delete(mem.etags, commentPath)
	} else if commentMeta.ETag != "" {
		mem.etags[commentPath] = commentMeta.ETag
	}
	if issueMeta.NotModified {
		cached, ok := mem.issues[issuePath]
		if !ok {
			return false, errSourceRead
		}
		view = cached
	} else if issueMeta.ETag != "" {
		mem.etags[issuePath], mem.issues[issuePath] = issueMeta.ETag, view
	}
	return exists && view.State == "OPEN" && view.NodeID == b.IssueID, nil
}

// issueState is OPEN or CLOSED for an inbox issue or a binding ("" if unknown).
// A binding's state is the last checked one; it costs no extra read.
func (c *Coord) issueState(ctx context.Context, n int) string {
	b, ok := c.sourceBinding(n)
	if !ok {
		return ghIssueStateByNum(ctx, c.cfg.Inbox, n)
	}
	if b.Closed != "" {
		return "CLOSED"
	}
	mem := c.sourceMem()
	mem.mu.Lock()
	live, known := mem.open[n]
	mem.mu.Unlock()
	switch {
	case !known:
		return ""
	case live:
		return "OPEN"
	}
	return "CLOSED"
}

// finishSource closes every open binding that is no longer admitted and has no
// job left, deciding its one plain reply.
func (c *Coord) finishSource(ctx context.Context, open map[int]Issue, pollOK bool) {
	if !pollOK {
		return
	}
	c.st.mu.Lock()
	var keys []int
	for key, b := range c.st.SourceBindings {
		if b.Closed != "" {
			continue
		}
		f, completing := c.st.CompletedRuns[key]
		done := completing && f.Phase == "observed" && c.st.Jobs[key] == nil
		if done || (open[key].Source == nil && c.st.Jobs[key] == nil && !completing) {
			keys = append(keys, key)
		}
	}
	c.st.mu.Unlock()
	sort.Ints(keys)
	for _, key := range keys {
		c.st.mu.Lock()
		f, completing := c.st.CompletedRuns[key]
		launched := c.st.SourceBindings[key].Launched
		c.st.mu.Unlock()
		switch {
		case completing && f.Phase == "observed":
			c.replySource(ctx, key, "done", "", "", "divybot: done. The agent completed this assignment.")
		case launched:
			c.replySource(ctx, key, "done", "source-closed", "", "divybot: done. The trigger comment was deleted or the issue was closed, and the agent was torn down.")
		default:
			c.replySource(ctx, key, "stopped", "source-closed", "", "divybot: stopped. The trigger comment was deleted or the issue was closed before an agent launched.")
		}
	}
}

func (c *Coord) pruneSourceBindings(now time.Time) {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	changed := false
	for key, b := range c.st.SourceBindings {
		if b.Closed != "" && len(b.Outbox) == 0 && now.Sub(b.CreatedAt) > sourceBindingRetention && c.st.Jobs[key] == nil {
			if _, fenced := c.st.CompletedRuns[key]; fenced {
				continue
			}
			delete(c.st.SourceBindings, key)
			changed = true
		}
	}
	if changed {
		_ = c.st.saveLocked()
	}
}

// home is the job's recorded source issue; nil for an inbox issue.
func (is Issue) home() *dispatchIssue {
	if is.Source == nil {
		return nil
	}
	return &dispatchIssue{Repo: is.Source.Repo, Number: is.Source.Number}
}

func (c *Coord) replySourceStarted(ctx context.Context, n int) {
	if _, ok := c.sourceBinding(n); !ok {
		return
	}
	c.st.mu.Lock()
	j := c.st.Jobs[n]
	host, agent, model := "", "", ""
	if j != nil {
		host, agent, model = j.Host, j.Agent, j.Overrides.Model
	}
	c.st.mu.Unlock()
	if model == "" {
		model = "the routed model"
	}
	c.replySource(ctx, n, "started", "", "", fmt.Sprintf("divybot: started. %s is working on this issue on %s with %s.", agent, host, model))
}

func (c *Coord) replySourcePR(ctx context.Context, n int, j *Job) {
	if _, ok := c.sourceBinding(n); !ok || j == nil || j.PR == 0 {
		return
	}
	ref := fmt.Sprintf("%s#%d", j.Repo, j.PR)
	c.replySource(ctx, n, "pr", "", ref, fmt.Sprintf("divybot: pull request opened: https://github.com/%s/pull/%d", j.Repo, j.PR))
}
