package main

import (
	"bufio"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// Native acceptance signals: official app-server frames received on the one
// subscribed acceptance connection (its generation), bound to the prepared
// thread. They wake the exact-input readback and can refuse; they never
// accept on their own. Readback (native_codex_acceptance.go) stays the
// authority for "accepted".

type signalKind string

const (
	signalReady    signalKind = "ready"    // thread/status/changed
	signalTurn     signalKind = "turn"     // turn/started, turn/completed
	signalItem     signalKind = "item"     // item/started, item/completed
	signalApproval signalKind = "approval" // server requests and serverRequest/resolved; never answered
	signalExit     signalKind = "exit"     // thread/closed, status notLoaded/systemError
)

// signalClass is the typed classification of one admitted signal.
type signalClass string

const (
	classInOrder    signalClass = "in-order"
	classAheadOfAck signalClass = "ahead-of-ack" // received while the single submit was in flight
	classDuplicate  signalClass = "duplicate"
	classForeign    signalClass = "foreign" // another thread on the shared daemon; ignored
)

// Typed pending classes recorded while the readback has not decided.
const (
	pendingNoTurn        = "pending-no-turn"
	pendingReadback      = "pending-readback"      // a bound turn signal is ahead of the readback
	pendingMaterializing = "pending-materializing" // the new turn's user item is not listed yet
)

const (
	acceptanceMaxWakes    = 64   // wake readbacks per acceptance; then interval polling only
	acceptanceWakeBatch   = 32   // queued frames coalesced into one wake
	acceptanceFrameLimit  = 1024 // frames per request or pre-effect barrier
	acceptancePumpBacklog = 256
	codexResumeRefused    = "wake-capability-missing"
	acceptanceBarrierBusy = "barrier-busy"
)

// High-volume streaming notifications the acceptance connection opts out of
// through the official InitializeCapabilities.optOutNotificationMethods.
var acceptanceOptOut = []string{
	"item/agentMessage/delta", "item/plan/delta", "item/reasoning/summaryTextDelta",
	"item/reasoning/summaryPartAdded", "item/reasoning/textDelta", "item/commandExecution/outputDelta",
	"item/commandExecution/terminalInteraction", "item/fileChange/outputDelta", "item/fileChange/patchUpdated",
	"item/mcpToolCall/progress", "command/exec/outputDelta", "process/outputDelta", "turn/diff/updated",
	"turn/plan/updated", "thread/tokenUsage/updated", "account/rateLimits/updated",
}

type nativeSignal struct {
	Generation string
	Thread     string
	Kind       signalKind
	Method     string
	Turn       string
	Status     string // turn status or thread status type
	Item       string // item type
	Seq        int64  // pump receive sequence on this connection
}

// acceptanceSignals reduces the bound signals of one acceptance generation.
// It is deterministic over the journalled inputs.
type acceptanceSignals struct {
	generation  string
	thread      string
	submitted   bool  // the single submit has begun
	acked       bool  // its acknowledgment (or failure) has been observed
	submitStart int64 // receive sequence when the submit began
	submitAck   int64 // receive sequence when the acknowledgment was observed
	seen        map[string]bool
	firstTurn   string
	materialize map[string]bool
	approvals   int
	duplicates  int
	aheadOfAck  int
	onFirstTurn func()
}

func newAcceptanceSignals(generation, thread string) *acceptanceSignals {
	return &acceptanceSignals{generation: generation, thread: thread, seen: map[string]bool{}, materialize: map[string]bool{}}
}

func (s *acceptanceSignals) preSubmit(seq int64) bool { return !s.submitted || seq <= s.submitStart }
func (s *acceptanceSignals) inFlight(seq int64) bool {
	return s.submitted && seq > s.submitStart && (!s.acked || seq <= s.submitAck)
}

// begin and ack bracket the single submit by receive sequence.
func (s *acceptanceSignals) begin(seq int64) { s.submitted, s.submitStart = true, seq }
func (s *acceptanceSignals) ack(seq int64)   { s.acked, s.submitAck = true, seq }

// classify is the pure decision for one signal; a non-nil error is a
// terminal typed refusal. apply records it after it was journalled.
func (s *acceptanceSignals) classify(sig nativeSignal) (signalClass, error) {
	if sig.Generation != s.generation { // guard:signal-generation
		return "", deliveryRefused("stale-generation")
	}
	if sig.Thread != s.thread {
		return classForeign, nil
	}
	if s.seen[signalKey(sig)] && sig.Kind != signalApproval { // guard:signal-duplicate
		return classDuplicate, nil
	}
	if sig.Kind == signalExit { // guard:signal-exit
		return "", deliveryRefused("native-exit")
	}
	if s.preSubmit(sig.Seq) {
		// Before the effect the thread must stay idle: any turn, item, active
		// status or pending approval is another writer. Zero submits follow.
		if sig.Kind != signalReady || sig.Status != "idle" { // guard:signal-pre-submit
			return "", deliveryRefused("baseline-active")
		}
		return classInOrder, nil
	}
	if (sig.Kind == signalTurn || sig.Kind == signalItem) && s.firstTurn != "" && s.firstTurn != sig.Turn { // guard:signal-one-turn
		return "", deliveryRefused("contradiction")
	}
	if s.inFlight(sig.Seq) {
		return classAheadOfAck, nil
	}
	return classInOrder, nil
}

func signalKey(sig nativeSignal) string {
	return string(sig.Kind) + "|" + sig.Method + "|" + sig.Turn + "|" + sig.Status + "|" + sig.Item
}

func (s *acceptanceSignals) apply(sig nativeSignal, class signalClass) {
	switch class {
	case classForeign, "":
		return
	case classDuplicate:
		s.duplicates++
		return
	case classAheadOfAck:
		s.aheadOfAck++
	}
	s.seen[signalKey(sig)] = true
	switch sig.Kind {
	case signalApproval:
		s.approvals++
	case signalTurn, signalItem:
		if s.firstTurn == "" {
			s.firstTurn = sig.Turn
			if s.onFirstTurn != nil {
				s.onFirstTurn()
			}
		}
		if sig.Kind == signalItem && sig.Item == "userMessage" {
			s.materialize[sig.Turn] = true
		}
	}
}

// admit is classify then apply, for callers that journal separately.
func (s *acceptanceSignals) admit(sig nativeSignal) (signalClass, error) {
	class, err := s.classify(sig)
	if err == nil {
		s.apply(sig, class)
	}
	return class, err
}

// candidate reports whether the readback's single new turn agrees with the
// signals: no signal may name a different new turn.
func (s *acceptanceSignals) candidate(turn string) bool {
	return s.firstTurn == "" || s.firstTurn == turn
}

// wakes reports whether a bound signal should trigger a readback now.
func wakes(sig nativeSignal) bool {
	return sig.Kind == signalTurn || sig.Kind == signalReady || sig.Kind == signalExit ||
		sig.Kind == signalItem && sig.Item == "userMessage"
}

// decodeSignal strictly decodes the non-turn signal notifications. ok=false
// means the method is not a signal; err means a malformed signal frame.
func decodeSignal(method string, params json.RawMessage) (nativeSignal, bool, error) {
	sig := nativeSignal{Method: method}
	switch method {
	case "thread/status/changed", "thread/closed", "item/started", "item/completed", "serverRequest/resolved":
	default:
		return sig, false, nil
	}
	m, ok := nativeObject(params, "threadId")
	thread, ok2 := "", false
	if ok {
		thread, ok2 = nativeText(m["threadId"], false)
	}
	if !ok2 || !privateNativeID(thread) {
		return sig, true, deliveryGap("signal")
	}
	sig.Thread = thread
	switch method {
	case "thread/closed":
		sig.Kind = signalExit
	case "serverRequest/resolved":
		sig.Kind = signalApproval
	case "thread/status/changed":
		status, ok := nativeObject(m["status"], "type")
		kind, ok2 := "", false
		if ok {
			kind, ok2 = nativeText(status["type"], false)
		}
		if !ok2 {
			return sig, true, deliveryGap("signal")
		}
		sig.Kind, sig.Status = signalReady, kind
		switch kind {
		case "idle", "active":
		case "notLoaded", "systemError":
			sig.Kind = signalExit
		default:
			return sig, true, deliveryGap("signal")
		}
	default: // item/started, item/completed
		if _, ok := m["turnId"]; !ok {
			return sig, true, deliveryGap("signal")
		}
		turn, ok := nativeText(m["turnId"], false)
		item, ok2 := nativeObject(m["item"], "type")
		kind, ok3 := "", false
		if ok2 {
			kind, ok3 = nativeText(item["type"], false)
		}
		if !ok || !ok3 || !privateNativeID(turn) {
			return sig, true, deliveryGap("signal")
		}
		sig.Kind, sig.Turn, sig.Item = signalItem, turn, kind
	}
	return sig, true, nil
}

// decodeServerRequest classifies a server request without answering it.
// Requests that name no thread (account, attestation) are not signals.
func decodeServerRequest(method string, params json.RawMessage) (nativeSignal, bool) {
	var m map[string]json.RawMessage
	if decodeNativeJSON(params, &m) != nil || m == nil {
		return nativeSignal{}, false
	}
	thread, ok := nativeText(m["threadId"], false)
	if !ok || !privateNativeID(thread) {
		return nativeSignal{}, false
	}
	turn, _ := nativeText(m["turnId"], false)
	return nativeSignal{Thread: thread, Kind: signalApproval, Method: method, Turn: turn}, true
}

// ---- the one reader of a subscribed acceptance connection ----

type pumpLine struct {
	line []byte
	ok   bool
	err  error
	seq  int64
}

// framePump owns the connection's scanner: it is the only Scan caller, its
// enqueue can always be stopped, and close joins it.
type framePump struct {
	lines    chan pumpLine
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
	received atomic.Int64
	end      *pumpLine
}

func startFramePump(scan *bufio.Scanner) *framePump {
	f := &framePump{lines: make(chan pumpLine, acceptancePumpBacklog), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(f.done)
		defer close(f.lines)
		for {
			l := pumpLine{ok: scan.Scan()}
			if l.ok {
				l.line, l.seq = append([]byte(nil), scan.Bytes()...), f.received.Add(1)
			} else {
				l.err = scan.Err()
			}
			select {
			case f.lines <- l: // guard:pump-stoppable
			case <-f.stop:
				return
			}
			if !l.ok {
				return
			}
		}
	}()
	return f
}

// close stops and joins the pump; the transport must already be ending so a
// blocked Scan returns.
func (f *framePump) close() {
	if f == nil {
		return
	}
	f.once.Do(func() { close(f.stop) })
	<-f.done
}

// take records one received line, remembering the end of the stream.
func (f *framePump) take(l pumpLine, open bool, seq *int64) ([]byte, bool, error) {
	if !open {
		l = pumpLine{err: goalError("goal-source-closed-or-frame-limit")}
	}
	if !l.ok {
		f.end = &l
		return nil, false, l.err
	}
	*seq = l.seq
	return l.line, true, nil
}

func (f *framePump) next(seq *int64) ([]byte, bool, error) {
	if f.end != nil {
		return nil, false, f.end.err
	}
	l, open := <-f.lines
	return f.take(l, open, seq)
}

// nextLine is every consumer's read: the pump when one owns the scanner.
func (p *goalRPC) nextLine() ([]byte, bool, error) {
	if p.pump != nil {
		return p.pump.next(&p.lineSeq)
	}
	if !p.scan.Scan() {
		return nil, false, p.scan.Err()
	}
	return p.scan.Bytes(), true, nil
}

// drainThrough also reduces frames received while draining the captured watermark.
// It stops at observed quiescence or refuses after a bounded number of frames.
func (p *goalRPC) drainThrough(watermark int64) error {
	for frames := 0; ; frames++ {
		if p.lineSeq >= watermark {
			watermark = p.pump.received.Load() // guard:barrier-quiescent
			if p.lineSeq >= watermark {
				return nil
			}
		}
		if frames >= acceptanceFrameLimit { // guard:barrier-frame-bound
			return deliveryRefused(acceptanceBarrierBusy)
		}
		line, ok, err := p.pump.next(&p.lineSeq)
		if !ok {
			return goalErrorOr(err)
		}
		if err := p.unsolicited(line); err != nil {
			return err
		}
	}
}

// unsolicited handles a frame that arrived with no request outstanding.
func (p *goalRPC) unsolicited(line []byte) error {
	var m map[string]json.RawMessage
	if decodeNativeJSON(line, &m) != nil || m == nil {
		return goalError("goal-response-invalid")
	}
	if _, id := m["id"]; id {
		if _, method := m["method"]; method && p.onServerRequest != nil {
			return p.serverRequest(m)
		}
		return goalError("goal-response-mismatch")
	}
	return p.notification(m)
}

// serverRequest observes a server request and never answers it: a reply from
// Orchid could resolve a request shared with the TUI.
func (p *goalRPC) serverRequest(m map[string]json.RawMessage) error {
	var method string
	if json.Unmarshal(m["method"], &method) != nil {
		return goalError("goal-response-invalid")
	}
	return p.onServerRequest(method, m["params"]) // guard:never-answer
}

// awaitWake consumes frames for at most d. It reports a wake when a bound
// signal asked for a readback, after coalescing a bounded queued batch.
func (p *goalRPC) awaitWake(ctx context.Context, d time.Duration, honor bool) (bool, error) {
	if p.pump == nil {
		return false, goalError("goal-transport-unavailable")
	}
	p.wake = false
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, nil
		case <-timer.C:
			return false, nil
		case l, open := <-p.pump.lines:
			line, ok, err := p.pump.take(l, open, &p.lineSeq)
			if !ok {
				return false, goalErrorOr(err)
			}
			if err := p.unsolicited(line); err != nil {
				return false, err
			}
			if !p.wake || !honor {
				p.wake = false
				continue
			}
			for i := 0; i < acceptanceWakeBatch && len(p.pump.lines) > 0; i++ { // guard:wake-coalesce
				line, ok, err := p.pump.next(&p.lineSeq)
				if !ok {
					return false, goalErrorOr(err)
				}
				if err := p.unsolicited(line); err != nil {
					return false, err
				}
			}
			return true, nil
		}
	}
}

