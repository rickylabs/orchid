package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// This protocol is private operator authority, never a public snapshot.
type ownerNativePortConfig struct {
	Socket       string `json:"socket"`
	StoreRoot    string `json:"store_root"`
	ApprovalRoot string `json:"approval_root"`
	OperatorUID  *int   `json:"operator_uid"`
}

type ownerNativeInstallRequest struct {
	SchemaVersion       int                  `json:"schema_version"`
	OperationID         string               `json:"operation_id"`
	ApprovalRef         string               `json:"approval_ref"`
	ExpectedIssueID     string               `json:"expected_issue_id"`
	IssueNumber         int                  `json:"issue_number"`
	Target              string               `json:"target"`
	ExpectedBriefDigest string               `json:"expected_brief_digest"`
	Tier                string               `json:"tier"`
	Role                string               `json:"role"`
	Profile             string               `json:"profile"`
	NativeOverride      *ownerNativeOverride `json:"ownerNativeOverride"`
	// Trigger "comment" binds the grant to a /swarm comment the owner will post on
	// the source issue Target#IssueNumber; ExpectedIssueID is that issue's node ID
	// and ExpectedBriefDigest the sha256 of the exact comment body. Absent = an
	// inbox issue, unchanged.
	Trigger string `json:"trigger,omitempty"`
}

type ownerNativeApproval struct {
	SchemaVersion  int                       `json:"schema_version"`
	Authorizer     string                    `json:"authorizer"`
	Inbox          string                    `json:"inbox"`
	MatrixRevision string                    `json:"matrix_revision"`
	TargetRevision string                    `json:"target_revision"`
	Request        ownerNativeInstallRequest `json:"request"`
}

type ownerNativeAck struct {
	SchemaVersion  int               `json:"schema_version"`
	OperationID    string            `json:"operation_id,omitempty"`
	State          string            `json:"state"`
	Reason         string            `json:"reason,omitempty"`
	IssueID        string            `json:"issue_id,omitempty"`
	IssueNumber    int               `json:"issue_number,omitempty"`
	Inbox          string            `json:"inbox,omitempty"`
	Target         string            `json:"target,omitempty"`
	BriefDigest    string            `json:"brief_digest,omitempty"`
	Profile        string            `json:"profile,omitempty"`
	MatrixRevision string            `json:"matrix_revision,omitempty"`
	TargetRevision string            `json:"target_revision,omitempty"`
	Route          *ownerNativeRoute `json:"route,omitempty"`
	RecordChecksum string            `json:"record_checksum,omitempty"`
}

type ownerNativeIssue struct {
	Issue  Issue    `json:"issue"`
	State  string   `json:"state"`
	Labels []string `json:"labels"`
}

type ownerNativeGrantRecord struct {
	SchemaVersion  int                       `json:"schema_version"`
	Inbox          string                    `json:"inbox"`
	MatrixRevision string                    `json:"matrix_revision"`
	TargetRevision string                    `json:"target_revision"`
	ApprovalDigest string                    `json:"approval_digest"`
	Request        ownerNativeInstallRequest `json:"request"`
	Issue          ownerNativeIssue          `json:"bound_issue"`
	Grant          MatrixGrant               `json:"grant"`
}

type ownerNativeIntent struct {
	SchemaVersion  int    `json:"schema_version"`
	OperationID    string `json:"operation_id"`
	RecordChecksum string `json:"record_checksum"`
}

var ownerNativeApprovalRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// An independent snapshot; c.cfg and its grant slice are never modified.
// Files remain the durable authority and are revalidated on scoped admission.
type ownerNativeGrantStore struct {
	mu      sync.Mutex
	cfg     *Config
	options ownerNativePortConfig
	deps    matrixConfigDeps
	active  map[string]ownerNativeGrantRecord
	known   map[string]bool
	// Syscall fault controls, not operator configuration.
	fileSync      func(*os.File) error
	dirSync       func(string) error
	link          func(string, string) error
	activate      func() error
	peerUID       func(*net.UnixConn) (int, error)
	prepareSocket func(string, os.FileInfo, int) error
}

func ownerNativePrivateDir(root string, uid int) bool {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !privateReceiptRoot(root) {
		return false
	}
	info, err := os.Lstat(root)
	return err == nil && info.IsDir() && info.Mode().Perm() == 0700 && ownerNativeOwned(info, uid)
}

func ownerNativePrivateRead(path string, uid int) ([]byte, error) {
	return ownerNativePrivateReadWithOpen(path, uid, ownerNativeOpen)
}

func ownerNativePrivateReadWithOpen(path string, uid int, open func(string) (*os.File, error)) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !ownerNativePrivateDir(filepath.Dir(path), uid) {
		return nil, errMatrix
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || !ownerNativeOwned(before, uid) || before.Size() > 1024*1024 {
		return nil, errMatrix
	}
	f, err := open(path)
	if err != nil {
		return nil, errMatrix
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errMatrix
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 || !utf8.Valid(raw) {
		return nil, errMatrix
	}
	return raw, nil
}

func ownerNativePortOptionsValid(options ownerNativePortConfig) bool {
	if options.OperatorUID == nil || *options.OperatorUID < 0 || uint64(*options.OperatorUID) >= 4294967295 ||
		!filepath.IsAbs(options.Socket) || filepath.Clean(options.Socket) != options.Socket ||
		!ownerNativePrivateDir(filepath.Dir(options.Socket), *options.OperatorUID) ||
		!ownerNativePrivateDir(options.StoreRoot, os.Getuid()) || !ownerNativePrivateDir(options.ApprovalRoot, *options.OperatorUID) {
		return false
	}
	return options.StoreRoot != options.ApprovalRoot && !strings.HasPrefix(options.ApprovalRoot, options.StoreRoot+string(os.PathSeparator)) && !strings.HasPrefix(options.StoreRoot, options.ApprovalRoot+string(os.PathSeparator))
}

func cloneOwnerNativeConfig(cfg *Config) (*Config, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, errMatrix
	}
	var copy Config
	if json.Unmarshal(raw, &copy) != nil {
		return nil, errMatrix
	}
	return &copy, nil
}

