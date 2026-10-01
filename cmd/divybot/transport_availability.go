package main

import (
	"os"
	"path/filepath"
	"time"
)

// A private, current snapshot of which matrix transports admission would offer
// right now, and why each other one is unavailable. It uses the exact predicate
// admission uses (transportAvailabilityReason), so a reader never sees a
// transport offered that admission would refuse. The governance reader
// (rickylabs/harness, contracts 0.28.0 `transportAvailability`) publishes it;
// the file carries no identity, path or quota figure.
type transportAvailabilityRow struct {
	Transport string  `json:"transport"`
	Available bool    `json:"available"`
	Reason    *string `json:"reason"`
}

type transportAvailabilitySnapshot struct {
	SchemaVersion int                        `json:"schemaVersion"`
	ObservedAt    string                     `json:"observedAt"`
	ValidUntil    string                     `json:"validUntil"`
	Transports    []transportAvailabilityRow `json:"transports"`
}

// Millisecond UTC times: the governance contract reads at most three fractional
// digits, so the snapshot never needs rounding on the way through.
const transportAvailabilityTime = "2006-01-02T15:04:05.000Z07:00"

func transportAvailabilityPath(receiptRoot string) string {
	return filepath.Join(receiptRoot, "governance", "transport-availability.json")
}

func buildTransportAvailability(budget map[string]int, quotas map[string]quota, now time.Time,
	sampleInterval time.Duration, ceiling float64, validFor time.Duration) transportAvailabilitySnapshot {
	// Published v1 is the exact three-subscription contract consumed by Harness
	// 0.30.0. Provider capacity is not a subscription meter; do not add a fourth
	// row and make existing strict readers discard all native availability.
	rows := make([]transportAvailabilityRow, 0, len(meteredTransports))
	for _, transport := range meteredTransports {
		reason := transportAvailabilityReason(budget[transport], quotas[transport], now, sampleInterval, ceiling)
		row := transportAvailabilityRow{Transport: transport, Available: reason == ""}
		if reason != "" {
			row.Reason = &reason
		}
		rows = append(rows, row)
	}
	return transportAvailabilitySnapshot{SchemaVersion: 1,
		ObservedAt: now.UTC().Format(transportAvailabilityTime),
		ValidUntil: now.Add(validFor).UTC().Format(transportAvailabilityTime),
		Transports: rows}
}

// publishTransportAvailability replaces the snapshot, or removes it when this
// tick cannot publish one, so a reader never keeps a decision past its tick.
func publishTransportAvailability(receiptRoot string, owner *receiptOwner, snapshot transportAvailabilitySnapshot) error {
	if !privateReceiptRoot(receiptRoot) {
		return errMatrix
	}
	dir := filepath.Dir(transportAvailabilityPath(receiptRoot))
	if err := os.Mkdir(dir, 0700); err == nil {
		if err = transferReceiptOwner(owner, dir); err != nil {
			return err
		}
	} else if !os.IsExist(err) {
		return err
	}
	if !privateReceiptRoot(dir) {
		return errMatrix
	}
	return writePrivateJSON(transportAvailabilityPath(receiptRoot), ".transport-availability-", owner, snapshot)
}

func (c *Coord) publishTransportAvailability(budget map[string]int, now time.Time) {
	root := c.cfg.Matrix.ReceiptRoot
	if root == "" || c.dry {
		return
	}
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err == nil {
		c.gov.mu.Lock()
		quotas := make(map[string]quota, len(meteredTransports))
		for _, transport := range meteredTransports {
			quotas[transport] = c.gov.q[transport]
		}
		c.gov.mu.Unlock()
		// Valid for two poll intervals: one missed tick does not blank it.
		snapshot := buildTransportAvailability(budget, quotas, now, c.cfg.Governor.sampleIntervalDur(),
			c.cfg.Governor.WeeklyCeiling, 2*durOr(c.cfg.PollInterval, 30*time.Second))
		err = publishTransportAvailability(root, owner, snapshot)
	}
	if err != nil && privateReceiptRoot(root) {
		_ = os.Remove(transportAvailabilityPath(root))
	}
}
