package main

import (
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---- the pure reducer ----

func TestAcceptanceSignalReducerTable(t *testing.T) {
	const gen, thread, other = "gen-a", "thread-a", "thread-b"
	turn := func(id string, seq int64) nativeSignal {
		return nativeSignal{Generation: gen, Thread: thread, Kind: signalTurn, Method: "turn/started", Turn: id, Status: "inProgress", Seq: seq}
	}
	for _, tc := range []struct {
		name   string
		begin  int64 // receive sequence at submit start; -1 = not submitted
		ack    int64 // receive sequence at acknowledgment; -1 = in flight
		prior  []nativeSignal
		sig    nativeSignal
		class  signalClass
		reason string
	}{
		{"stale-generation", 0, 0, nil, nativeSignal{Generation: "gen-old", Thread: thread, Kind: signalTurn, Turn: "t1", Seq: 5}, "", "stale-generation"},
		{"foreign-thread", 0, 0, nil, nativeSignal{Generation: gen, Thread: other, Kind: signalTurn, Turn: "t1", Seq: 5}, classForeign, ""},
		{"pre-submit-turn", -1, -1, nil, turn("t1", 1), "", "baseline-active"},
		{"pre-submit-active", -1, -1, nil, nativeSignal{Generation: gen, Thread: thread, Kind: signalReady, Status: "active", Seq: 1}, "", "baseline-active"},
		{"pre-submit-idle", -1, -1, nil, nativeSignal{Generation: gen, Thread: thread, Kind: signalReady, Status: "idle", Seq: 1}, classInOrder, ""},
		{"pre-submit-approval", -1, -1, nil, nativeSignal{Generation: gen, Thread: thread, Kind: signalApproval, Method: "item/commandExecution/requestApproval", Seq: 1}, "", "baseline-active"},
		{"received-before-submit", 4, 9, nil, turn("t1", 4), "", "baseline-active"},
		{"ahead-of-ack", 4, -1, nil, turn("t1", 5), classAheadOfAck, ""},
		{"ahead-of-ack-processed-late", 4, 9, nil, turn("t1", 7), classAheadOfAck, ""},
		{"after-ack", 4, 9, nil, turn("t1", 10), classInOrder, ""},
		{"duplicate", 4, 9, []nativeSignal{turn("t1", 10)}, turn("t1", 11), classDuplicate, ""},
		{"second-turn", 4, 9, []nativeSignal{turn("t1", 10)}, turn("t2", 11), "", "contradiction"},
		{"item-other-turn", 4, 9, []nativeSignal{turn("t1", 10)}, nativeSignal{Generation: gen, Thread: thread, Kind: signalItem, Method: "item/started", Turn: "t2", Item: "userMessage", Seq: 11}, "", "contradiction"},
		{"native-exit", 4, 9, nil, nativeSignal{Generation: gen, Thread: thread, Kind: signalExit, Method: "thread/closed", Seq: 10}, "", "native-exit"},
		{"approval-observed", 4, 9, nil, nativeSignal{Generation: gen, Thread: thread, Kind: signalApproval, Method: "item/commandExecution/requestApproval", Turn: "t1", Seq: 10}, classInOrder, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAcceptanceSignals(gen, thread)
			if tc.begin >= 0 {
				s.begin(tc.begin)
			}
			if tc.ack >= 0 {
				s.ack(tc.ack)
			}
			for _, p := range tc.prior {
				if _, err := s.admit(p); err != nil {
					t.Fatalf("prior signal refused: %v", err)
				}
			}
			class, err := s.admit(tc.sig)
			if tc.reason != "" {
				if err == nil || err.Error() != "delivery-unconfirmed-"+tc.reason {
					t.Fatalf("refusal = %v, want %s", err, tc.reason)
				}
				return
			}
			if err != nil || class != tc.class {
				t.Fatalf("class = %q err = %v, want %q", class, err, tc.class)
			}
		})
	}
	t.Run("duplicate-counts-no-turn", func(t *testing.T) {
		s := newAcceptanceSignals(gen, thread)
		s.begin(0)
		s.ack(0)
		for i := int64(1); i <= 3; i++ {
			if _, err := s.admit(turn("t1", i)); err != nil {
				t.Fatal(err)
			}
		}
		if s.duplicates != 2 || s.firstTurn != "t1" || !s.candidate("t1") || s.candidate("t2") {
			t.Fatalf("duplicates=%d first=%q", s.duplicates, s.firstTurn)
		}
	})
	t.Run("item-materializes", func(t *testing.T) {
		s := newAcceptanceSignals(gen, thread)
		s.begin(0)
		s.ack(0)
		item := nativeSignal{Generation: gen, Thread: thread, Kind: signalItem, Method: "item/started", Turn: "t1", Item: "userMessage", Seq: 1}
		if _, err := s.admit(item); err != nil || !s.materialize["t1"] || !wakes(item) {
			t.Fatalf("user item not recorded: %v", err)
		}
		if wakes(nativeSignal{Kind: signalItem, Item: "agentMessage"}) || wakes(nativeSignal{Kind: signalApproval}) {
			t.Fatal("non-decision signals must not wake a readback")
		}
	})
}

