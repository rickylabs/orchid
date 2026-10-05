package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// A launch attempt runs from the reserved launch call to one terminal outcome.
// The tracker records only native facts: fixed code-location stages, structured
// Herdr start errors captured before any refinement, context ends, exact
// pre-workspace stops, registration and the goal commit. Screen-derived
// diagnostics never become facts, so they can never choose a reason.

const (
	stagePreparation = iota + 1
	stageClientSelection
	stageWorkspace
	stageEnvironment
	stageThreadPreparation
	stageRegistration
	stageGoalDelivery
	stageGoalConfirmation
)

var stageNames = map[int]string{
	stagePreparation: "preparation", stageClientSelection: "client-selection", stageWorkspace: "workspace",
	stageEnvironment: "environment", stageThreadPreparation: "thread-preparation", stageRegistration: "registration",
	stageGoalDelivery: "goal-delivery", stageGoalConfirmation: "goal-confirmation",
}

// errOrchidShutdown is the root cancellation cause on SIGINT/SIGTERM; a derived
// context reports it through context.Cause.
var errOrchidShutdown = errors.New("orchid-shutdown")

var attemptIDPattern = regexp.MustCompile(`^attempt_[a-f0-9]{64}$`)

func attemptIDFor(reservationKey string) string {
	return "attempt_" + shaText([]byte("orchid-launch-attempt\x00"+reservationKey))
}

type attemptContextEnd struct {
	Kind     string `json:"kind"` // deadline | cancelled
	Shutdown bool   `json:"shutdown"`
	Stage    string `json:"stage"`
}

// Each fact kind is recorded at most once and never overwritten.
type attemptFacts struct {
	HerdrStartError        string             `json:"herdrStartError,omitempty"`
	ContextEnded           *attemptContextEnd `json:"contextEnded,omitempty"`
	CodexClientUnmatched   bool               `json:"codexClientUnmatched,omitempty"`
	CodexClientCheckFailed bool               `json:"codexClientCheckFailed,omitempty"`
	AgySettingsUnreadable  bool               `json:"agySettingsUnreadable,omitempty"`
	Registered             bool               `json:"registered,omitempty"`
	GoalCommitted          bool               `json:"goalCommitted,omitempty"`
	RunStarted             bool               `json:"runStarted,omitempty"`
}

func (f attemptFacts) evidence() []string {
	out := []string{}
	add := func(ok bool, name string) {
		if ok {
			out = append(out, name)
		}
	}
	add(f.HerdrStartError != "", "herdrStartError")
	add(f.ContextEnded != nil, "contextEnded")
	add(f.CodexClientUnmatched, "codexClientUnmatched")
	add(f.CodexClientCheckFailed, "codexClientCheckFailed")
	add(f.AgySettingsUnreadable, "agySettingsUnreadable")
	add(f.Registered, "registered")
	add(f.GoalCommitted, "goalCommitted")
	add(f.RunStarted, "runStarted")
	sort.Strings(out)
	return out
}

