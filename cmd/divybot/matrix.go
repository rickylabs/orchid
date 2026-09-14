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
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MatrixConfig is private operator configuration, not issue-supplied authority.
// Pins are replaceable named values. No default model list is compiled into divybot.
type MatrixConfig struct {
	Source          string               `json:"source"`
	Revision        string               `json:"revision"`
	ReceiptRoot     string               `json:"receipt_root"`
	TargetRevisions map[string]string    `json:"target_revisions"`
	Pins            map[string]MatrixPin `json:"pins"`
	Grants          []MatrixGrant        `json:"grants"`
}

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
	IssueID       string           `json:"issue_id"`
	Repo          string           `json:"repo"`
	BriefDigest   string           `json:"brief_digest"`
	Tier          string           `json:"tier"`
	Role          string           `json:"role"`
	Authorization *MatrixAuthority `json:"authorization,omitempty"`
	Override      *MatrixOverride  `json:"ownerMatrixOverride,omitempty"`
}
type matrixRequest struct {
	PinName       string           `json:"pinName,omitempty"`
	Tier          string           `json:"tier"`
	Role          string           `json:"role"`
	ProfileText   string           `json:"profileText,omitempty"`
	Pin           *MatrixPin       `json:"pin,omitempty"`
	Authorization *MatrixAuthority `json:"authorization,omitempty"`
	Override      *MatrixOverride  `json:"ownerMatrixOverride,omitempty"`
	WorklogText   string           `json:"worklogText,omitempty"`
	Available     []string         `json:"availableTransports"`
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
}

var sourceRevision = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var profileStem = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var errMatrix = errors.New("matrix-refused")
var errEvaluatorEvidence = errors.New("inconclusive: observer-unavailable")

// reasonCode reuses the closed unknown-observation vocabulary of the I1 route receipt.
// This records a refusal, never a successful launch receipt or an observed session identity.
type matrixRefusal struct {
	Status     string `json:"status"`
	ReasonCode string `json:"reasonCode"`
}

func evaluatorRefusal() matrixRefusal { return matrixRefusal{"inconclusive", "observer-unavailable"} }
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
	if strictJSON(out, &route) != nil {
		return route, errMatrix
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
		return 0, errMatrix
	}
	return b.Buffer.Write(p)
}
func matrixCommand(ctx context.Context, cwd, command string, input []byte, args ...string) ([]byte, error) {
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
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return nil, errMatrix
	}
	return out.Bytes(), nil
}
func sourceClean(ctx context.Context, cfg MatrixConfig) bool {
	if !filepath.IsAbs(cfg.Source) || !sourceRevision.MatchString(cfg.Revision) {
		return false
	}
	head, e := matrixCommand(ctx, cfg.Source, "git", nil, "rev-parse", "HEAD")
	if e != nil || strings.TrimSpace(string(head)) != cfg.Revision {
		return false
	}
	status, e := matrixCommand(ctx, cfg.Source, "git", nil, "status", "--porcelain", "--untracked-files=all")
	return e == nil && len(status) == 0
}
func resolveMatrix(ctx context.Context, cfg MatrixConfig, request matrixRequest) (matrixRoute, error) {
	var route matrixRoute
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !sourceClean(ctx, cfg) {
		return route, matrixReason("source-invalid")
	}
	script, e := os.CreateTemp("", "matrix-*.ts")
	if e != nil {
		return route, errMatrix
	}
	defer os.Remove(script.Name())
	_, e = script.WriteString(matrixBridge)
	closeErr := script.Close()
	if e != nil || closeErr != nil {
		return route, errMatrix
	}
	input, e := json.Marshal(request)
	if e != nil {
		return route, errMatrix
	}
	out, e := matrixCommand(ctx, cfg.Source, "deno", input, "run", "--no-config", "--no-lock", "--no-prompt", "--allow-read="+cfg.Source, "--allow-run=deno", script.Name())
	if e != nil || !sourceClean(ctx, cfg) {
		return route, errMatrix
	}
	route, e = decodeMatrixResult(out)
	if e != nil {
		return route, e
	}
	for _, s := range []string{route.Provider, route.Model, route.LogicalModel, route.Effort, route.RequestedEffort, route.Transport, route.Family, route.Tier, route.Role} {
		if !cleanText(s) {
			return matrixRoute{}, errMatrix
		}
	}
	if !digestPattern.MatchString(route.Digest) {
		return matrixRoute{}, errMatrix
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
						return errMatrix
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
				return errMatrix
			}
			_, e = tokens.Token()
			return e
		}
		return nil
	}
	if value() != nil {
		return errMatrix
	}
	if _, e := tokens.Token(); e != io.EOF {
		return errMatrix
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errMatrix
	}
	return nil
}

