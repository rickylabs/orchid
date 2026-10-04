package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MatrixConfig is private operator configuration, not issue-supplied authority.
// Pins are replaceable named values. No default model list is compiled into divybot.
type MatrixConfig struct {
	Source          string                      `json:"source"`
	Revision        string                      `json:"revision"`
	ProfileRevision string                      `json:"profile_revision,omitempty"`
	ReceiptOwnerUID *int                        `json:"receipt_owner_uid,omitempty"`
	ReceiptOwnerGID *int                        `json:"receipt_owner_gid,omitempty"`
	ReceiptRoot     string                      `json:"receipt_root"`
	TargetRevisions map[string]string           `json:"target_revisions"`
	Pins            map[string]MatrixPin        `json:"pins"`
	Grants          []MatrixGrant               `json:"grants"`
	BudgetDefaults  map[string]map[string]int64 `json:"budget_defaults,omitempty"`
}

// profileRevision pins the Harness profiles, as the cockpit's profile pin does. Unset,
// profiles come from the same Harness revision as the routing source.
func (m MatrixConfig) profileRevision() string {
	if m.ProfileRevision != "" {
		return m.ProfileRevision
	}
	return m.Revision
}

// The identity travels with every new revision pin. An older receipt without this
// field remains readable as historical evidence, but cannot be retried as Harness.
const matrixSourceRepository = "rickylabs/harness"

type MatrixPin struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}
type MatrixAuthority struct {
	Authorizer string `json:"authorizer"`
	Rationale  string `json:"rationale"`
}
type MatrixOverride struct {
	Pin         string    `json:"pin,omitempty"`
	Authorizer  string    `json:"authorizer"`
	Rationale   string    `json:"rationale"`
	WorklogPath string    `json:"worklogPath"`
	Route       MatrixPin `json:"route"`
}

// A grant is bound to the GitHub node identity and entire brief, not a local issue number.
// It has no mutable decision/answer lifecycle. Editing the brief requires a distinct grant.
type MatrixGrant struct {
	IssueID        string               `json:"issue_id"`
	Repo           string               `json:"repo"`
	BriefDigest    string               `json:"brief_digest"`
	Tier           string               `json:"tier"`
	Role           string               `json:"role"`
	Authorization  *MatrixAuthority     `json:"authorization,omitempty"`
	Override       *MatrixOverride      `json:"ownerMatrixOverride,omitempty"`
	NativeOverride *ownerNativeOverride `json:"ownerNativeOverride,omitempty"`
}
type matrixRequest struct {
	PinName           string               `json:"pinName,omitempty"`
	Tier              string               `json:"tier"`
	Role              string               `json:"role"`
	ProfileText       string               `json:"profileText,omitempty"`
	Pin               *MatrixPin           `json:"pin,omitempty"`
	Authorization     *MatrixAuthority     `json:"authorization,omitempty"`
	Override          *MatrixOverride      `json:"ownerMatrixOverride,omitempty"`
	NativeOverride    *ownerNativeOverride `json:"ownerNativeOverride,omitempty"`
	WorklogText       string               `json:"worklogText,omitempty"`
	Available         []string             `json:"availableTransports"`
	OpenCodeProviders []string             `json:"openCodeProviders,omitempty"`
}
type matrixRoute struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	LogicalModel    string `json:"logicalModel"`
	Effort          string `json:"effort"`
	RequestedEffort string `json:"requestedEffort"`
	Transport       string `json:"transport"`
	Family          string `json:"family"`
	Tier            string `json:"tier"`
	Role            string `json:"role"`
	Digest          string `json:"digest"`
	TokenBudget     *int64 `json:"tokenBudget"`
	BudgetSource    string `json:"budgetSource"`
}

var sourceRevision = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var profileStem = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var budgetTierPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Placement evidence uses a configured short name, never an SSH target or address.
var placementHostName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,62}$`)
var errMatrix = errors.New("matrix-refused")
var errEvaluatorEvidence = errors.New("inconclusive: observer-unavailable")

// reasonCode reuses the closed unknown-observation vocabulary of the I1 route receipt.
// This records a refusal, never a successful launch receipt or an observed session identity.
type matrixRefusal struct {
	Status     string `json:"status"`
	ReasonCode string `json:"reasonCode"`
	Cause      string `json:"cause,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

func evaluatorRefusal() matrixRefusal {
	return matrixRefusal{"inconclusive", "observer-unavailable", "", ""}
}
func reportMatrixRefusal(refusal matrixRefusal) {
	// Only the closed, internally constructed record can reach the log. No caller data is echoed.
	if !validMatrixRefusal(refusal) {
		return
	}
	encoded, _ := json.Marshal(refusal)
	log.Printf("matrix launch refused: %s", encoded)
}
func decodeMatrixResult(out []byte) (matrixRoute, error) {
	var refusal matrixRefusal
	if strictJSON(out, &refusal) == nil && refusal == evaluatorRefusal() {
		return matrixRoute{}, errEvaluatorEvidence
	}
	if strictJSON(out, &refusal) == nil && validMatrixRefusal(refusal) {
		return matrixRoute{}, matrixReason(refusal.ReasonCode)
	}
	var route matrixRoute
	if e := strictJSON(out, &route); e != nil {
		return route, matrixSite("decode.envelope", e)
	}
	return route, nil
}