type outcomeRoute struct {
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

type attemptDecision struct {
	Bytes      string `json:"bytes"`
	ObservedAt string `json:"observedAt"`
}

// The private, mutable progress record. Old readers never read it.
type attemptProgress struct {
	SchemaVersion        int              `json:"schemaVersion"`
	AttemptID            string           `json:"attemptId"`
	ActionDispatchID     string           `json:"actionDispatchId"`
	DispatchID           string           `json:"dispatchId"`
	PredecessorAttemptID *string          `json:"predecessorAttemptId"`
	RetryOperationID     *string          `json:"retryOperationId"`
	Issue                dispatchIssue    `json:"issue"`
	ResolvedRoute        outcomeRoute     `json:"resolvedRoute"`
	Run                  bool             `json:"run"`
	Stage                string           `json:"stage"`
	Facts                attemptFacts     `json:"facts"`
	Instance             string           `json:"instance"`
	StartedAt            string           `json:"startedAt"`
	UpdatedAt            string           `json:"updatedAt"`
	Decision             *attemptDecision `json:"decision"`
}

// The consumer-visible admission fact, written once.
type attemptAdmission struct {
	SchemaVersion        int           `json:"schemaVersion"`
	AttemptID            string        `json:"attemptId"`
	ActionDispatchID     string        `json:"actionDispatchId"`
	DispatchID           string        `json:"dispatchId"`
	PredecessorAttemptID *string       `json:"predecessorAttemptId"`
	RetryOperationID     *string       `json:"retryOperationId"`
	Issue                dispatchIssue `json:"issue"`
	ResolvedRoute        outcomeRoute  `json:"resolvedRoute"`
	AdmittedAt           string        `json:"admittedAt"`
}

// The consumer-visible terminal outcome, written once.
type launchOutcome struct {
	SchemaVersion        int           `json:"schemaVersion"`
	AttemptID            string        `json:"attemptId"`
	ActionDispatchID     string        `json:"actionDispatchId"`
	DispatchID           string        `json:"dispatchId"`
	PredecessorAttemptID *string       `json:"predecessorAttemptId"`
	RetryOperationID     *string       `json:"retryOperationId"`
	Issue                dispatchIssue `json:"issue"`
	ResolvedRoute        outcomeRoute  `json:"resolvedRoute"`
	Outcome              string        `json:"outcome"`
	Effects              string        `json:"effects"`
	Phase                string        `json:"phase"`
	ReasonCode           string        `json:"reasonCode"`
	Signal               string        `json:"signal"`
	Evidence             []string      `json:"evidence"`
	ObservedAt           string        `json:"observedAt"`
}

type launchAttempt struct {
	mu      sync.Mutex
	root    string
	owner   *receiptOwner
	publish bool // consumer files (admission, outcome) only when enabled
	key     string
	account string
	stage   int
	tracked bool
	p       attemptProgress
	now     func() time.Time
	// After the decision is taken nothing else is written; a decision whose first
	// durable write failed is kept here and retried, never re-decided.
	decided  bool
	durable  bool
	admitted bool
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func attemptFile(root, prefix, attemptID string) string {
	return filepath.Join(root, prefix+"-"+shaText([]byte(attemptID))+".json")
}

func newLaunchAttempt(root string, owner *receiptOwner, publish bool, instance, key, dispatchID string, issue dispatchIssue,
	route outcomeRoute, run bool, retry *retryExpectation, now func() time.Time) *launchAttempt {
	t := &launchAttempt{root: root, owner: owner, publish: publish, key: key, now: now}
	t.p = attemptProgress{SchemaVersion: 1, AttemptID: attemptIDFor(key), ActionDispatchID: actionOpaque("assignment", "orchid-"+key),
		DispatchID: dispatchID, Issue: issue, ResolvedRoute: route, Run: run, Instance: instance, StartedAt: stamp(now())}
	if retry != nil {
		pred := attemptIDFor(strings.TrimPrefix(retry.Dispatch.RunID, "orchid-"))
		op := retry.OperationID
		t.p.PredecessorAttemptID, t.p.RetryOperationID = &pred, &op
	}
	return t
}

// begin persists progress before any launch work. A failure leaves the attempt
// untracked; the launch still proceeds and is never refused for telemetry.
func (t *launchAttempt) begin() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tracked = t.persistLocked() == nil // guard:attempt-begin
	if !t.tracked {
		log.Printf("launch attempt untracked: progress unavailable")
		return
	}
	t.admitLocked()
}

// admissionBytes is the admission fact, derived only from the progress record,
// so a retry or a recovery republishes identical bytes.
func admissionBytes(p attemptProgress) ([]byte, error) {
	return json.Marshal(attemptAdmission{SchemaVersion: 1, AttemptID: p.AttemptID, ActionDispatchID: p.ActionDispatchID, DispatchID: p.DispatchID,
		PredecessorAttemptID: p.PredecessorAttemptID, RetryOperationID: p.RetryOperationID, Issue: p.Issue,
		ResolvedRoute: p.ResolvedRoute, AdmittedAt: p.StartedAt})
}

// admitLocked publishes the admission fact once. A failure never blocks the
// launch; the fact is retried, and an outcome may still publish without it.
func (t *launchAttempt) admitLocked() {
	if !t.publish || !t.tracked || t.admitted {
		return
	}
	body, err := admissionBytes(t.p)
	if err == nil {
		err = publishRecord(t.root, attemptFile(t.root, "admitted", t.p.AttemptID), body, t.owner)
	}
	t.admitted = err == nil || errors.Is(err, errOutcomeConflict)
	if !t.admitted {
		log.Printf("launch attempt admission unavailable; retrying")
	}
}

// writeProgressRecord writes the private progress record (a seam for failure tests).
var writeProgressRecord = func(path string, owner *receiptOwner, p attemptProgress) error {
	return writePrivateJSON(path, ".attempt-", owner, p)
}

func (t *launchAttempt) persistLocked() error {
	t.p.UpdatedAt = stamp(t.now())
	return writeProgressRecord(attemptFile(t.root, "attempt", t.p.AttemptID), t.owner, t.p)
}

func (t *launchAttempt) update(change func() bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.decided { // guard:no-write-after-decision
		return
	}
	if change() && t.tracked {
		_ = t.persistLocked()
	}
}

// stage records entry to a fixed code location; stages never decrease, and a
// run-mode job never leaves the environment stage.
func (t *launchAttempt) enter(s int) {
	t.update(func() bool {
		if t.p.Run && s > stageEnvironment {
			s = stageEnvironment
		}
		if s <= t.stage { // guard:stage-monotonic
			return false
		}
		t.stage, t.p.Stage = s, stageNames[s]
		return true
	})
}

// contextEnded captures a child context's end at the failing call, before any
// cleanup cancels it. The first end wins.
func (t *launchAttempt) contextEnded(ctx context.Context) {
	if t == nil || ctx == nil || ctx.Err() == nil {
		return
	}
	t.update(func() bool {
		if t.p.Facts.ContextEnded != nil { // guard:fact-immutable
			return false
		}
		kind := "cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = "deadline"
		}
		t.p.Facts.ContextEnded = &attemptContextEnd{Kind: kind, Shutdown: errors.Is(context.Cause(ctx), errOrchidShutdown), Stage: t.p.Stage}
		return true
	})
}

