package main

import "time"

// Explicit seats for a native transport without a quota source. This is never
// a subscription balance, and cannot disable Claude or Codex quota admission.
type UnmeteredTransportLimit struct {
	MaxActive int `json:"max_active"`
}
type UnmeteredTransportLimits map[string]UnmeteredTransportLimit

func (limits UnmeteredTransportLimits) valid() bool {
	for transport, limit := range limits {
		if transport != "agy" || limit.MaxActive < 0 || limit.MaxActive > 256 {
			return false
		}
	}
	return true
}

func (limits UnmeteredTransportLimits) cap(transport string) int {
	if transport != "agy" || !limits.valid() {
		return 0
	}
	return limits[transport].MaxActive
}

// Admission and its published availability use the same distinction between
// independently metered accounts and explicitly configured unmetered seats.
func admissionTransportReason(transport string, budget int, q quota, now time.Time,
	sampleInterval time.Duration, ceiling float64, limits UnmeteredTransportLimits) string {
	if transport == "agy" || transport == "opencode" {
		if budget <= 0 || (transport == "agy" && limits.cap(transport) <= 0) {
			return availabilityNoCapacity
		}
		return ""
	}
	return transportAvailabilityReason(budget, q, now, sampleInterval, ceiling)
}