// opResult carries op's error, or its panic to re-raise on the caller.
type opResult struct {
	err      error
	panicked bool
	value    any
}

// whileWaiting runs op exactly once and keeps consuming frames until it
// returns, so native evidence that precedes op's acknowledgment is journalled
// in order. op's own error is returned unchanged; a frame error wins only if
// op succeeded, and op is never repeated. A panic in op is re-raised here,
// on the caller's goroutine, exactly as if op had run inline.
func (p *goalRPC) whileWaiting(ctx context.Context, op func() error) (opErr error, frameErr error) {
	result := make(chan opResult, 1)
	go func() { // guard:submit-once
		done := false
		defer func() {
			if !done {
				result <- opResult{panicked: true, value: recover()}
			}
		}()
		err := op()
		done = true
		result <- opResult{err: err}
	}()
	finish := func(r opResult) error {
		if r.panicked {
			panic(r.value)
		}
		return r.err
	}
	for {
		select {
		case r := <-result:
			return finish(r), frameErr
		case l, open := <-p.pump.lines:
			if frameErr != nil {
				if !open {
					return finish(<-result), frameErr
				}
				continue // keep draining until the single op returns
			}
			line, ok, err := p.pump.take(l, open, &p.lineSeq)
			if !ok {
				frameErr = goalErrorOr(err)
				return finish(<-result), frameErr
			}
			frameErr = p.unsolicited(line)
		case <-ctx.Done():
			return finish(<-result), ctx.Err()
		}
	}
}

func goalErrorOr(err error) error {
	if err != nil {
		return err
	}
	return goalError("goal-source-closed-or-frame-limit")
}