//go:embed matrix-bridge.ts
var matrixBridge string

func shaText(b []byte) string { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }
func briefDigest(is Issue) string {
	b, _ := json.Marshal(struct{ Title, Body string }{is.Title, is.Body})
	return shaText(b)
}
func cleanText(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\r\n\t\x00\u2028\u2029") && !strings.ContainsFunc(s, func(r rune) bool { return r < 32 || (r >= 127 && r <= 159) })
}

// limitedOutput bounds both subprocess output and decoder input without logging either.
type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1024*1024 {
		return 0, matrixSite("output.limit", errMatrix)
	}
	return b.Buffer.Write(p)
}
func matrixCommand(ctx context.Context, cwd, command string, input []byte, args ...string) ([]byte, error) {
	defer children.hold()()
	cmd := exec.CommandContext(ctx, command, args...)
	// Matrix subprocesses may themselves invoke the CLI. Cancel the entire group,
	// otherwise a timed-out bridge could leave a child behind after the next poll.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Dir = cwd
	cmd.Stdin = bytes.NewReader(input)
	cmd.WaitDelay = time.Second
	var out limitedOutput
	var stderr stderrTail
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if e := cmd.Run(); e != nil {
		failure := classifyCommandFailure(ctx, e)
		// Operator log only: a closed cause, the exit status and a scrubbed stderr tail.
		// The public refusal comment carries the closed cause alone.
		log.Printf("matrix subprocess %s failed: cause=%s %s", filepath.Base(command), matrixCause(failure), commandStatus(e, stderr.Bytes()))
		return nil, failure
	}
	return out.Bytes(), nil
}

// Before this split every failure read as command.exit, so a subprocess killed by the
// resolution deadline looked exactly like one that failed on its own.
func classifyCommandFailure(ctx context.Context, err error) error {
	var site *matrixSiteError
	var exit *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return matrixSite("command.timeout", errMatrix)
	case ctx.Err() != nil:
		return matrixSite("command.canceled", errMatrix)
	case errors.As(err, &site):
		return err // output.limit from the bounded stdout writer
	case errors.As(err, &exit) && exit.Exited():
		return matrixSite("command.exit", errMatrix)
	case errors.As(err, &exit):
		return matrixSite("command.signal", errMatrix)
	default:
		return matrixSite("command.start", errMatrix)
	}
}

func commandStatus(err error, stderr []byte) string {
	status := "exit=unknown"
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			status = "signal=" + ws.Signal().String()
		} else {
			status = "exit=" + strconv.Itoa(exit.ExitCode())
		}
	}
	return status + " stderr=" + strconv.Quote(scrubStderr(stderr))
}

// stderrTail keeps only the last few KiB a subprocess writes to stderr.
type stderrTail struct{ buf []byte }

const stderrTailLimit = 4096

func (t *stderrTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > stderrTailLimit {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-stderrTailLimit:]...)
	}
	return len(p), nil
}
func (t *stderrTail) Bytes() []byte { return t.buf }

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// scrubStderr keeps the last non-empty line and drops every word that could carry a path,
// URL, address, assignment or credential. What survives is the error's wording, never its data.
func scrubStderr(b []byte) string {
	lines := strings.Split(ansiEscape.ReplaceAllString(string(b), ""), "\n")
	line := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if line = strings.TrimSpace(lines[i]); line != "" {
			break
		}
	}
	words := strings.Fields(line)
	for i, w := range words {
		printable := !strings.ContainsFunc(w, func(r rune) bool { return r < 0x21 || r > 0x7e })
		if !printable || len(w) > 32 || strings.ContainsAny(w, `/\@=`) || strings.Contains(w, "://") || strings.ContainsFunc(w, func(r rune) bool { return r >= '0' && r <= '9' }) && len(w) > 12 {
			words[i] = "<redacted>"
		}
	}
	out := strings.Join(words, " ")
	if len(out) > 160 {
		out = out[:160]
	}
	return out
}
func sourceClean(ctx context.Context, cfg MatrixConfig) bool { return sourceCheck(ctx, cfg) == nil }
func sourceCheck(ctx context.Context, cfg MatrixConfig) error {
	if !filepath.IsAbs(cfg.Source) || !sourceRevision.MatchString(cfg.Revision) {
		return matrixSite("source.arguments", matrixReason("source-invalid"))
	}
	head, e := matrixCommand(ctx, cfg.Source, "git", nil, "rev-parse", "HEAD")
	if e != nil {
		return matrixSite("source.head-command", matrixReason("source-invalid"))
	}
	if strings.TrimSpace(string(head)) != cfg.Revision {
		return matrixSite("source.revision-mismatch", matrixReason("source-invalid"))
	}
	status, e := matrixCommand(ctx, cfg.Source, "git", nil, "status", "--porcelain", "--untracked-files=all")
	if e != nil {
		return matrixSite("source.status-command", matrixReason("source-invalid"))
	}
	if len(status) != 0 {
		return matrixSite("source.dirty", matrixReason("source-invalid"))
	}
	return nil
}

// matrixResolveTimeout bounds one whole resolution: two source checks, the bridge and the
// bridge's nested matrix CLI. A real git status check hit the prior 90 s bound under host
// disk I/O pressure on 2026-09-28. Allow one more bounded interval for source inspection;
// the caller's earlier deadline still wins. Keep this above the bridge's nested-CLI deadline.
const matrixResolveTimeout = 180 * time.Second

