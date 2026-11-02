package main

import (
	"regexp"
	"time"
)

// Native-run clocked instants: protocol v2's additive launch and first-task-start fields.
// Extend a new producer endpoint here: persist its first instant once on the Job with the
// source generation that sampled it, then project it through nativeRunInstantOf.

// nativeRunSchemaVersion 2 adds the optional launch and taskStart instants to version 1.
const nativeRunSchemaVersion = 2

// nativeRunInstantLayout is millisecond UTC; the wire never carries sub-millisecond digits.
const nativeRunInstantLayout = "2006-01-02T15:04:05.000Z"

// A source generation is a process instance identity (newInstanceID): 32 lowercase hex.
var nativeRunGeneration = regexp.MustCompile(`^[a-f0-9]{32}$`)

// clockedInstant is a persisted first instant and the source generation whose clock sampled it.
// It is written once and never advanced by reads, restart, resume or redelivery.
type clockedInstant struct {
	At         time.Time `json:"at"`
	Generation string    `json:"generation"`
}

// nativeRunInstant is the wire form: compare it only with instants of the same generation.
type nativeRunInstant struct {
	At         string `json:"at"`
	Generation string `json:"generation"`
}

// nativeRunGenerationLocked names the source generation in force now: the published
// watch's, else this process instance, which is what the socket publishes when it starts.
// Caller holds c.st.mu.
func (c *Coord) nativeRunGenerationLocked() string {
	if w := c.st.nativeRuns; w != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.generation
	}
	return c.instance
}

// recordLaunchClockLocked stamps the clock identity of j.SpawnedAt once, at registration.
// Adopted jobs never pass through here, so their adoption time is never exposed as a launch.
// Caller holds c.st.mu.
func (c *Coord) recordLaunchClockLocked(j *Job) {
	if j.LaunchGeneration == "" && !j.SpawnedAt.IsZero() {
		j.LaunchGeneration = c.nativeRunGenerationLocked()
	}
}

// recordTaskStartLocked persists the first certified accepted-turn instant once; a later
// acceptance never renews it. Caller holds c.st.mu.
func (c *Coord) recordTaskStartLocked(j *Job, accepted time.Time) {
	if j.TaskStart == nil && !accepted.IsZero() {
		j.TaskStart = &clockedInstant{At: accepted, Generation: c.nativeRunGenerationLocked()}
	}
}

// nativeRunInstantOf projects a persisted instant; an unknown or unclocked one is omitted.
func nativeRunInstantOf(at time.Time, generation string) *nativeRunInstant {
	if at.IsZero() || !nativeRunGeneration.MatchString(generation) {
		return nil
	}
	return &nativeRunInstant{At: at.UTC().Truncate(time.Millisecond).Format(nativeRunInstantLayout), Generation: generation}
}

// validNativeRunInstant accepts an absent instant or a canonical millisecond UTC one.
func validNativeRunInstant(i *nativeRunInstant) bool {
	if i == nil {
		return true
	}
	at, err := time.Parse(nativeRunInstantLayout, i.At)
	return err == nil && !at.IsZero() && at.Format(nativeRunInstantLayout) == i.At && nativeRunGeneration.MatchString(i.Generation)
}
