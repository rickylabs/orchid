package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// One row per validator rule on the acceptance path: missing, empty,
// malformed or truncated input is refused, and a valid input is accepted.
// Each row isolates one check, so removing that check turns the row RED.

func rulesTurn() map[string]any {
	return map[string]any{"id": "turn-a", "status": "inProgress", "itemsView": "full", "error": nil,
		"items": []any{map[string]any{"type": "userMessage", "id": "u", "content": []any{map[string]any{"type": "text", "text": "exact"}}}}}
}

func rulesPage(turns ...any) map[string]any {
	if turns == nil {
		turns = []any{rulesTurn()}
	}
	return map[string]any{"data": turns, "nextCursor": nil}
}

func rulesJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func TestAcceptanceValidatorHelpers(t *testing.T) {
	rows := []struct {
		name string
		ok   bool
		got  bool
	}{
		{"object-ok", true, func() bool { _, ok := nativeObject(json.RawMessage(`{"a":1}`), "a"); return ok }()},
		{"object-missing-key", false, func() bool { _, ok := nativeObject(json.RawMessage(`{"a":1}`), "a", "b"); return ok }()},
		{"object-not-object", false, func() bool { _, ok := nativeObject(json.RawMessage(`[1]`)); return ok }()},
		{"object-null", false, func() bool { _, ok := nativeObject(json.RawMessage(`null`)); return ok }()},
		{"object-truncated", false, func() bool { _, ok := nativeObject(json.RawMessage(`{"a":`)); return ok }()},
		{"object-duplicate-key", false, func() bool { _, ok := nativeObject(json.RawMessage(`{"a":1,"a":2}`), "a"); return ok }()},
		{"text-ok", true, func() bool { _, ok := nativeText(json.RawMessage(`"x"`), false); return ok }()},
		{"text-empty-allowed", true, func() bool { _, ok := nativeText(json.RawMessage(`""`), true); return ok }()},
		{"text-empty-refused", false, func() bool { _, ok := nativeText(json.RawMessage(`""`), false); return ok }()},
		{"text-number", false, func() bool { _, ok := nativeText(json.RawMessage(`5`), true); return ok }()},
		{"text-null", false, func() bool { _, ok := nativeText(json.RawMessage(`null`), true); return ok }()},
		{"text-absent", false, func() bool { _, ok := nativeText(nil, true); return ok }()},
		{"array-ok", true, func() bool { _, ok := nativeArray(json.RawMessage(`[]`)); return ok }()},
		{"array-object", false, func() bool { _, ok := nativeArray(json.RawMessage(`{}`)); return ok }()},
		{"array-null", false, func() bool { _, ok := nativeArray(json.RawMessage(`null`)); return ok }()},
		{"array-absent", false, func() bool { _, ok := nativeArray(nil); return ok }()},
	}
	for _, r := range rows {
		if r.got != r.ok {
			t.Errorf("%s: got %v, want %v", r.name, r.got, r.ok)
		}
	}
}