func resolveMatrix(ctx context.Context, cfg MatrixConfig, request matrixRequest) (matrixRoute, error) {
	var route matrixRoute
	ctx, cancel := context.WithTimeout(ctx, matrixResolveTimeout)
	defer cancel()
	if e := sourceCheck(ctx, cfg); e != nil {
		return route, e
	}
	script, e := os.CreateTemp("", "matrix-*.ts")
	if e != nil {
		return route, matrixSite("resolve.temp-create", errMatrix)
	}
	defer os.Remove(script.Name())
	_, e = script.WriteString(matrixBridge)
	closeErr := script.Close()
	if e != nil || closeErr != nil {
		return route, matrixSite("resolve.temp-write-close", errMatrix)
	}
	input, e := json.Marshal(request)
	if e != nil {
		return route, matrixSite("resolve.encode-request", errMatrix)
	}
	out, e := matrixCommand(ctx, cfg.Source, "deno", input, "run", "--no-config", "--no-lock", "--no-prompt", "--allow-read="+cfg.Source, "--allow-run=deno", script.Name())
	if e != nil {
		return route, matrixSite("resolve.command", e)
	}
	if e := sourceCheck(ctx, cfg); e != nil {
		return route, matrixSite("resolve.source-changed", e)
	}
	route, e = decodeMatrixResult(out)
	if e != nil {
		return route, e
	}
	for _, s := range []string{route.Provider, route.Model, route.LogicalModel, route.Effort, route.RequestedEffort, route.Transport, route.Family, route.Tier, route.Role} {
		if !cleanText(s) {
			return matrixRoute{}, matrixSite("resolve.route-field", errMatrix)
		}
	}
	if !digestPattern.MatchString(route.Digest) {
		return matrixRoute{}, matrixSite("resolve.route-digest", errMatrix)
	}
	return route, nil
}

// strictJSON rejects unknown fields, trailing documents and semantic duplicate keys.
func strictJSON(b []byte, out any) error {
	tokens := json.NewDecoder(bytes.NewReader(b))
	var value func() error
	value = func() error {
		token, e := tokens.Token()
		if e != nil {
			return e
		}
		if d, ok := token.(json.Delim); ok {
			switch d {
			case '{':
				seen := map[string]bool{}
				for tokens.More() {
					k, e := tokens.Token()
					if e != nil {
						return e
					}
					key, ok := k.(string)
					if !ok || seen[key] {
						return matrixSite("json.duplicate-key", errMatrix)
					}
					seen[key] = true
					if e := value(); e != nil {
						return e
					}
				}
			case '[':
				for tokens.More() {
					if e := value(); e != nil {
						return e
					}
				}
			default:
				return matrixSite("json.delimiter", errMatrix)
			}
			_, e = tokens.Token()
			return e
		}
		return nil
	}
	if e := value(); e != nil {
		return matrixSite("json.value", e)
	}
	if _, e := tokens.Token(); e != io.EOF {
		return matrixSite("json.trailing", errMatrix)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return matrixSite("json.decode", errMatrix)
	}
	return nil
}

// profilePath names a profile inside the Harness repository.
func profilePath(profile string) string { return "profiles/" + profile + ".md" }

// readRoutingFile reads only a pinned revision; content and private filenames never reach logs.
func readRoutingFile(ctx context.Context, repo, revision, name string) (string, error) {
	if !repositoryName.MatchString(repo) || !sourceRevision.MatchString(revision) || filepath.IsAbs(name) || strings.Contains(name, "\\") || filepath.ToSlash(filepath.Clean(name)) != name || strings.HasPrefix(name, "../") {
		return "", matrixSite("routing-file.arguments", errMatrix)
	}
	data, e := matrixCommand(ctx, "", "gh", nil, "api", "repos/"+repo+"/contents/"+name+"?ref="+revision)
	var file struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Type     string `json:"type"`
	}
	if e != nil {
		return "", matrixSite("routing-file.command", e)
	}
	if json.Unmarshal(data, &file) != nil || file.Encoding != "base64" || file.Type != "file" {
		return "", matrixSite("routing-file.response", errMatrix)
	}
	decoded, e := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if e != nil || len(decoded) > 256*1024 {
		return "", matrixSite("routing-file.base64", errMatrix)
	}
	return string(decoded), nil
}