func newOwnerNativeGrantStore(ctx context.Context, cfg *Config, options ownerNativePortConfig, deps matrixConfigDeps) (*ownerNativeGrantStore, error) {
	if cfg == nil || !ownerNativePortOptionsValid(options) || deps.command == nil || deps.read == nil || deps.resolve == nil {
		return nil, errMatrix
	}
	copy, err := cloneOwnerNativeConfig(cfg)
	if err != nil {
		return nil, errMatrix
	}
	if len(validateMatrixConfig(ctx, copy)) > 0 {
		return nil, errMatrix
	}
	uid := *options.OperatorUID
	options.OperatorUID = &uid
	s := &ownerNativeGrantStore{cfg: copy, options: options, deps: deps, active: map[string]ownerNativeGrantRecord{}, known: map[string]bool{}}
	for _, name := range []string{"records", "intents", "claims"} {
		path := filepath.Join(options.StoreRoot, name)
		if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return nil, errMatrix
		}
		if !ownerNativePrivateDir(path, os.Getuid()) || syncDirectory(options.StoreRoot) != nil {
			return nil, errMatrix
		}
	}
	if err := s.recover(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func ownerNativeRequestValid(r ownerNativeInstallRequest) bool {
	return r.SchemaVersion == 1 && actionIDPattern.MatchString(r.OperationID) && ownerNativeApprovalRef.MatchString(r.ApprovalRef) && r.IssueNumber > 0 && cleanText(r.ExpectedIssueID) && len(r.ExpectedIssueID) <= 256 &&
		repositoryName.MatchString(r.Target) && digestPattern.MatchString(r.ExpectedBriefDigest) && budgetTierPattern.MatchString(r.Tier) &&
		profileStem.MatchString(strings.ReplaceAll(r.Role, "_", "-")) && profileStem.MatchString(r.Profile) && validOwnerNativeOverride(r.NativeOverride) && (r.Trigger == "" || r.Trigger == ownerNativeCommentTrigger)
}

func (s *ownerNativeGrantStore) issueKey(n int) string {
	return shaText([]byte(s.cfg.Inbox + "\x00" + strconv.Itoa(n)))
}
func ownerNativeOperationKey(operation string) string { return shaText([]byte(operation)) }
func (s *ownerNativeGrantStore) recordPath(operation string) string {
	return filepath.Join(s.options.StoreRoot, "records", ownerNativeOperationKey(operation)+".json")
}
func (s *ownerNativeGrantStore) intentDir(n int) string {
	return filepath.Join(s.options.StoreRoot, "intents", s.issueKey(n))
}

const ownerNativeCommentTrigger = "comment"

// A grant subject is the issue a grant is bound to: an inbox issue, or a source
// issue whose owner /swarm comment will trigger the launch. Comment subjects use
// their own key space, so a source issue in the inbox repository never shares
// intents with the same issue used as an inbox binding.
type ownerNativeSubject struct {
	comment bool
	repo    string
	number  int
}

func (s *ownerNativeGrantStore) requestSubject(r ownerNativeInstallRequest) ownerNativeSubject {
	if r.Trigger == ownerNativeCommentTrigger {
		return ownerNativeSubject{comment: true, repo: r.Target, number: r.IssueNumber}
	}
	return ownerNativeSubject{repo: s.cfg.Inbox, number: r.IssueNumber}
}

func (s *ownerNativeGrantStore) issueSubject(is Issue) ownerNativeSubject {
	if is.Source != nil {
		return ownerNativeSubject{comment: true, repo: is.Source.Repo, number: is.Source.Number}
	}
	return ownerNativeSubject{repo: s.cfg.Inbox, number: is.Number}
}

func (s *ownerNativeGrantStore) subjectKey(subject ownerNativeSubject) string {
	if subject.comment {
		return shaText([]byte("comment\x00" + strings.ToLower(subject.repo) + "\x00" + strconv.Itoa(subject.number)))
	}
	return s.issueKey(subject.number)
}

func (s *ownerNativeGrantStore) subjectDir(subject ownerNativeSubject) string {
	return filepath.Join(s.options.StoreRoot, "intents", s.subjectKey(subject))
}

func (s *ownerNativeGrantStore) fetchSubject(ctx context.Context, subject ownerNativeSubject) (ownerNativeIssue, error) {
	return s.fetchIssueIn(ctx, subject.repo, subject.number)
}

// subjectCurrent: an inbox issue must still carry the exact brief and target; a
// source issue only has to be the same open issue, because its brief is the
// comment, which is checked by digest at admission.
func (s *ownerNativeGrantStore) subjectCurrent(subject ownerNativeSubject, bound, current ownerNativeIssue, repo string, inert bool) bool {
	if subject.comment {
		return current.State == "OPEN" && bound.Issue.ID == current.Issue.ID && bound.Issue.Number == current.Issue.Number
	}
	return ownerNativeIssueMatches(bound, current, inert) && s.targetMatches(current.Labels, repo)
}

func (s *ownerNativeGrantStore) healthy() bool {
	return ownerNativePortOptionsValid(s.options) && ownerNativePrivateDir(filepath.Join(s.options.StoreRoot, "intents"), os.Getuid()) && ownerNativePrivateDir(filepath.Join(s.options.StoreRoot, "records"), os.Getuid())
}

// A plain sentence for the operator log. The wire reason stays the closed
// override-invalid/grant-conflict pair that Cockpit decodes strictly.
type ownerNativeWhy string

func (w ownerNativeWhy) Error() string { return string(w) }

func ownerNativeExplain(err error, fallback string) string {
	if why, ok := err.(ownerNativeWhy); ok {
		return string(why)
	}
	return fallback
}

// Domain-separates an app approval from operator-file bytes, so removing a file
// still fences a record that the file approved.
const ownerNativeAppApproval = "owner-app-approval-v1\x00"

func (s *ownerNativeGrantStore) approval(r ownerNativeInstallRequest) (string, error) {
	if !s.healthy() || !ownerNativeRequestValid(r) {
		return "", ownerNativeWhy("the private grant store is unhealthy or the request is malformed")
	}
	path := filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		// Owner rule (Eric, 2026-10-04): the verified owner's Approve in Cockpit is
		// the approval. Only the operator peer reaches this endpoint, and it is the
		// same UID that owns the approval root, so a file adds no authority. The
		// approval is bound to the current inbox and pins, as a file would be.
		target := s.cfg.Matrix.TargetRevisions[r.Target]
		if !sourceRevision.MatchString(s.cfg.Matrix.Revision) || !sourceRevision.MatchString(target) {
			return "", ownerNativeWhy("the matrix or target revision is not pinned")
		}
		raw, err := json.Marshal(ownerNativeApproval{SchemaVersion: 1, Authorizer: "eric", Inbox: s.cfg.Inbox, MatrixRevision: s.cfg.Matrix.Revision, TargetRevision: target, Request: r})
		if err != nil {
			return "", errMatrix
		}
		return shaText(append([]byte(ownerNativeAppApproval), raw...)), nil
	}
	raw, err := ownerNativePrivateRead(path, *s.options.OperatorUID)
	var a ownerNativeApproval
	if err != nil || ownerNativeStrictJSON(raw, &a) != nil || a.SchemaVersion != 1 || a.Authorizer != "eric" || a.Inbox != s.cfg.Inbox ||
		!sourceRevision.MatchString(a.MatrixRevision) || a.MatrixRevision != s.cfg.Matrix.Revision ||
		!sourceRevision.MatchString(a.TargetRevision) || a.TargetRevision != s.cfg.Matrix.TargetRevisions[r.Target] || !reflect.DeepEqual(a.Request, r) {
		return "", ownerNativeWhy("an operator approval file exists for this launch and does not match it")
	}
	return shaText(raw), nil
}