// herdrStartError records Herdr's structured start error code, read from the
// command's own JSON before any diagnostic refinement.
func (t *launchAttempt) herdrStartError(output string) {
	code := herdrStartErrorCode(output)
	if code == "" {
		return
	}
	t.update(func() bool {
		if t.p.Facts.HerdrStartError != "" {
			return false
		}
		t.p.Facts.HerdrStartError = code
		return true
	})
}

func herdrStartErrorCode(output string) string {
	if len(output) > 1024*1024 {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var response struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if len(lines) == 0 || json.Unmarshal([]byte(lines[len(lines)-1]), &response) != nil || response.Error == nil {
		return ""
	}
	switch response.Error.Code {
	case "timeout", "agent_not_ready", "agent_pane_busy":
		return response.Error.Code
	}
	return ""
}

func (t *launchAttempt) mark(set func(f *attemptFacts) *bool) {
	t.update(func() bool {
		flag := set(&t.p.Facts)
		if *flag {
			return false
		}
		*flag = true
		return true
	})
}

func (t *launchAttempt) codexClientUnmatched() {
	t.mark(func(f *attemptFacts) *bool { return &f.CodexClientUnmatched })
}
func (t *launchAttempt) codexClientCheckFailed() {
	t.mark(func(f *attemptFacts) *bool { return &f.CodexClientCheckFailed })
}
func (t *launchAttempt) agySettingsUnreadable() {
	t.mark(func(f *attemptFacts) *bool { return &f.AgySettingsUnreadable })
}
func (t *launchAttempt) registered() { t.mark(func(f *attemptFacts) *bool { return &f.Registered }) }
func (t *launchAttempt) goalCommitted() {
	t.mark(func(f *attemptFacts) *bool { return &f.GoalCommitted })
}
func (t *launchAttempt) runStarted() { t.mark(func(f *attemptFacts) *bool { return &f.RunStarted }) }

// decideOutcome is the one reduction from native facts and the last stage.
// Only accumulated native facts decide; the launch call's own return value does
// not (later bookkeeping errors never discard a proved start).
func decideOutcome(p attemptProgress, _ error, observedAt string) launchOutcome {
	o := launchOutcome{SchemaVersion: 1, AttemptID: p.AttemptID, ActionDispatchID: p.ActionDispatchID, DispatchID: p.DispatchID,
		PredecessorAttemptID: p.PredecessorAttemptID, RetryOperationID: p.RetryOperationID, Issue: p.Issue,
		ResolvedRoute: p.ResolvedRoute, Evidence: p.Facts.evidence(), ObservedAt: observedAt,
		Outcome: "unconfirmed", Effects: "possible", Phase: p.Stage}
	if o.Phase == "" {
		o.Phase = stageNames[stagePreparation]
	}
	set := func(reason, signal string) launchOutcome { o.ReasonCode, o.Signal = reason, signal; return o }
	f := p.Facts
	switch {
	case !p.Run && f.Registered && f.GoalCommitted: // guard:decide-success
		o.Outcome, o.Effects, o.Phase = "started", "present", stageNames[stageGoalConfirmation]
		return set("started", "orchid-commit")
	case p.Run && f.RunStarted: // guard:decide-run-success
		o.Outcome, o.Effects, o.Phase = "started", "present", stageNames[stageEnvironment]
		return set("started", "orchid-run")
	case f.CodexClientUnmatched: // guard:decide-early-stop
		o.Outcome, o.Effects, o.Phase = "failed", "none", stageNames[stageClientSelection]
		return set("codex-client-unmatched", "codex-app-server")
	case f.CodexClientCheckFailed:
		o.Outcome, o.Effects, o.Phase = "failed", "none", stageNames[stageClientSelection]
		return set("codex-client-check-failed", "codex-client-check")
	case f.AgySettingsUnreadable:
		o.Outcome, o.Effects, o.Phase = "failed", "none", stageNames[stageClientSelection]
		return set("agy-settings-unreadable", "agy-settings")
	case f.ContextEnded != nil: // guard:decide-context-first
		ce := f.ContextEnded
		if ce.Stage != "" {
			o.Phase = ce.Stage
		}
		if ce.Kind == "cancelled" {
			if ce.Shutdown {
				return set("launch-cancelled", "orchid-shutdown")
			}
			return set("launch-cancelled", "orchid-cancel")
		}
		switch o.Phase {
		case stageNames[stageRegistration]:
			return set("startup-timeout", "orchid-deadline")
		case stageNames[stageGoalDelivery], stageNames[stageGoalConfirmation]:
			return set("goal-unconfirmed", "orchid-deadline")
		}
		return set("deadline-exceeded", "orchid-deadline")
	case f.HerdrStartError != "":
		o.Phase = stageNames[stageRegistration]
		switch f.HerdrStartError {
		case "timeout":
			return set("startup-timeout", "herdr-structured-error")
		case "agent_not_ready":
			return set("startup-blocked", "herdr-structured-error")
		default:
			return set("startup-busy", "herdr-structured-error")
		}
	}
	return set("progress-unconfirmed", "orchid-progress")
}

// interruptedOutcome is the recovery decision for an undecided attempt of a dead
// producer instance: never relaunched, and no original reason is invented.
func interruptedOutcome(p attemptProgress, observedAt string) launchOutcome {
	o := decideOutcome(attemptProgress{SchemaVersion: p.SchemaVersion, AttemptID: p.AttemptID, ActionDispatchID: p.ActionDispatchID,
		DispatchID: p.DispatchID, PredecessorAttemptID: p.PredecessorAttemptID, RetryOperationID: p.RetryOperationID,
		Issue: p.Issue, ResolvedRoute: p.ResolvedRoute, Stage: p.Stage}, errMatrix, observedAt)
	o.Evidence, o.ReasonCode, o.Signal = p.Facts.evidence(), "launch-interrupted", "orchid-recovery"
	return o
}

// finish takes the one terminal decision, makes it durable, then publishes it.
func (t *launchAttempt) finish(parent context.Context, launchErr error) {
	if t == nil {
		return
	}
	if launchErr != nil {
		t.contextEnded(parent)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.tracked || t.decided {
		return
	}
	at := stamp(t.now())
	body, err := json.Marshal(decideOutcome(t.p, launchErr, at))
	if err != nil {
		return
	}
	t.decided = true
	t.p.Decision = &attemptDecision{Bytes: string(body), ObservedAt: at}
	t.settleLocked()
}

// settleLocked makes the frozen decision durable (keeping it in memory if the
// write fails), then publishes it once and retires the progress record. A
// durable decision that fails to publish is completed by recovery.
func (t *launchAttempt) settleLocked() {
	if !t.durable {
		t.p.UpdatedAt = t.p.Decision.ObservedAt                                                      // frozen: retries write identical bytes
		if writeProgressRecord(attemptFile(t.root, "attempt", t.p.AttemptID), t.owner, t.p) != nil { // guard:decision-durable
			log.Printf("launch attempt decision not yet durable; retrying")
			return
		}
		t.durable = true
	}
	_ = completeDecision(t.root, t.owner, t.publish, t.p)
}

// pending reports work left for the heartbeat: an admission not yet published,
// or a decision not yet durable.
func (t *launchAttempt) pending() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tracked && ((t.publish && !t.admitted) || (t.decided && !t.durable))
}

// retry re-attempts pending publication without re-deciding anything.
func (t *launchAttempt) retry() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.admitLocked()
	if t.decided && !t.durable {
		t.settleLocked()
	}
}

