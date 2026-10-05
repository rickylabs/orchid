package main

import (
	"reflect"
	"sync"
	"time"
)

// Route B native-evidence shadow. A private, observation-only reducer of
// official per-CLI evidence runs beside today's checks and records what it
// would decide next to the inputs today's decision actually used. Nothing
// reads its verdicts back: admission, prompt delivery, Stop, completion,
// capacity, Remote Control decoration and public success stay unchanged.
// Authority is per fact and per registered source epoch, never freshest-wins;
// missing, expired, conflicting or disconnected evidence stays unknown.

type shadowFact string

const (
	shadowConnection  shadowFact = "connection"
	shadowActivity    shadowFact = "activity"
	shadowTurnOutcome shadowFact = "turn-outcome"
	shadowAttachment  shadowFact = "attachment"
)

type shadowSource string

const shadowCodexCanonical shadowSource = "codex-canonical"

// Facts each registered official source may claim, with their closed values.
// Herdr served status and screen reads are today's provenance, never a source.
var shadowCapabilities = map[shadowSource]map[shadowFact][]string{
	shadowCodexCanonical: {
		shadowConnection:  {"connected", "not-connected"},
		shadowActivity:    {"working"},
		shadowTurnOutcome: {"completed", "failed", "interrupted"},
	},
	// Claude's own per-process session record (see native_claude_bridge.go):
	// bridge identity metadata only, never a connection.
	shadowClaudeSession: {
		shadowBridgeIdentity: {"present", "absent"},
	},
}

var shadowSourceVendor = map[shadowSource]string{shadowCodexCanonical: "codex", shadowClaudeSession: "claude"}

const (
	shadowLiveWindow  = remoteControlFreshness
	shadowRowLimit    = 512
	shadowLedgerLimit = 32
	shadowMaxScopes   = 128
)

// Dispatcher-owned invocation binding: dispatch, route, cwd, native identity,
// placement and process. Any change is a new invocation epoch with no proof.
// HookConfirmed is a later confirmation of the same identity, not a route.
type shadowBinding struct {
	Key, Vendor, Root, Host, Pane, Workspace, IdentitySource string
	Issue                                                    int
	Label, Repo, Cwd, Name, Model, Effort                    string
	TUI                                                      remoteTUIProcess
}

func shadowBindingFor(j *Job) (shadowBinding, bool) {
	if j == nil || j.RemoteControl == nil || (j.Agent != "codex" && j.Agent != "claude") || !digestPattern.MatchString(j.DispatchKey) || !privateNativeID(j.RemoteControl.NativeSessionID) {
		return shadowBinding{}, false
	}
	run := j.RemoteControl
	b := shadowBinding{Key: j.DispatchKey, Vendor: j.Agent, Root: run.NativeSessionID, Host: j.Host, Pane: j.Pane, Workspace: j.Workspace, IdentitySource: run.IdentitySource,
		Issue: j.Issue, Label: j.Label, Repo: j.Repo, Cwd: run.Cwd, Name: run.Name, Model: run.Model, Effort: run.Effort} // guard:route-binding
	if j.RemoteControl.TUIProcess != nil {
		b.TUI = *j.RemoteControl.TUIProcess
	}
	return b, true
}

type shadowRow struct {
	Source      shadowSource
	SourceEpoch uint64
	Seq         uint64
	Fact        shadowFact
	Value       string
	Thread      string
	Turn        string
	ObservedAt  time.Time
}

type shadowVerdict struct {
	Fact      shadowFact `json:"fact"`
	Value     string     `json:"value"`
	Authority string     `json:"authority"`
	Reason    string     `json:"reason,omitempty"`
	Live      bool       `json:"live"`
}

type shadowSourceState struct {
	epoch       uint64
	next        uint64
	seen        map[uint64]shadowRow
	unavailable string
	closed      bool
}

type shadowReducer struct {
	binding    shadowBinding
	invocation uint64
	epochs     uint64
	sources    map[shadowSource]*shadowSourceState
	facts      map[shadowFact]shadowRow
	current    string
	outcomes   map[string]string
	conflict   string
}

func newShadowReducer(b shadowBinding, invocation uint64) *shadowReducer {
	return &shadowReducer{binding: b, invocation: invocation, sources: map[shadowSource]*shadowSourceState{}, facts: map[shadowFact]shadowRow{}, outcomes: map[string]string{}}
}

