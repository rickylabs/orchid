package main

import "time"

// Why each matrix transport can or cannot take a seat, published beside the
// availability rows. Physical capacity (configured seats against the jobs this
// dispatcher counts) and pacing (the native meter and the governor cap) are
// separate facts: a quota percentage is never seat evidence, and a seat budget
// the dispatcher did not compute is unknown, never zero.
const (
	capacityFree     = "free"
	capacityFull     = "full"
	capacityDisabled = "disabled"
	capacityUnknown  = "unknown"

	// Reasons, present exactly when capacity is disabled or unknown.
	capacitySeatsNotConfigured = "seats-not-configured" // the operator configured no seats
	capacitySeatBudgetMissing  = "seat-budget-missing"  // no target names the transport, so no seat budget was computed
	capacityConfigInvalid      = "seats-config-invalid" // the seat configuration cannot be read

	pacingClear     = "clear"
	pacingLimited   = "limited"
	pacingUnknown   = "unknown"
	pacingUnmetered = "unmetered"

	// The governor holds the admission cap below the configured seats while the
	// meter still has headroom. Other pacing reasons are the quota reason codes.
	pacingGovernor = "governor-pacing"
)

type transportCapacityRow struct {
	Transport      string  `json:"transport"`
	Capacity       string  `json:"capacity"`
	CapacityReason *string `json:"capacityReason"`
	// Seat counts; null for OpenCode (its seats are per provider pool) and for
	// counts the dispatcher did not compute.
	MaxActive    *int    `json:"maxActive"`
	Active       *int    `json:"active"`
	AdmissionCap *int    `json:"admissionCap"`
	Pacing       string  `json:"pacing"`
	PacingReason *string `json:"pacingReason"`
}

// transportSeats is one tick's seat accounting, exactly as admissionBudget
// computed it: the governor's admission cap and the seats counted as occupied.
type transportSeats struct {
	caps    map[string]int
	running map[string]int
}

func buildTransportCapacity(seats transportSeats, cfg *Config, pools []openCodeProviderPool,
	quotas map[string]quota, now time.Time) []transportCapacityRow {
	rows := make([]transportCapacityRow, 0, len(matrixTransports))
	for _, transport := range matrixTransports {
		var row transportCapacityRow
		switch transport {
		case "opencode":
			row = openCodeCapacity(cfg.OpenCode, pools)
		case "agy":
			row = seats.capacity(transport, cfg.UnmeteredTransports.cap(transport))
		default:
			row = seats.capacity(transport, cfg.Governor.MaxActive)
			row.Pacing, row.PacingReason = meteredPacing(quotas[transport], now, cfg.Governor.sampleIntervalDur(),
				cfg.Governor.WeeklyCeiling, row.AdmissionCap, row.MaxActive)
		}
		row.Transport = transport
		if row.Pacing == "" {
			row.Pacing = pacingUnmetered // AGY and OpenCode have explicit seats, no subscription meter.
		}
		rows = append(rows, row)
	}
	return rows
}

func (s transportSeats) capacity(transport string, maxActive int) transportCapacityRow {
	active := s.running[transport]
	row := transportCapacityRow{Active: &active}
	// A computed cap is published as computed, zero included; null means admission
	// computed none this tick. A negative governor max_active is not rejected by
	// configuration and becomes the cap, so it is published as zero seats.
	limit, provisioned := s.caps[transport]
	if provisioned { // guard:computed-cap
		limit = max(limit, 0) // guard:negative-cap-clamp
		row.AdmissionCap = &limit
	}
	switch {
	case maxActive <= 0:
		zero := 0
		row.Capacity, row.CapacityReason, row.MaxActive = capacityDisabled, reasonRef(capacitySeatsNotConfigured), &zero
		return row
	case !provisioned:
		row.Capacity, row.CapacityReason = capacityUnknown, reasonRef(capacitySeatBudgetMissing)
		return row
	case active >= maxActive: // guard:seats-full
		row.Capacity = capacityFull
	default:
		row.Capacity = capacityFree
	}
	row.MaxActive = &maxActive
	return row
}

// OpenCode seats are per provider; the aggregate is free when any pool has a
// seat, as the availability row and its pools already state.
func openCodeCapacity(cfg OpenCodeConfig, pools []openCodeProviderPool) transportCapacityRow {
	if !cfg.valid() { // guard:opencode-seats-unknown
		return transportCapacityRow{Capacity: capacityUnknown, CapacityReason: reasonRef(capacityConfigInvalid)}
	}
	configured, free := false, false
	for _, pool := range pools {
		configured = configured || pool.MaxActive > 0
		free = free || pool.MaxActive > pool.Active
	}
	switch {
	case !configured:
		return transportCapacityRow{Capacity: capacityDisabled, CapacityReason: reasonRef(capacitySeatsNotConfigured)}
	case free:
		return transportCapacityRow{Capacity: capacityFree}
	default:
		return transportCapacityRow{Capacity: capacityFull}
	}
}

// meteredPacing reads the native meter and the governor cap. An unreadable,
// stale or expired meter is unknown pacing; it says nothing about seats.
func meteredPacing(q quota, now time.Time, sampleInterval time.Duration, ceiling float64, admissionCap, maxActive *int) (string, *string) {
	switch reason := quotaReason(q, now, sampleInterval, ceiling); reason {
	case "":
		if admissionCap != nil && maxActive != nil && *admissionCap < *maxActive { // guard:governor-pacing
			return pacingLimited, reasonRef(pacingGovernor)
		}
		return pacingClear, nil
	case availabilityFiveHourCeiling, availabilityWeeklyCeiling:
		return pacingLimited, reasonRef(reason)
	default:
		return pacingUnknown, reasonRef(reason)
	}
}

func reasonRef(reason string) *string { return &reason }