// The Go reader binds the whole title/body and also verifies inert issue state.
func (s *ownerNativeGrantStore) fetchIssue(ctx context.Context, n int) (ownerNativeIssue, error) {
	return s.fetchIssueIn(ctx, s.cfg.Inbox, n)
}

func (s *ownerNativeGrantStore) fetchIssueIn(ctx context.Context, repo string, n int) (ownerNativeIssue, error) {
	raw, err := s.deps.command(ctx, "", "gh", nil, "issue", "view", strconv.Itoa(n), "--repo", repo, "--json", "id,number,title,body,state,labels")
	var fields map[string]json.RawMessage
	if err != nil || len(raw) > 1024*1024 || !utf8.Valid(raw) || strictJSON(raw, &fields) != nil {
		return ownerNativeIssue{}, errMatrix
	}
	for _, key := range []string{"id", "number", "title", "body", "state", "labels"} {
		if value, ok := fields[key]; !ok || string(value) == "null" {
			return ownerNativeIssue{}, errMatrix
		}
	}
	var decoded struct {
		ID                 string
		Number             int
		Title, Body, State string
		Labels             []struct{ Name string }
	}
	if json.Unmarshal(raw, &decoded) != nil || !cleanText(decoded.ID) || decoded.Number != n || (decoded.State != "OPEN" && decoded.State != "CLOSED") || len(decoded.Labels) > 128 {
		return ownerNativeIssue{}, errMatrix
	}
	out := ownerNativeIssue{Issue: Issue{ID: decoded.ID, Number: n, Title: decoded.Title, Body: decoded.Body}, State: decoded.State, Labels: []string{}}
	for _, l := range decoded.Labels {
		if !cleanText(l.Name) || len(l.Name) > 128 {
			return ownerNativeIssue{}, errMatrix
		}
		out.Labels = append(out.Labels, l.Name)
	}
	return out, nil
}

func (s *ownerNativeGrantStore) targetMatches(labels []string, repo string) bool {
	for _, target := range s.cfg.Targets {
		if containsString(labels, target.Label) {
			return !target.Disabled && target.Repo == repo
		}
	}
	return false
}

func ownerNativeIssueMatches(bound ownerNativeIssue, current ownerNativeIssue, inert bool) bool {
	return current.State == "OPEN" && bound.Issue.ID == current.Issue.ID && bound.Issue.Number == current.Issue.Number && briefDigest(bound.Issue) == briefDigest(current.Issue) && (!inert || !containsString(current.Labels, "harness"))
}

