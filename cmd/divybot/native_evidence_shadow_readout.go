package main

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"time"
)

// Private, local-only readout of the native-evidence shadow. A background
// loop, off every decision path, writes one owner-only file in the existing
// private receipt root when the ledger changed. No listener, nothing served
// to the app or GitHub. Every string is a closed code; the only other values
// are the public issue number, vendor, counts, times and the build revision.

const (
	shadowReadoutFile  = "native-evidence-shadow.json"
	shadowReadoutEvery = 30 * time.Second
)

type shadowNativeCounts struct {
	Accepted map[string]int `json:"accepted"`
	Rejected map[string]int `json:"rejected"`
}

type shadowReadoutRun struct {
	Issue       int                `json:"issue"`
	Vendor      string             `json:"vendor"`
	Native      shadowNativeCounts `json:"native"`
	Comparisons []shadowComparison `json:"comparisons"`
}

type shadowReadout struct {
	SchemaVersion int                `json:"schemaVersion"`
	Revision      string             `json:"revision"`
	StartedAt     string             `json:"startedAt"`
	GeneratedAt   string             `json:"generatedAt"`
	Runs          []shadowReadoutRun `json:"runs"`
}

var (
	shadowRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	shadowToday           = map[string]map[string]bool{
		shadowSiteRemoteHook:    {"pass": true, "refuse": true},
		shadowSiteConnection:    {"connected": true, "unconfirmed": true},
		shadowSiteGoalReadiness: {"ready": true, "not-ready": true},
	}
	shadowClosedValues = map[string]bool{
		"unknown": true, "connected": true, "not-connected": true, "working": true,
		"completed": true, "failed": true, "interrupted": true, "ready": true, "not-ready": true,
	}
	shadowClosedReasons = map[string]bool{
		"": true, "duplicate": true, "source-unregistered": true, "source-epoch-foreign": true, "source-unavailable": true,
		"scope-foreign": true, "capability-unsupported": true, "sequence-conflict": true, "sequence-gap": true,
		"row-invalid": true, "row-limit": true, "source-disconnected": true, "native-absent": true, "native-expired": true,
		"conflict-unreconciled": true, "native-current-connection-unsupported": true, "native-activity-unsupported": true,
		"native-turn-outcome-unsupported": true, "native-attachment-unsupported": true,
		"codex-tui-attachment-unproven": true, "claude-tui-attachment-unproven": true,
		"occupant-unstable": true, "herdr-not-ready": true, "native-turn-active": true, "native-thread-unreadable": true,
		"native-thread-not-loaded": true, "native-thread-system-error": true, "native-thread-unknown": true,
		"herdr-status-unknown": true,
	}
)

func shadowClosed(value string, allowed map[string]bool) string {
	if allowed[value] { // guard:readout-closed
		return value
	}
	return shadowInputOther
}

func shadowRevision() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && shadowRevisionPattern.MatchString(setting.Value) {
				return setting.Value
			}
		}
	}
	return "unknown"
}

// Closed, ID-free projection of the in-memory ledger, ordered by issue.
func (s *nativeEvidenceShadow) readout(started time.Time) shadowReadout {
	out := shadowReadout{SchemaVersion: 1, Revision: shadowRevision(), StartedAt: started.UTC().Format(time.RFC3339Nano), Runs: []shadowReadoutRun{}}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out.GeneratedAt = s.now().UTC().Format(time.RFC3339Nano)
	for _, scope := range s.scopes {
		vendor := scope.reducer.binding.Vendor
		if vendor != "codex" && vendor != "claude" {
			continue
		}
		run := shadowReadoutRun{Issue: scope.reducer.binding.Issue, Vendor: vendor,
			Native: shadowNativeCounts{Accepted: map[string]int{}, Rejected: map[string]int{}}, Comparisons: []shadowComparison{}}
		for fact, n := range scope.accepted {
			run.Native.Accepted[shadowClosed(fact, map[string]bool{"connection": true, "activity": true, "turn-outcome": true})] += n
		}
		for reason, n := range scope.rejected {
			run.Native.Rejected[shadowClosed(reason, shadowClosedReasons)] += n
		}
		for _, c := range scope.ledger {
			closed := shadowComparison{Site: c.Site, Today: shadowClosed(c.Today, shadowToday[c.Site]), ScreenDerived: c.ScreenDerived,
				Agreement: shadowClosed(c.Agreement, map[string]bool{"agree": true, "disagree": true, "shadow-unknown": true}), At: c.At.UTC(), Provenance: []string{}}
			for _, input := range c.Provenance {
				closed.Provenance = append(closed.Provenance, shadowClosed(input, shadowKnownInputs))
			}
			for _, v := range c.Proposed {
				closed.Proposed = append(closed.Proposed, shadowVerdict{Fact: v.Fact, Value: shadowClosed(v.Value, shadowClosedValues),
					Authority: shadowClosed(v.Authority, map[string]bool{"native": true, "none": true}), Reason: shadowClosed(v.Reason, shadowClosedReasons), Live: v.Live})
			}
			run.Comparisons = append(run.Comparisons, closed)
		}
		out.Runs = append(out.Runs, run)
	}
	sort.Slice(out.Runs, func(i, k int) bool {
		if out.Runs[i].Issue != out.Runs[k].Issue {
			return out.Runs[i].Issue < out.Runs[k].Issue
		}
		return out.Runs[i].Vendor < out.Runs[k].Vendor
	})
	return out
}

func (s *nativeEvidenceShadow) changeVersion() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// Only into the existing private receipt root (absolute, 0700, no symlink,
// outside any repository), atomically, 0600, transferred to the receipt owner.
func writeShadowReadout(root string, owner *receiptOwner, r shadowReadout) error {
	if root == "" || !privateReceiptRoot(root) { // guard:readout-private-root
		return errors.New("shadow-readout-root-unavailable")
	}
	return writePrivateJSON(filepath.Join(root, shadowReadoutFile), ".native-evidence-shadow-", owner, r)
}

// Background only. Never blocks a decision; failures retry on the next tick.
func (c *Coord) shadowReadoutLoop(ctx context.Context, ticks <-chan time.Time, started time.Time) {
	if c == nil || c.shadow == nil || c.cfg == nil || c.cfg.Matrix.ReceiptRoot == "" {
		return
	}
	written, wroteOnce := uint64(0), false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			func() {
				defer shadowContain()
				version := c.shadow.changeVersion()
				if wroteOnce && version == written { // guard:readout-on-change
					return
				}
				owner, err := configuredReceiptOwner(c.cfg.Matrix)
				if err != nil {
					return
				}
				if writeShadowReadout(c.cfg.Matrix.ReceiptRoot, owner, c.shadow.readout(started)) == nil {
					written, wroteOnce = version, true
				}
			}()
		}
	}
}