// A new producer incarnation revokes the previous epoch's live facts and turn
// lineage. Contradictions remain sticky; re-registration cannot launder them.
func (r *shadowReducer) registerSource(src shadowSource) uint64 {
	r.epochs++
	r.sources[src] = &shadowSourceState{epoch: r.epochs, next: 1, seen: map[uint64]shadowRow{}}
	r.revoke(src)
	return r.epochs
}

func (r *shadowReducer) revoke(src shadowSource) {
	for fact, row := range r.facts {
		if row.Source == src {
			delete(r.facts, fact)
		}
	}
	r.current, r.outcomes = "", map[string]string{}
}

func (r *shadowReducer) fail(state *shadowSourceState, src shadowSource, reason string) (bool, string) {
	state.unavailable = reason
	r.revoke(src)
	return false, reason
}

func shadowValueAllowed(src shadowSource, fact shadowFact, value string) bool {
	for _, v := range shadowCapabilities[src][fact] {
		if v == value {
			return true
		}
	}
	return false
}

func (r *shadowReducer) ingest(row shadowRow, received time.Time) (bool, string) {
	state := r.sources[row.Source]
	if state == nil { // guard:source-registered
		return false, "source-unregistered"
	}
	if row.SourceEpoch != state.epoch { // guard:source-epoch
		return false, "source-epoch-foreign"
	}
	if state.unavailable != "" || state.closed { // guard:source-available
		return false, "source-unavailable"
	}
	if row.Thread != r.binding.Root { // guard:scope
		return false, "scope-foreign"
	}
	if _, ok := shadowCapabilities[row.Source][row.Fact]; !ok { // guard:capability
		return false, "capability-unsupported"
	}
	if row.Seq != 0 && row.Seq < state.next { // guard:duplicate
		if prior, ok := state.seen[row.Seq]; ok && prior == row {
			return false, "duplicate"
		}
		return r.fail(state, row.Source, "sequence-conflict")
	}
	if row.Seq != state.next { // guard:sequence-gap
		return r.fail(state, row.Source, "sequence-gap")
	}
	if !shadowValueAllowed(row.Source, row.Fact, row.Value) || (row.Fact == shadowActivity && row.Turn == "") || row.ObservedAt.IsZero() || row.ObservedAt.After(received) { // guard:row-valid
		return r.fail(state, row.Source, "row-invalid")
	}
	if len(state.seen) >= shadowRowLimit { // guard:row-limit
		return r.fail(state, row.Source, "row-limit")
	}
	state.seen[row.Seq] = row
	state.next++
	r.apply(row)
	return true, ""
}

func (r *shadowReducer) apply(row shadowRow) {
	switch row.Fact {
	case shadowActivity:
		if _, terminal := r.outcomes[row.Turn]; terminal { // guard:sticky-restart
			r.conflict = "conflict-unreconciled"
		}
		if row.Turn != r.current { // guard:turn-rescope
			delete(r.facts, shadowTurnOutcome) // the prior turn's outcome stays history only
		}
		r.current = row.Turn
		r.facts[row.Fact] = row
	case shadowTurnOutcome:
		if row.Turn == "" { // guard:turn-association
			return // an unassociated terminal is never assigned to the newest turn
		}
		if prior, seen := r.outcomes[row.Turn]; seen && prior != row.Value { // guard:terminal-conflict
			// Equal-authority contradiction; a higher sequence is not causal
			// reconciliation. Keep the first value as history and stay unknown.
			r.conflict = "conflict-unreconciled"
			return
		}
		r.outcomes[row.Turn] = row.Value
		if row.Turn == r.current { // guard:turn-lineage
			delete(r.facts, shadowActivity)
			r.facts[row.Fact] = row
		}
	default:
		r.facts[row.Fact] = row
	}
}

// Transport/publisher liveness only. It never renews a native fact.
func (r *shadowReducer) heartbeat(src shadowSource, epoch uint64, at time.Time) {}

// A completed snapshot read keeps its bounded window; loss invalidates.
func (r *shadowReducer) close(src shadowSource, epoch uint64, clean bool) {
	state := r.sources[src]
	if state == nil || state.epoch != epoch {
		return
	}
	state.closed = true
	if !clean && state.unavailable == "" { // guard:disconnect
		r.fail(state, src, "source-disconnected")
	}
}