func (s *ownerNativeGrantStore) makeRecord(ctx context.Context, r ownerNativeInstallRequest) (ownerNativeGrantRecord, error) {
	digest, err := s.approval(r)
	if err != nil {
		return ownerNativeGrantRecord{}, err
	}
	if r.Trigger == ownerNativeCommentTrigger {
		return s.makeCommentRecord(ctx, r, digest)
	}
	is, err := s.fetchIssue(ctx, r.IssueNumber)
	switch {
	case err != nil:
		return ownerNativeGrantRecord{}, ownerNativeWhy("the inbox issue could not be read")
	case is.State != "OPEN":
		return ownerNativeGrantRecord{}, ownerNativeWhy("the inbox issue is closed")
	case containsString(is.Labels, "harness"):
		return ownerNativeGrantRecord{}, ownerNativeWhy("the inbox issue is already labelled harness")
	case is.Issue.ID != r.ExpectedIssueID:
		return ownerNativeGrantRecord{}, ownerNativeWhy("the inbox issue is not the approved issue")
	case briefDigest(is.Issue) != r.ExpectedBriefDigest:
		return ownerNativeGrantRecord{}, ownerNativeWhy("the brief changed after it was approved")
	case !s.targetMatches(is.Labels, r.Target):
		return ownerNativeGrantRecord{}, ownerNativeWhy("the issue's target label does not name the approved target")
	}
	found := false
	for _, t := range s.cfg.Targets {
		if t.Repo == r.Target && !t.Disabled {
			found = true
		}
	}
	o := parseOverrides(is.Issue.Body)
	profile := o.Profile
	if profile == "" {
		profile = "leaf"
	}
	if !found {
		return ownerNativeGrantRecord{}, ownerNativeWhy("the target is not configured or is disabled")
	}
	if profile != r.Profile {
		return ownerNativeGrantRecord{}, ownerNativeWhy("the brief's profile is not the approved profile")
	}
	input, _ := json.Marshal(MatrixGrant{Tier: r.Tier, Role: r.Role, NativeOverride: r.NativeOverride})
	grant, problems := bindMatrixGrant(is.Issue, r.Target, input)
	if len(problems) > 0 {
		return ownerNativeGrantRecord{}, ownerNativeWhy("the grant could not be bound to the issue")
	}
	copy, err := cloneOwnerNativeConfig(s.cfg)
	if err != nil {
		return ownerNativeGrantRecord{}, err
	}
	for _, old := range copy.Matrix.Grants {
		if old.IssueID == grant.IssueID && old.Repo == grant.Repo && old.BriefDigest == grant.BriefDigest {
			return ownerNativeGrantRecord{}, ownerNativeWhy("a startup grant already covers this issue and brief")
		}
	}
	copy.Matrix.Grants = append(copy.Matrix.Grants, grant)
	if problems := validateMatrixConfig(ctx, copy); len(problems) > 0 {
		return ownerNativeGrantRecord{}, ownerNativeWhy("the matrix config refuses the grant (" + problems[0].Field + ": " + problems[0].Reason + ")")
	}
	if problems := validateMatrixIssue(ctx, copy, is.Issue, r.Target, s.deps); len(problems) > 0 {
		return ownerNativeGrantRecord{}, ownerNativeWhy("the issue cannot launch on this route (" + problems[0].Field + ": " + problems[0].Reason + ")")
	}
	return ownerNativeGrantRecord{SchemaVersion: 1, Inbox: s.cfg.Inbox, MatrixRevision: s.cfg.Matrix.Revision, TargetRevision: s.cfg.Matrix.TargetRevisions[r.Target], ApprovalDigest: digest, Request: r, Issue: is, Grant: grant}, nil
}

// makeCommentRecord binds a grant to a source issue before the owner posts the
// trigger. The comment does not exist yet, so its brief is bound by digest only;
// route and profile are checked against the comment when it is admitted.
func (s *ownerNativeGrantStore) makeCommentRecord(ctx context.Context, r ownerNativeInstallRequest, digest string) (ownerNativeGrantRecord, error) {
	is, err := s.fetchIssueIn(ctx, r.Target, r.IssueNumber)
	switch {
	case err != nil:
		return ownerNativeGrantRecord{}, ownerNativeWhy("the source issue could not be read")
	case is.State != "OPEN":
		return ownerNativeGrantRecord{}, ownerNativeWhy("the source issue is closed")
	case is.Issue.ID != r.ExpectedIssueID:
		return ownerNativeGrantRecord{}, ownerNativeWhy("the source issue is not the approved issue")
	}
	grant, ok := s.commentGrant(r, is.Issue)
	if !ok {
		return ownerNativeGrantRecord{}, ownerNativeWhy("the target is not configured or is disabled")
	}
	copy, err := cloneOwnerNativeConfig(s.cfg)
	if err != nil {
		return ownerNativeGrantRecord{}, err
	}
	for _, old := range copy.Matrix.Grants {
		if old.IssueID == grant.IssueID && old.Repo == grant.Repo && old.BriefDigest == grant.BriefDigest {
			return ownerNativeGrantRecord{}, ownerNativeWhy("a startup grant already covers this issue and brief")
		}
	}
	copy.Matrix.Grants = append(copy.Matrix.Grants, grant)
	if problems := validateMatrixConfig(ctx, copy); len(problems) > 0 {
		return ownerNativeGrantRecord{}, ownerNativeWhy("the matrix config refuses the grant (" + problems[0].Field + ": " + problems[0].Reason + ")")
	}
	return ownerNativeGrantRecord{SchemaVersion: 1, Inbox: s.cfg.Inbox, MatrixRevision: s.cfg.Matrix.Revision, TargetRevision: s.cfg.Matrix.TargetRevisions[r.Target], ApprovalDigest: digest, Request: r, Issue: is, Grant: grant}, nil
}

// commentGrant is the grant a comment request binds: the source issue, the
// configured target repository and the digest of the comment to come.
func (s *ownerNativeGrantStore) commentGrant(r ownerNativeInstallRequest, source Issue) (MatrixGrant, bool) {
	found := false
	for _, t := range s.cfg.Targets {
		if t.Repo == r.Target && !t.Disabled {
			found = true
		}
	}
	input, _ := json.Marshal(MatrixGrant{Tier: r.Tier, Role: r.Role, NativeOverride: r.NativeOverride})
	grant, problems := bindMatrixGrant(source, r.Target, input)
	grant.BriefDigest = r.ExpectedBriefDigest
	return grant, found && len(problems) == 0
}

