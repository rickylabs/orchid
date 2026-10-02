package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// Only this fixed vocabulary crosses the public log/comment boundary. No native
// error strings, configuration values, source output or issue prose are copied.
var matrixReasons = map[string]struct{ field, hint string }{
	"budget-reached":               {"provider_budgets", "The exact provider/model policy is exhausted or its configured overage limit has been reached."},
	"budget-unavailable":           {"provider_budgets", "Paid admission needs an exact policy, fresh scoped allowance and a verified full-run bound with durable reservations; missing proof cannot permit a launch."},
	"opencode-route-invalid":       {"route.provider/model", "Use an exact qualified OpenCode model; router may agree with its provider prefix, never substitute it."},
	"opencode-variant-unavailable": {"route.effort", "The native catalog must list the exact requested variant; no default or lower effort is substituted."},
	"opencode-catalog-unavailable": {"route.discovery", "The dispatch host's bounded native OpenCode catalog is unavailable or invalid."},
	"opencode-model-unavailable":   {"route.model", "The dispatch host's native catalog does not list the exact configured model."},
	"opencode-state-unavailable":   {"launch.state", "Private per-launch OpenCode state could not be prepared; inspect the existing fence."},
	"opencode-provider-capacity":   {"opencode.providers", "The exact provider requires an explicit positive concurrency limit and a free slot."},
	"agy-settings-unavailable":     {"launch.trust", "AGY needs readable valid effective settings and private trust for the exact checkout; inspect this launch before retrying."},
	"goal-budget-invalid":          {"issue.max-tokens", "Use an exact nonnegative token count or decimal k/m suffix within the supported integer range; omit the key for unknown."},
	"goal-objective-invalid":       {"issue.title", "Provide a nonempty assignment title within the native goal length bound."},
	"goal-prompt-delivery-failed":  {"launch.prompt", "Agent registration was confirmed, but prompt acceptance was not; inspect the run before any new dispatch."},
	"goal-prompt-unconfirmed":      {"launch.prompt", "Agent registration was confirmed, but prompt acceptance was not; inspect the run before any new dispatch."},
	"codex-effort-invalid":         {"route.effort", "The Codex effort is outside the matrix contract; correct the route before launching."},
	"receipt-owner-invalid":        {"matrix.receipt_owner_uid/receipt_owner_gid", "Configure both nonnegative numeric owner IDs, or omit both."},
	"source-missing":               {"matrix.source", "Configure an absolute clean Harness checkout."},
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
	"override-invalid":             {"matrix.grants", "Verify the complete authorized route, authorizer and matching pin. Legacy matrix overrides also require the exact owner worklog entry."},
	"route-unavailable":            {"matrix", "No matrix route is available on the eligible transports."},
	"retry-pins-unavailable":       {"matrix", "The original dispatch pins no longer resolve exactly; start a new issue for a changed route."},
	"routing-invalid":              {"issue.tier/role", "Specify a workload tier and a role allowed by the selected profile."},
	"resolution-failed":            {"matrix", "Verify Deno, the pinned source contract and matrix CLI; resolution could not complete."},
	"quota-unavailable":            {"governor", "Native transports require fresh quota, headroom and capacity; OpenCode requires explicit free provider capacity and native discovery."},
	"harness-conflict":             {"issue.harness", "The requested harness conflicts with the selected matrix transport."},
	"router-unsupported":           {"issue.router", "Router substitution has no supported adapter."},
	"host-unavailable":             {"hosts", "No eligible host has capacity for the selected transport and target."},
	"receipt-persistence-failed":   {"matrix.receipt_root", "Inspect receipt permissions and any existing reservation; never erase a fence to retry blindly."},
	"dispatch-persistence-failed":  {"matrix.receipt_root", "Dispatch binding could not be persisted; inspect the existing reservation."},
	"launch-failed":                {"launch", "Inspect the registration abandonment notice and durable launch fence."},
	"launch-inconclusive":          {"launch", "Launch effects could have occurred; inspect the durable fence and the agent before any new dispatch."},
	"observer-unavailable":         {"route.observed", "Evaluator launch is inconclusive until independent model and session evidence exists."},
}

type matrixReason string

func (e matrixReason) Error() string { return string(e) }
func (e matrixReason) Unwrap() error { return errMatrix }

var validQuotaTransports = map[string]bool{
	"claude":   true,
	"codex":    true,
	"agy":      true,
	"opencode": true,
}

var validQuotaConditions = map[string]bool{
	"blocked by capacity": true,
	"absent":              true,
	"stale":               true,
	"expired":             true,
	"over ceiling":        true,
}

