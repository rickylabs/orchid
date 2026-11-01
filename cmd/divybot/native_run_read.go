package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"sync"
	"time"
	"unicode"
)

// Native run reads address Orchid's existing durable maps; retained receipts are never enumerated.
const nativeRunBatchLimit = 128

var nativeRunRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

var nativeRunLabel = regexp.MustCompile(`^(codex|codex-run|claude|agy|opencode|opencode-run)-([1-9][0-9]{0,15})$`)

const nativeRunSafeInteger = uint64(9007199254740991)

type nativeRunScope struct {
	Host    string `json:"host"`
	Session string `json:"session"`
	Machine string `json:"machine"`
}

type nativeRunQuery struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type nativeRunRecord struct {
	CompletedAt     string         `json:"completedAt,omitempty"`
	AgentID         string         `json:"agentId"`
	AssignmentID    string         `json:"assignmentId"`
	SourceCommentID int64          `json:"sourceCommentId"`
	Scope           nativeRunScope `json:"scope"`
	Key             string         `json:"key"`
	Issue           dispatchIssue  `json:"issue"`
	DispatchKey     string         `json:"dispatchKey"`
	NativeSessionID string         `json:"nativeSessionId"`
	Agent           string         `json:"agent"`
	Label           string         `json:"label"`
	Phase           string         `json:"phase"`
	ObservedAt      string         `json:"observedAt"`
}

type nativeRunMissing struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

type nativeRunLookup struct {
	ObservedAt    string             `json:"observedAt"`
	Complete      bool               `json:"complete"`
	Type          string             `json:"type"`
	SchemaVersion int                `json:"schemaVersion"`
	Generation    string             `json:"generation"`
	Revision      uint64             `json:"revision"`
	Records       []nativeRunRecord  `json:"records"`
	Missing       []nativeRunMissing `json:"missing"`
}

// Each subscriber owns one dirty bit. Slow readers cannot accumulate a history queue.
type nativeRunWatch struct {
	mu         sync.Mutex
	generation string
	revision   uint64
	subs       map[chan struct{}]struct{}
	unproven   bool
}

// The watch owns its lock: legacy save callers need not hold State.mu to publish invalidation.
func (s *State) notifyNativeRunChangeLocked() {
	if s.nativeRuns == nil {
		return
	}
	w := s.nativeRuns
	w.mu.Lock()
	defer w.mu.Unlock()
	w.notify()
}

func (w *nativeRunWatch) notify() {
	if w.revision == nativeRunSafeInteger {
		w.generation = newInstanceID()
		w.revision = 0
	} else {
		w.revision++
	}
	w.wake()
}

func (w *nativeRunWatch) wake() {
	for ch := range w.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *State) publishNativeRunCommit(committed bool) {
	if s.nativeRuns == nil {
		return
	}
	w := s.nativeRuns
	w.mu.Lock()
	defer w.mu.Unlock()
	w.unproven = !committed
	if committed {
		w.notify()
	} else {
		w.wake()
	}
}

func validNativeRunQuery(q nativeRunQuery) bool {
	m := nativeRunLabel.FindStringSubmatch(q.Label)
	if len(m) != 3 || m[2] != q.Key {
		return false
	}
	n, err := strconv.ParseUint(q.Key, 10, 53)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == q.Key
}

// nativeRunsLookup is the byte-level production seam. Its cost depends only
// on requested keys; neither retained receipts nor historical maps are walked.
func (s *State) nativeRunsLookup(raw json.RawMessage) (json.RawMessage, error) {
	queries, err := decodeNativeRunKeys(raw)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nativeRuns == nil {
		s.nativeRuns = &nativeRunWatch{generation: newInstanceID(), subs: map[chan struct{}]struct{}{}}
	}
	return json.Marshal(s.lookupNativeRunsLocked(queries, s.nativeRunInbox))
}

func (c *Coord) lookupNativeRuns(queries []nativeRunQuery) nativeRunLookup {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	return c.st.lookupNativeRunsLocked(queries, c.cfg.Inbox)
}

func (s *State) lookupNativeRunsLocked(queries []nativeRunQuery, inbox string) nativeRunLookup {
	w := s.nativeRuns
	out := nativeRunLookup{ObservedAt: stamp(time.Now()), Type: "run_lookup", SchemaVersion: 1,
		Records: []nativeRunRecord{}, Missing: []nativeRunMissing{}}
	if w != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		out.Generation, out.Revision = w.generation, w.revision
		out.Complete = !w.unproven
	}
	for _, q := range queries {
		reason := ""
		var record *nativeRunRecord
		if w == nil || w.unproven {
			reason = "source_unavailable"
		} else if !validNativeRunQuery(q) {
			reason = "identity_mismatch"
		} else {
			n, _ := strconv.Atoi(q.Key)
			j := s.Jobs[n]
			fence, completed := s.CompletedRuns[n]
			if completed {
				// No inbox fallback or receipt reconstruction for legacy completion fences.
				record = fence.NativeRun
				reason = completedNativeRunReason(fence, record)
				if j != nil && !activeMatchesCompletion(s.nativeRunRecordLocked(n, j, inbox), record, fence) {
					reason = "identity_mismatch"
				}
			} else if j != nil {
				record = s.nativeRunRecordLocked(n, j, inbox)
			} else {
				reason = "run_not_found"
			}
			if reason == "" && (record == nil || !validNativeRunRecord(*record)) {
				reason = "native_identity_unavailable"
			}
			if reason == "" && (record.Key != q.Key || record.Label != q.Label) {
				reason = "identity_mismatch"
			}
		}
		if reason != "" {
			out.Missing = append(out.Missing, nativeRunMissing{q.Key, reason})
			continue
		}
		copy := *record
		if copy.Phase == "active" {
			copy.ObservedAt = out.ObservedAt
		}
		out.Records = append(out.Records, copy)
	}
	return out
}