func (s *ownerNativeGrantStore) recordValid(record ownerNativeGrantRecord) bool {
	if record.Request.Trigger == ownerNativeCommentTrigger {
		return s.commentRecordValid(record)
	}
	found := false
	for _, target := range s.cfg.Targets {
		if target.Repo == record.Request.Target && !target.Disabled {
			found = true
		}
	}
	profile := parseOverrides(record.Issue.Issue.Body).Profile
	if profile == "" {
		profile = "leaf"
	}
	if !found || profile != record.Request.Profile || !s.targetMatches(record.Issue.Labels, record.Request.Target) {
		return false
	}
	if record.SchemaVersion != 1 || record.Inbox != s.cfg.Inbox || record.MatrixRevision != s.cfg.Matrix.Revision || record.TargetRevision != s.cfg.Matrix.TargetRevisions[record.Request.Target] || !ownerNativeRequestValid(record.Request) ||
		record.Issue.Issue.Number != record.Request.IssueNumber || record.Issue.Issue.ID != record.Request.ExpectedIssueID || record.Grant.BriefDigest != record.Request.ExpectedBriefDigest || !ownerNativeIssueMatches(record.Issue, record.Issue, true) {
		return false
	}
	approval, err := s.approval(record.Request)
	if err != nil || approval != record.ApprovalDigest {
		return false
	}
	for _, old := range s.cfg.Matrix.Grants {
		if old.IssueID == record.Grant.IssueID && old.Repo == record.Grant.Repo && old.BriefDigest == record.Grant.BriefDigest {
			return false
		}
	}
	input, _ := json.Marshal(MatrixGrant{Tier: record.Request.Tier, Role: record.Request.Role, NativeOverride: record.Request.NativeOverride})
	bound, problems := bindMatrixGrant(record.Issue.Issue, record.Request.Target, input)
	return len(problems) == 0 && reflect.DeepEqual(bound, record.Grant)
}

func (s *ownerNativeGrantStore) commentRecordValid(record ownerNativeGrantRecord) bool {
	r := record.Request
	if record.SchemaVersion != 1 || record.Inbox != s.cfg.Inbox || record.MatrixRevision != s.cfg.Matrix.Revision || record.TargetRevision != s.cfg.Matrix.TargetRevisions[r.Target] || !ownerNativeRequestValid(r) ||
		record.Issue.Issue.Number != r.IssueNumber || record.Issue.Issue.ID != r.ExpectedIssueID || record.Issue.State != "OPEN" || record.Grant.BriefDigest != r.ExpectedBriefDigest {
		return false
	}
	approval, err := s.approval(r)
	if err != nil || approval != record.ApprovalDigest {
		return false
	}
	for _, old := range s.cfg.Matrix.Grants {
		if old.IssueID == record.Grant.IssueID && old.Repo == record.Grant.Repo && old.BriefDigest == record.Grant.BriefDigest {
			return false
		}
	}
	bound, ok := s.commentGrant(r, record.Issue.Issue)
	return ok && reflect.DeepEqual(bound, record.Grant)
}

func (s *ownerNativeGrantStore) publish(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if !ownerNativePrivateDir(dir, os.Getuid()) {
		return errMatrix
	}
	f, err := os.CreateTemp(dir, ".owner-native-")
	if err != nil {
		return errMatrix
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(raw)
	fileSync := s.fileSync
	if fileSync == nil {
		fileSync = func(f *os.File) error { return f.Sync() }
	}
	syncErr := fileSync(f)
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errMatrix
	}
	link := s.link
	if link == nil {
		link = os.Link
	}
	if err = link(f.Name(), path); err != nil {
		if !os.IsExist(err) {
			return errMatrix
		}
		prior, e := ownerNativePrivateRead(path, os.Getuid())
		if e != nil || !bytes.Equal(prior, raw) {
			return matrixReason("grant-conflict")
		}
	}
	dirSync := s.dirSync
	if dirSync == nil {
		dirSync = syncDirectory
	}
	if dirSync(dir) != nil {
		return errMatrix
	}
	saved, e := ownerNativePrivateRead(path, os.Getuid())
	if e != nil || !bytes.Equal(saved, raw) {
		return errMatrix
	}
	return nil
}

func ownerNativeFailure(operation, state, reason string) ownerNativeAck {
	return ownerNativeAck{SchemaVersion: 1, OperationID: operation, State: state, Reason: reason}
}

// Every refused or unknown owner grant leaves one plain line in the daemon log.
func (s *ownerNativeGrantStore) refuse(issue int, operation, state, reason, why string) ownerNativeAck {
	subject := "owner grant"
	if issue > 0 {
		subject = fmt.Sprintf("issue #%d: owner grant", issue)
	}
	log.Printf("%s %s %s (%s): %s", subject, operation, state, reason, why)
	return ownerNativeFailure(operation, state, reason)
}

