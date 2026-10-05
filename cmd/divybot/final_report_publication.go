package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const finalReportFile = ".divybot-final-report.md"
const finalReportTemp = ".divybot-final-report.tmp"

type finalReportScope struct {
	SchemaVersion  int           `json:"schemaVersion"`
	Key            string        `json:"key"`
	Destination    dispatchIssue `json:"destination"`
	Host           string        `json:"host"`
	Source         string        `json:"source"`
	Repo           string        `json:"repo"`
	Cwd            string        `json:"cwd"`
	BindingDigest  string        `json:"bindingDigest"`
	DispatchDigest string        `json:"dispatchDigest"`
}
type finalPostedComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	IssueURL  string    `json:"issue_url"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}
type finalPublicationCalls struct {
	proof   func(context.Context, Host, *Job, finalReportScope) error
	report  func(context.Context, Host, finalReportScope) (string, error)
	post    func(context.Context, dispatchIssue, string) (int64, error)
	comment func(context.Context, dispatchIssue, int64) (finalPostedComment, error)
	write   func(string, string, any) error // syscall fault control, not configuration
}

func (c *Coord) finalWrite(dir, name string, value any) error {
	if !ownerNativePrivateDir(dir, os.Getuid()) {
		return errFinalPublication
	}
	if c.finalCalls.write != nil {
		return c.finalCalls.write(dir, name, value)
	}
	return actionImmutableJSON(dir, name, value)
}

var errFinalPublication = errors.New("final-publication-unconfirmed")

type finalPublicationIntent struct {
	ScopeDigest string    `json:"scopeDigest"`
	Body        string    `json:"body"`
	BodyDigest  string    `json:"bodyDigest"`
	StartedAt   time.Time `json:"startedAt"`
}
type finalPublicationResult struct {
	IntentDigest string `json:"intentDigest"`
	CommentID    int64  `json:"commentId"`
}

func finalJSONDigest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return shaText(b)
}

// Keep private posting work outside the reader-facing native receipt tree.
func (c *Coord) finalPublicationDir(key string, create bool) (string, error) {
	if c.st == nil || c.st.path == "" || !digestPattern.MatchString(key) {
		return "", errFinalPublication
	}
	state, err := filepath.Abs(c.st.path)
	if err != nil {
		return "", errFinalPublication
	}
	root := filepath.Join(filepath.Dir(state), "final-publication")
	dir := filepath.Join(root, key)
	for _, p := range []string{root, dir} {
		if create {
			if e := os.Mkdir(p, 0700); e != nil && !os.IsExist(e) {
				return "", errFinalPublication
			}
		}
		if !ownerNativePrivateDir(p, os.Getuid()) {
			return "", errFinalPublication
		}
	}
	return dir, nil
}

func finalPrivateRead(dir, name string, out any) error {
	b, err := ownerNativePrivateRead(filepath.Join(dir, name), os.Getuid())
	if err != nil {
		return err
	}
	return strictJSON(b, out)
}

func (c *Coord) finalScope(j *Job) (string, finalReportScope, error) {
	var s finalReportScope
	if j == nil || !j.FinalReportManaged || j.RunMode {
		return "", s, errFinalPublication
	}
	dir, err := c.finalPublicationDir(j.DispatchKey, false)
	if err != nil || finalPrivateRead(dir, "scope.json", &s) != nil {
		return "", s, errFinalPublication
	}
	if s.SchemaVersion != 1 || s.Key != j.DispatchKey || s.Destination != jobHome(c.cfg.Inbox, j) ||
		!repositoryName.MatchString(s.Destination.Repo) || s.Destination.Number < 1 || s.Host != j.Host || s.Source != j.Agent || s.Repo != j.Repo ||
		!filepath.IsAbs(s.Cwd) || filepath.Clean(s.Cwd) != s.Cwd || !digestPattern.MatchString(s.BindingDigest) || !digestPattern.MatchString(s.DispatchDigest) {
		return "", s, errFinalPublication
	}
	return dir, s, nil
}

func finalRenderedBody(report, key string) (string, error) {
	marker := finalCommentMarker(key)
	if marker == "" || len(report) > 60000 || !utf8.ValidString(report) || strings.TrimSpace(report) == "" || strings.Contains(report, "<!-- orchid-run") {
		return "", errFinalPublication
	}
	return report + "\n\n" + marker + "\n", nil
}

func (c *Coord) finalProof(ctx context.Context, h Host, j *Job, s finalReportScope) error {
	if ctx.Err() != nil || !deliveryConfirmed(j) {
		return errFinalPublication
	}
	_, current, err := c.finalScope(j)
	if err != nil || current != s {
		return errFinalPublication
	}
	if c.finalCalls.proof != nil {
		return c.finalCalls.proof(ctx, h, j, s)
	}
	return c.proveFinalReportSource(ctx, h, j, s)
}
func (c *Coord) finalReport(ctx context.Context, h Host, s finalReportScope) (string, error) {
	if c.finalCalls.report != nil {
		return c.finalCalls.report(ctx, h, s)
	}
	return h.readFinalReport(ctx, s.Cwd)
}
func (c *Coord) finalPost(ctx context.Context, d dispatchIssue, body string) (int64, error) {
	if c.finalCalls.post != nil {
		return c.finalCalls.post(ctx, d, body)
	}
	input, err := os.CreateTemp("", "orchid-final-")
	if err != nil {
		return 0, errFinalPublication
	}
	defer os.Remove(input.Name())
	err = json.NewEncoder(input).Encode(struct {
		Body string `json:"body"`
	}{body})
	if closeErr := input.Close(); err != nil || closeErr != nil || ctx.Err() != nil {
		return 0, errFinalPublication
	}
	var result finalPostedComment
	err = ghJSON(ctx, &result, "api", "--method", "POST", "repos/"+d.Repo+"/issues/"+fmt.Sprint(d.Number)+"/comments", "--input", input.Name())
	return result.ID, err
}
func (c *Coord) finalComment(ctx context.Context, d dispatchIssue, id int64) (finalPostedComment, error) {
	if c.finalCalls.comment != nil {
		return c.finalCalls.comment(ctx, d, id)
	}
	var comment finalPostedComment
	err := ghJSON(ctx, &comment, "api", "repos/"+d.Repo+"/issues/comments/"+fmt.Sprint(id))
	return comment, err
}

// The intent is a durable one-POST fence. Only its returned, durably stored ID
// may be reconciled; a lost reply is deliberately left unknown, never retried.
func (c *Coord) publishFinalReport(ctx context.Context, j *Job) (int64, error) {
	c.finalMu.Lock()
	defer c.finalMu.Unlock()
	if c.dry || ctx.Err() != nil {
		return 0, errFinalPublication
	}
	dir, s, err := c.finalScope(j)
	if err != nil {
		return 0, err
	}
	h, ok := c.hosts[j.Host]
	if !ok {
		return 0, errFinalPublication
	}
	if c.finalProof(ctx, h, j, s) != nil {
		return 0, errFinalPublication
	}
	report, err := c.finalReport(ctx, h, s)
	if err != nil || ctx.Err() != nil {
		return 0, errFinalPublication
	}
	body, err := finalRenderedBody(report, s.Key)
	if err != nil {
		return 0, err
	}
	if c.finalProof(ctx, h, j, s) != nil || ctx.Err() != nil {
		return 0, errFinalPublication
	}
	var intent finalPublicationIntent
	intentPath := filepath.Join(dir, "intent.json")
	_, statErr := os.Lstat(intentPath)
	fresh := os.IsNotExist(statErr)
	if fresh {
		intent = finalPublicationIntent{ScopeDigest: finalJSONDigest(s), Body: body, BodyDigest: shaText([]byte(body)), StartedAt: time.Now().UTC()}
		if ctx.Err() != nil || c.finalWrite(dir, "intent.json", intent) != nil || ctx.Err() != nil {
			return 0, errFinalPublication
		}
	} else if statErr != nil || finalPrivateRead(dir, "intent.json", &intent) != nil {
		return 0, errFinalPublication
	}
	if intent.ScopeDigest != finalJSONDigest(s) || intent.Body != body || intent.BodyDigest != shaText([]byte(body)) ||
		intent.StartedAt.IsZero() || intent.StartedAt.Before(j.SpawnedAt) || intent.StartedAt.After(time.Now()) {
		return 0, errFinalPublication
	}
	// A ready file cannot be swapped between the capture and the effect.
	again, err := c.finalReport(ctx, h, s)
	if err != nil || again != report || c.finalProof(ctx, h, j, s) != nil || ctx.Err() != nil {
		return 0, errFinalPublication
	}
	var result finalPublicationResult
	if fresh {
		id, postErr := c.finalPost(ctx, s.Destination, body)
		if postErr != nil || id < 1 || ctx.Err() != nil {
			return 0, errFinalPublication
		}
		result = finalPublicationResult{IntentDigest: finalJSONDigest(intent), CommentID: id}
		if c.finalWrite(dir, "post.json", result) != nil || ctx.Err() != nil {
			return 0, errFinalPublication
		}
	} else if finalPrivateRead(dir, "post.json", &result) != nil {
		return 0, errFinalPublication
	}
	if result.CommentID < 1 || result.IntentDigest != finalJSONDigest(intent) {
		return 0, errFinalPublication
	}
	comment, err := c.finalComment(ctx, s.Destination, result.CommentID)
	if err != nil || ctx.Err() != nil || comment.ID != result.CommentID || comment.IssueURL != "https://api.github.com/repos/"+s.Destination.Repo+"/issues/"+fmt.Sprint(s.Destination.Number) ||
		c.cfg.BotLogin == "" || comment.User.Login != c.cfg.BotLogin || comment.Body != body || comment.CreatedAt.Before(intent.StartedAt.Truncate(time.Second)) || comment.CreatedAt.After(time.Now()) {
		return 0, errFinalPublication
	}
	if c.finalProof(ctx, h, j, s) != nil || ctx.Err() != nil {
		return 0, errFinalPublication
	}
	currentReport, err := c.finalReport(ctx, h, s)
	if err != nil || currentReport != report || c.finalProof(ctx, h, j, s) != nil || ctx.Err() != nil {
		return 0, errFinalPublication
	}
	var accepted finalPublicationResult
	if _, e := os.Lstat(filepath.Join(dir, "accepted.json")); os.IsNotExist(e) {
		if c.finalWrite(dir, "accepted.json", result) != nil || ctx.Err() != nil {
			_ = os.Remove(filepath.Join(dir, "accepted.json"))
			return 0, errFinalPublication
		}
	} else if e != nil {
		return 0, errFinalPublication
	}
	if finalPrivateRead(dir, "accepted.json", &accepted) != nil || accepted != result || ctx.Err() != nil {
		return 0, errFinalPublication
	}
	return result.CommentID, nil
}

func (c *Coord) observeFinalReport(ctx context.Context, j *Job, ref agentRef, known bool) {
	if c.dry || j == nil || j.RemoteCleanup != "" || !j.FinalReportManaged || j.RunMode || !deliveryConfirmed(j) || !known ||
		ref.Host != j.Host || ref.Pane != j.Pane || ref.Workspace != j.Workspace || ref.Agent != j.Agent {
		return
	}
	c.st.mu.Lock()
	_, fenced := c.st.CompletedRuns[j.Issue]
	c.st.mu.Unlock()
	if fenced {
		return
	}
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	id, err := c.publishFinalReport(check, j)
	if err == nil && id > 0 {
		log.Printf("issue #%d: final publication accepted", j.Issue)
	}
}

// The model supplies visible text; the dispatcher owns destination and marker.
func finalReportInstruction(destination dispatchIssue, key string) string {
	if !repositoryName.MatchString(destination.Repo) || destination.Number < 1 || !digestPattern.MatchString(key) {
		return ""
	}
	return fmt.Sprintf("\n## Final report publication\nWhen the brief requires a final GitHub report, write its requested visible text to `%s`, then rename that file to `%s` when ready. Both files are excluded from Git. Orchid posts the report on the bound assignment %s#%d and adds its run marker. Use this file handoff instead of any earlier final-comment helper or gh posting instruction for this canonical final. Do not add a marker or run gh to post it. Writing the file queues publication; it does not prove the comment was accepted. Keep the brief's requested visible format and privacy rules.\n", finalReportTemp, finalReportFile, destination.Repo, destination.Number)
}