func prepareMatrixRequest(cfg MatrixConfig, is Issue, repo string, o Overrides) (matrixRequest, error) {
	req := matrixRequest{Tier: o.Tier, Role: o.Role}
	if is.ID == "" {
		return req, matrixReason("issue-identity-missing")
	}
	if o.RoutingInvalid {
		return req, matrixReason("brief-routing-invalid")
	}
	count := 0
	for _, grant := range cfg.Grants {
		if grant.IssueID != is.ID || grant.Repo != repo {
			continue
		}
		// Grants for prior brief versions remain immutable history, but never authorize this version.
		if grant.BriefDigest != briefDigest(is) {
			continue
		}
		count++
		if count != 1 || (o.Tier != "" && grant.Tier != "" && o.Tier != grant.Tier) || (o.Role != "" && grant.Role != "" && o.Role != grant.Role) {
			return req, matrixReason("grant-conflict")
		}
		if req.Tier == "" {
			req.Tier = grant.Tier
		}
		if req.Role == "" {
			req.Role = grant.Role
		}
		req.Authorization, req.Override, req.NativeOverride = grant.Authorization, grant.Override, grant.NativeOverride
	}
	if o.Pin != "" {
		p, ok := cfg.Pins[o.Pin]
		if !ok || !cleanText(p.Model) || !cleanText(p.Effort) || o.Model != "" || o.Effort != "" {
			return req, matrixReason("pin-invalid")
		}
		req.PinName, req.Pin = o.Pin, &p
	} else if o.Model != "" || o.Effort != "" {
		req.Pin = &MatrixPin{Model: o.Model, Effort: o.Effort}
	}
	if req.NativeOverride != nil {
		if req.Override != nil || !validOwnerNativeOverride(req.NativeOverride) {
			return req, matrixReason("override-invalid")
		}
	}
	return req, nil
}

type matrixObservation struct {
	Status     string `json:"status"`
	ReasonCode string `json:"reasonCode"`
	Reason     string `json:"reason"`
}
type matrixReceipt struct {
	SchemaVersion       int                  `json:"schemaVersion"`
	OwnerNativeOverride *ownerNativeOverride `json:"ownerNativeOverride,omitempty"`
	Resolution          struct {
		SourceRepository string `json:"sourceRepository,omitempty"`
		SourceRevision   string `json:"sourceRevision"`
		Digest           string `json:"digest"`
		ResolvedAt       string `json:"resolvedAt"`
		Selected         struct {
			LogicalModel  string `json:"logicalModel"`
			PhysicalModel string `json:"physicalModel"`
		} `json:"selected"`
	} `json:"resolution"`
	Requested map[string]string            `json:"requested"`
	Observed  map[string]matrixObservation `json:"observed"`
}

func receiptFor(cfg MatrixConfig, route matrixRoute) matrixReceipt {
	var r matrixReceipt
	r.SchemaVersion = 1
	r.Resolution.SourceRepository = matrixSourceRepository
	r.Resolution.SourceRevision, r.Resolution.Digest = cfg.Revision, route.Digest
	r.Resolution.ResolvedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	r.Resolution.Selected.LogicalModel, r.Resolution.Selected.PhysicalModel = route.LogicalModel, route.Model
	r.Requested = map[string]string{"model": route.Model, "effort": route.RequestedEffort, "transport": route.Transport, "tier": route.Tier, "role": route.Role}
	r.Observed = map[string]matrixObservation{}
	for field := range r.Requested {
		r.Observed[field] = matrixObservation{"unknown", "observer-unavailable", "No independent runtime observation is available."}
	}
	return r
}

// A private, durable reservation is single-use, command-bound and survives process restarts.
// A failed/uncertain launch remains reserved; no automatic retry can duplicate its effect.
// This is dispatch context, separate from both the matrix policy receipt and native session identity.
// It stays in the existing private reservation directory and is read by dsh-telemetry.
type dispatchIssue struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}
type dispatchLocation struct {
	PaneID      string `json:"paneId"`
	WorkspaceID string `json:"workspaceId"`
}
type dispatchBinding struct {
	Provider        string            `json:"provider"`
	SchemaVersion   int               `json:"schemaVersion"`
	RunID           string            `json:"runId"`
	Issue           dispatchIssue     `json:"issue"`
	ParentRunID     *string           `json:"parentRunId"`
	Source          string            `json:"source"`
	Host            string            `json:"host,omitempty"`
	Profile         string            `json:"profile"`
	ProfileRevision string            `json:"profileRevision"`
	MatrixSource    string            `json:"matrixSource,omitempty"`
	MatrixRevision  string            `json:"matrixRevision"`
	Model           string            `json:"model"`
	Effort          string            `json:"effort,omitempty"`
	State           string            `json:"state"`
	Location        *dispatchLocation `json:"location"`
	TokenBudget     *int64            `json:"tokenBudget"`
	BudgetSource    string            `json:"budgetSource"`
}

type durableMatrixReceipt struct {
	mu       sync.Mutex
	file     string
	digest   string
	command  string
	claimed  bool
	owner    *receiptOwner
	dispatch *dispatchBinding
}

func (r *durableMatrixReceipt) claim(command string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed || r.command != command {
		return false
	}
	b, e := os.ReadFile(r.file)
	if e != nil || shaText(b) != r.digest {
		return false
	}
	r.claimed = true
	return true
}

