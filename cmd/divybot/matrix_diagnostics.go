package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"
)

// Only this fixed vocabulary crosses the public log/comment boundary. No native
// error strings, configuration values, source output or issue prose are copied.
var matrixReasons = map[string]struct{ field, hint string }{
	"source-missing":               {"matrix.source", "Configure an absolute clean NetScript checkout."},
	"source-invalid":               {"matrix.source", "Verify the checkout is clean and HEAD equals matrix.revision."},
	"revision-invalid":             {"matrix.revision", "Configure a full lowercase source commit."},
	"receipt-root-invalid":         {"matrix.receipt_root", "Use an existing private directory, mode 0700, outside Git and without symlink aliases."},
	"target-revision-invalid":      {"matrix.target_revisions", "Pin the target repository to a full lowercase commit."},
	"issue-invalid":                {"inbox", "Verify the inbox repository and issue number."},
	"issue-identity-missing":       {"issue.id", "Fetch the complete GitHub issue identity."},
	"brief-routing-invalid":        {"issue.body", "Correct duplicate or malformed routing fields."},
	"grant-conflict":               {"matrix.grants", "Remove duplicate matching grants or correct tier/role conflicts."},
	"pin-invalid":                  {"matrix.pins", "Configure the named pin and do not combine it with direct model/effort fields."},
	"profile-invalid":              {"issue.profile", "Use a valid profile name and a compatible routing row."},
	"profile-unavailable":          {"matrix.target_revisions", "Ensure the selected profile exists at the pinned target revision and is readable."},
	"override-worklog-unavailable": {"matrix.grants.ownerMatrixOverride.worklogPath", "Ensure the override worklog is readable at the pinned target revision."},
	"authorization-required":       {"matrix.grants.authorization", "Add a matching brief-bound privileged-tier authorization with a named authorizer and rationale."},
	"authorization-invalid":        {"matrix.grants.authorization", "Correct the authorizer or rationale in the matching grant."},
	"override-required":            {"matrix.grants.ownerMatrixOverride", "A route deviation requires the trusted owner override and its matching pin."},
	"override-invalid":             {"matrix.grants.ownerMatrixOverride", "Verify the named pin, route and exact owner worklog entry."},
	"route-unavailable":            {"matrix", "No matrix route is available on the eligible transports."},
	"routing-invalid":              {"issue.tier/role", "Specify a workload tier and a role allowed by the selected profile."},
	"resolution-failed":            {"matrix", "Verify Deno, the pinned source contract and matrix CLI; resolution could not complete."},
	"quota-unavailable":            {"governor", "No transport has both fresh subscription windows, headroom and available capacity."},
	"harness-conflict":             {"issue.harness", "The requested harness conflicts with the selected matrix transport."},
	"router-unsupported":           {"issue.router", "Router substitution has no supported adapter."},
	"host-unavailable":             {"hosts", "No eligible host has capacity for the selected transport and target."},
	"receipt-persistence-failed":   {"matrix.receipt_root", "Inspect receipt permissions and any existing reservation; never erase a fence to retry blindly."},
	"dispatch-persistence-failed":  {"matrix.receipt_root", "Dispatch binding could not be persisted; inspect the existing reservation."},
	"launch-failed":                {"launch", "Inspect the registration abandonment notice and durable launch fence."},
	"observer-unavailable":         {"route.observed", "Evaluator launch is inconclusive until independent model and session evidence exists."},
}

type matrixReason string

func (e matrixReason) Error() string { return string(e) }
func (e matrixReason) Unwrap() error { return errMatrix }
func validMatrixRefusal(r matrixRefusal) bool {
	_, ok := matrixReasons[r.ReasonCode]
	return ok && ((r.ReasonCode == "observer-unavailable" && r.Status == "inconclusive") || (r.ReasonCode != "observer-unavailable" && r.Status == "refused"))
}
func refusalFor(err error) matrixRefusal {
	if errors.Is(err, errEvaluatorEvidence) {
		return evaluatorRefusal()
	}
	var reason matrixReason
	if errors.As(err, &reason) {
		r := matrixRefusal{"refused", string(reason)}
		if validMatrixRefusal(r) {
			return r
		}
	}
	return matrixRefusal{"refused", "resolution-failed"}
}

func postMatrixComment(ctx context.Context, repo string, n int, body string) error {
	f, err := os.CreateTemp("", "matrix-comment-*.md")
	if err != nil {
		return errMatrix
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(body)
	closed := f.Close()
	if err != nil || closed != nil {
		return errMatrix
	}
	_, err = run(ctx, "gh", "issue", "comment", fmt.Sprint(n), "--repo", repo, "--body-file", f.Name())
	return err
}

func (c *Coord) reportIssueMatrixRefusal(ctx context.Context, n int, is Issue, r matrixRefusal, post func(context.Context, string, int, string) error) {
	if !validMatrixRefusal(r) {
		r = refusalFor(errMatrix)
	}
	detail := matrixReasons[r.ReasonCode]
	log.Printf("issue #%d: matrix-launch-refused status=%s reason=%s field=%s; %s", n, r.Status, r.ReasonCode, detail.field, detail.hint)
	if c.dry {
		return
	}
	key := shaText([]byte(is.ID + "\x00" + briefDigest(is) + "\x00" + r.ReasonCode))
	c.st.mu.Lock()
	notified := c.st.MatrixNotices[n] == key
	c.st.mu.Unlock()
	if notified {
		return
	}
	body := fmt.Sprintf("divybot: matrix launch %s. Reason: `%s`. Field: `%s`. %s No agent was launched by this refused attempt.", r.Status, r.ReasonCode, detail.field, detail.hint)
	if r.ReasonCode == "launch-failed" || r.ReasonCode == "dispatch-persistence-failed" {
		body = fmt.Sprintf("divybot: matrix launch %s. Reason: `%s`. Field: `%s`. %s Launch outcome requires inspection; this notice does not authorize another attempt.", r.Status, r.ReasonCode, detail.field, detail.hint)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if post(ctx, c.cfg.Inbox, n, body) != nil {
		log.Printf("issue #%d: matrix refusal comment unavailable; will retry", n)
		return
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	if c.st.MatrixNotices == nil {
		c.st.MatrixNotices = map[int]string{}
	}
	c.st.MatrixNotices[n] = key
	if c.st.saveLocked() != nil {
		log.Printf("issue #%d: matrix refusal notification persistence failed", n)
	}
	// A crash between comment and state write may duplicate a comment, never a launch.
}