func (s *ownerNativeGrantStore) install(ctx context.Context, r ownerNativeInstallRequest) ownerNativeAck {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ownerNativeRequestValid(r) {
		return s.refuse(r.IssueNumber, "", "REFUSED", "override-invalid", "the install request is malformed")
	}
	// Detach typed callers too; no caller-owned route pointer enters admission.
	input, _ := json.Marshal(r)
	var detached ownerNativeInstallRequest
	if ownerNativeStrictJSON(input, &detached) != nil {
		return s.refuse(r.IssueNumber, "", "REFUSED", "override-invalid", "the install request could not be detached")
	}
	r = detached
	record, err := s.makeRecord(ctx, r)
	if err != nil {
		return s.refuse(r.IssueNumber, r.OperationID, "REFUSED", "override-invalid", ownerNativeExplain(err, "the approved launch could not be bound"))
	}
	// Do not publish a different payload through a previously used operation.
	if _, e := os.Lstat(s.recordPath(r.OperationID)); !os.IsNotExist(e) {
		old, readErr := ownerNativePrivateRead(s.recordPath(r.OperationID), os.Getuid())
		var prior ownerNativeGrantRecord
		if e != nil || readErr != nil || ownerNativeStrictJSON(old, &prior) != nil || !reflect.DeepEqual(prior, record) {
			return s.refuse(r.IssueNumber, r.OperationID, "REFUSED", "grant-conflict", "this operation already holds a different grant")
		}
	}
	subject := s.requestSubject(r)
	current, err := s.fetchSubject(ctx, subject)
	if err != nil || !s.subjectCurrent(subject, record.Issue, current, r.Target, true) {
		return s.refuse(r.IssueNumber, r.OperationID, "REFUSED", "override-invalid", "the issue changed while the grant was being prepared")
	}
	records, e := ownerNativePrivateEntries(filepath.Join(s.options.StoreRoot, "records"), 1024)
	if e != nil {
		return s.refuse(r.IssueNumber, r.OperationID, "UNKNOWN", "override-invalid", "the grant records could not be listed")
	}
	if len(records) == 1024 {
		if _, e = os.Lstat(s.recordPath(r.OperationID)); e != nil {
			return s.refuse(r.IssueNumber, r.OperationID, "REFUSED", "grant-conflict", "the grant store holds its maximum number of records")
		}
	}
	dir := s.subjectDir(subject)
	s.known[s.subjectKey(subject)] = true
	if err = os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return s.refuse(r.IssueNumber, r.OperationID, "UNKNOWN", "override-invalid", "the issue grant directory could not be created")
	}
	if !ownerNativePrivateDir(dir, os.Getuid()) || syncDirectory(filepath.Dir(dir)) != nil {
		return s.refuse(r.IssueNumber, r.OperationID, "UNKNOWN", "override-invalid", "the issue grant directory is not private")
	}
	entries, err := ownerNativePrivateEntries(dir, 32)
	if err != nil || len(entries) > 32 {
		return s.refuse(r.IssueNumber, r.OperationID, "REFUSED", "grant-conflict", "the issue holds its maximum number of grant intents")
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".owner-native-") {
			continue
		}
		raw, e := ownerNativePrivateRead(filepath.Join(dir, entry.Name()), os.Getuid())
		var intent ownerNativeIntent
		if e != nil || ownerNativeStrictJSON(raw, &intent) != nil || intent.SchemaVersion != 1 || !digestPattern.MatchString(intent.RecordChecksum) || !actionIDPattern.MatchString(intent.OperationID) || entry.Name() != ownerNativeOperationKey(intent.OperationID)+".json" {
			return s.refuse(r.IssueNumber, r.OperationID, "UNKNOWN", "override-invalid", "an earlier grant intent for this issue is unreadable")
		}
		if intent.OperationID == r.OperationID {
			continue
		}
		oldRaw, e := ownerNativePrivateRead(s.recordPath(intent.OperationID), os.Getuid())
		var old ownerNativeGrantRecord
		if e != nil || ownerNativeStrictJSON(oldRaw, &old) != nil || shaText(oldRaw) != intent.RecordChecksum || old.Request.OperationID != intent.OperationID || s.requestSubject(old.Request) != subject {
			return s.refuse(r.IssueNumber, r.OperationID, "UNKNOWN", "override-invalid", "an earlier grant record for this issue is unreadable")
		}
		if old.Grant.BriefDigest == record.Grant.BriefDigest {
			return s.refuse(r.IssueNumber, r.OperationID, "REFUSED", "grant-conflict", "another operation already granted this exact brief")
		}
	}
	if len(entries) == 32 {
		if _, e := os.Lstat(filepath.Join(dir, ownerNativeOperationKey(r.OperationID)+".json")); e != nil {
			return s.refuse(r.IssueNumber, r.OperationID, "REFUSED", "grant-conflict", "the issue holds its maximum number of grant intents")
		}
	}
	raw, _ := json.Marshal(record)
	intentRaw, _ := json.Marshal(ownerNativeIntent{SchemaVersion: 1, OperationID: r.OperationID, RecordChecksum: shaText(raw)})
	if err = s.publish(filepath.Join(dir, ownerNativeOperationKey(r.OperationID)+".json"), intentRaw); err != nil {
		return s.refuse(r.IssueNumber, r.OperationID, "UNKNOWN", "override-invalid", "the grant intent could not be saved")
	}
	if err = s.publish(s.recordPath(r.OperationID), raw); err != nil {
		return s.refuse(r.IssueNumber, r.OperationID, "UNKNOWN", "override-invalid", "the grant record could not be saved")
	}
	if !s.recordValid(record) {
		return s.refuse(r.IssueNumber, r.OperationID, "UNKNOWN", "override-invalid", "the saved grant did not read back valid")
	}
	if s.activate != nil && s.activate() != nil {
		return s.refuse(r.IssueNumber, r.OperationID, "UNKNOWN", "override-invalid", "the saved grant could not be activated")
	}
	s.active[r.OperationID] = record
	ack := s.ackLocked(ctx, r.OperationID, true)
	if ack.State == "LIVE" {
		log.Printf("issue #%d: owner grant %s LIVE for %s %s (%s)", r.IssueNumber, r.OperationID, ack.Route.Harness, ack.Route.Model, ack.Route.Effort)
	}
	return ack
}

func (s *ownerNativeGrantStore) readRecordLocked(operation string) (ownerNativeGrantRecord, []byte, error) {
	raw, err := ownerNativePrivateRead(s.recordPath(operation), os.Getuid())
	var record ownerNativeGrantRecord
	if err != nil || ownerNativeStrictJSON(raw, &record) != nil || record.Request.OperationID != operation || !s.recordValid(record) {
		return record, nil, errMatrix
	}
	intentRaw, e := ownerNativePrivateRead(filepath.Join(s.subjectDir(s.requestSubject(record.Request)), ownerNativeOperationKey(operation)+".json"), os.Getuid())
	var intent ownerNativeIntent
	if e != nil || ownerNativeStrictJSON(intentRaw, &intent) != nil || intent.SchemaVersion != 1 || intent.OperationID != operation || intent.RecordChecksum != shaText(raw) {
		return record, nil, errMatrix
	}
	return record, raw, nil
}