// ReceiptRoot must already exist, be private, and be outside every Git worktree.
func privateReceiptRoot(root string) bool {
	if !filepath.IsAbs(root) {
		return false
	}
	resolved, e := filepath.EvalSymlinks(root)
	if e != nil || resolved != filepath.Clean(root) {
		return false
	}
	st, e := os.Stat(root)
	if e != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return false
	}
	for p := root; ; p = filepath.Dir(p) {
		if _, e := os.Lstat(filepath.Join(p, ".git")); e == nil || !os.IsNotExist(e) {
			return false
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return true
}
func persistMatrixReceipt(root, key, command string, receipt matrixReceipt, binding any, owners ...*receiptOwner) (*durableMatrixReceipt, error) {
	var owner *receiptOwner
	if len(owners) > 0 {
		owner = owners[0]
	}
	if !privateReceiptRoot(root) || !digestPattern.MatchString(key) {
		return nil, matrixSite("receipt.root-or-key", errMatrix)
	}
	dir, e := os.MkdirTemp(root, ".pending-")
	if e != nil {
		return nil, matrixSite("receipt.temp-directory", errMatrix)
	}
	defer os.RemoveAll(dir)
	body, e := json.Marshal(receipt)
	if e != nil {
		return nil, matrixSite("receipt.encode", errMatrix)
	}
	metadata, e := json.Marshal(binding)
	if e != nil {
		return nil, matrixSite("receipt.binding-encode", errMatrix)
	}
	for name, data := range map[string][]byte{"receipt.json": body, "binding.json": metadata} {
		f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, matrixSite("receipt.file-create", errMatrix)
		}
		_, w := f.Write(data)
		syncErr := f.Sync()
		closeErr := f.Close()
		if w != nil || syncErr != nil || closeErr != nil {
			return nil, matrixSite("receipt.file-write-sync-close", errMatrix)
		}
	}
	if syncDirectory(dir) != nil {
		return nil, matrixSite("receipt.directory-sync", errMatrix)
	}
	target := filepath.Join(root, key)
	// mkdir is the cross-process exclusion primitive. Its presence fences every later attempt.
	if os.Mkdir(target, 0700) != nil {
		return nil, matrixSite("receipt.reservation-exists-or-create", errMatrix)
	}
	if owner == nil {
		// Preserve the existing publication path when no owner is configured.
		if os.Rename(dir, filepath.Join(target, "record")) != nil || syncDirectory(target) != nil || syncDirectory(root) != nil {
			return nil, matrixSite("receipt.publish-sync", errMatrix)
		}
	} else {
		// Rename into a hidden staging name first. The final record name is never
		// visible until every file and both reservation directories have their owner.
		staged := filepath.Join(target, ".pending-record")
		if os.Rename(dir, staged) != nil {
			return nil, matrixSite("receipt.owner-stage", errMatrix)
		}
		defer os.RemoveAll(staged)
		if transferReceiptOwner(owner, filepath.Join(staged, "receipt.json"), filepath.Join(staged, "binding.json"), staged, target) != nil {
			return nil, matrixSite("receipt.owner-transfer", errMatrix)
		}
		if syncDirectory(staged) != nil || syncDirectory(target) != nil {
			return nil, matrixSite("receipt.owner-sync", errMatrix)
		}
		if os.Rename(staged, filepath.Join(target, "record")) != nil || syncDirectory(target) != nil || syncDirectory(root) != nil {
			return nil, matrixSite("receipt.owner-publish", errMatrix)
		}
	}
	return &durableMatrixReceipt{file: filepath.Join(target, "record", "receipt.json"), digest: shaText(body), command: command, owner: owner}, nil
}

// Atomic snapshots use the existing reservation as their durable ownership boundary.
func (r *durableMatrixReceipt) writeDispatch(state string, location *dispatchLocation) error {
	if r == nil {
		return matrixSite("dispatch.nil-receipt", errMatrix)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatch == nil {
		return matrixSite("dispatch.nil-binding", errMatrix)
	}
	if state != "dispatched" {
		if r.writeNativeIdentityLocked(nil) != nil {
			return matrixSite("dispatch.native-clear", errMatrix)
		}
	}
	next := *r.dispatch
	next.State, next.Location = state, location
	data, e := json.Marshal(next)
	if e != nil {
		return matrixSite("dispatch.encode", errMatrix)
	}
	dir := filepath.Dir(r.file)
	f, e := os.CreateTemp(dir, ".dispatch-")
	if e != nil {
		return matrixSite("dispatch.temp-create", errMatrix)
	}
	defer os.Remove(f.Name())
	_, written := f.Write(data)
	if e := transferReceiptOwner(r.owner, f.Name()); e != nil {
		f.Close()
		return matrixSite("dispatch.owner-transfer", errMatrix)
	}
	synced := f.Sync()
	closed := f.Close()
	if written != nil || synced != nil || closed != nil {
		return matrixSite("dispatch.write-sync-close", errMatrix)
	}
	if os.Rename(f.Name(), filepath.Join(dir, "dispatch.json")) != nil || syncDirectory(dir) != nil {
		return matrixSite("dispatch.publish-sync", errMatrix)
	}
	r.dispatch = &next
	return nil
}

func syncDirectory(dir string) error {
	f, e := os.Open(dir)
	if e != nil {
		return matrixSite("directory.open", errMatrix)
	}
	defer f.Close()
	return f.Sync()
}

// The injection points exercise the common production attempt, not a parallel test-only path.
type matrixAttemptDeps struct {
	report    func(matrixRefusal)
	read      func(context.Context, string, string, string) (string, error)
	resolve   func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error)
	persist   func(string, string, string, matrixReceipt, any, ...*receiptOwner) (*durableMatrixReceipt, error)
	launch    func(context.Context, int, Issue, Host, string, Overrides, *durableMatrixReceipt) error
	host      func(Target, string) (Host, bool)
	retry     *retryExpectation
	preflight bool
}

type retryExpectation struct {
	OperationID string
	Dispatch    dispatchBinding
	Tier        string
	Role        string
}