func TestAcceptanceSignalDecoding(t *testing.T) {
	th := "11111111-1111-4111-8111-111111111111"
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	for _, tc := range []struct {
		name   string
		method string
		params any
		kind   signalKind
		signal bool
		gap    bool
	}{
		{"status-idle", "thread/status/changed", map[string]any{"threadId": th, "status": map[string]any{"type": "idle"}}, signalReady, true, false},
		{"status-not-loaded", "thread/status/changed", map[string]any{"threadId": th, "status": map[string]any{"type": "notLoaded"}}, signalExit, true, false},
		{"status-system-error", "thread/status/changed", map[string]any{"threadId": th, "status": map[string]any{"type": "systemError"}}, signalExit, true, false},
		{"status-unknown", "thread/status/changed", map[string]any{"threadId": th, "status": map[string]any{"type": "paused"}}, "", true, true},
		{"status-missing", "thread/status/changed", map[string]any{"threadId": th}, "", true, true},
		{"closed", "thread/closed", map[string]any{"threadId": th}, signalExit, true, false},
		{"item", "item/started", map[string]any{"threadId": th, "turnId": th, "item": map[string]any{"type": "userMessage"}, "startedAtMs": 1}, signalItem, true, false},
		{"item-no-turn", "item/completed", map[string]any{"threadId": th, "item": map[string]any{"type": "userMessage"}}, "", true, true},
		{"item-no-type", "item/completed", map[string]any{"threadId": th, "turnId": th, "item": map[string]any{}}, "", true, true},
		{"resolved", "serverRequest/resolved", map[string]any{"threadId": th, "requestId": 3}, signalApproval, true, false},
		{"no-thread", "thread/closed", map[string]any{}, "", true, true},
		{"not-a-signal", "item/agentMessage/delta", map[string]any{"threadId": th}, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sig, ok, err := decodeSignal(tc.method, raw(tc.params))
			if ok != tc.signal || (err != nil) != tc.gap || !tc.gap && ok && sig.Kind != tc.kind {
				t.Fatalf("decode = %+v ok=%v err=%v", sig, ok, err)
			}
		})
	}
	if sig, ok := decodeServerRequest("item/commandExecution/requestApproval", raw(map[string]any{"threadId": th, "turnId": th, "itemId": "x"})); !ok || sig.Kind != signalApproval || sig.Thread != th {
		t.Fatal("thread-bound approval request not observed")
	}
	if _, ok := decodeServerRequest("account/chatgptAuthTokens/refresh", raw(map[string]any{"reason": "x"})); ok {
		t.Fatal("a request naming no thread is not an acceptance signal")
	}
}

// ---- end to end over the real bridge and AF_UNIX fixture ----

