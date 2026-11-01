package main

import (
	"context"
	"errors"
)

// The Remote Control Codex launch boundary: how Orchid knows the prepared
// thread is attached to the intended TUI consumer, and how it accepts the one
// goal turn. Read this file first; it names every authority and its limits.
//
//   - Attachment (this file + remote_control_identity.go): the private daemon
//     log proof, the Herdr structured occupant and the kernel TUI process
//     fingerprint. None of these is a vendor-native attachment acknowledgment.
//   - Acceptance signals (codex_acceptance_signals.go): official app-server
//     notifications on one subscribed canonical connection, bound to the
//     prepared thread and that connection's generation.
//   - Acceptance decision (native_codex_acceptance.go): the durable receipt,
//     journal and exact-input page readback.
//   - Stage evidence (codex_launch_stages.go): per-launch stage durations.
//
// See docs/native-attachment.md for the protocol inventory behind the gaps.

// Upstream capability gaps, named so a later Codex release can be checked
// against them. Until an official acknowledgment exists the outcome stays
// private-log-proven or unknown: never vendor-native.
const (
	// Codex app-server v2 (inventoried at 0.166.0) has no request or
	// notification naming the connections or clients subscribed to a thread,
	// and none announcing another client's thread/resume. Only the resuming
	// client sees its own response.
	codexAttachmentNativeGap = "codex-app-server-v2-no-thread-subscriber-acknowledgment"
	// The Codex TUI exposes no programmatic composer-ready acknowledgment.
	// Herdr's interactive_ready with idle/done remains the typed-input gate.
	codexReadinessNativeGap = "codex-tui-no-composer-ready-acknowledgment"
)

// attachmentAuthority names which source proved a TUI attachment.
type attachmentAuthority struct {
	Source           string // private-daemon-log, herdr-session-start or none
	NativeCapability string // always "missing" until the gap above is closed
	Gap              string
}

const (
	attachmentSourcePrivateLog = "private-daemon-log"
	attachmentSourceHerdrHook  = "herdr-session-start"
	attachmentSourceNone       = "none"
	nativeCapabilityMissing    = "missing"
)

// attachmentAuthorityOf reports the authority behind a run's attachment.
// "codex-native-status" is a persisted legacy label: its proof is the private
// daemon log plus Herdr's structured occupant, not a vendor-native signal.
func attachmentAuthorityOf(run *remoteControlRun) attachmentAuthority {
	a := attachmentAuthority{Source: attachmentSourceNone, NativeCapability: nativeCapabilityMissing, Gap: codexAttachmentNativeGap}
	switch {
	case run == nil:
	case run.IdentitySource == "codex-native-status" && codexResumeProven(run):
		a.Source = attachmentSourcePrivateLog
	case run.IdentitySource == attachmentSourceHerdrHook:
		a.Source = attachmentSourceHerdrHook
	}
	return a
}

// attachmentOutcome is the typed result of an attachment or binding check.
type attachmentOutcome string

const (
	attachmentPrivateLogProven attachmentOutcome = "private-log-proven"
	attachmentPending          attachmentOutcome = "pending"          // no complete record yet
	attachmentAmbiguous        attachmentOutcome = "ambiguous"        // >1 creator or >1 TUI connection
	attachmentConsumerChanged  attachmentOutcome = "consumer-changed" // foreign producer/version, occupant or process
	attachmentProcessReused    attachmentOutcome = "process-reused"   // same PID, different kernel start/argv
	attachmentUnreadable       attachmentOutcome = "unreadable"
	attachmentDeadline         attachmentOutcome = "deadline"
)

// attachmentError keeps the closed outward reason every caller already
// matches, and carries the typed outcome for logs and stage evidence.
type attachmentError struct{ outcome attachmentOutcome }

const attachmentRefusalReason = goalError("remote-control-identity-unconfirmed")

func (e attachmentError) Error() string { return string(attachmentRefusalReason) }
func (e attachmentError) Unwrap() error { return attachmentRefusalReason }

func attachmentRefused(o attachmentOutcome) error { return attachmentError{o} }

// attachmentOutcomeOf extracts the typed outcome; "" when err carries none.
func attachmentOutcomeOf(err error) attachmentOutcome {
	var e attachmentError
	if errors.As(err, &e) {
		return e.outcome
	}
	return ""
}

// compareTUIProcess classifies a fresh TUI process read against the one
// captured at attachment. The kernel start/argv fingerprint is cleanup and
// continuity identity only; it never supplies issue ancestry or task success.
func compareTUIProcess(stored, current *remoteTUIProcess) attachmentOutcome {
	switch {
	case stored == nil || current == nil:
		return attachmentUnreadable
	case *stored == *current:
		return ""
	case stored.PID == current.PID: // guard:process-reused
		return attachmentProcessReused
	default:
		return attachmentConsumerChanged
	}
}

// checkTUIProcess reads the pane's TUI process now and compares it.
func checkTUIProcess(ctx context.Context, read func(context.Context) (*remoteTUIProcess, error), stored *remoteTUIProcess) error {
	current, err := read(ctx)
	if err != nil || ctx.Err() != nil {
		return attachmentRefused(attachmentUnreadable)
	}
	if o := compareTUIProcess(stored, current); o != "" {
		return attachmentRefused(o)
	}
	return nil
}

// resumeRecordOutcome maps the private log reader's closed state to a typed
// outcome. Unknown text is unreadable, never proven.
func resumeRecordOutcome(state, reason string) attachmentOutcome {
	switch state {
	case "proven":
		return attachmentPrivateLogProven
	case "pending":
		return attachmentPending
	case "refused":
		if reason == string(attachmentAmbiguous) {
			return attachmentAmbiguous
		}
		if reason == string(attachmentConsumerChanged) {
			return attachmentConsumerChanged
		}
	}
	return attachmentUnreadable
}
