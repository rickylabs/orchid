package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"
)

// Vendor authentication observation. This adapter owns the auth concern for
// every transport. Only a typed native event can yield "unauthenticated":
// terminal text, quoted errors, Herdr status and process identity never can.
// Its one effect, a credential resynchronization plus one notification, runs at
// most once per native event and never tears down, stops or re-prompts a session.
//
//	claude   the managed lifecycle hook's StopFailure with error
//	         "authentication_failed", bound to the exact occupant and session.
//	codex    Remote Control: the bound thread's newest turn, status "failed"
//	         with error.codexErrorInfo "unauthorized". Plain Codex hooks carry
//	         no failure event, so it stays unknown.
//	agy      unknown: its Stop hook types only terminationReason ERROR with a
//	         free-text error (agyGapStopError); credentials are not synced.
//	opencode unknown here: credentials are not centrally synced; its own
//	         adapter blocks a provider error from its native store.
type vendorAuthState string

const (
	authUnknown         vendorAuthState = "unknown"
	authUnauthenticated vendorAuthState = "unauthenticated"
)

// Closed provenance for an unauthenticated outcome; logs carry nothing else.
const (
	authSourceClaudeStopFailure = "claude-stop-failure"
	authSourceCodexTurnError    = "codex-turn-error"
)

// Typed vendor values that mean the session's credential was rejected.
const (
	claudeAuthFailure = "authentication_failed"
	codexAuthFailure  = "unauthorized"
)

// vendorAuthObservation is one classified native fact. Key digests the exact
// native event (agent, session or thread, sequence or turn) and fences effects.
type vendorAuthObservation struct {
	State  vendorAuthState
	Source string
	Key    string
}

var unknownAuth = vendorAuthObservation{State: authUnknown}

// claudeAuthObservation accepts only a lifecycle receipt that matches the exact
// current occupant and native session; a stale generation cannot match.
func claudeAuthObservation(a AgentInfo, j *Job) vendorAuthObservation {
	e := a.VendorEvent
	if j == nil || j.Agent != "claude" || !jobOccupant(a, j) || e == nil || !e.matches(a) ||
		e.Event != "StopFailure" || e.Failure != claudeAuthFailure {
		return unknownAuth
	}
	return vendorAuthObservation{State: authUnauthenticated, Source: authSourceClaudeStopFailure,
		Key: shaText([]byte(fmt.Sprintf("claude\x00%s\x00%s\x00%d", e.Terminal, e.Session, e.Seq)))}
}

// codexAuthObservation classifies the bound thread's newest native turn.
func codexAuthObservation(thread, turn, status, code string) vendorAuthObservation {
	if !privateNativeID(thread) || !privateNativeID(turn) || status != "failed" || code != codexAuthFailure {
		return unknownAuth
	}
	return vendorAuthObservation{State: authUnauthenticated, Source: authSourceCodexTurnError,
		Key: shaText([]byte("codex\x00" + thread + "\x00" + turn))}
}

// authObservationDue debounces reads to native state changes of this occupant.
// A failed read or an undurable fence leaves the change pending for retry.
func authObservationDue(j *Job, ref agentRef) bool {
	return !j.authObserved || j.authObservedSeq != [2]uint64{ref.StateChangeSeq, ref.CompletionSeq}
}

// superviseVendorAuth is supervision's one auth step for a matched occupant.
// Herdr's displayed status never gates Claude, whose detected status can
// disagree with its hook receipt. Remote Control Codex skips only "working",
// whose newest turn is still in progress and so cannot be a failed turn.
func (c *Coord) superviseVendorAuth(ctx context.Context, host Host, j *Job, ref agentRef, matched, recovering bool) {
	if !matched || recovering || j == nil || rcCodex(j) && ref.Status == "working" {
		return
	}
	c.observeVendorAuth(ctx, host, j, ref)
}