func accRecords(h *accHarness, kind string) []journalRecord {
	var out []journalRecord
	for _, r := range h.journal() {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func accHasClass(h *accHarness, kind, class string) bool {
	for _, r := range accRecords(h, kind) {
		if r.Class == class {
			return true
		}
	}
	return false
}

// The acceptance connection subscribes through the official rejoin, between
// two idle reads and before the baseline page, opting out of streaming deltas.
func TestAcceptanceOfficialSubscriptionOrder(t *testing.T) {
	h := newAccHarness(t)
	accAccepted(t, h, h.accept(accSent))
	got := h.f.requested()
	want := []string{"initialize", "thread/read", "thread/read", "thread/resume", "thread/read", "thread/turns/list"}
	if len(got) < len(want) || !reflect.DeepEqual(got[:len(want)], want) {
		t.Fatalf("request order = %v", got)
	}
	if h.f.resumeParams["excludeTurns"] != true || h.f.resumeParams["threadId"] != h.f.run.NativeSessionID || len(h.f.resumeParams) != 2 {
		t.Fatalf("resume must be the bare official rejoin: %v", h.f.resumeParams)
	}
	caps, _ := h.f.initParams["capabilities"].(map[string]any)
	optOut, _ := caps["optOutNotificationMethods"].([]any)
	if len(optOut) != len(acceptanceOptOut) || caps["experimentalApi"] != true {
		t.Fatalf("initialize capabilities = %v", caps)
	}
	if !accHasClass(h, "subscription", "") || accRecords(h, "subscription")[0].Outcome != "subscribed" {
		t.Fatal("subscription not journalled")
	}
}

// Missing capability: a refused rejoin keeps bounded polling, never a gap.
func TestAcceptanceSubscriptionRefusedKeepsPolling(t *testing.T) {
	h := newAccHarness(t)
	h.f.resumeRefused = true
	accAccepted(t, h, h.accept(accSent))
	subs := accRecords(h, "subscription")
	if len(subs) != 1 || subs[0].Outcome != codexResumeRefused {
		t.Fatalf("missing capability not typed: %+v", subs)
	}
}

// Lost subscription acknowledgment: no effect, no submit.
func TestAcceptanceLostSubscriptionAckIsPreEffect(t *testing.T) {
	h := newAccHarness(t)
	h.f.reply = func(f *accFixture, method string, _ map[string]any, _ int) *accReply {
		if method == "thread/resume" {
			return &accReply{drop: true}
		}
		return nil
	}
	accRefused(t, h, h.accept(accSent), "pre-effect")
	if h.submits != 0 || h.receipt().Reason != "source-gap" {
		t.Fatalf("lost subscribe ack: submits=%d reason=%s", h.submits, h.receipt().Reason)
	}
}

func accStartedNotice(f *accFixture) map[string]any {
	return accNotice("turn/started", f.run.NativeSessionID, accNewTurn, "inProgress")
}

// Native acceptance arriving before the submit's own reply is journalled in
// order as ahead-of-ack, and the readback then accepts.
func TestAcceptanceNativeAheadOfSubmitAck(t *testing.T) {
	h := newAccHarness(t)
	h.onSubmit = func() {
		h.f.push(accStartedNotice(h.f))
		time.Sleep(200 * time.Millisecond) // the reply is still outstanding
	}
	accAccepted(t, h, h.accept(accSent))
	if !accHasClass(h, "notice", string(classAheadOfAck)) {
		t.Fatalf("early native acceptance not classified ahead-of-ack: %+v", accRecords(h, "notice"))
	}
}

// Early native evidence never upgrades a lost submit acknowledgment.
func TestAcceptanceAheadOfAckThenLostAck(t *testing.T) {
	h := newAccHarness(t)
	h.submitErr = errors.New("lost")
	h.onSubmit = func() {
		h.f.push(accStartedNotice(h.f))
		time.Sleep(200 * time.Millisecond)
	}
	accRefused(t, h, h.accept(accSent), "submit-uncertain")
	if h.submits != 1 || !accHasClass(h, "notice", string(classAheadOfAck)) || h.receipt().Reason != "submit-uncertain" {
		t.Fatalf("lost ack after early evidence: submits=%d reason=%s", h.submits, h.receipt().Reason)
	}
}

// Native acceptance ahead of the readback is a typed pending state.
func TestAcceptanceNoticeAheadOfReadback(t *testing.T) {
	h := newAccHarness(t)
	h.f.page = func(f *accFixture, i int) ([]any, any) {
		if i == 0 {
			saved := f.getSent()
			f.setSent("")
			defer f.setSent(saved)
		}
		return accDefaultPage(f, i)
	}
	h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
		if full == 0 {
			return &accReply{pre: []any{accStartedNotice(f)}}
		}
		return nil
	}
	accAccepted(t, h, h.accept(accSent))
	if !accHasClass(h, "pending", pendingReadback) {
		t.Fatalf("pending classes: %+v", accRecords(h, "pending"))
	}
}

// Delayed item materialization: the user item signal wakes the readback.
func TestAcceptanceItemSignalWakesMaterializingTurn(t *testing.T) {
	h := newAccHarness(t)
	h.acc.every = 5 * time.Second // only a wake can end the wait early
	clock := newLaunchStageClock(nil)
	h.host.Stages = clock
	h.f.page = func(f *accFixture, i int) ([]any, any) {
		turns, cursor := accDefaultPage(f, i)
		if i == 0 && f.getSent() != "" {
			turns[0] = accTurn(accNewTurn, "inProgress")
		}
		return turns, cursor
	}
	h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
		if full == 0 {
			return &accReply{after: func(c net.Conn) {
				time.Sleep(50 * time.Millisecond)
				f.push(map[string]any{"method": "item/started", "params": map[string]any{"threadId": f.run.NativeSessionID, "turnId": accNewTurn,
					"item": accUser(f.getSent()), "startedAtMs": 1}})
			}}
		}
		return nil
	}
	accAccepted(t, h, h.accept(accSent))
	if !accHasClass(h, "pending", pendingMaterializing) || clock.counts[stageCountWakes] < 1 {
		t.Fatalf("materializing wake: pending=%+v wakes=%d", accRecords(h, "pending"), clock.counts[stageCountWakes])
	}
}

