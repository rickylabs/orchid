package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A source binding is a launch triggered by a /swarm comment on the source issue
// itself, with no inbox copy. Its identity is repository + issue + comment ID; the
// comment's REST ID is also its job key, which is far above any inbox issue
// number, so a call site that still addresses the inbox fails on a missing issue
// instead of touching another one. Every reply goes to the source issue.

// ownerGitHubID is the only author whose comment can trigger a launch (Eric,
// 2026-10-05). Checked by numeric ID, never by login.
const ownerGitHubID int64 = 129366361

const (
	// Comment pages read per repository per tick. A longer backlog is read on the
	// following ticks, because the cursor only advances past what was read.
	sourceCommentPages = 5
	// Re-read window behind the newest comment seen, for listing lag. Comment IDs
	// already bound are never acted on twice.
	sourceCursorOverlap = 2 * time.Minute
	// A comment first seen more than this long after it was created is never a
	// trigger, so an old comment that is edited later cannot launch.
	sourceTriggerFreshness = 24 * time.Hour
	// Closed bindings are kept this long to deduplicate re-read comments.
	sourceBindingRetention = 7 * 24 * time.Hour
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
	Closed    string          `json:"closed,omitempty"` // terminal reply state; "" while open
	Replies   map[string]bool `json:"replies,omitempty"`
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
	State  string // OPEN | CLOSED
	PR     bool
}

// sourceGitHub is the binding's whole GitHub surface, so a fake can stand in for it.
type sourceGitHub interface {
	comments(ctx context.Context, repo, since string, page int) ([]sourceComment, error)
	issue(ctx context.Context, repo string, n int) (sourceIssueView, error)
	commentExists(ctx context.Context, repo string, id int64) (bool, error)
	reply(ctx context.Context, repo string, n int, body string) error
}

type ghSource struct{}