func (r *shadowReducer) decide(fact shadowFact, now time.Time) shadowVerdict {
	unknown := func(reason string) shadowVerdict {
		return shadowVerdict{Fact: fact, Value: "unknown", Authority: "none", Reason: reason}
	}
	if fact == shadowAttachment {
		return unknown(r.binding.Vendor + "-tui-attachment-unproven")
	}
	if fact == shadowConnection && r.binding.Vendor == "claude" { // guard:claude-connection-no-official-surface
		return unknown("no-official-surface")
	}
	var src shadowSource
	for s, vendor := range shadowSourceVendor {
		if vendor == r.binding.Vendor {
			src = s
		}
	}
	if src == "" { // guard:vendor-capability
		if fact == shadowConnection {
			return unknown("native-current-connection-unsupported")
		}
		return unknown("native-" + string(fact) + "-unsupported")
	}
	if r.conflict != "" && (fact == shadowActivity || fact == shadowTurnOutcome) { // guard:sticky-conflict
		return unknown(r.conflict)
	}
	if state := r.sources[src]; state == nil {
		return unknown("native-absent")
	} else if state.unavailable != "" {
		return unknown(state.unavailable)
	}
	row, ok := r.facts[fact]
	if !ok {
		return unknown("native-absent")
	}
	if fact == shadowTurnOutcome {
		// Historical native outcome for the exact current turn: audit evidence,
		// never current action, readiness or release authority.
		return shadowVerdict{Fact: fact, Value: row.Value, Authority: "native"}
	}
	if now.Before(row.ObservedAt) || now.Sub(row.ObservedAt) > shadowLiveWindow { // guard:freshness
		return unknown("native-expired")
	}
	return shadowVerdict{Fact: fact, Value: row.Value, Authority: "native", Live: true}
}

// Closed provenance codes for the inputs today's decision consumed.
const (
	shadowInputHerdrAgent   = "herdr-agent-info"
	shadowInputScreenFooter = "screen-footer"
	shadowInputCodexStatus  = "codex-canonical-status"
	shadowInputTUIProcess   = "codex-tui-process"
	shadowInputUnchecked    = "unchecked"
	shadowInputOther        = "other"

	shadowSiteRemoteHook = "remote-hook"
	shadowSiteConnection = "remote-control-connection"
)

var shadowKnownInputs = map[string]bool{shadowInputHerdrAgent: true, shadowInputScreenFooter: true, shadowInputCodexStatus: true, shadowInputTUIProcess: true, shadowInputUnchecked: true,
	shadowInputScreenComposer: true}

var shadowSiteFacts = map[string][]shadowFact{
	shadowSiteRemoteHook: {shadowAttachment, shadowActivity},
	shadowSiteConnection: {shadowConnection, shadowActivity, shadowBridgeIdentity},
	// Its native verdict is computed at the call site from native inputs only
	// (compareNative), not from the reducer.
	shadowSiteGoalReadiness: {shadowReadiness},
}

// Private in-memory record. Closed codes only: no native, placement, path,
// prompt or URL value enters it.
type shadowComparison struct {
	Site          string          `json:"site"`
	Today         string          `json:"today"`
	Provenance    []string        `json:"provenance"`
	ScreenDerived bool            `json:"screenDerived"`
	Proposed      []shadowVerdict `json:"proposed"`
	Agreement     string          `json:"agreement"`
	At            time.Time       `json:"at"`
}

type shadowScopeState struct {
	reducer  *shadowReducer
	ledger   []shadowComparison
	touched  uint64
	accepted map[string]int // closed fact codes
	rejected map[string]int // closed refusal codes
}

type nativeEvidenceShadow struct {
	bg          sync.WaitGroup  // background native reads (tests wait on it)
	inflight    map[string]bool // one background read per scope and source
	mu          sync.Mutex
	now         func() time.Time
	invocations uint64
	ticks       uint64
	version     uint64 // bumped on every recorded change; drives the private readout
	scopes      map[string]*shadowScopeState
	parity      map[shadowParityKey]int64 // durable outcome counts (native_evidence_shadow_parity.go)
	paritySince time.Time
}

func newNativeEvidenceShadow(now func() time.Time) *nativeEvidenceShadow {
	return &nativeEvidenceShadow{now: now, scopes: map[string]*shadowScopeState{}}
}