func retryPinsMatch(expected retryExpectation, cfg MatrixConfig, profile string, route matrixRoute, host Host) bool {
	d := expected.Dispatch
	return actionIDPattern.MatchString(expected.OperationID) && d.State == "dispatched" &&
		d.Source == route.Transport && d.Provider == route.Provider && d.Model == route.Model && d.Effort == route.Effort &&
		d.Profile == profile && d.ProfileRevision == cfg.profileRevision() && d.MatrixSource == matrixSourceRepository && d.MatrixRevision == cfg.Revision &&
		d.Host == host.Name && d.BudgetSource == route.BudgetSource && reflect.DeepEqual(d.TokenBudget, route.TokenBudget) &&
		expected.Tier == route.Tier && expected.Role == route.Role
}

func (c *Coord) matrixAttempt(ctx context.Context, n int, is Issue, target Target, budget map[string]int, d matrixAttemptDeps) (string, bool) {
	report := d.report
	if report == nil {
		report = reportMatrixRefusal
	}
	refuse := func(code string) (string, bool) { report(refusalFor(matrixReason(code))); return "", false }
	refuseQuota := func(detail string) (string, bool) { report(refusalForQuota(detail)); return "", false }
	cfg, grantError := c.matrixConfigForIssue(ctx, is, target.Repo)
	if grantError != nil {
		report(refusalFor(grantError))
		return "", false
	}
	owner, ownerError := configuredReceiptOwner(cfg)
	if ownerError != nil {
		return refuse("receipt-owner-invalid")
	}
	if cfg.Source == "" {
		return refuse("source-missing")
	}
	if !sourceRevision.MatchString(cfg.Revision) {
		return refuse("revision-invalid")
	}
	if !privateReceiptRoot(cfg.ReceiptRoot) {
		return refuse("receipt-root-invalid")
	}
	if !repositoryName.MatchString(c.cfg.Inbox) || n < 1 || is.Number != n {
		return refuse("issue-invalid")
	}
	o := parseOverrides(is.Body)
	req, e := prepareMatrixRequest(cfg, is, target.Repo, o)
	if e != nil {
		report(refusalFor(e))
		return "", false
	}
	revision := cfg.TargetRevisions[target.Repo]
	if !sourceRevision.MatchString(revision) {
		return refuse("target-revision-invalid")
	}
	// leaf.md itself declares the unnamed-profile default; its routing still comes from Markdown.
	if o.Profile == "" {
		o.Profile = "leaf"
	}
	if !profileStem.MatchString(o.Profile) {
		return refuse("profile-invalid")
	}
	profileRevision := cfg.profileRevision()
	if !sourceRevision.MatchString(profileRevision) { // guard:profile-revision
		return refuse("revision-invalid")
	}
	// Profiles are Harness Markdown (decision B), read at the pinned Harness profile revision
	// the cockpit lists from, never from the target repository, which need not carry any.
	req.ProfileText, e = d.read(ctx, matrixSourceRepository, profileRevision, profilePath(o.Profile)) // guard:profile-source
	if e != nil || req.ProfileText == "" {
		report(refusalWithReason(matrixSite("attempt.profile-read", e), "profile-unavailable"))
		return "", false
	}
	if req.Override != nil {
		req.WorklogText, e = d.read(ctx, target.Repo, revision, req.Override.WorklogPath)
		if e != nil {
			report(refusalWithReason(e, "override-worklog-unavailable"))
			return "", false
		}
	}
	now := time.Now()
	var quotaConditions []string
	c.gov.mu.Lock()
	for _, transport := range matrixTransports {
		if transport == "opencode" && len(c.cfg.OpenCode.Providers) == 0 {
			continue // A disabled adapter is not a blocked subscription quota.
		}
		q := c.gov.q[transport]
		cond := availabilityConditions[admissionTransportReason(transport, budget[transport], q, now,
			c.cfg.Governor.sampleIntervalDur(), c.cfg.Governor.WeeklyCeiling, c.cfg.UnmeteredTransports)]
		if transport == "opencode" {
			cond = "blocked by capacity"
			if c.cfg.OpenCode.valid() && budget[transport] > 0 && len(c.cfg.OpenCode.Providers) > 0 {
				cond = ""
			}
		}
		if cond == "" {
			req.Available = append(req.Available, transport)
		} else {
			quotaConditions = append(quotaConditions, transport+": "+cond)
		}
	}
	c.gov.mu.Unlock()
	for provider, limit := range c.cfg.OpenCode.Providers {
		if limit.MaxActive > 0 && budget["opencode:"+provider] > 0 {
			req.OpenCodeProviders = append(req.OpenCodeProviders, provider)
		}
	}
	quotaDetail := strings.Join(quotaConditions, ", ")
	route, e := resolveLaunchRoute(ctx, cfg, req, d.resolve)
	if errors.Is(e, errEvaluatorEvidence) || (req.NativeOverride == nil && e == nil && strings.HasSuffix(route.Role, "_evaluation")) {
		report(evaluatorRefusal())
		return "", false
	}
	if e != nil {
		if len(req.Available) == 0 && (refusalFor(e).ReasonCode == "resolution-failed" || refusalFor(e).ReasonCode == "route-unavailable") {
			return refuseQuota(quotaDetail)
		}
		// With the pinned route's transport quota-blocked (left out of Available), the resolver
		// reports what that leaves: a deviation needing an owner override, or no route. The cause
		// is quota. Resolve once more with every transport offered; when that picks a transport
		// the quota check blocked, report the quota condition instead.
		if quotaOnly(ctx, d, cfg, req, len(quotaConditions) > 0) {
			return refuseQuota(quotaDetail)
		}
		report(refusalFor(e))
		return "", false
	}
	if !containsString(req.Available, route.Transport) {
		return refuseQuota(quotaDetail)
	}
	agent := route.Transport
	if o.Harness != "" && o.Harness != agent {
		if o.Harness == "codex-run" && agent == "codex" {
			agent = o.Harness
		} else {
			return refuse("harness-conflict")
		}
	}
	if err := routeRouterError(route, o); err != nil {
		return refuse(string(err.(matrixReason)))
	}
	var openCodeProvider string
	if agent == "opencode" {
		native, _ := resolveOpenCodeRoute(Overrides{Model: route.Model, Router: o.Router, Effort: route.Effort})
		openCodeProvider = native.Provider
		if reason := c.providerBudgetLaunchReason(agent, Overrides{Model: route.Model, Router: o.Router, Effort: route.Effort}, now); reason != "" {
			return refuse(reason)
		}
		if c.cfg.OpenCode.Providers[openCodeProvider].MaxActive < 1 || budget["opencode:"+openCodeProvider] < 1 {
			return refuse("opencode-provider-capacity")
		}
	}
	resolvedBudget, budgetSource, budgetErr := resolveRouteBudget(cfg, route.Tier, o.Profile, o)
	if budgetErr != nil {
		return refuse(closedGoalReason(budgetErr))
	}
	route.TokenBudget, route.BudgetSource = resolvedBudget, budgetSource
	if resolvedBudget != nil {
		o.MaxTokens, o.MaxTokensPresent = strconv.FormatInt(*resolvedBudget, 10), true
	} else {
		o.MaxTokens, o.MaxTokensPresent = "", false
	}
	o.Model, o.Effort, o.Harness, o.Tier, o.Role = route.Model, route.Effort, agent, route.Tier, route.Role
	if req.NativeOverride != nil && o.Effort == "provider_default" {
		o.Effort = "" // Native default is an omitted flag, not a Harness effort substitution.
	}
	if agent == "codex" {
		assignment := is
		assignment.Number = n
		if _, e := nativeGoalIntent(c.cfg.Inbox, assignment, o); e != nil {
			return refuse(closedGoalReason(e))
		}
	}
	command, renderErr := buildAgentCmd(agent, o)
	if renderErr != nil {
		report(refusalFor(matrixSite("attempt.command-render", renderErr)))
		return "", false
	}
	host, ok := d.host(target, route.Transport)
	if !ok || !placementHostName.MatchString(host.Name) {
		return refuse("host-unavailable")
	}
	if d.retry != nil && !retryPinsMatch(*d.retry, cfg, o.Profile, route, host) {
		return refuse("retry-pins-unavailable")
	}
	if c.dry || d.preflight {
		return route.Transport, true
	}
	binding := struct {
		IssueID, Repo, BriefDigest, ProfileRevision, ProfileDigest, Host string
		Request                                                          matrixRequest
		Route                                                            matrixRoute
	}{is.ID, target.Repo, briefDigest(is), profileRevision, shaText([]byte(req.ProfileText)), host.Name, req, route}
	// Same brief cannot be automatically launched twice, including after ambiguous transport failure.
	key := shaText([]byte(is.ID + "\x00" + target.Repo + "\x00" + briefDigest(is)))
	if d.retry != nil {
		key = shaText([]byte("retry\x00" + key + "\x00" + d.retry.OperationID))
	}
	receipt := receiptFor(cfg, route)
	matrixSource, matrixRevision := matrixSourceRepository, cfg.Revision
	if req.NativeOverride != nil {
		receipt = receiptForOwnerNative(route, req.NativeOverride)
		matrixSource, matrixRevision = ownerNativeSource, ""
	}
	handle, e := d.persist(cfg.ReceiptRoot, key, command, receipt, binding, owner)
	if e != nil {
		report(refusalWithReason(e, "receipt-persistence-failed"))
		return "", false
	}
	if handle == nil {
		report(refusalWithReason(matrixSite("attempt.nil-receipt", errMatrix), "receipt-persistence-failed"))
		return "", false
	}
	handle.dispatch = &dispatchBinding{SchemaVersion: 1, RunID: "orchid-" + key,
		Issue: dispatchIssue{Repo: c.cfg.Inbox, Number: n}, Source: route.Transport,
		Host: host.Name, Profile: o.Profile, ProfileRevision: profileRevision, MatrixSource: matrixSource, MatrixRevision: matrixRevision,
		Provider: route.Provider, Model: route.Model, Effort: route.Effort,
		TokenBudget: resolvedBudget, BudgetSource: budgetSource}
	if e := handle.writeDispatch("reserved", nil); e != nil {
		report(refusalWithReason(e, "dispatch-persistence-failed"))
		return "", false
	}
	// Supersede an earlier refusal before any launch effects. A failed launch
	// remains inconclusive; it cannot revive a false no-agent refusal.
	c.publishLaunchState(n, is, "launching", "")
	if openCodeProvider != "" {
		budget["opencode:"+openCodeProvider]-- // no refund after an ambiguous launch effect
	}
	if e := d.launch(ctx, n, is, host, agent, o, handle); e != nil {
		if agySettingsBlocked(e) {
			// spawn already persisted and reported this specific no-seat block.
			// Keep the one-attempt fence without overwriting it with uncertainty.
			// No new reason enters the reader's closed launch-state vocabulary.
			return "", false
		}
		var reason matrixReason
		if errors.As(e, &reason) && reason == "goal-prompt-unconfirmed" {
			// Registration and job persistence succeeded. Prompt confirmation is
			// uncertain, but the already-dispatched receipt still truthfully binds
			// the agent. A later exact native report may finish that binding.
			report(refusalFor(e))
		} else {
			// A failed call can have executed its effect before returning an error.
			// Keep the fence and report uncertainty, never a false no-agent claim.
			_ = handle.writeDispatch("uncertain", handle.dispatch.Location)
			report(refusalWithReason(e, "launch-inconclusive"))
		}
		return "", false
	}
	c.publishLaunchState(n, is, "launched", "")
	return route.Transport, true
}

