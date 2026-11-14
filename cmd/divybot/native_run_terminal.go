package main

import (
	"errors"
	"strconv"
	"time"
)

// Assignment ends are business facts, not workspace cleanup state. The immutable
// dispatch-keyed fact survives cleanup and restart within configured retention.
// Reason remains source-private: the existing v2 reader shape is unchanged.
type nativeTerminalFact struct {
	Record nativeRunRecord `json:"record"`
	Reason string          `json:"reason"`
}

func nativeTerminalReason(reason string) bool {
	switch reason {
	case "assignment_completed", "cancelled", "retired", "timed_out", "ended_unobserved":
		return true
	}
	return false
}

func validNativeTerminal(f nativeTerminalFact) bool {
	return nativeTerminalReason(f.Reason) && f.Record.Phase == "completed" && validNativeRunRecord(f.Record)
}

// Caller holds State.mu. The first committed end is immutable, including its
// clock and reason. A later cleanup observation cannot rewrite it.
func (s *State) publishNativeTerminalLocked(record nativeRunRecord, reason string) error {
	if !nativeTerminalReason(reason) || !validNativeRunRecord(record) {
		return errors.New("terminal_identity_unavailable")
	}
	if prior, ok := s.NativeTerminals[record.DispatchKey]; ok {
		if !validNativeTerminal(prior) || !activeMatchesCompletion(&record, &prior.Record,
			completedRun{DispatchKey: prior.Record.DispatchKey, NativeSessionID: prior.Record.NativeSessionID}) {
			return errors.New("terminal_identity_conflict")
		}
		return s.saveLocked() // retry any uncertain directory sync without rewriting the fact
	}
	if record.Phase == "active" {
		record.Phase = "completed"
		record.ObservedAt = stamp(time.Now())
		record.CompletedAt = record.ObservedAt
	}
	if !validNativeRunRecord(record) {
		return errors.New("terminal_identity_unavailable")
	}
	if at, err := time.Parse(time.RFC3339Nano, record.CompletedAt); err != nil || at.After(time.Now()) {
		return errors.New("terminal_clock_unavailable")
	}
	if record.Launch != nil {
		copy := *record.Launch
		record.Launch = &copy
	}
	if record.TaskStart != nil {
		copy := *record.TaskStart
		record.TaskStart = &copy
	}
	n, _ := strconv.Atoi(record.Key)
	priorKey, hadKey := s.NativeTerminalKeys[n]
	priorBlock, hadBlock := s.LaunchBlocks[n]
	if s.NativeTerminals == nil {
		s.NativeTerminals = map[string]nativeTerminalFact{}
	}
	if s.NativeTerminalKeys == nil {
		s.NativeTerminalKeys = map[int]string{}
	}
	if s.LaunchBlocks == nil {
		s.LaunchBlocks = map[int]launchBlock{}
	}
	s.NativeTerminals[record.DispatchKey] = nativeTerminalFact{Record: record, Reason: reason}
	s.NativeTerminalKeys[n] = record.DispatchKey
	if !hadBlock {
		s.LaunchBlocks[n] = launchBlock{Reason: "assignment_ended", Notified: true}
	}
	// guard:terminal-durable-before-release
	if err := s.saveLocked(); err != nil {
		var saved stateSaveError
		if errors.As(err, &saved) && saved.afterRename {
			return err // the pathname already names this fact; never erase or re-clock it
		}
		delete(s.NativeTerminals, record.DispatchKey)
		if hadKey {
			s.NativeTerminalKeys[n] = priorKey
		} else {
			delete(s.NativeTerminalKeys, n)
		}
		if hadBlock {
			s.LaunchBlocks[n] = priorBlock
		} else {
			delete(s.LaunchBlocks, n)
		}
		return err
	}
	return nil
}

// Legacy jobs without a canonical native identity may still be cleaned up, but
// cannot acquire an invented association or terminal record.
func (c *Coord) recordNativeEndLocked(n int, j *Job, reason string) error {
	record := c.st.nativeRunRecordLocked(n, j, c.cfg.Inbox)
	if record == nil {
		return nil
	}
	return c.st.publishNativeTerminalLocked(*record, reason)
}

func (s *State) nativeEndedLocked(j *Job) bool {
	if j == nil {
		return false
	}
	f, ok := s.NativeTerminals[j.DispatchKey]
	return ok && validNativeTerminal(f) && f.Record.Key == strconv.Itoa(j.Issue)
}

// Exact point lookup. A new assignment on the same inbox coordinate supersedes
// its index, never the old immutable fact. No receipt/history scan is needed.
func (s *State) terminalNativeRunLocked(n int, j *Job, inbox string) (*nativeRunRecord, string) {
	key := s.NativeTerminalKeys[n]
	if j != nil {
		key = j.DispatchKey
	}
	f, found := s.NativeTerminals[key]
	if !found {
		return nil, ""
	}
	if !validNativeTerminal(f) || f.Record.Key != strconv.Itoa(n) {
		return nil, "identity_mismatch"
	}
	if j != nil && !activeMatchesCompletion(s.nativeRunRecordLocked(n, j, inbox), &f.Record,
		completedRun{DispatchKey: key, NativeSessionID: f.Record.NativeSessionID}) {
		return nil, "identity_mismatch"
	}
	return &f.Record, ""
}