// Shadow failures stay inside the shadow; today's caller never sees them.
func shadowContain() { _ = recover() } // guard:contain

// Caller holds s.mu. Registers (or re-registers) the dispatcher binding.
func (s *nativeEvidenceShadow) bindLocked(j *Job) *shadowScopeState {
	b, ok := shadowBindingFor(j)
	if !ok {
		return nil
	}
	s.ticks++
	scope := s.scopes[b.Key]
	if scope == nil || !reflect.DeepEqual(scope.reducer.binding, b) { // guard:invocation-binding
		if scope == nil && len(s.scopes) >= shadowMaxScopes {
			s.evictLocked()
		}
		s.invocations++
		s.version++
		scope = &shadowScopeState{reducer: newShadowReducer(b, s.invocations), accepted: map[string]int{}, rejected: map[string]int{}}
		s.scopes[b.Key] = scope
	}
	scope.touched = s.ticks
	return scope
}

func (s *nativeEvidenceShadow) evictLocked() {
	oldest := ""
	for key, scope := range s.scopes {
		if oldest == "" || scope.touched < s.scopes[oldest].touched {
			oldest = key
		}
	}
	delete(s.scopes, oldest)
}

type nativeShadowScope struct {
	shadow     *nativeEvidenceShadow
	key        string
	invocation uint64
}

func (s *nativeEvidenceShadow) scope(j *Job) (out *nativeShadowScope) {
	if s == nil {
		return nil
	}
	defer shadowContain()
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := s.bindLocked(j)
	if scope == nil {
		return nil
	}
	return &nativeShadowScope{shadow: s, key: j.DispatchKey, invocation: scope.reducer.invocation}
}

// One serialized publisher per registered source epoch; it allocates the
// contiguous sequence the reducer validates.
type shadowPublisher struct {
	scope  *nativeShadowScope
	source shadowSource
	thread string
	epoch  uint64
	seq    uint64
}

func (sc *nativeShadowScope) open(src shadowSource, thread string) (out *shadowPublisher) {
	if sc == nil || sc.shadow == nil {
		return nil
	}
	defer shadowContain()
	sc.shadow.mu.Lock()
	defer sc.shadow.mu.Unlock()
	scope := sc.shadow.scopes[sc.key]
	if scope == nil || scope.reducer.invocation != sc.invocation || shadowSourceVendor[src] != scope.reducer.binding.Vendor || thread != scope.reducer.binding.Root {
		return nil
	}
	return &shadowPublisher{scope: sc, source: src, thread: thread, epoch: scope.reducer.registerSource(src)}
}

func (p *shadowPublisher) publish(fact shadowFact, value, thread, turn string) {
	if p == nil || p.scope == nil || p.scope.shadow == nil {
		return
	}
	defer shadowContain()
	s := p.scope.shadow
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := s.scopes[p.scope.key]
	if scope == nil || scope.reducer.invocation != p.scope.invocation { // guard:publisher-invocation
		return // delayed evidence from a replaced invocation
	}
	now := s.now()
	p.seq++
	if ok, reason := scope.reducer.ingest(shadowRow{Source: p.source, SourceEpoch: p.epoch, Seq: p.seq, Fact: fact, Value: value, Thread: thread, Turn: turn, ObservedAt: now}, now); ok {
		scope.accepted[string(fact)]++
	} else {
		scope.rejected[reason]++
	}
	s.version++
}

// Exact-thread canonical turn notification, already validated by its reader.
func (p *shadowPublisher) turn(thread, id, status string) {
	if status == "inProgress" {
		p.publish(shadowActivity, "working", thread, id)
		return
	}
	p.publish(shadowTurnOutcome, status, thread, id)
}

// Daemon remote-control status read; positive and negative are both evidence.
func (p *shadowPublisher) bridgeIdentity(present bool) {
	if p == nil {
		return
	}
	value := "absent"
	if present {
		value = "present"
	}
	p.publish(shadowBridgeIdentity, value, p.thread, "")
}

// begin claims the one background read of name for this scope.
func (sc *nativeShadowScope) begin(name string) bool {
	if sc == nil || sc.shadow == nil {
		return false
	}
	s := sc.shadow
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight == nil {
		s.inflight = map[string]bool{}
	}
	key := sc.key + "\x00" + name
	if s.inflight[key] {
		return false
	}
	s.inflight[key] = true
	s.bg.Add(1)
	return true
}