// Native subscription meters keep their published v1 contract. OpenCode
// admission uses explicit provider capacity, not one fabricated vendor meter.
var meteredTransports = []string{"claude", "codex"}

// Adapters supported by the matrix dispatcher.
var matrixTransports = []string{"claude", "codex", "agy", "opencode"}

// quotaOnly reports whether a refused request resolves once every transport is offered,
// to a non-evaluator route on a transport the quota check left out. Only then was quota
// the sole reason for the refusal.
func quotaOnly(ctx context.Context, d matrixAttemptDeps, cfg MatrixConfig, req matrixRequest, blocked bool) bool {
	if !blocked {
		return false
	}
	unconstrained := req
	unconstrained.Available = append([]string(nil), matrixTransports...)
	route, e := resolveLaunchRoute(ctx, cfg, unconstrained, d.resolve)
	return e == nil && !strings.HasSuffix(route.Role, "_evaluation") && !containsString(req.Available, route.Transport)
}

func containsString(xs []string, value string) bool {
	for _, x := range xs {
		if x == value {
			return true
		}
	}
	return false
}

// Transport availability reasons: one machine code per way a transport can be
// unavailable to admission. Admission and the published snapshot
// (transport_availability.go) read the same codes, so they cannot disagree.
const (
	availabilityNoCapacity           = "no-capacity"
	availabilityMeterUnread          = "meter-unread"
	availabilityMeterStale           = "meter-stale"
	availabilityWindowExpired        = "window-expired"
	availabilityFiveHourCeiling      = "5h-ceiling"
	availabilityWeeklyCeiling        = "weekly-ceiling"
	availabilityCeilingMisconfigured = "ceiling-misconfigured"
)