// observeVendorAuth reads the transport's typed native source once per change.
// A transport without one is never read: its auth state is unknown.
func (c *Coord) observeVendorAuth(ctx context.Context, host Host, j *Job, ref agentRef) {
	if c.dry || j == nil || j.Pane == "" || !authObservationDue(j, ref) {
		return
	}
	read, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var observation vendorAuthObservation
	var err error
	switch {
	case j.Agent == "claude":
		observation, err = claudeAuthRead(read, host, j)
	case rcCodex(j):
		observation, err = codexAuthRead(read, host, c.cfg.Matrix.ReceiptRoot, c.cfg.Inbox, j)
	default:
		return // no typed native auth source: unknown, no read
	}
	if err != nil || read.Err() != nil {
		return
	}
	if _, err := c.applyVendorAuth(ctx, host, j, observation); err != nil {
		return // the fence is not durable: keep this change pending
	}
	j.authObserved, j.authObservedSeq = true, [2]uint64{ref.StateChangeSeq, ref.CompletionSeq}
}

var errAuthSourceUnavailable = errors.New("auth-source-unavailable")

// claudeAuthRead is one exact-occupant Herdr read with its lifecycle receipt.
// When no event matched, the receipt is re-read once to tell an absent or
// foreign receipt (observed) from one that could not be read (retry).
func claudeAuthRead(ctx context.Context, host Host, j *Job) (vendorAuthObservation, error) {
	a, err := host.agentInfoOf(ctx, j.Pane)
	if err != nil {
		return unknownAuth, err
	}
	if a.VendorEvent == nil {
		event, read := host.vendorLifecycleObserved(ctx, a)
		if !read {
			return unknownAuth, errAuthSourceUnavailable
		}
		a.VendorEvent = event
	}
	return claudeAuthObservation(a, j), nil
}

// codexAuthConnect opens the bound thread's native connection (a test seam).
var codexAuthConnect = func(ctx context.Context, host Host, thread string, use func(*goalRPC) error) error {
	return host.withGoalConnection(ctx, thread, use)
}

// codexAuthRead is one bounded newest-turn read of the bound native thread.
func codexAuthRead(ctx context.Context, host Host, root, inbox string, j *Job) (vendorAuthObservation, error) {
	thread, err := readGoalIdentity(root, j, inbox)
	if err != nil {
		return unknownAuth, err
	}
	observation := unknownAuth
	err = codexAuthConnect(ctx, host, thread, func(p *goalRPC) error {
		turn, status, code, err := p.newestNativeTurn()
		observation = codexAuthObservation(p.thread, turn, status, code)
		return err
	})
	return observation, err
}

// applyVendorAuth reserves the native event durably before its effect, so a
// repeated read, a restart or a stale event cannot repeat it. It reports
// whether the effect ran, and an error when the fence could not be saved.
func (c *Coord) applyVendorAuth(ctx context.Context, host Host, j *Job, o vendorAuthObservation) (bool, error) {
	if o.State != authUnauthenticated || !digestPattern.MatchString(o.Key) {
		return false, nil
	}
	c.st.mu.Lock()
	if c.st.Jobs[j.Issue] != j || j.AuthSignal == o.Key {
		c.st.mu.Unlock()
		return false, nil
	}
	prior := j.AuthSignal
	j.AuthSignal = o.Key
	if err := c.st.saveLocked(); err != nil {
		j.AuthSignal = prior // no durable fence, no effect
		c.st.mu.Unlock()
		return false, err
	}
	c.st.mu.Unlock()
	log.Printf("issue #%d: native auth failure source=%s on %s — resyncing creds (no teardown)", j.Issue, o.Source, host.Name)
	if c.auth != nil {
		sync, cancel := context.WithTimeout(ctx, 30*time.Second)
		_ = c.auth.syncToHost(sync, host)
		cancel()
	}
	c.notify(fmt.Sprintf("issue #%d native auth failure on %s", j.Issue, host.Name))
	return true, nil
}