// readRoutingFile reads only a pinned target revision; content and private filenames never reach logs.
func readRoutingFile(ctx context.Context, repo, revision, name string) (string, error) {
	if !repositoryName.MatchString(repo) || !sourceRevision.MatchString(revision) || filepath.IsAbs(name) || strings.Contains(name, "\\") || filepath.ToSlash(filepath.Clean(name)) != name || strings.HasPrefix(name, "../") {
		return "", errMatrix
	}
	data, e := matrixCommand(ctx, "", "gh", nil, "api", "repos/"+repo+"/contents/"+name+"?ref="+revision)
	var file struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Type     string `json:"type"`
	}
	if e != nil || json.Unmarshal(data, &file) != nil || file.Encoding != "base64" || file.Type != "file" {
		return "", errMatrix
	}
	decoded, e := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if e != nil || len(decoded) > 256*1024 {
		return "", errMatrix
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
		req.Authorization, req.Override = grant.Authorization, grant.Override
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
	return req, nil
}

type matrixObservation struct {
	Status     string `json:"status"`
	ReasonCode string `json:"reasonCode"`
	Reason     string `json:"reason"`
}
type matrixReceipt struct {
	SchemaVersion int `json:"schemaVersion"`
	Resolution    struct {
		SourceRevision string `json:"sourceRevision"`
		Digest         string `json:"digest"`
		ResolvedAt     string `json:"resolvedAt"`
		Selected       struct {
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
	Provider      string            `json:"provider"`
	SchemaVersion int               `json:"schemaVersion"`
	RunID         string            `json:"runId"`
	Issue         dispatchIssue     `json:"issue"`
	ParentRunID   *string           `json:"parentRunId"`
	Source        string            `json:"source"`
	Profile       string            `json:"profile"`
	Model         string            `json:"model"`
	Effort        string            `json:"effort"`
	State         string            `json:"state"`
	Location      *dispatchLocation `json:"location"`
}

type durableMatrixReceipt struct {
	mu       sync.Mutex
	file     string
	digest   string
	command  string
	claimed  bool
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
func persistMatrixReceipt(root, key, command string, receipt matrixReceipt, binding any) (*durableMatrixReceipt, error) {
	if !privateReceiptRoot(root) || !digestPattern.MatchString(key) {
		return nil, errMatrix
	}
	dir, e := os.MkdirTemp(root, ".pending-")
	if e != nil {
		return nil, errMatrix
	}
	defer os.RemoveAll(dir)
	body, e := json.Marshal(receipt)
	if e != nil {
		return nil, errMatrix
	}
	metadata, e := json.Marshal(binding)
	if e != nil {
		return nil, errMatrix
	}
	for name, data := range map[string][]byte{"receipt.json": body, "binding.json": metadata} {
		f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, errMatrix
		}
		_, w := f.Write(data)
		syncErr := f.Sync()
		closeErr := f.Close()
		if w != nil || syncErr != nil || closeErr != nil {
			return nil, errMatrix
		}
	}
	if syncDirectory(dir) != nil {
		return nil, errMatrix
	}
	target := filepath.Join(root, key)
	// mkdir is the cross-process exclusion primitive. Its presence fences every later attempt.
	if os.Mkdir(target, 0700) != nil {
		return nil, errMatrix
	}
	if os.Rename(dir, filepath.Join(target, "record")) != nil || syncDirectory(target) != nil || syncDirectory(root) != nil {
		return nil, errMatrix
	}
	return &durableMatrixReceipt{file: filepath.Join(target, "record", "receipt.json"), digest: shaText(body), command: command}, nil
}

// Atomic snapshots use the existing reservation as their durable ownership boundary.
func (r *durableMatrixReceipt) writeDispatch(state string, location *dispatchLocation) error {
	if r == nil {
		return errMatrix
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatch == nil {
		return errMatrix
	}
	next := *r.dispatch
	next.State, next.Location = state, location
	data, e := json.Marshal(next)
	if e != nil {
		return errMatrix
	}
	dir := filepath.Dir(r.file)
	f, e := os.CreateTemp(dir, ".dispatch-")
	if e != nil {
		return errMatrix
	}
	defer os.Remove(f.Name())
	_, written := f.Write(data)
	synced := f.Sync()
	closed := f.Close()
	if written != nil || synced != nil || closed != nil {
		return errMatrix
	}
	if os.Rename(f.Name(), filepath.Join(dir, "dispatch.json")) != nil || syncDirectory(dir) != nil {
		return errMatrix
	}
	r.dispatch = &next
	return nil
}

func syncDirectory(dir string) error {
	f, e := os.Open(dir)
	if e != nil {
		return errMatrix
	}
	defer f.Close()
	return f.Sync()
}

// The injection points exercise the common production attempt, not a parallel test-only path.
type matrixAttemptDeps struct {
	report  func(matrixRefusal)
	read    func(context.Context, string, string, string) (string, error)
	resolve func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error)
	persist func(string, string, string, matrixReceipt, any) (*durableMatrixReceipt, error)
	launch  func(context.Context, int, Issue, Host, string, Overrides, *durableMatrixReceipt) bool
	host    func(Target, string) (Host, bool)
}

func (c *Coord) matrixAttempt(ctx context.Context, n int, is Issue, target Target, budget map[string]int, d matrixAttemptDeps) (string, bool) {
	report := d.report
	if report == nil {
		report = reportMatrixRefusal
	}
	refuse := func(code string) (string, bool) { report(refusalFor(matrixReason(code))); return "", false }
	cfg := c.cfg.Matrix
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
	req.ProfileText, e = d.read(ctx, target.Repo, revision, "profiles/"+o.Profile+".md")
	if e != nil || req.ProfileText == "" {
		return refuse("profile-unavailable")
	}
	if req.Override != nil {
		req.WorklogText, e = d.read(ctx, target.Repo, revision, req.Override.WorklogPath)
		if e != nil {
			return refuse("override-worklog-unavailable")
		}
	}
	now := time.Now()
	c.gov.mu.Lock()
	for _, transport := range []string{"claude", "codex", "agy"} {
		q := c.gov.q[transport]
		if budget[transport] > 0 && q.ok && !q.at.After(now) && now.Sub(q.at) <= 3*c.cfg.Governor.sampleIntervalDur() && quotaHasHeadroom(q, now, c.cfg.Governor.WeeklyCeiling) {
			req.Available = append(req.Available, transport)
		}
	}
	c.gov.mu.Unlock()
	route, e := d.resolve(ctx, cfg, req)
	if errors.Is(e, errEvaluatorEvidence) || (e == nil && strings.HasSuffix(route.Role, "_evaluation")) {
		report(evaluatorRefusal())
		return "", false
	}
	if e != nil {
		if len(req.Available) == 0 && (refusalFor(e).ReasonCode == "resolution-failed" || refusalFor(e).ReasonCode == "route-unavailable") {
			return refuse("quota-unavailable")
		}
		report(refusalFor(e))
		return "", false
	}
	if !containsString(req.Available, route.Transport) {
		return refuse("quota-unavailable")
	}
	agent := route.Transport
	if o.Harness != "" && o.Harness != agent {
		if o.Harness == "codex-run" && agent == "codex" {
			agent = o.Harness
		} else {
			return refuse("harness-conflict")
		}
	}
	if o.Router != "" {
		return refuse("router-unsupported")
	} // no gateway adapter or native router substitution
	o.Model, o.Effort, o.Harness, o.Tier, o.Role = route.Model, route.Effort, agent, route.Tier, route.Role
	host, ok := d.host(target, route.Transport)
	if !ok {
		return refuse("host-unavailable")
	}
	if c.dry {
		return route.Transport, true
	}
	binding := struct {
		IssueID, Repo, BriefDigest, ProfileRevision, ProfileDigest string
		Request                                                    matrixRequest
		Route                                                      matrixRoute
	}{is.ID, target.Repo, briefDigest(is), revision, shaText([]byte(req.ProfileText)), req, route}
	// Same brief cannot be automatically launched twice, including after ambiguous transport failure.
	key := shaText([]byte(is.ID + "\x00" + target.Repo + "\x00" + briefDigest(is)))
	handle, e := d.persist(cfg.ReceiptRoot, key, buildAgentCmd(agent, o), receiptFor(cfg, route), binding)
	if e != nil || handle == nil {
		return refuse("receipt-persistence-failed")
	}
	handle.dispatch = &dispatchBinding{SchemaVersion: 1, RunID: "orchid-" + key,
		Issue: dispatchIssue{Repo: c.cfg.Inbox, Number: n}, Source: route.Transport,
		Profile: o.Profile, Provider: route.Provider, Model: route.Model, Effort: route.Effort}
	if handle.writeDispatch("reserved", nil) != nil {
		return refuse("dispatch-persistence-failed")
	}
	if !d.launch(ctx, n, is, host, agent, o, handle) {
		return refuse("launch-failed")
	}
	return route.Transport, true
}
func containsString(xs []string, value string) bool {
	for _, x := range xs {
		if x == value {
			return true
		}
	}
	return false
}
func quotaHasHeadroom(q quota, now time.Time, ceiling float64) bool {
	if ceiling <= 0 || ceiling > 100 {
		return false
	}
	// Missing/expired buckets are unknown, not entitlement. Both subscription windows must be known.
	for _, r := range []RateLimit{q.five, q.seven} {
		if r.ResetsAt <= now.Unix() || r.UsedPct < 0 || r.UsedPct >= ceiling {
			return false
		}
	}
	return true
}