// The operator-facing words each reason had before the codes existed. Refusal
// details keep them; the 5h/weekly split is in the codes.
var availabilityConditions = map[string]string{
	availabilityNoCapacity:           "blocked by capacity",
	availabilityMeterUnread:          "absent",
	availabilityMeterStale:           "stale",
	availabilityWindowExpired:        "expired",
	availabilityFiveHourCeiling:      "over ceiling",
	availabilityWeeklyCeiling:        "over ceiling",
	availabilityCeilingMisconfigured: "over ceiling",
}

func quotaHeadroomReason(q quota, now time.Time, ceiling float64) string {
	if ceiling <= 0 || ceiling > 100 {
		return availabilityCeilingMisconfigured
	}
	// A zero reset means the meter did not publish this window. Every published
	// window must be fresh and under ceiling; an empty meter grants no headroom.
	published := 0
	for _, window := range []struct {
		limit  RateLimit
		reason string
	}{{q.five, availabilityFiveHourCeiling}, {q.seven, availabilityWeeklyCeiling}} {
		r := window.limit
		if r.ResetsAt == 0 {
			continue
		}
		published++
		if r.ResetsAt <= now.Unix() {
			return availabilityWindowExpired
		}
		if r.UsedPct < 0 || r.UsedPct >= ceiling {
			return window.reason
		}
	}
	if published == 0 {
		return availabilityMeterUnread
	}
	return ""
}

func quotaHeadroomCondition(q quota, now time.Time, ceiling float64) string {
	return availabilityConditions[quotaHeadroomReason(q, now, ceiling)]
}

func quotaHasHeadroom(q quota, now time.Time, ceiling float64) bool {
	return quotaHeadroomCondition(q, now, ceiling) == ""
}

func transportAvailabilityReason(budget int, q quota, now time.Time, sampleInterval time.Duration, ceiling float64) string {
	if budget <= 0 {
		return availabilityNoCapacity
	}
	if !q.ok {
		return availabilityMeterUnread
	}
	if q.at.After(now) || now.Sub(q.at) > 3*sampleInterval {
		return availabilityMeterStale
	}
	return quotaHeadroomReason(q, now, ceiling)
}

func transportQuotaCondition(budget int, q quota, now time.Time, sampleInterval time.Duration, ceiling float64) string {
	return availabilityConditions[transportAvailabilityReason(budget, q, now, sampleInterval, ceiling)]
}