func TestAcceptancePageRules(t *testing.T) {
	type row struct {
		name string
		page func() map[string]any
		ok   bool
	}
	withTurn := func(change func(map[string]any)) func() map[string]any {
		return func() map[string]any {
			turn := rulesTurn()
			change(turn)
			return rulesPage(turn)
		}
	}
	withItem := func(change func(map[string]any)) func() map[string]any {
		return withTurn(func(turn map[string]any) { change(turn["items"].([]any)[0].(map[string]any)) })
	}
	withPart := func(change func(map[string]any)) func() map[string]any {
		return withItem(func(item map[string]any) { change(item["content"].([]any)[0].(map[string]any)) })
	}
	rows := []row{
		{"valid", func() map[string]any { return rulesPage() }, true},
		{"valid-error-object", withTurn(func(m map[string]any) { m["error"] = map[string]any{"message": "x"} }), true},
		{"valid-cursor-string", func() map[string]any { p := rulesPage(); p["nextCursor"] = "c"; return p }, true},
		// page
		{"page-missing-data", func() map[string]any { p := rulesPage(); delete(p, "data"); return p }, false},
		{"page-data-null", func() map[string]any { p := rulesPage(); p["data"] = nil; return p }, false},
		{"page-data-object", func() map[string]any { p := rulesPage(); p["data"] = map[string]any{}; return p }, false},
		{"page-over-k", func() map[string]any {
			turns := []any{}
			for i := 0; i < acceptancePageK+1; i++ {
				turn := rulesTurn()
				turn["id"] = "turn-" + string(rune('a'+i))
				turns = append(turns, turn)
			}
			return rulesPage(turns...)
		}, false},
		{"page-missing-cursor", func() map[string]any { p := rulesPage(); delete(p, "nextCursor"); return p }, false},
		{"page-cursor-empty", func() map[string]any { p := rulesPage(); p["nextCursor"] = ""; return p }, false},
		{"page-cursor-number", func() map[string]any { p := rulesPage(); p["nextCursor"] = 5; return p }, false},
		{"page-cursor-long", func() map[string]any { p := rulesPage(); p["nextCursor"] = strings.Repeat("c", 1025); return p }, false},
		{"page-duplicate-id", func() map[string]any { return rulesPage(rulesTurn(), rulesTurn()) }, false},
		// turn: every key, type and vocabulary
		{"turn-missing-id", withTurn(func(m map[string]any) { delete(m, "id") }), false},
		{"turn-missing-status", withTurn(func(m map[string]any) { delete(m, "status") }), false},
		{"turn-missing-items", withTurn(func(m map[string]any) { delete(m, "items") }), false},
		{"turn-missing-items-view", withTurn(func(m map[string]any) { delete(m, "itemsView") }), false},
		{"turn-missing-error", withTurn(func(m map[string]any) { delete(m, "error") }), false},
		{"turn-id-empty", withTurn(func(m map[string]any) { m["id"] = "" }), false},
		{"turn-id-invalid", withTurn(func(m map[string]any) { m["id"] = "bad id" }), false},
		{"turn-id-number", withTurn(func(m map[string]any) { m["id"] = 5 }), false},
		{"turn-status-empty", withTurn(func(m map[string]any) { m["status"] = "" }), false},
		{"turn-status-unknown", withTurn(func(m map[string]any) { m["status"] = "paused" }), false},
		{"turn-items-view-empty", withTurn(func(m map[string]any) { m["itemsView"] = "" }), false},
		{"turn-items-view-summary", withTurn(func(m map[string]any) { m["itemsView"] = "summary" }), false},
		{"turn-error-string", withTurn(func(m map[string]any) { m["error"] = "x" }), false},
		{"turn-error-array", withTurn(func(m map[string]any) { m["error"] = []any{} }), false},
		{"turn-items-null", withTurn(func(m map[string]any) { m["items"] = nil }), false},
		{"turn-items-object", withTurn(func(m map[string]any) { m["items"] = map[string]any{} }), false},
		{"turn-items-over-bound", withTurn(func(m map[string]any) {
			items := []any{}
			for i := 0; i <= acceptanceMaxItems; i++ {
				items = append(items, map[string]any{"type": "agentMessage"})
			}
			m["items"] = items
		}), false},
		// items
		{"item-not-object", withTurn(func(m map[string]any) { m["items"] = []any{"x"} }), false},
		{"item-missing-type", withItem(func(m map[string]any) { delete(m, "type") }), false},
		{"item-type-empty", withItem(func(m map[string]any) { m["type"] = "" }), false},
		{"item-type-number", withItem(func(m map[string]any) { m["type"] = 5 }), false},
		{"user-missing-content", withItem(func(m map[string]any) { delete(m, "content") }), false},
		{"user-content-null", withItem(func(m map[string]any) { m["content"] = nil }), false},
		{"user-content-object", withItem(func(m map[string]any) { m["content"] = map[string]any{} }), false},
		// parts
		{"part-not-object", withItem(func(m map[string]any) { m["content"] = []any{"x"} }), false},
		{"part-missing-type", withPart(func(m map[string]any) { delete(m, "type") }), false},
		{"part-type-empty", withPart(func(m map[string]any) { m["type"] = "" }), false},
		{"part-text-missing", withPart(func(m map[string]any) { delete(m, "text") }), false},
		{"part-text-number", withPart(func(m map[string]any) { m["text"] = 5 }), false},
		{"part-text-null", withPart(func(m map[string]any) { m["text"] = nil }), false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, err := decodeTurnPage(rulesJSON(r.page()), true)
			if (err == nil) != r.ok {
				t.Fatalf("got err=%v, want ok=%v", err, r.ok)
			}
		})
	}
	// Truncated page bytes.
	if _, err := decodeTurnPage(json.RawMessage(`{"data":[{"id":"turn-a"`), true); err == nil {
		t.Fatal("truncated page accepted")
	}
}

// Evidence must carry what the decision needs; candidates decide from it.
func TestAcceptanceEvidenceDecides(t *testing.T) {
	page, err := decodeTurnPage(rulesJSON(rulesPage()), true)
	if err != nil {
		t.Fatal(err)
	}
	ev := page.Turns[0]
	digest := ev.InputDigest
	if ev.InputLength != len("exact") || digest == "" || !ev.ErrorNull || ev.FirstType != "userMessage" || ev.Parts != 1 || ev.PartType != "text" || ev.Users != 1 {
		t.Fatalf("incomplete evidence: %+v", ev)
	}
	if candidateRefusal(ev, digest, len("exact")) != "" {
		t.Fatal("valid evidence refused")
	}
	for name, change := range map[string]func(*turnEvidence){
		"error-present":  func(e *turnEvidence) { e.ErrorNull = false },
		"no-digest":      func(e *turnEvidence) { e.InputDigest = "" },
		"other-digest":   func(e *turnEvidence) { e.InputDigest = strings.Repeat("0", 64) },
		"length":         func(e *turnEvidence) { e.InputLength++ },
		"first-not-user": func(e *turnEvidence) { e.FirstType = "agentMessage" },
		"no-items":       func(e *turnEvidence) { e.Items = 0 },
		"two-parts":      func(e *turnEvidence) { e.Parts = 2 },
		"no-parts":       func(e *turnEvidence) { e.Parts = 0 },
		"part-not-text":  func(e *turnEvidence) { e.PartType = "image" },
		"two-users":      func(e *turnEvidence) { e.Users = 2 },
		"status-failed":  func(e *turnEvidence) { e.Status = "failed" },
		"status-empty":   func(e *turnEvidence) { e.Status = "" },
	} {
		e := ev
		change(&e)
		if candidateRefusal(e, digest, len("exact")) == "" {
			t.Errorf("%s: candidate accepted", name)
		}
	}
}