func (sc *nativeShadowScope) end(name string) {
	s := sc.shadow
	s.mu.Lock()
	delete(s.inflight, sc.key+"\x00"+name)
	s.mu.Unlock()
	s.bg.Done()
}

func (p *shadowPublisher) connection(connected bool) {
	if p == nil {
		return
	}
	value := "not-connected"
	if connected {
		value = "connected"
	}
	p.publish(shadowConnection, value, p.thread, "")
}

func (p *shadowPublisher) close(err error) {
	if p == nil || p.scope == nil || p.scope.shadow == nil {
		return
	}
	defer shadowContain()
	s := p.scope.shadow
	s.mu.Lock()
	defer s.mu.Unlock()
	if scope := s.scopes[p.scope.key]; scope != nil && scope.reducer.invocation == p.scope.invocation {
		scope.reducer.close(p.source, p.epoch, err == nil)
	}
}

// Record today's decision, the closed inputs it used and the shadow proposal.
func (s *nativeEvidenceShadow) compare(j *Job, site, today string, inputs []string) {
	s.record(j, site, today, inputs, nil, time.Time{})
}

// compareNative records today's decision beside a native verdict the caller
// computed from native inputs only.
func (s *nativeEvidenceShadow) compareNative(j *Job, site, today string, inputs []string, native shadowVerdict) {
	s.record(j, site, today, inputs, &native, time.Time{})
}

// compareNativeAt is compareNative stamped with the moment today's decision
// was observed, not when the native read finished.
func (s *nativeEvidenceShadow) compareNativeAt(j *Job, site, today string, inputs []string, native shadowVerdict, at time.Time) {
	s.record(j, site, today, inputs, &native, at)
}

func (s *nativeEvidenceShadow) record(j *Job, site, today string, inputs []string, native *shadowVerdict, at time.Time) {
	if s == nil {
		return
	}
	defer shadowContain()
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := s.bindLocked(j)
	facts := shadowSiteFacts[site]
	if scope == nil || len(facts) == 0 {
		return
	}
	now := s.now()
	c := shadowComparison{Site: site, Today: today, At: now, Provenance: []string{}}
	if !at.IsZero() {
		c.At = at
	}
	for _, input := range inputs {
		if !shadowKnownInputs[input] { // guard:closed-provenance
			input = shadowInputOther
		}
		c.Provenance = append(c.Provenance, input)
		c.ScreenDerived = c.ScreenDerived || input == shadowInputScreenFooter || input == shadowInputScreenComposer
	}
	if native != nil {
		c.Proposed = append(c.Proposed, *native)
	} else {
		for _, fact := range facts {
			c.Proposed = append(c.Proposed, scope.reducer.decide(fact, now))
		}
	}
	c.Agreement = shadowAgreement(site, today, c.Proposed[0])
	if len(scope.ledger) >= shadowLedgerLimit { // guard:ledger-bound
		scope.ledger = scope.ledger[1:]
	}
	scope.ledger = append(scope.ledger, c)
	s.countParityLocked(site, scope.reducer.binding.Vendor, c) // guard:parity-counted
	s.version++
}

func shadowAgreement(site, today string, v shadowVerdict) string {
	if v.Value == "unknown" {
		return "shadow-unknown"
	}
	native := v.Value
	if site == shadowSiteConnection && native == "not-connected" {
		native = "unconfirmed"
	}
	if native == today {
		return "agree"
	}
	return "disagree"
}

func (s *nativeEvidenceShadow) ledger(j *Job) []shadowComparison {
	if s == nil || j == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := s.scopes[j.DispatchKey]
	if scope == nil {
		return nil
	}
	return append([]shadowComparison(nil), scope.ledger...)
}

func (s *nativeEvidenceShadow) verdict(j *Job, fact shadowFact) shadowVerdict {
	unknown := shadowVerdict{Fact: fact, Value: "unknown", Authority: "none", Reason: "native-absent"}
	if s == nil {
		return unknown
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := shadowBindingFor(j)
	scope := s.scopes[b.Key]
	if !ok || scope == nil || !reflect.DeepEqual(scope.reducer.binding, b) {
		return unknown
	}
	return scope.reducer.decide(fact, s.now())
}