var errOutcomeConflict = errors.New("launch-outcome-conflict")

// syncAttemptDir makes the attempt directory's entries durable (a seam for
// failure tests).
var syncAttemptDir = syncDirectory

// publishRecord publishes one consumer record (a seam for failure tests).
var publishRecord = publishOnce

// completeDecision publishes the frozen decision bytes once (when enabled) and
// only then retires the progress record.
func completeDecision(root string, owner *receiptOwner, publish bool, p attemptProgress) error {
	if p.Decision == nil {
		return errMatrix
	}
	if publish {
		if err := publishRecord(root, attemptFile(root, "outcome", p.AttemptID), []byte(p.Decision.Bytes), owner); err != nil { // guard:publish-before-retire
			if errors.Is(err, errOutcomeConflict) {
				log.Printf("launch outcome conflict: existing record kept")
			}
			return err
		}
	}
	if err := os.Remove(attemptFile(root, "attempt", p.AttemptID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDirectory(root)
}

// publishOnce exposes body at path exactly once: a hard link never replaces an
// existing record. An identical existing record is success; a different one is
// a conflict that keeps the existing bytes. The directory is synced either way.
func publishOnce(root, path string, body []byte, owner *receiptOwner) error {
	f, err := os.CreateTemp(root, ".publish-")
	if err != nil {
		return errMatrix
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || transferReceiptOwner(owner, f.Name()) != nil {
		return errMatrix
	}
	if err = os.Link(f.Name(), path); err != nil { // guard:publish-no-replace
		if !errors.Is(err, fs.ErrExist) {
			return errMatrix
		}
		st, statErr := os.Lstat(path)
		if statErr != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
			return errOutcomeConflict
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(existing, body) {
			return errOutcomeConflict
		}
	}
	return syncDirectory(root) // guard:publish-dir-sync
}

func readAttemptProgress(path string) (attemptProgress, bool) {
	var p attemptProgress
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 64*1024 {
		return p, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || decodeNativeJSON(raw, &p) != nil || p.SchemaVersion != 1 || !attemptIDPattern.MatchString(p.AttemptID) ||
		filepath.Base(path) != "attempt-"+shaText([]byte(p.AttemptID))+".json" {
		return p, false
	}
	return p, true
}

// recoverLaunchAttempts completes publication for decided attempts and decides
// undecided attempts of a dead instance. It never reruns launch work.
func recoverLaunchAttempts(root string, owner *receiptOwner, publish bool, instance string, now time.Time) {
	paths, _ := filepath.Glob(filepath.Join(root, "attempt-*.json"))
	for _, path := range paths {
		p, ok := readAttemptProgress(path)
		if !ok {
			continue
		}
		if p.Decision == nil && p.Instance == instance { // guard:recover-live-skip
			continue // live work of this instance
		}
		if publish {
			if body, err := admissionBytes(p); err == nil {
				_ = publishRecord(root, attemptFile(root, "admitted", p.AttemptID), body, owner) // identical bytes, or the kept record
			}
		}
		if p.Decision != nil {
			// A visible decision may come from a write whose directory sync failed:
			// make it durable before anything is published from it.
			if syncAttemptDir(root) != nil { // guard:recover-sync-before-publish
				continue
			}
		}
		if p.Decision == nil {
			at := stamp(now)
			body, err := json.Marshal(interruptedOutcome(p, at))
			if err != nil {
				continue
			}
			p.Decision = &attemptDecision{Bytes: string(body), ObservedAt: at}
			if writeProgressRecord(path, owner, p) != nil { // guard:recover-decision-durable
				continue
			}
		}
		_ = completeDecision(root, owner, publish, p)
	}
}

// ============================ producer heartbeat ============================

type producerHeartbeat struct {
	Instance   string `json:"instance"`
	StartedAt  string `json:"startedAt"`
	ObservedAt string `json:"observedAt"`
	Stopping   bool   `json:"stopping"`
}

const heartbeatEvery = 10 * time.Second
const heartbeatFresh = 30 * time.Second

func newInstanceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// heartbeatLoop runs independently of ticks and launches; it also drives
// publication-only recovery.
func (c *Coord) heartbeatLoop(ctx context.Context, every <-chan time.Time) {
	root := c.cfg.Matrix.ReceiptRoot
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err != nil || !privateReceiptRoot(root) {
		return
	}
	c.beat(false)
	recoverLaunchAttempts(root, owner, c.cfg.Matrix.LaunchOutcomes, c.instance, time.Now())
	for {
		select {
		case <-ctx.Done():
			c.beat(true)
			return
		case <-every:
			c.beat(false)
			c.retryAttempts()
			recoverLaunchAttempts(root, owner, c.cfg.Matrix.LaunchOutcomes, c.instance, time.Now())
		}
	}
}

// beat writes the heartbeat. Once stopping is marked it stays marked, so a late
// regular beat can never make a stopping producer look live again.
func (c *Coord) beat(stopping bool) {
	root := c.cfg.Matrix.ReceiptRoot
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err != nil || !privateReceiptRoot(root) {
		return
	}
	c.beatMu.Lock()
	defer c.beatMu.Unlock()
	if stopping {
		c.stopping = true
	}
	if c.beatStarted == "" {
		c.beatStarted = stamp(time.Now())
	}
	_ = writePrivateJSON(filepath.Join(root, "producer.json"), ".producer-", owner,
		producerHeartbeat{Instance: c.instance, StartedAt: c.beatStarted, ObservedAt: stamp(time.Now()), Stopping: c.stopping}) // guard:stopping-sticky
}

// markStopping records the shutdown before any launch is cancelled, so the
// snapshot reads unknown (never zero) while launches unwind.
func (c *Coord) markStopping() {
	if c != nil && !c.dry {
		c.beat(true)
	}
}

// ============================ live attempts and the governor ============================

// newMatrixAttempt binds the tracker to the native reservation of this launch.
func (c *Coord) newMatrixAttempt(cfg MatrixConfig, owner *receiptOwner, key string, is Issue, n int, route matrixRoute, agent string, retry *retryExpectation) *launchAttempt {
	if c.dry || !privateReceiptRoot(cfg.ReceiptRoot) || !digestPattern.MatchString(key) {
		return nil
	}
	return newLaunchAttempt(cfg.ReceiptRoot, owner, cfg.LaunchOutcomes, c.instance, key, launchStateDispatchID(c.cfg.Inbox, is),
		dispatchIssue{Repo: c.cfg.Inbox, Number: n}, outcomeRoute{Harness: route.Transport, Model: route.Model, Effort: route.Effort},
		strings.HasSuffix(agent, "-run"), retry, time.Now)
}

func (c *Coord) trackAttempt(t *launchAttempt, account string) {
	if t == nil {
		return
	}
	t.account = account
	c.attemptsMu.Lock()
	defer c.attemptsMu.Unlock()
	if c.attempts == nil {
		c.attempts = map[string]*launchAttempt{}
	}
	c.attempts[t.key] = t
}

// untrackAttempt ends live tracking at the launch's terminal return: a finished
// attempt is never active work, even while its publication is still pending.
func (c *Coord) untrackAttempt(t *launchAttempt) {
	if t == nil {
		return
	}
	c.attemptsMu.Lock()
	defer c.attemptsMu.Unlock()
	delete(c.attempts, t.key) // guard:untrack-at-return
	if t.pending() {
		if c.unsettled == nil {
			c.unsettled = map[*launchAttempt]bool{}
		}
		c.unsettled[t] = true
	}
}

// retryAttempts retries pending admissions of live attempts and pending
// decisions of finished ones, from memory.
func (c *Coord) retryAttempts() {
	c.attemptsMu.Lock()
	work := []*launchAttempt{}
	for _, t := range c.attempts {
		work = append(work, t)
	}
	for t := range c.unsettled {
		work = append(work, t)
	}
	c.attemptsMu.Unlock()
	for _, t := range work {
		t.retry()
	}
	c.attemptsMu.Lock()
	for t := range c.unsettled {
		if !t.pending() {
			delete(c.unsettled, t)
		}
	}
	c.attemptsMu.Unlock()
}

// activeByAccount counts registered jobs plus live in-flight attempts, once per
// reservation key. Callers hold c.st.mu.
func (c *Coord) activeByAccount() map[string]int {
	active := map[string]int{}
	jobs := map[string]bool{}
	for _, j := range c.st.Jobs {
		active[accountKey(j.Agent)]++
		if j.DispatchKey != "" {
			jobs[j.DispatchKey] = true
		}
	}
	c.attemptsMu.Lock()
	defer c.attemptsMu.Unlock()
	for key, t := range c.attempts {
		if !jobs[key] { // guard:active-dedupe
			active[t.account]++
		}
	}
	return active
}

// ============================ read-only snapshot ============================

type launchSnapshot struct {
	Producer   string          `json:"producer"` // live | unknown
	InFlight   []snapshotEntry `json:"inFlight"`
	Backlog    int             `json:"backlog"`
	Unresolved int             `json:"unresolved"`
	Untracked  []int           `json:"untracked"`
	// Invalid counts unreadable or malformed records. Any invalid evidence makes
	// the snapshot unknown: an incomplete observation never certifies zero work.
	Invalid int `json:"invalid"`
	// UnknownReason is the one closed reason the snapshot cannot certify:
	// producer-not-live or evidence-invalid. Empty when it can.
	UnknownReason string `json:"unknownReason,omitempty"`
}

var instancePattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type snapshotEntry struct {
	Issue      int    `json:"issue"`
	Stage      string `json:"stage"`
	AgeSeconds int64  `json:"ageSeconds"`
}

var actionDispatchPattern = regexp.MustCompile(`^assignment_[a-f0-9]{64}$`)

// snapshotProgressValid is the operator snapshot's strict check of a progress
// record: a producer instance, its identity fields and, when decided, a
// complete frozen decision for this very attempt. It only shapes the operator
// readout; recovery and admission never use it.
func snapshotProgressValid(p attemptProgress) bool {
	if !instancePattern.MatchString(p.Instance) || !actionDispatchPattern.MatchString(p.ActionDispatchID) || p.Issue.Number < 1 ||
		!repositoryName.MatchString(p.Issue.Repo) {
		return false
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", p.StartedAt); err != nil {
		return false
	}
	if p.Decision == nil {
		return true
	}
	return frozenDecisionValid(p)
}

// outcomeTuples are the only outcome/effects/reason/signal combinations the
// reducer produces.
var outcomeTuples = map[[4]string]bool{
	{"started", "present", "started", "orchid-commit"}:                       true,
	{"started", "present", "started", "orchid-run"}:                          true,
	{"failed", "none", "codex-client-unmatched", "codex-app-server"}:         true,
	{"failed", "none", "codex-client-check-failed", "codex-client-check"}:    true,
	{"failed", "none", "agy-settings-unreadable", "agy-settings"}:            true,
	{"unconfirmed", "possible", "launch-cancelled", "orchid-shutdown"}:       true,
	{"unconfirmed", "possible", "launch-cancelled", "orchid-cancel"}:         true,
	{"unconfirmed", "possible", "startup-timeout", "orchid-deadline"}:        true,
	{"unconfirmed", "possible", "startup-timeout", "herdr-structured-error"}: true,
	{"unconfirmed", "possible", "goal-unconfirmed", "orchid-deadline"}:       true,
	{"unconfirmed", "possible", "deadline-exceeded", "orchid-deadline"}:      true,
	{"unconfirmed", "possible", "startup-blocked", "herdr-structured-error"}: true,
	{"unconfirmed", "possible", "startup-busy", "herdr-structured-error"}:    true,
	{"unconfirmed", "possible", "progress-unconfirmed", "orchid-progress"}:   true,
	{"unconfirmed", "possible", "launch-interrupted", "orchid-recovery"}:     true,
}

var outcomeEvidenceNames = map[string]bool{"herdrStartError": true, "contextEnded": true, "codexClientUnmatched": true,
	"codexClientCheckFailed": true, "agySettingsUnreadable": true, "registered": true, "goalCommitted": true, "runStarted": true}

func sameOptional(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// frozenDecisionValid accepts a frozen decision only when it is the complete,
// canonical outcome the reducer writes for this very attempt: every field
// present (re-encoding reproduces the bytes exactly), schema 1, the progress
// record's identity envelope, a known stage, sorted known evidence and a
// reducer-producible outcome tuple. Anything else is incomplete evidence.
func frozenDecisionValid(p attemptProgress) bool {
	var o launchOutcome
	if decodeNativeJSON([]byte(p.Decision.Bytes), &o) != nil { // guard:snapshot-decision-complete
		return false
	}
	if canonical, err := json.Marshal(o); err != nil || string(canonical) != p.Decision.Bytes { // guard:snapshot-decision-canonical
		return false
	}
	if o.SchemaVersion != 1 || o.AttemptID != p.AttemptID || o.ActionDispatchID != p.ActionDispatchID || o.DispatchID != p.DispatchID ||
		o.Issue != p.Issue || o.ResolvedRoute != p.ResolvedRoute || !sameOptional(o.PredecessorAttemptID, p.PredecessorAttemptID) ||
		!sameOptional(o.RetryOperationID, p.RetryOperationID) || o.ObservedAt != p.Decision.ObservedAt { // guard:snapshot-decision-envelope
		return false
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", o.ObservedAt); err != nil {
		return false
	}
	phase := false
	for _, name := range stageNames {
		phase = phase || o.Phase == name
	}
	if !phase || !outcomeTuples[[4]string{o.Outcome, o.Effects, o.ReasonCode, o.Signal}] { // guard:snapshot-decision-vocabulary
		return false
	}
	for i, e := range o.Evidence {
		if !outcomeEvidenceNames[e] || (i > 0 && o.Evidence[i-1] >= e) { // guard:snapshot-decision-evidence
			return false
		}
	}
	return true
}

// snapshotDispatch strictly reads one reservation's dispatch record: no
// duplicate keys, the reservation's own run identity, a valid issue and a known
// state, before its state is trusted.
func snapshotDispatch(root, key string) (dispatchBinding, bool) {
	var d dispatchBinding
	raw, err := os.ReadFile(filepath.Join(root, key, "record", "dispatch.json"))
	if err != nil || len(raw) > 64*1024 || decodeNativeJSON(raw, &d) != nil { // guard:snapshot-dispatch-strict
		return d, false
	}
	if d.SchemaVersion != 1 || d.RunID != "orchid-"+key || !repositoryName.MatchString(d.Issue.Repo) || d.Issue.Number < 1 { // guard:snapshot-dispatch-identity
		return d, false
	}
	switch d.State {
	case "reserved", "launching", "dispatched", "uncertain":
		return d, true
	}
	return d, false
}

// readLaunchSnapshot is a read-only snapshot, not a lock: a zero cannot stop the
// next launch. A stale or stopping producer is unknown, never zero.
func readLaunchSnapshot(root string, now time.Time) launchSnapshot {
	s := launchSnapshot{Producer: "unknown", InFlight: []snapshotEntry{}, Untracked: []int{}}
	var hb producerHeartbeat
	live := false
	if raw, err := os.ReadFile(filepath.Join(root, "producer.json")); err == nil && decodeNativeJSON(raw, &hb) == nil && instancePattern.MatchString(hb.Instance) { // guard:snapshot-producer-identity
		if at, err := time.Parse("2006-01-02T15:04:05.000Z", hb.ObservedAt); err == nil && !hb.Stopping && now.Sub(at) < heartbeatFresh && now.Sub(at) > -heartbeatFresh { // guard:snapshot-fresh
			live = true
			s.Producer = "live"
		}
	}
	// One traversal that keeps read errors: an unreadable store or reservation
	// is unknown evidence, never an empty one.
	entries, err := os.ReadDir(root)
	if err != nil { // guard:snapshot-traversal
		s.Invalid++
	}
	tracked := map[string]bool{}
	var reservations []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "attempt-") && strings.HasSuffix(name, ".json"):
			p, ok := readAttemptProgress(filepath.Join(root, name))
			if !ok || !snapshotProgressValid(p) { // guard:snapshot-invalid-attempt
				s.Invalid++
				continue
			}
			tracked[p.ActionDispatchID] = true
			switch {
			case p.Decision != nil:
				s.Backlog++
			case live && p.Instance == hb.Instance:
				age := int64(0)
				if at, err := time.Parse("2006-01-02T15:04:05.000Z", p.StartedAt); err == nil {
					age = int64(now.Sub(at) / time.Second)
				}
				s.InFlight = append(s.InFlight, snapshotEntry{Issue: p.Issue.Number, Stage: p.Stage, AgeSeconds: age})
			default:
				s.Unresolved++
			}
		case digestPattern.MatchString(name):
			reservations = append(reservations, name)
		case e.IsDir():
			// Not a reservation name: it must not hold a reservation record.
			if _, err := os.Lstat(filepath.Join(root, name, "record")); !errors.Is(err, fs.ErrNotExist) { // guard:snapshot-foreign-record
				s.Invalid++
			}
		}
	}
	// Reserved/launching receipts without a progress record are untracked attempts.
	for _, key := range reservations {
		d, ok := snapshotDispatch(root, key)
		if !ok { // guard:snapshot-invalid-dispatch
			s.Invalid++
			continue
		}
		if d.State != "reserved" && d.State != "launching" {
			continue
		}
		if !tracked[actionOpaque("assignment", d.RunID)] {
			s.Untracked = append(s.Untracked, d.Issue.Number)
		}
	}
	sort.Ints(s.Untracked)
	sort.Slice(s.InFlight, func(i, j int) bool { return s.InFlight[i].Issue < s.InFlight[j].Issue })
	switch {
	case s.Producer != "live":
		s.UnknownReason = "producer-not-live"
	case s.Invalid > 0:
		s.UnknownReason = "evidence-invalid"
	}
	return s
}

// launchesCLI prints the read-only launch snapshot. Exit 0: producer live and
// nothing in flight; 3: launches in flight or untracked; 2: producer unknown or
// the store is unreadable (treat as in flight before any restart).
func launchesCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("launches", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "divybot.json", "path to config json")
	if fs.Parse(args) != nil {
		return 2
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil || !privateReceiptRoot(cfg.Matrix.ReceiptRoot) {
		fmt.Fprintln(stderr, "launches: receipt root unavailable")
		return 2
	}
	s := readLaunchSnapshot(cfg.Matrix.ReceiptRoot, time.Now())
	out := struct {
		launchSnapshot
		Registered int `json:"registered"`
	}{s, len(loadState(cfg.StateFile).Jobs)}
	body, _ := json.Marshal(out)
	fmt.Fprintln(stdout, string(body))
	return snapshotExit(s)
}

// snapshotExit is the CLI's verdict: 0 only for a live producer, valid evidence
// and nothing in flight.
func snapshotExit(s launchSnapshot) int {
	switch {
	case s.Producer != "live" || s.Invalid > 0 || s.UnknownReason != "": // guard:cli-invalid-unknown
		return 2
	case len(s.InFlight) > 0 || len(s.Untracked) > 0:
		return 3
	}
	return 0
}