func TestAcceptanceNoticeRules(t *testing.T) {
	notice := func(change func(p, turn map[string]any)) json.RawMessage {
		turn := map[string]any{"id": "turn-a", "status": "completed"}
		p := map[string]any{"threadId": "thread-a", "turn": turn}
		if change != nil {
			change(p, turn)
		}
		return rulesJSON(p)
	}
	rows := []struct {
		name   string
		method string
		params json.RawMessage
		ok     bool
	}{
		{"valid-completed", "turn/completed", notice(nil), true},
		{"valid-started", "turn/started", notice(func(_, t map[string]any) { t["status"] = "inProgress" }), true},
		{"missing-thread", "turn/completed", notice(func(p, _ map[string]any) { delete(p, "threadId") }), false},
		{"missing-turn", "turn/completed", notice(func(p, _ map[string]any) { delete(p, "turn") }), false},
		{"missing-turn-id", "turn/completed", notice(func(_, t map[string]any) { delete(t, "id") }), false},
		{"missing-status", "turn/completed", notice(func(_, t map[string]any) { delete(t, "status") }), false},
		{"status-empty", "turn/completed", notice(func(_, t map[string]any) { t["status"] = "" }), false},
		{"status-unknown", "turn/completed", notice(func(_, t map[string]any) { t["status"] = "unknown-status" }), false},
		{"status-wrong-method", "turn/started", notice(nil), false},
		{"thread-invalid", "turn/completed", notice(func(p, _ map[string]any) { p["threadId"] = "a b" }), false},
		{"params-array", "turn/completed", json.RawMessage(`[]`), false},
		{"params-truncated", "turn/completed", json.RawMessage(`{"threadId":"t`), false},
	}
	for _, r := range rows {
		if _, ok := parseTurnNotice(r.method, r.params); ok != r.ok {
			t.Errorf("%s: got %v, want %v", r.name, ok, r.ok)
		}
	}
	if !nativeTurnStatusValid("turn/started", "inProgress") || nativeTurnStatusValid("turn/started", "completed") || nativeTurnStatusValid("other", "completed") {
		t.Fatal("shared notice vocabulary drifted")
	}
}

func TestAcceptanceJournalParseRules(t *testing.T) {
	header := `{"seq":1,"kind":"header","generation":"g","command":"c"}`
	second := `{"seq":2,"kind":"baseline"}`
	valid := header + "\n" + second + "\n"
	rows := []struct {
		name string
		body string
		n    int
		ok   bool
	}{
		{"valid", valid, 2, true},
		{"final-newline-missing", header + "\n" + second, 2, false},
		{"truncated-record", header + "\n" + `{"seq":2,"kin`, 2, false},
		{"empty", "", 1, false},
		{"seq-gap", header + "\n" + `{"seq":3,"kind":"baseline"}` + "\n", 2, false},
		{"first-not-header", second + "\n", 1, false},
		{"garbage-line", header + "\nnot json\n", 2, false},
	}
	for _, r := range rows {
		_, _, err := parseJournal([]byte(r.body), r.n)
		if (err == nil) != r.ok {
			t.Errorf("%s: err=%v want ok=%v", r.name, err, r.ok)
		}
	}
	// The digest covers exactly the persisted bytes.
	_, a, _ := parseJournal([]byte(valid), 2)
	_, b, _ := parseJournal([]byte(valid+"extra"), 2)
	if a == "" || a != b {
		t.Fatal("prefix digest is not the exact committed byte span")
	}
}

func TestAcceptanceBaselineStatusRules(t *testing.T) {
	h := newAccHarness(t)
	for name, status := range map[string]any{"missing-type": map[string]any{}, "empty-type": map[string]any{"type": ""}, "number-type": map[string]any{"type": 5}} {
		t.Run(name, func(t *testing.T) {
			h := newAccHarness(t)
			h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
				if method != "thread/read" || f.fullReads != 0 || f.getSent() != "" {
					return nil
				}
				thread := remoteThreadFixture(f.run)
				thread["thread"].(map[string]any)["status"] = status
				body, _ := json.Marshal(response(0, thread))
				return &accReply{raw: []byte(strings.Replace(string(body), `"id":0`, `"id":%ID%`, 1))}
			}
			if err := h.accept(accSent); err == nil || h.submits != 0 {
				t.Fatal("malformed baseline status submitted")
			}
		})
	}
	_ = h
}