// Duplicate notifications are journalled as duplicates and count no turn.
func TestAcceptanceDuplicateNotifications(t *testing.T) {
	h := newAccHarness(t)
	h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
		if full == 0 {
			return &accReply{pre: []any{accStartedNotice(f), accStartedNotice(f), accStartedNotice(f)}}
		}
		return nil
	}
	accAccepted(t, h, h.accept(accSent))
	dups := 0
	for _, r := range accRecords(h, "notice") {
		if r.Class == string(classDuplicate) {
			dups++
		}
	}
	if dups != 2 {
		t.Fatalf("duplicate notices = %d, want 2", dups)
	}
}

// A changed consumer or reused process identity before the effect: zero submits.
func TestAcceptancePreEffectBindingTyped(t *testing.T) {
	for _, tc := range []struct {
		outcome attachmentOutcome
		reason  string
	}{
		{attachmentConsumerChanged, "binding-changed"},
		{attachmentProcessReused, "process-reused"},
		{attachmentUnreadable, "binding-changed"},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			h := newAccHarness(t)
			h.preBinding = func() error { return attachmentRefused(tc.outcome) }
			accRefused(t, h, h.accept(accSent), "pre-effect")
			if h.submits != 0 || h.receipt().Reason != tc.reason || !accHasClass(h, "binding", string(tc.outcome)) {
				t.Fatalf("submits=%d reason=%s", h.submits, h.receipt().Reason)
			}
		})
	}
}

// After the effect a reused process identity is typed and never resent.
func TestAcceptancePostEffectProcessReused(t *testing.T) {
	h := newAccHarness(t)
	h.binding = func(call int) error { return attachmentRefused(attachmentProcessReused) }
	accRefused(t, h, h.accept(accSent), "process-reused")
	if h.submits != 1 {
		t.Fatalf("submits = %d", h.submits)
	}
}

// A turn signal before the effect means another writer: zero submits.
func TestAcceptancePreSubmitTurnSignal(t *testing.T) {
	h := newAccHarness(t)
	h.f.reply = func(f *accFixture, method string, params map[string]any, full int) *accReply {
		if method == "thread/turns/list" && params["itemsView"] == "notLoaded" {
			return &accReply{pre: []any{accNotice("turn/started", f.run.NativeSessionID, "turn-foreign", "inProgress")}}
		}
		return nil
	}
	accRefused(t, h, h.accept(accSent), "pre-effect")
	if h.submits != 0 || h.receipt().Reason != "baseline-active" {
		t.Fatalf("submits=%d reason=%s", h.submits, h.receipt().Reason)
	}
}

// Native exit of the bound thread after the effect is typed; no resend.
func TestAcceptanceNativeExitAfterEffect(t *testing.T) {
	h := newAccHarness(t)
	h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
		if full == 0 {
			return &accReply{pre: []any{map[string]any{"method": "thread/closed", "params": map[string]any{"threadId": f.run.NativeSessionID}}}}
		}
		return nil
	}
	accRefused(t, h, h.accept(accSent), "native-exit")
	if h.submits != 1 {
		t.Fatalf("submits = %d", h.submits)
	}
}

// Approval requests are observed and never answered; foreign ones ignored.
func TestAcceptanceApprovalNeverAnswered(t *testing.T) {
	h := newAccHarness(t)
	h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
		if full == 0 {
			return &accReply{pre: []any{
				map[string]any{"id": 900, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": f.run.NativeSessionID, "turnId": accNewTurn, "itemId": "item-1"}},
				map[string]any{"id": 901, "method": "item/fileChange/requestApproval", "params": map[string]any{"threadId": privateTestID(t), "turnId": "t", "itemId": "i"}},
			}}
		}
		return nil
	}
	accAccepted(t, h, h.accept(accSent))
	h.f.mu.Lock()
	replies := h.f.replies
	h.f.mu.Unlock()
	if replies != 0 {
		t.Fatalf("Orchid answered %d server requests", replies)
	}
	approvals := 0
	for _, r := range accRecords(h, "signal") {
		if strings.HasSuffix(r.Method, "requestApproval") {
			approvals++
		}
	}
	if approvals != 1 {
		t.Fatalf("bound approval journalled %d times", approvals)
	}
}

// Transport loss while waiting for a wake is a source gap after the effect.
func TestAcceptanceSourceLostDuringWake(t *testing.T) {
	h := newAccHarness(t)
	h.f.page = func(f *accFixture, i int) ([]any, any) {
		saved := f.getSent()
		f.setSent("")
		defer f.setSent(saved)
		return accDefaultPage(f, i)
	}
	h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
		if full == 0 {
			return &accReply{after: func(c net.Conn) { _ = c.Close() }}
		}
		return nil
	}
	accRefused(t, h, h.accept(accSent), "")
	if h.submits != 1 || h.receipt().State != "unconfirmed" {
		t.Fatalf("submits=%d state=%s", h.submits, h.receipt().State)
	}
}
