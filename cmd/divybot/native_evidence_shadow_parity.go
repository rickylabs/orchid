package main

import (
	"errors"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"
)

// Durable parity counts of the native-evidence shadow, observe-only.
//
// The readout's ledger is per live job and per process: it is dropped when the
// job ends and lost on restart, so it can never show how often native evidence
// agreed with today's decision over real launches. These counts can. Each
// recorded comparison adds one to its closed outcome class (site, vendor,
// today's decision, the native value, agreement, the native reason), never an
// id, time or text. The counts live in one owner-only file beside the readout,
// are read back once when the readout loop starts and only then rewritten, and
// accumulate across job ends and restarts. Nothing on a decision path reads them.

// parityLoadBetween runs between holding the root and the lookup (tests only).
var parityLoadBetween func()

const (
	shadowParityFile     = "native-evidence-parity.json"
	shadowParitySchema   = 1
	shadowParityMaxCount = 1 << 40
)

var (
	shadowParityVendors    = map[string]bool{"codex": true, "claude": true}
	shadowParityAgreements = map[string]bool{"agree": true, "disagree": true, "shadow-unknown": true}
)

type shadowParityKey struct {
	Site, Vendor, Today, Native, Agreement, Reason string
}

type shadowParityRow struct {
	Site      string `json:"site"`
	Vendor    string `json:"vendor"`
	Today     string `json:"today"`
	Native    string `json:"native"`
	Agreement string `json:"agreement"`
	Reason    string `json:"reason"`
	Count     int64  `json:"count"`
}

type shadowParityState struct {
	SchemaVersion int               `json:"schemaVersion"`
	Since         string            `json:"since"`
	Counts        []shadowParityRow `json:"counts"`
}

// shadowParityKeyFor closes every field exactly as the readout does.
func shadowParityKeyFor(site, vendor string, c shadowComparison) shadowParityKey {
	native := shadowVerdict{Value: "unknown"}
	if len(c.Proposed) > 0 {
		native = c.Proposed[0]
	}
	sites := map[string]bool{}
	for known := range shadowToday {
		sites[known] = true
	}
	return shadowParityKey{Site: shadowClosed(site, sites), Vendor: shadowClosed(vendor, shadowParityVendors),
		Today: shadowClosed(c.Today, shadowToday[site]), Native: shadowClosed(native.Value, shadowClosedValues),
		Agreement: shadowClosed(c.Agreement, shadowParityAgreements), Reason: shadowClosed(native.Reason, shadowClosedReasons)}
}

// Caller holds s.mu.
func (s *nativeEvidenceShadow) countParityLocked(site, vendor string, c shadowComparison) {
	if !shadowParityVendors[vendor] {
		return
	}
	if s.parity == nil {
		s.parity = map[shadowParityKey]int64{}
	}
	key := shadowParityKeyFor(site, vendor, c)
	if s.parity[key] < shadowParityMaxCount { // guard:parity-bounded
		s.parity[key]++
	}
}

// parityState is the sorted, closed snapshot written to disk and the readout.
func (s *nativeEvidenceShadow) parityState() shadowParityState {
	if s == nil {
		return shadowParityState{SchemaVersion: shadowParitySchema, Counts: []shadowParityRow{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parityStateLocked()
}

// Caller holds s.mu.
func (s *nativeEvidenceShadow) parityStateLocked() shadowParityState {
	out := shadowParityState{SchemaVersion: shadowParitySchema, Counts: []shadowParityRow{}}
	if !s.paritySince.IsZero() {
		out.Since = s.paritySince.UTC().Format(time.RFC3339Nano)
	}
	for k, n := range s.parity {
		out.Counts = append(out.Counts, shadowParityRow{Site: k.Site, Vendor: k.Vendor, Today: k.Today, Native: k.Native,
			Agreement: k.Agreement, Reason: k.Reason, Count: n})
	}
	sort.Slice(out.Counts, func(i, k int) bool {
		a, b := out.Counts[i], out.Counts[k]
		for _, pair := range [][2]string{{a.Site, b.Site}, {a.Vendor, b.Vendor}, {a.Today, b.Today}, {a.Native, b.Native},
			{a.Agreement, b.Agreement}, {a.Reason, b.Reason}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})
	return out
}

// restoredParity validates a stored state strictly: every field must already be
// closed and every count positive and bounded, or the whole state is refused.
func restoredParity(raw []byte) (map[shadowParityKey]int64, time.Time, error) {
	var st shadowParityState
	if !utf8.Valid(raw) || strictJSON(raw, &st) != nil || st.SchemaVersion != shadowParitySchema || st.Counts == nil {
		return nil, time.Time{}, errors.New("parity-state-invalid")
	}
	since, err := time.Parse(time.RFC3339Nano, st.Since)
	if err != nil {
		return nil, time.Time{}, errors.New("parity-state-invalid")
	}
	counts := map[shadowParityKey]int64{}
	for _, row := range st.Counts {
		key := shadowParityKey{Site: row.Site, Vendor: row.Vendor, Today: row.Today, Native: row.Native, Agreement: row.Agreement, Reason: row.Reason}
		if shadowParityKeyFor(row.Site, row.Vendor, shadowComparison{Today: row.Today, Agreement: row.Agreement,
			Proposed: []shadowVerdict{{Value: row.Native, Reason: row.Reason}}}) != key { // guard:parity-restore-closed
			return nil, time.Time{}, errors.New("parity-state-invalid")
		}
		if row.Count <= 0 || row.Count > shadowParityMaxCount || counts[key] != 0 {
			return nil, time.Time{}, errors.New("parity-state-invalid")
		}
		counts[key] = row.Count
	}
	return counts, since, nil
}

// loadParity adds the stored counts once, before the loop ever writes them.
// The receipt root must be present, private and owned first: a root that is
// missing or unsafe right now says nothing about the file, so loading stays
// pending. Absent file: counting starts now. Unreadable (an IO, ownership or
// safety failure): false, so nothing is written and the next tick tries again.
// Read but invalid (encoding included): counting restarts now, and the new
// "since" shows it.
func (s *nativeEvidenceShadow) loadParity(root string, uid int, started time.Time) bool {
	if !ownerNativePrivateDir(root, uid) { // guard:parity-root-available
		return false
	}
	path := filepath.Join(root, shadowParityFile)
	counts, since := map[shadowParityKey]int64{}, started
	// Absence is judged inside one held descriptor of the root, so a root that
	// moves away and back during the load can never make a stored file look absent.
	absent, ok := heldChildAbsent(root, shadowParityFile, uid, parityLoadBetween) // guard:parity-absent-held-root
	if !ok {
		return false
	}
	if !absent {
		raw, err := privateFileBytes(path, uid, ownerNativeOpen)
		if err != nil {
			return false // guard:parity-no-overwrite-unread
		}
		if restored, at, err := restoredParity(raw); err == nil {
			counts, since = restored, at
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.parity == nil {
		s.parity = map[shadowParityKey]int64{}
	}
	for k, n := range counts { // guard:parity-restore-adds
		if s.parity[k]+n > shadowParityMaxCount {
			s.parity[k] = shadowParityMaxCount
		} else {
			s.parity[k] += n
		}
	}
	s.paritySince = since
	s.version++
	return true
}

func writeShadowParity(root string, owner *receiptOwner, st shadowParityState) error {
	if root == "" || !privateReceiptRoot(root) {
		return errors.New("shadow-readout-root-unavailable")
	}
	return writePrivateJSON(filepath.Join(root, shadowParityFile), ".native-evidence-parity-", owner, st)
}