func validQuotaDetail(detail string) bool {
	if detail == "" {
		return false
	}
	items := strings.Split(detail, ", ")
	if len(items) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, item := range items {
		parts := strings.Split(item, ": ")
		if len(parts) != 2 {
			return false
		}
		transport, cond := parts[0], parts[1]
		if !validQuotaTransports[transport] || seen[transport] {
			return false
		}
		seen[transport] = true
		if !validQuotaConditions[cond] {
			return false
		}
	}
	return true
}

func validMatrixRefusal(r matrixRefusal) bool {
	_, ok := matrixReasons[r.ReasonCode]
	if !ok || (r.Cause != "" && !matrixSites[r.Cause]) {
		return false
	}
	inconclusive := r.ReasonCode == "observer-unavailable" || r.ReasonCode == "launch-inconclusive" || r.ReasonCode == "goal-prompt-delivery-failed" || r.ReasonCode == "goal-prompt-unconfirmed"
	if (inconclusive && r.Status != "inconclusive") || (!inconclusive && r.Status != "refused") {
		return false
	}
	if r.Detail != "" && (r.ReasonCode != "quota-unavailable" || !validQuotaDetail(r.Detail)) {
		return false
	}
	return true
}
func refusalFor(err error) matrixRefusal {
	if errors.Is(err, errEvaluatorEvidence) {
		return evaluatorRefusal()
	}
	var reason matrixReason
	if errors.As(err, &reason) {
		status := "refused"
		if reason == "observer-unavailable" || reason == "launch-inconclusive" || reason == "goal-prompt-delivery-failed" || reason == "goal-prompt-unconfirmed" {
			status = "inconclusive"
		}
		r := matrixRefusal{status, string(reason), matrixCause(err), ""}
		if validMatrixRefusal(r) {
			return r
		}
	}
	return matrixRefusal{"refused", "resolution-failed", matrixCause(err), ""}
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
	if r.Detail != "" {
		log.Printf("issue #%d: matrix-launch-refused status=%s reason=%s field=%s cause=%s detail=%s; %s", n, r.Status, r.ReasonCode, detail.field, r.Cause, r.Detail, detail.hint)
	} else {
		log.Printf("issue #%d: matrix-launch-refused status=%s reason=%s field=%s cause=%s; %s", n, r.Status, r.ReasonCode, detail.field, r.Cause, detail.hint)
	}
	if c.dry {
		return
	}
	// Pre-launch refusals and post-launch delivery blocks are distinct observations.
	// An inconclusive registered launch must never be called a no-agent refusal.
	if r.ReasonCode == "goal-prompt-unconfirmed" {
		c.publishLaunchState(n, is, "blocked", r.ReasonCode)
	} else if r.Status == "refused" && r.ReasonCode != "launch-failed" {
		c.publishLaunchState(n, is, "refused", r.ReasonCode)
	}
	key := shaText([]byte(is.ID + "\x00" + briefDigest(is) + "\x00" + r.ReasonCode + "\x00" + r.Cause + "\x00" + r.Detail))
	c.st.mu.Lock()
	notified := c.st.MatrixNotices[n] == key
	c.st.mu.Unlock()
	if notified {
		return
	}
	body := fmt.Sprintf("divybot: matrix launch %s. Reason: `%s`. Field: `%s`. %s No agent was launched by this refused attempt. %s", r.Status, r.ReasonCode, detail.field, detail.hint, matrixRetryNote)
	if r.ReasonCode == "launch-failed" || r.ReasonCode == "launch-inconclusive" || r.ReasonCode == "dispatch-persistence-failed" || r.ReasonCode == "goal-prompt-delivery-failed" || r.ReasonCode == "goal-prompt-unconfirmed" {
		body = fmt.Sprintf("divybot: matrix launch %s. Reason: `%s`. Field: `%s`. %s Launch outcome requires inspection; this notice does not authorize another attempt.", r.Status, r.ReasonCode, detail.field, detail.hint)
	}
	if r.Cause != "" {
		body += " Cause: `" + r.Cause + "`."
	}
	if r.Detail != "" {
		body += " Detail: `" + r.Detail + "`."
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
	if block, ok := c.st.LaunchBlocks[n]; ok && block.Reason == "goal-prompt-unconfirmed" && r.ReasonCode == "goal-prompt-unconfirmed" {
		block.Notified = true
		c.st.LaunchBlocks[n] = block
	}
	if c.st.saveLocked() != nil {
		log.Printf("issue #%d: matrix refusal notification persistence failed", n)
	}
	// A crash between comment and state write may duplicate a comment, never a launch.
}

// Refused attempts are not terminal: tick re-runs matrixAttempt on every poll while the label stays.
const matrixRetryNote = "divybot re-checks this issue on every poll while the label stays, so a later poll may still launch it; a follow-up notice is posted if it does."

// reportMatrixLaunchAfterRefusal closes the loop on a refusal notice. Without it the thread reads
// "no agent was launched" followed by an agent's own output, with nothing explaining the retry.
func (c *Coord) reportMatrixLaunchAfterRefusal(ctx context.Context, n int, post func(context.Context, string, int, string) error) {
	if c.dry || c.st == nil {
		return
	}
	c.st.mu.Lock()
	_, refused := c.st.MatrixNotices[n]
	c.st.mu.Unlock()
	if !refused {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	body := "divybot: a later poll launched an agent for this issue. The earlier matrix refusal notice on this issue is superseded."
	if post(ctx, c.cfg.Inbox, n, body) != nil {
		log.Printf("issue #%d: superseded-refusal notice unavailable", n)
		return
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	delete(c.st.MatrixNotices, n)
	if c.st.saveLocked() != nil {
		log.Printf("issue #%d: matrix refusal notification persistence failed", n)
	}
}

var matrixSites = map[string]bool{
	"spawn.opencode-route": true, "spawn.opencode-environment": true, "spawn.opencode-preflight": true, "launch.opencode-route": true,
	"spawn.native-store-binding": true, "spawn.agy-trust": true, "spawn.agy-readiness": true,
	"attempt.command-render": true, "spawn.command-render": true, "spawn.registration-budget": true,
	"spawn.registration-render": true,
	"spawn.native-binding":      true, "dispatch.native-clear": true,
	"receipt.owner-stage": true, "receipt.owner-transfer": true, "receipt.owner-sync": true, "receipt.owner-publish": true, "dispatch.owner-transfer": true,
	"source.arguments": true, "source.head-command": true, "source.revision-mismatch": true, "source.status-command": true, "source.dirty": true,
	"decode.envelope":                      true,
	"output.limit":                         true,
	"command.exit":                         true,
	"command.timeout":                      true,
	"command.canceled":                     true,
	"command.signal":                       true,
	"command.start":                        true,
	"resolve.temp-create":                  true,
	"resolve.temp-write-close":             true,
	"resolve.encode-request":               true,
	"resolve.command-or-source":            true,
	"resolve.route-field":                  true,
	"resolve.route-digest":                 true,
	"json.duplicate-key":                   true,
	"json.delimiter":                       true,
	"json.value":                           true,
	"json.trailing":                        true,
	"json.decode":                          true,
	"routing-file.arguments":               true,
	"routing-file.response":                true,
	"routing-file.base64":                  true,
	"receipt.root-or-key":                  true,
	"receipt.temp-directory":               true,
	"receipt.encode":                       true,
	"receipt.binding-encode":               true,
	"receipt.file-create":                  true,
	"receipt.file-write-sync-close":        true,
	"receipt.directory-sync":               true,
	"receipt.reservation-exists-or-create": true,
	"receipt.publish-sync":                 true,
	"dispatch.nil-receipt":                 true,
	"dispatch.nil-binding":                 true,
	"dispatch.encode":                      true,
	"dispatch.temp-create":                 true,
	"dispatch.write-sync-close":            true,
	"dispatch.publish-sync":                true,
	"directory.open":                       true,
	"resolve.command":                      true,
	"resolve.source-changed":               true,
	"routing-file.command":                 true,
	"attempt.nil-receipt":                  true,
	"attempt.profile-read":                 true,
	"spawn.receipt-claim":                  true,
	"spawn.launching-binding":              true,
	"spawn.dispatched-binding":             true,
	"spawn.environment":                    true,
	"spawn.agent-start":                    true,
	"spawn.agent-envelope":                 true,
	"spawn.workspace-create":               true,
	"spawn.workspace-envelope":             true,
	"spawn.workspace-handles":              true,
	"launch.target":                        true,
	"launch.auth-sync":                     true,
	"launch.worktree":                      true,
	"launch.goal-file":                     true,
	"launch.fence":                         true,
	"launch.registration":                  true,
	"launch.state-save":                    true,
}

type matrixSiteError struct {
	site  string
	cause error
}

func (e *matrixSiteError) Error() string { return "matrix-refused: " + e.site }
func (e *matrixSiteError) Unwrap() error { return e.cause }
func matrixSite(site string, cause error) error {
	if cause == nil {
		cause = errMatrix
	}
	return &matrixSiteError{site, cause}
}
func matrixCause(err error) string {
	// Preserve the innermost named cause: wrappers cannot erase the originating site.
	site := ""
	for err != nil {
		if e, ok := err.(*matrixSiteError); ok && matrixSites[e.site] {
			site = e.site
		}
		err = errors.Unwrap(err)
	}
	return site
}
func refusalWithReason(err error, code string) matrixRefusal {
	r := refusalFor(matrixReason(code))
	r.Cause = matrixCause(err)
	return r
}
func refusalForQuota(detail string) matrixRefusal {
	r := refusalFor(matrixReason("quota-unavailable"))
	r.Detail = detail
	return r
}