func (ghSource) comments(ctx context.Context, repo, since string, page int) ([]sourceComment, error) {
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
	err := ghJSON(ctx, &raw, "api", fmt.Sprintf("repos/%s/issues/comments?since=%s&sort=updated&direction=asc&per_page=100&page=%d", repo, since, page))
	out := make([]sourceComment, 0, len(raw))
	for _, r := range raw {
		out = append(out, sourceComment{ID: r.ID, Body: r.Body, HTMLURL: r.HTMLURL, IssueURL: r.IssueURL, AuthorID: r.User.ID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	return out, err
}

func (ghSource) issue(ctx context.Context, repo string, n int) (sourceIssueView, error) {
	var raw struct {
		NodeID      string          `json:"node_id"`
		Title       string          `json:"title"`
		State       string          `json:"state"`
		PullRequest *map[string]any `json:"pull_request"`
	}
	if err := ghJSON(ctx, &raw, "api", fmt.Sprintf("repos/%s/issues/%d", repo, n)); err != nil {
		return sourceIssueView{}, err
	}
	return sourceIssueView{NodeID: raw.NodeID, Title: raw.Title, State: strings.ToUpper(raw.State), PR: raw.PullRequest != nil}, nil
}

func (ghSource) commentExists(ctx context.Context, repo string, id int64) (bool, error) {
	out, err := run(ctx, "gh", "api", "-i", fmt.Sprintf("repos/%s/issues/comments/%d", repo, id))
	if err == nil {
		return true, nil
	}
	if strings.Contains(out, "HTTP/2.0 404") || strings.Contains(out, "HTTP/1.1 404") || strings.Contains(out, "(HTTP 404)") {
		return false, nil
	}
	return false, err
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
	if strings.TrimSpace(first) != "/swarm" { // guard:source-first-line
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

// sourceTick reads new comments on every configured target repository and binds
// each owner /swarm comment once. The first run only sets the cursor, so no
// comment written before this dispatcher ran ever launches.
func (c *Coord) sourceTick(ctx context.Context) {
	api := c.sourceAPI()
	now := time.Now().UTC()
	for _, repo := range c.sourceRepos() {
		c.st.mu.Lock()
		if c.st.SourceCursors == nil {
			c.st.SourceCursors = map[string]time.Time{}
		}
		cursor, started := c.st.SourceCursors[repo]
		if !started { // guard:source-first-start
			c.st.SourceCursors[repo] = now
			_ = c.st.saveLocked()
			c.st.mu.Unlock()
			continue
		}
		c.st.mu.Unlock()
		newest := cursor
		complete := true
		for page := 1; page <= sourceCommentPages; page++ {
			list, err := api.comments(ctx, repo, cursor.Format(time.RFC3339), page)
			if err != nil {
				log.Printf("source triggers: list %s failed: %v", repo, err)
				complete = false
				break
			}
			for _, cm := range list {
				if !c.considerSourceComment(ctx, api, repo, cm, now) {
					complete = false
				}
				if cm.UpdatedAt.After(newest) {
					newest = cm.UpdatedAt
				}
			}
			if len(list) < 100 {
				break
			}
		}
		// Advance only past what was fully handled; the overlap re-reads the tail.
		if next := newest.Add(-sourceCursorOverlap); complete && next.After(cursor) { // guard:source-cursor-advance
			c.st.mu.Lock()
			c.st.SourceCursors[repo] = next
			_ = c.st.saveLocked()
			c.st.mu.Unlock()
		}
	}
	c.pruneSourceBindings(now)
}

// considerSourceComment binds one comment if it is a fresh owner trigger. It
// returns false only when the comment must be read again (a transient failure).
func (c *Coord) considerSourceComment(ctx context.Context, api sourceGitHub, repo string, cm sourceComment, now time.Time) bool {
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
	if now.Sub(cm.CreatedAt) > sourceTriggerFreshness { // guard:source-fresh
		log.Printf("source triggers: %s comment %d is older than %s; not a trigger", repo, cm.ID, sourceTriggerFreshness)
		return true
	}
	n := sourceIssueNumber(cm.IssueURL)
	if n == 0 {
		return true
	}
	view, err := api.issue(ctx, repo, n)
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
		}
	}
	c.st.mu.Lock()
	if c.st.SourceBindings == nil {
		c.st.SourceBindings = map[int]*sourceBinding{}
	}
	if refusal != "" {
		b.Closed = "refused"
	}
	c.st.SourceBindings[key] = b
	err = c.st.saveLocked()
	c.st.mu.Unlock()
	if err != nil {
		c.st.mu.Lock()
		delete(c.st.SourceBindings, key)
		c.st.mu.Unlock()
		return false
	}
	log.Printf("source triggers: bound %s#%d comment %d", repo, n, cm.ID)
	if refusal != "" {
		detail := matrixReasons[refusal]
		c.replySource(ctx, key, "refused", refusal, "", fmt.Sprintf("divybot: launch refused. Reason: `%s`. %s Nothing was launched. Post a new /swarm comment to try again.", refusal, detail.hint))
	}
	return true
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

// replySource posts one reply per binding, state and reason. Terminal states
// close the binding, so it is never admitted again.
func (c *Coord) replySource(ctx context.Context, key int, state, reason, pr, text string) bool {
	c.st.mu.Lock()
	b := c.st.SourceBindings[key]
	if b == nil {
		c.st.mu.Unlock()
		return false
	}
	id := state + "\x00" + reason + "\x00" + pr
	if b.Replies[id] {
		c.st.mu.Unlock()
		return true
	}
	repo, n, comment := b.Repo, b.Issue, b.Comment
	c.st.mu.Unlock()
	if c.dry {
		return true
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := c.sourceAPI().reply(rctx, repo, n, sourceMarker(comment, state, reason, pr)+"\n"+text)
	cancel()
	if err != nil {
		log.Printf("source triggers: reply %s on %s#%d failed; will retry", state, repo, n)
		return false
	}
	c.st.mu.Lock()
	if b.Replies == nil {
		b.Replies = map[string]bool{}
	}
	b.Replies[id] = true
	if sourceTerminal(state) && b.Closed == "" {
		b.Closed = state
	}
	_ = c.st.saveLocked()
	c.st.mu.Unlock()
	return true
}

// Only done and stopped end a binding. A blocked launch keeps its binding open,
// like an inbox issue, so a registered agent stays supervised; its launch fence
// prevents any relaunch.
func sourceTerminal(state string) bool {
	return state == "done" || state == "stopped"
}

// postIssueNotice posts a notice to an inbox issue, or as a marked reply on a
// binding's source issue.
func (c *Coord) postIssueNotice(ctx context.Context, n int, state, reason, body string, post func(context.Context, string, int, string) error) error {
	if _, ok := c.sourceBinding(n); ok {
		if !c.replySource(ctx, n, state, reason, "", body) {
			return errMatrix
		}
		return nil
	}
	return post(ctx, c.cfg.Inbox, n, body)
}

// closeSource ends an open binding without a reply that was already posted.
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
// ok=false on any read failure, so the teardown pass is skipped this tick.
func (c *Coord) pollSourceBindings(ctx context.Context, open map[int]Issue, all map[int]bool) bool {
	api := c.sourceAPI()
	c.st.mu.Lock()
	var keys []int
	for key, b := range c.st.SourceBindings {
		if b.Closed == "" {
			keys = append(keys, key)
		}
	}
	c.st.mu.Unlock()
	sort.Ints(keys)
	ok := true
	for _, key := range keys {
		b, found := c.sourceBinding(key)
		if !found {
			continue
		}
		exists, err := api.commentExists(ctx, b.Repo, b.Comment)
		if err != nil {
			ok = false
			continue
		}
		view, err := api.issue(ctx, b.Repo, b.Issue)
		if err != nil {
			ok = false
			continue
		}
		if !exists || view.State != "OPEN" || view.NodeID != b.IssueID { // guard:source-still-open
			continue // the teardown pass and finishSource end it
		}
		is := b.issue(c)
		open[key] = is
		all[key] = true
	}
	return ok
}

// issueState is OPEN or CLOSED for an inbox issue or a binding ("" if unknown).
func (c *Coord) issueState(ctx context.Context, n int) string {
	b, ok := c.sourceBinding(n)
	if !ok {
		return ghIssueStateByNum(ctx, c.cfg.Inbox, n)
	}
	if b.Closed != "" {
		return "CLOSED"
	}
	api := c.sourceAPI()
	exists, err := api.commentExists(ctx, b.Repo, b.Comment)
	if err != nil {
		return ""
	}
	view, err := api.issue(ctx, b.Repo, b.Issue)
	if err != nil {
		return ""
	}
	if !exists || view.State != "OPEN" {
		return "CLOSED"
	}
	return "OPEN"
}

// finishSource closes every open binding that is no longer admitted and has no
// job left, replying once with the plain reason.
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
		launched := c.st.SourceBindings[key].Replies["started\x00\x00"]
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
		if b.Closed != "" && now.Sub(b.CreatedAt) > sourceBindingRetention && c.st.Jobs[key] == nil {
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