func (s *ownerNativeGrantStore) ackLocked(ctx context.Context, operation string, inert bool) ownerNativeAck {
	record, raw, err := s.readRecordLocked(operation)
	if err != nil || !reflect.DeepEqual(s.active[operation], record) {
		return s.refuse(0, operation, "UNKNOWN", "override-invalid", "the saved grant is missing, damaged or no longer valid")
	}
	subject := s.requestSubject(record.Request)
	current, err := s.fetchSubject(ctx, subject)
	if err != nil || !s.subjectCurrent(subject, record.Issue, current, record.Grant.Repo, inert) {
		return s.refuse(0, operation, "REFUSED", "override-invalid", "the issue changed after the grant was saved")
	}
	route := record.Request.NativeOverride.Route
	live := ownerNativeAck{SchemaVersion: 1, OperationID: operation, State: "LIVE", IssueID: record.Grant.IssueID, IssueNumber: record.Request.IssueNumber, Inbox: record.Inbox, Target: record.Grant.Repo, BriefDigest: record.Grant.BriefDigest, Profile: record.Request.Profile, MatrixRevision: record.MatrixRevision, TargetRevision: record.TargetRevision, Route: &route, RecordChecksum: shaText(raw)}
	if subject.comment {
		return live // the brief is the comment to come; admission checks it by digest
	}
	config, e := s.matrixForIssueLocked(ctx, Issue{ID: current.Issue.ID, Number: current.Issue.Number, Title: current.Issue.Title, Body: current.Issue.Body, Labels: current.Labels}, record.Grant.Repo)
	if e != nil {
		return s.refuse(0, operation, "UNKNOWN", "override-invalid", "the issue no longer admits exactly one owner grant")
	}
	request, e := prepareMatrixRequest(config, current.Issue, record.Grant.Repo, parseOverrides(current.Issue.Body))
	if e != nil || !reflect.DeepEqual(request.NativeOverride, record.Request.NativeOverride) {
		return s.refuse(0, operation, "UNKNOWN", "override-invalid", "the issue brief no longer carries the granted route")
	}
	return live
}

func (s *ownerNativeGrantStore) readStatus(ctx context.Context, operation string) ownerNativeAck {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !actionIDPattern.MatchString(operation) {
		return s.refuse(0, "", "REFUSED", "override-invalid", "the status request is malformed")
	}
	return s.ackLocked(ctx, operation, false)
}

func (s *ownerNativeGrantStore) recover(ctx context.Context) error {
	records, err := ownerNativePrivateEntries(filepath.Join(s.options.StoreRoot, "records"), 1024)
	if err != nil || len(records) > 1024 {
		return errMatrix
	}
	for _, entry := range records {
		if strings.HasPrefix(entry.Name(), ".owner-native-") {
			continue
		}
		raw, e := ownerNativePrivateRead(filepath.Join(s.options.StoreRoot, "records", entry.Name()), os.Getuid())
		var record ownerNativeGrantRecord
		if e != nil || ownerNativeStrictJSON(raw, &record) != nil {
			return errMatrix
		}
		key := s.subjectKey(s.requestSubject(record.Request))
		s.known[key] = true
		if entry.Name() != ownerNativeOperationKey(record.Request.OperationID)+".json" {
			return errMatrix
		}
		valid, _, e := s.readRecordLocked(record.Request.OperationID)
		if e == nil {
			if syncDirectory(filepath.Dir(s.recordPath(record.Request.OperationID))) != nil || syncDirectory(s.subjectDir(s.requestSubject(record.Request))) != nil {
				return errMatrix
			}
			s.active[record.Request.OperationID] = valid
		}
	}
	return nil
}

func (s *ownerNativeGrantStore) matrixForIssue(is Issue, repo string) (MatrixConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.matrixForIssueLocked(context.Background(), is, repo)
}

func (s *ownerNativeGrantStore) matrixForIssueContext(ctx context.Context, is Issue, repo string) (MatrixConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.matrixForIssueLocked(ctx, is, repo)
}

