package main

import (
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Native run reads address Orchid's existing durable maps; retained receipts are never enumerated.
const nativeRunBatchLimit = 128

var nativeRunRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

var nativeRunLabel = regexp.MustCompile(`^(codex|claude|agy|opencode)-([1-9][0-9]{0,15})$`)

type nativeRunQuery struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type nativeRunRecord struct {
	Key             string        `json:"key"`
	Issue           dispatchIssue `json:"issue"`
	DispatchKey     string        `json:"dispatchKey"`
	NativeSessionID string        `json:"nativeSessionId"`
	Agent           string        `json:"agent"`
	Label           string        `json:"label"`
	Phase           string        `json:"phase"`
	ObservedAt      string        `json:"observedAt"`
}

type nativeRunMissing struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

type nativeRunLookup struct {
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
	w.revision++
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
	w.notify()
}

func validNativeRunQuery(q nativeRunQuery) bool {
	m := nativeRunLabel.FindStringSubmatch(q.Label)
	if len(m) != 3 || m[2] != q.Key {
		return false
	}
	n, err := strconv.ParseUint(q.Key, 10, 53)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == q.Key
}

// The label selects a coordinate; only the native maps and recorded identity establish the run.
func (c *Coord) lookupNativeRuns(queries []nativeRunQuery) nativeRunLookup {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	w := c.st.nativeRuns
	out := nativeRunLookup{Type: "run_lookup", SchemaVersion: 1, Records: []nativeRunRecord{}, Missing: []nativeRunMissing{}}
	if w != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		out.Generation, out.Revision = w.generation, w.revision
		out.Complete = !w.unproven
	}
	for _, q := range queries {
		if w == nil || w.unproven {
			out.Missing = append(out.Missing, nativeRunMissing{q.Key, "source_unavailable"})
			continue
		}
		n, _ := strconv.Atoi(q.Key)
		j := c.st.Jobs[n]
		fence, completed := c.st.CompletedRuns[n]
		issue := dispatchIssue{Repo: c.cfg.Inbox, Number: n}
		if b := c.st.SourceBindings[n]; b != nil {
			if b.Comment != int64(n) || b.Repo == "" || b.Issue < 1 {
				out.Missing = append(out.Missing, nativeRunMissing{q.Key, "binding_unavailable"})
				continue
			}
			issue = dispatchIssue{Repo: b.Repo, Number: b.Issue}
		} else if j != nil {
			issue = jobHome(c.cfg.Inbox, j)
		} else if !completed {
			out.Missing = append(out.Missing, nativeRunMissing{q.Key, "run_not_found"})
			continue
		}
		record := nativeRunRecord{Key: q.Key, Issue: issue, ObservedAt: stamp(time.Now()), Phase: "active"}
		if j != nil {
			if j.Label != q.Label || j.Agent+"-"+q.Key != j.Label {
				out.Missing = append(out.Missing, nativeRunMissing{q.Key, "identity_mismatch"})
				continue
			}
			record.Agent, record.Label = j.Agent, j.Label
			record.DispatchKey, record.NativeSessionID = j.DispatchKey, j.NativeSessionID
		}
		if completed {
			if fence.Phase != "retiring" && fence.Phase != "close-failed" && fence.Phase != "close-sent" && fence.Phase != "observed" {
				out.Missing = append(out.Missing, nativeRunMissing{q.Key, "binding_unavailable"})
				continue
			}
			if j != nil && (fence.DispatchKey != j.DispatchKey || fence.NativeSessionID != j.NativeSessionID) {
				out.Missing = append(out.Missing, nativeRunMissing{q.Key, "identity_mismatch"})
				continue
			}
			record.DispatchKey, record.NativeSessionID, record.Phase = fence.DispatchKey, fence.NativeSessionID, "completed"
			// A removed job has no retained vendor/label proof. The receipt owner verifies these fields.
		}
		if !digestPattern.MatchString(record.DispatchKey) || !privateNativeID(record.NativeSessionID) ||
			!nativeRunRepo.MatchString(issue.Repo) || len(issue.Repo) > 256 || issue.Number < 1 {
			out.Missing = append(out.Missing, nativeRunMissing{q.Key, "native_identity_unavailable"})
			continue
		}
		out.Records = append(out.Records, record)
	}
	return out
}

func nativeRunFailure(id string) map[string]any {
	return map[string]any{"id": id, "error": map[string]string{"code": "source_unavailable"}}
}