// completedNativeRunReason is the missing reason for a completion fence, or ""
// when its retained identity stands on its own.
func completedNativeRunReason(fence completedRun, record *nativeRunRecord) string {
	reason := ""
	switch fence.Phase {
	case "retiring", "close-failed", "close-sent", "observed":
	default:
		reason = "binding_unavailable"
	}
	if record == nil {
		return reason
	}
	if record.DispatchKey != fence.DispatchKey || record.NativeSessionID != fence.NativeSessionID {
		return "identity_mismatch"
	}
	if record.Phase != "completed" {
		return "identity_mismatch"
	}
	return reason
}

// A job still tracked under a completion fence must be the run the fence retained.
func activeMatchesCompletion(active, record *nativeRunRecord, fence completedRun) bool {
	if active == nil || active.DispatchKey != fence.DispatchKey || active.NativeSessionID != fence.NativeSessionID {
		return false
	}
	if record == nil {
		return true
	}
	if active.Scope != record.Scope || active.Issue != record.Issue {
		return false
	}
	return active.Label == record.Label && active.Agent == record.Agent
}

func nativeRunScopeText(s string) bool {
	if len(s) > 128 {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) || c == '/' || c == '\\' {
			return false
		}
	}
	return true
}

func validNativeRunPhase(r nativeRunRecord) bool {
	switch r.Phase {
	case "active":
		return r.CompletedAt == ""
	case "completed":
		at, e := time.Parse(time.RFC3339Nano, r.CompletedAt)
		return e == nil && !at.IsZero() && r.ObservedAt == r.CompletedAt
	}
	return false
}

func validNativeRunRecord(r nativeRunRecord) bool {
	if !validNativeRunPhase(r) {
		return false
	}
	m := nativeRunLabel.FindStringSubmatch(r.Label)
	if !validNativeRunQuery(nativeRunQuery{r.Key, r.Label}) || len(m) != 3 || m[1] != r.Agent {
		return false
	}
	if !digestPattern.MatchString(r.DispatchKey) || !privateNativeID(r.NativeSessionID) {
		return false
	}
	if !nativeRunRepo.MatchString(r.Issue.Repo) || len(r.Issue.Repo) > 256 {
		return false
	}
	if r.Issue.Number <= 0 || uint64(r.Issue.Number) > nativeRunSafeInteger {
		return false
	}
	if r.Scope.Host == "" || r.Scope.Session == "" || !nativeRunScopeText(r.Scope.Host) {
		return false
	}
	if !nativeRunScopeText(r.Scope.Session) || !nativeRunScopeText(r.Scope.Machine) {
		return false
	}
	opaque := "orchid-" + r.DispatchKey
	if r.AgentID != actionOpaque("agent", opaque) || r.AssignmentID != actionOpaque("assignment", opaque) {
		return false
	}
	if r.SourceCommentID < 0 || uint64(r.SourceCommentID) > nativeRunSafeInteger {
		return false
	}
	return r.SourceCommentID == 0 || strconv.FormatInt(r.SourceCommentID, 10) == r.Key
}

// Caller holds State.mu. Native identity conflicts are unavailable, not a
// precedence rule. Persist this projection before removing a completed job.
func (s *State) nativeRunRecordLocked(n int, j *Job, inbox string) *nativeRunRecord {
	if j == nil || j.Issue != n || j.HerdrScope == nil {
		return nil
	}
	native := j.NativeSessionID
	if j.RemoteControl != nil && j.RemoteControl.NativeSessionID != "" {
		if native != "" && native != j.RemoteControl.NativeSessionID {
			return nil
		}
		native = j.RemoteControl.NativeSessionID
	}
	issue := jobHome(inbox, j)
	comment := int64(0)
	if b := s.SourceBindings[n]; b != nil {
		if b.Comment != int64(n) || (j.Home != nil && (issue.Repo != b.Repo || issue.Number != b.Issue)) {
			return nil
		}
		issue = dispatchIssue{Repo: b.Repo, Number: b.Issue}
		comment = b.Comment
	} else if j.Home != nil {
		comment = int64(n)
	}
	opaque := "orchid-" + j.DispatchKey
	r := &nativeRunRecord{
		Key: strconv.Itoa(n), Issue: issue, DispatchKey: j.DispatchKey, NativeSessionID: native,
		Agent: j.Agent, Label: j.Label, Phase: "active", SourceCommentID: comment,
		AgentID: actionOpaque("agent", opaque), AssignmentID: actionOpaque("assignment", opaque),
		Scope: nativeRunScope{Host: j.Host, Session: j.HerdrScope.Session, Machine: j.HerdrScope.Machine},
	}
	if !validNativeRunRecord(*r) {
		return nil
	}
	return r
}

func nativeRunFailure(id string) map[string]any {
	return map[string]any{"id": id, "error": map[string]string{"code": "source_unavailable"}}
}