func (s *ownerNativeGrantStore) matrixForIssueLocked(ctx context.Context, is Issue, repo string) (MatrixConfig, error) {
	if !s.healthy() {
		return MatrixConfig{}, matrixReason("override-invalid")
	}
	copy, err := cloneOwnerNativeConfig(s.cfg)
	if err != nil {
		return MatrixConfig{}, errMatrix
	}
	subject := s.issueSubject(is)
	key := s.subjectKey(subject)
	dir := s.subjectDir(subject)
	_, statErr := os.Lstat(dir)
	if os.IsNotExist(statErr) && !s.known[key] && subject.comment {
		return MatrixConfig{}, matrixReason("source-grant-missing")
	}
	if os.IsNotExist(statErr) && !s.known[key] {
		// Startup owner grants remain usable, and still fence edited briefs.
		ownerKnown := false
		matches := 0
		for _, grant := range copy.Matrix.Grants {
			if grant.IssueID == is.ID && grant.NativeOverride != nil {
				ownerKnown = true
				if grant.Repo == repo && grant.BriefDigest == briefDigest(is) {
					matches++
				}
			}
		}
		if !ownerKnown {
			return copy.Matrix, nil
		}
		if matches != 1 {
			return MatrixConfig{}, matrixReason("override-invalid")
		}
		current, e := s.fetchSubject(ctx, subject)
		if e != nil || !s.subjectCurrent(subject, ownerNativeIssue{Issue: s.subjectIssue(subject, is)}, current, repo, false) {
			return MatrixConfig{}, matrixReason("override-invalid")
		}
		req, e := prepareMatrixRequest(copy.Matrix, is, repo, parseOverrides(is.Body))
		if e != nil || req.NativeOverride == nil {
			return MatrixConfig{}, matrixReason("override-invalid")
		}
		return copy.Matrix, nil
	}
	entries, err := ownerNativePrivateEntries(dir, 32)
	if err != nil || !ownerNativePrivateDir(dir, os.Getuid()) || len(entries) > 32 {
		return MatrixConfig{}, matrixReason("override-invalid")
	}
	matches := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".owner-native-") {
			continue
		}
		raw, e := ownerNativePrivateRead(filepath.Join(dir, entry.Name()), os.Getuid())
		var intent ownerNativeIntent
		if e != nil || ownerNativeStrictJSON(raw, &intent) != nil || intent.SchemaVersion != 1 || !digestPattern.MatchString(intent.RecordChecksum) || !actionIDPattern.MatchString(intent.OperationID) || entry.Name() != ownerNativeOperationKey(intent.OperationID)+".json" {
			return MatrixConfig{}, matrixReason("override-invalid")
		}
		recordRaw, e := ownerNativePrivateRead(s.recordPath(intent.OperationID), os.Getuid())
		var history ownerNativeGrantRecord
		if e != nil || ownerNativeStrictJSON(recordRaw, &history) != nil || shaText(recordRaw) != intent.RecordChecksum || history.Request.OperationID != intent.OperationID || s.requestSubject(history.Request) != subject {
			return MatrixConfig{}, matrixReason("override-invalid")
		}
		if history.Grant.IssueID == is.ID && history.Grant.Repo == repo && history.Grant.BriefDigest == briefDigest(is) && s.targetMatches(is.Labels, repo) {
			record, _, e := s.readRecordLocked(intent.OperationID)
			if e != nil || !reflect.DeepEqual(s.active[intent.OperationID], record) {
				return MatrixConfig{}, matrixReason("override-invalid")
			}
			if subject.comment && !s.claimLocked(intent.OperationID, is.Number) { // guard:grant-claim-once
				return MatrixConfig{}, matrixReason("grant-conflict")
			}
			copy.Matrix.Grants = append(copy.Matrix.Grants, record.Grant)
			matches++
		}
	}
	// A comment launches only on its own grant (owner rule); an inbox issue with
	// owner intents must match exactly one.
	if matches == 0 && subject.comment { // guard:source-grant-admission
		return MatrixConfig{}, matrixReason("source-grant-missing")
	}
	if matches != 1 {
		return MatrixConfig{}, matrixReason("grant-conflict")
	}
	if subject.comment {
		// The source feed admits a binding only after its own budgeted, conditional
		// check found the trigger and the same open issue; admission adds no read.
		return copy.Matrix, nil // guard:source-admission-no-read
	}
	current, err := s.fetchSubject(ctx, subject)
	if err != nil || !s.subjectCurrent(subject, ownerNativeIssue{Issue: s.subjectIssue(subject, is)}, current, repo, false) {
		return MatrixConfig{}, matrixReason("override-invalid")
	}
	return copy.Matrix, nil
}

// subjectIssue is the issue identity a subject is checked against: a comment
// binding's key is its comment ID, so the source issue number stands in for it.
func (s *ownerNativeGrantStore) subjectIssue(subject ownerNativeSubject, is Issue) Issue {
	if subject.comment {
		is.Number = subject.number
	}
	return is
}

// commentGrantReady is true when exactly one active comment grant matches the
// source issue and the comment body digest, unclaimed or claimed by this binding.
func (s *ownerNativeGrantStore) commentGrantReady(repo string, number int, issueID, digest string, binding int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.healthy() {
		return false
	}
	subject := ownerNativeSubject{comment: true, repo: repo, number: number}
	matches := 0
	for operation, record := range s.active {
		if s.requestSubject(record.Request) != subject || record.Grant.IssueID != issueID || record.Grant.Repo != repo || record.Grant.BriefDigest != digest {
			continue
		}
		var claim ownerNativeClaim
		raw, err := ownerNativePrivateRead(filepath.Join(s.options.StoreRoot, "claims", ownerNativeOperationKey(operation)+".json"), os.Getuid())
		if err == nil && (ownerNativeStrictJSON(raw, &claim) != nil || claim.Binding != binding) {
			continue // claimed by another comment
		}
		matches++
	}
	return matches == 1
}

type ownerNativeClaim struct {
	SchemaVersion int    `json:"schema_version"`
	OperationID   string `json:"operation_id"`
	Binding       int    `json:"binding"`
}

// claimLocked lets one comment binding use a comment grant. A second comment
// with the same body is refused rather than launched on the same approval.
func (s *ownerNativeGrantStore) claimLocked(operation string, binding int) bool {
	raw, _ := json.Marshal(ownerNativeClaim{SchemaVersion: 1, OperationID: operation, Binding: binding})
	return s.publish(filepath.Join(s.options.StoreRoot, "claims", ownerNativeOperationKey(operation)+".json"), raw) == nil
}

// Directory reads are bounded before allocation, including crash leftovers.
func ownerNativePrivateEntries(path string, limit int) ([]os.DirEntry, error) {
	if !ownerNativePrivateDir(path, os.Getuid()) {
		return nil, errMatrix
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, errMatrix
	}
	f, err := ownerNativeOpen(path)
	if err != nil {
		return nil, errMatrix
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || !after.IsDir() {
		return nil, errMatrix
	}
	entries, err := f.ReadDir(limit + 1)
	if (err != nil && err != io.EOF) || len(entries) > limit {
		return nil, errMatrix
	}
	return entries, nil
}
