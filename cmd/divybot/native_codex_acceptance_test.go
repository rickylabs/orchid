package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Controls for the native Remote Control Codex prompt acceptance (SHAPE rev8
// §5). Every uncertain branch asserts submit and Enter counts separately.

func accRefused(t *testing.T, h *accHarness, err error, want string) {
	t.Helper()
	if err == nil || h.receipt().State == "native-accepted" {
		t.Fatalf("refusal expected (%s): err=%v state=%s", want, err, h.receipt().State)
	}
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Fatalf("refusal reason: got %v, want %s", err, want)
	}
}

func accAccepted(t *testing.T, h *accHarness, err error) {
	t.Helper()
	if err != nil || h.receipt().State != "native-accepted" || h.submits != 1 || !verifyDeliveryEvidence(h.dir, nil) {
		t.Fatalf("acceptance expected: err=%v state=%s reason=%s submits=%d", err, h.receipt().State, h.receipt().Reason, h.submits)
	}
}

// Replace the new turn T built from the exact submitted bytes.
func (h *accHarness) newTurn(build func(sent string) []any) {
	h.f.page = func(f *accFixture, i int) ([]any, any) {
		turns, cursor := accDefaultPage(f, i)
		if sent := f.getSent(); sent != "" {
			turns = append(build(sent), turns[1:]...)
		}
		return turns, cursor
	}
}

func TestAcceptanceExactInputShape(t *testing.T) {
	image := map[string]any{"type": "image", "url": "fixture"}
	cases := []struct {
		name   string
		build  func(sent string) []any
		accept bool
		reason string
	}{
		{"exact", func(s string) []any { return []any{accTurn(accNewTurn, "inProgress", accUser(s), accAgent())} }, true, ""},
		{"later-execution-items", func(s string) []any {
			return []any{accTurn(accNewTurn, "completed", accUser(s), accAgent(), map[string]any{"type": "reasoning", "id": "r"}, map[string]any{"type": "commandExecution", "id": "c"})}
		}, true, ""},
		{"one-byte-change", func(s string) []any { return []any{accTurn(accNewTurn, "inProgress", accUser(s[:len(s)-1]+"X"))} }, false, "input-mismatch"},
		{"foreign-input", func(string) []any { return []any{accTurn(accNewTurn, "inProgress", accUser("foreign prompt"))} }, false, "input-mismatch"},
		{"system-input", func(string) []any { return []any{accTurn(accNewTurn, "inProgress", accUser("scheduled: refresh"))} }, false, "input-mismatch"},
		{"user-not-first", func(s string) []any { return []any{accTurn(accNewTurn, "inProgress", accAgent(), accUser(s))} }, false, "input-mismatch"},
		{"two-text-parts", func(s string) []any {
			u := accUser(s)
			u["content"] = []any{map[string]any{"type": "text", "text": s}, map[string]any{"type": "text", "text": "x"}}
			return []any{accTurn(accNewTurn, "inProgress", u)}
		}, false, "input-mismatch"},
		{"text-and-image", func(s string) []any {
			u := accUser(s)
			u["content"] = []any{map[string]any{"type": "text", "text": s}, image}
			return []any{accTurn(accNewTurn, "inProgress", u)}
		}, false, "input-mismatch"},
		{"non-text", func(string) []any {
			u := accUser("")
			u["content"] = []any{image}
			return []any{accTurn(accNewTurn, "inProgress", u)}
		}, false, "input-mismatch"},
		// A non-user first item carrying the exact text must still be refused.
		{"non-user-first-with-content", func(s string) []any {
			first := map[string]any{"type": "systemMessage", "id": "sys", "content": []any{map[string]any{"type": "text", "text": s}}}
			return []any{accTurn(accNewTurn, "inProgress", first, accUser(s))}
		}, false, "input-mismatch"},
		// A non-text part that also carries a text field is not text input.
		{"non-text-with-text-field", func(s string) []any {
			u := accUser(s)
			u["content"] = []any{map[string]any{"type": "image", "text": s}}
			return []any{accTurn(accNewTurn, "inProgress", u)}
		}, false, "input-mismatch"},
		{"second-user-message", func(s string) []any {
			return []any{accTurn(accNewTurn, "inProgress", accUser(s), accAgent(), accUser("second"))}
		}, false, "input-mismatch"},
		{"two-new-turns", func(s string) []any {
			return []any{accTurn("turn-newer", "inProgress", accUser(s)), accTurn(accNewTurn, "completed", accUser(s))}
		}, false, "multiple-new-turns"},
		{"failed", func(s string) []any { return []any{accTurn(accNewTurn, "failed", accUser(s))} }, false, "turn-failed"},
		{"interrupted", func(s string) []any { return []any{accTurn(accNewTurn, "interrupted", accUser(s))} }, false, "turn-failed"},
		{"error", func(s string) []any {
			turn := accTurn(accNewTurn, "completed", accUser(s))
			turn["error"] = map[string]any{"message": "fixture"}
			return []any{turn}
		}, false, "turn-failed"},
		// P1: a policy-rejected input surfaces as failed/error and is refused.
		{"policy-rejected", func(s string) []any {
			turn := accTurn(accNewTurn, "failed", accUser(s))
			turn["error"] = map[string]any{"message": "blocked by policy"}
			return []any{turn}
		}, false, "turn-failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newAccHarness(t)
			h.newTurn(c.build)
			err := h.accept(accSent)
			if c.accept {
				accAccepted(t, h, err)
				return
			}
			accRefused(t, h, err, c.reason)
			if h.submits != 1 {
				t.Fatalf("submits=%d", h.submits)
			}
		})
	}
}

// R13: the same goal under another nonce is a different command.
func TestAcceptanceExactDigestOnly(t *testing.T) {
	other := strings.ReplaceAll(accSent, "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210")
	h := newAccHarness(t)
	h.newTurn(func(string) []any { return []any{accTurn(accNewTurn, "inProgress", accUser(other))} })
	accRefused(t, h, h.accept(accSent), "input-mismatch")
	h = newAccHarness(t)
	h.f.baseline = []any{accBaseTurn("base-older")}
	accAccepted(t, h, h.accept(accSent))
	if h.receipt().Digest == shaText([]byte(other)) {
		t.Fatal("digest not bound to the exact transported bytes")
	}
}

// B2: exactly 64 KiB of text passes the bound; one more byte is refused.
func TestAcceptanceTextBound(t *testing.T) {
	for _, size := range []int{acceptanceMaxText, acceptanceMaxText + 1} {
		h := newAccHarness(t)
		sent := strings.Repeat("a", size)
		err := h.accept(sent)
		if size == acceptanceMaxText {
			accAccepted(t, h, err)
		} else {
			accRefused(t, h, err, "bounds")
		}
	}
}

// R10 (unit): a long argument is digested exactly as transported.
func TestAcceptanceLongArgumentDigest(t *testing.T) {
	h := newAccHarness(t)
	sent := "Assignment delivery marker: orch-goal-1\n" + strings.Repeat("long rendered goal line\n", 900) + "Assignment delivery marker: orch-goal-1"
	accAccepted(t, h, h.accept(sent))
	sum := sha256.Sum256([]byte(sent))
	if r := h.receipt(); r.Digest != hex.EncodeToString(sum[:]) || r.Length != len(sent) {
		t.Fatal("receipt digest is not the exact argument")
	}
}

// ---- receipt durability (R5–R9) ----

func TestAcceptanceReceiptPreEffectFailures(t *testing.T) {
	for _, stage := range []string{"receipt-prepared-file", "receipt-prepared-dir", "receipt-attempted-file", "receipt-attempted-rename", "receipt-attempted-dir"} {
		t.Run(stage, func(t *testing.T) {
			h := newAccHarness(t)
			h.acc.fs.fail = func(s string) error {
				if s == stage {
					return errors.New("injected")
				}
				return nil
			}
			if err := h.accept(accSent); err == nil || h.submits != 0 {
				t.Fatalf("pre-effect durability failure submitted: err=%v submits=%d", err, h.submits)
			}
		})
	}
	t.Run("existing-receipt", func(t *testing.T) {
		h := newAccHarness(t)
		if os.WriteFile(filepath.Join(h.dir, deliveryReceiptName), []byte(`{}`), 0600) != nil {
			t.Fatal("fixture")
		}
		if err := h.accept(accSent); err == nil || h.submits != 0 {
			t.Fatal("existing receipt allowed another submit")
		}
	})
	t.Run("verify-mismatch", func(t *testing.T) {
		h := newAccHarness(t)
		h.acc.fs.hold = func(s string) {
			if s == "receipt-prepared-dir" {
				r, _ := readDeliveryReceipt(h.dir, nil)
				r.Length++
				b, _ := json.Marshal(r)
				_ = os.WriteFile(filepath.Join(h.dir, deliveryReceiptName), b, 0600)
			}
		}
		if err := h.accept(accSent); err == nil || h.submits != 0 {
			t.Fatal("unverified receipt allowed a submit")
		}
	})
}

func TestAcceptanceReceiptIntegrity(t *testing.T) {
	tamper := map[string]func(h *accHarness){
		"mode": func(h *accHarness) { _ = os.Chmod(filepath.Join(h.dir, deliveryReceiptName), 0644) },
		"symlink": func(h *accHarness) {
			path := filepath.Join(h.dir, deliveryReceiptName)
			b, _ := os.ReadFile(path)
			other := filepath.Join(h.dir, "copy")
			_ = os.WriteFile(other, b, 0600)
			_ = os.Remove(path)
			_ = os.Symlink(other, path)
		},
		"size": func(h *accHarness) {
			path := filepath.Join(h.dir, deliveryReceiptName)
			b, _ := os.ReadFile(path)
			_ = os.WriteFile(path, append(b, bytes.Repeat([]byte(" "), acceptanceMaxRecord)...), 0600)
		},
		"duplicate-field": func(h *accHarness) {
			path := filepath.Join(h.dir, deliveryReceiptName)
			b, _ := os.ReadFile(path)
			_ = os.WriteFile(path, append([]byte(`{"state":"prepared",`), b[1:]...), 0600)
		},
		"replaced-binding": func(h *accHarness) {
			r, _ := readDeliveryReceipt(h.dir, nil)
			r.Binding.Pane = "w9:p9"
			b, _ := json.Marshal(r)
			_ = os.WriteFile(filepath.Join(h.dir, deliveryReceiptName), b, 0600)
		},
	}
	for name, change := range tamper {
		t.Run(name, func(t *testing.T) {
			h := newAccHarness(t)
			h.acc.fs.hold = func(s string) {
				if s == "receipt-prepared-dir" {
					change(h)
				}
			}
			if err := h.accept(accSent); err == nil || h.submits != 0 {
				t.Fatalf("tampered receipt (%s) allowed a submit", name)
			}
		})
	}
	t.Run("owner", func(t *testing.T) {
		h := newAccHarness(t)
		h.acc.owner = &receiptOwner{uid: os.Getuid() + 1, gid: os.Getgid(), chown: func(string, int, int) error { return nil }, sync: func(string) error { return nil }}
		if err := h.accept(accSent); err == nil || h.submits != 0 {
			t.Fatal("foreign-owner receipt allowed a submit")
		}
	})
	t.Run("torn-temp-ignored", func(t *testing.T) {
		h := newAccHarness(t)
		if os.WriteFile(filepath.Join(h.dir, ".delivery-command-torn"), []byte(`{"state":"native-accepted"`), 0600) != nil {
			t.Fatal("fixture")
		}
		accAccepted(t, h, h.accept(accSent))
	})
}

func TestAcceptanceConcurrentEntry(t *testing.T) {
	h := newAccHarness(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	submits := 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = h.host.acceptCodexPrompt(ctx, h.acc, accSent, func(context.Context) error {
				mu.Lock()
				submits++
				mu.Unlock()
				h.f.setSent(accSent)
				return nil
			})
		}()
	}
	wg.Wait()
	if submits != 1 {
		t.Fatalf("concurrent entry submitted %d times", submits)
	}
}

func TestAcceptanceKillPointsNeverResubmit(t *testing.T) {
	for _, point := range []string{"receipt-prepared-dir", "receipt-attempted-dir", "after-submit"} {
		t.Run(point, func(t *testing.T) {
			h := newAccHarness(t)
			crash := true
			h.acc.fs.hold = func(s string) {
				if crash && s == point {
					crash = false
					panic("crash")
				}
			}
			if point == "after-submit" {
				h.onSubmit = func() {
					if crash {
						crash = false
						panic("crash")
					}
				}
			}
			func() {
				defer func() { _ = recover() }()
				_ = h.accept(accSent)
			}()
			first := h.submits
			err := h.accept(accSent)
			if err == nil || h.submits != first || first > 1 {
				t.Fatalf("re-entry after %s: err=%v submits %d→%d", point, err, first, h.submits)
			}
		})
	}
}

// R8: a lost acknowledgement is terminal even when the effect happened.
func TestAcceptanceLostAckNeverUpgrades(t *testing.T) {
	h := newAccHarness(t)
	h.submitErr = errors.New("lost")
	accRefused(t, h, h.accept(accSent), "submit-uncertain")
	if r := h.receipt(); h.submits != 1 || r.State != "unconfirmed" || r.Reason != "submit-uncertain" {
		t.Fatalf("lost ack: submits=%d state=%s reason=%s", h.submits, r.State, r.Reason)
	}
}

// ---- journal (J1–J6), each on an otherwise-accepting trace ----

func accFailAt(stage string) func(string) error {
	return func(s string) error {
		if s == stage {
			return errors.New("injected")
		}
		return nil
	}
}

func TestAcceptanceJournalFaults(t *testing.T) {
	for _, stage := range []string{"journal-header", "journal-dir"} {
		t.Run("pre-effect-"+stage, func(t *testing.T) {
			h := newAccHarness(t)
			h.acc.fs.fail = accFailAt(stage)
			if err := h.accept(accSent); err == nil || h.submits != 0 {
				t.Fatal("journal registration failure submitted")
			}
		})
	}
	for _, stage := range []string{"journal-notice", "journal-page", "journal-fence"} {
		t.Run("post-effect-"+stage, func(t *testing.T) {
			h := newAccHarness(t)
			h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
				if full == 0 {
					return &accReply{pre: []any{accNotice("turn/started", f.run.NativeSessionID, accNewTurn, "inProgress")}}
				}
				return nil
			}
			h.acc.fs.fail = accFailAt(stage)
			accRefused(t, h, h.accept(accSent), "")
			if h.submits != 1 {
				t.Fatal("post-effect journal failure changed the effect count")
			}
		})
	}
}

// J3: a held fsync blocks every later decision until it returns.
func TestAcceptanceJournalHeldFsync(t *testing.T) {
	h := newAccHarness(t)
	release := make(chan struct{})
	held := make(chan struct{})
	once := sync.Once{}
	h.acc.fs.hold = func(s string) {
		if s == "journal-page" && h.f.getSent() != "" {
			once.Do(func() { close(held); <-release })
		}
	}
	done := make(chan error, 1)
	go func() { done <- h.accept(accSent) }()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("the candidate page was never journaled behind a held fsync: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the candidate page was never journaled behind a held fsync")
	}
	time.Sleep(300 * time.Millisecond)
	h.f.mu.Lock()
	reads := h.f.fullReads
	h.f.mu.Unlock()
	if reads != 1 || h.calls != 0 || h.receipt().State == "native-accepted" {
		t.Fatalf("decision advanced while the candidate page fsync was held: reads=%d binding=%d", reads, h.calls)
	}
	close(release)
	accAccepted(t, h, <-done)
}

func TestAcceptanceJournalIdentity(t *testing.T) {
	t.Run("inode-replaced", func(t *testing.T) {
		h := newAccHarness(t)
		// Swap after the native-accepted outcome line is written: identical
		// bytes, different inode, so only the identity check can refuse.
		swapped := false
		h.acc.fs.hold = func(s string) {
			if s == "journal-outcome" && !swapped {
				swapped = true
				r := h.receipt()
				path := filepath.Join(h.dir, r.Journal)
				b, _ := os.ReadFile(path)
				_ = os.WriteFile(path+".swap", b, 0600)
				_ = os.Rename(path+".swap", path)
			}
		}
		accRefused(t, h, h.accept(accSent), "journal")
	})
	t.Run("verifier", func(t *testing.T) {
		h := newAccHarness(t)
		accAccepted(t, h, h.accept(accSent))
		original := h.receipt()
		write := func(r deliveryReceipt) {
			b, _ := json.Marshal(r)
			_ = os.WriteFile(filepath.Join(h.dir, deliveryReceiptName), b, 0600)
		}
		for name, change := range map[string]func(*deliveryReceipt){
			"header-generation": func(r *deliveryReceipt) { r.Generation = strings.Repeat("e", 32) },
			"seq-plus":          func(r *deliveryReceipt) { r.JournalSeq++ },
			"seq-minus":         func(r *deliveryReceipt) { r.JournalSeq-- },
			"digest":            func(r *deliveryReceipt) { r.PrefixSha256 = strings.Repeat("0", 64) },
		} {
			r := original
			change(&r)
			write(r)
			if verifyDeliveryEvidence(h.dir, nil) {
				t.Fatalf("verifier accepted corrupted %s", name)
			}
		}
		write(original)
		if !verifyDeliveryEvidence(h.dir, nil) {
			t.Fatal("verifier positive control failed")
		}
		path := filepath.Join(h.dir, original.Journal)
		b, _ := os.ReadFile(path)
		_ = os.WriteFile(path, b[:len(b)-1], 0600) // only the final newline removed
		if verifyDeliveryEvidence(h.dir, nil) {
			t.Fatal("verifier accepted an unterminated final record")
		}
		_ = os.WriteFile(path, b[:len(b)/2], 0600)
		if verifyDeliveryEvidence(h.dir, nil) {
			t.Fatal("verifier accepted a truncated journal")
		}
	})
}

// ---- notices, fence and transport ----

func TestAcceptanceStickyContradiction(t *testing.T) {
	h := newAccHarness(t)
	h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
		if full == 0 {
			return &accReply{pre: []any{
				accNotice("turn/started", f.run.NativeSessionID, "turn-competing", "inProgress"),
				accNotice("turn/started", f.run.NativeSessionID, accNewTurn, "inProgress"),
			}}
		}
		return nil
	}
	accRefused(t, h, h.accept(accSent), "contradiction")
}

// R17: a competing notice queued while the step-2 check is held is consumed
// by the fence on the same connection.
func TestAcceptanceFenceConsumesQueuedNotice(t *testing.T) {
	h := newAccHarness(t)
	step2, written := make(chan struct{}), make(chan struct{})
	h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
		if full == 0 {
			return &accReply{after: func(c net.Conn) {
				<-step2
				body, _ := json.Marshal(accNotice("turn/started", f.run.NativeSessionID, "turn-competing", "inProgress"))
				_ = nativeFixtureFrame(c, body, 1)
				time.Sleep(100 * time.Millisecond)
				close(written)
			}}
		}
		return nil
	}
	h.binding = func(call int) error {
		if call == 1 {
			close(step2)
			<-written
		}
		return nil
	}
	accRefused(t, h, h.accept(accSent), "contradiction")
}

// R18 / D1 / E1: events after the fence response while step 4 is held.
func accAfterFence(h *accHarness, act func(f *accFixture, c net.Conn), post [][]byte) {
	step4, acted := make(chan struct{}), make(chan struct{})
	h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
		if full == 1 {
			return &accReply{post: post, after: func(c net.Conn) {
				<-step4
				if act != nil {
					act(f, c)
				}
				time.Sleep(150 * time.Millisecond)
				close(acted)
			}}
		}
		return nil
	}
	h.binding = func(call int) error {
		if call == 2 {
			close(step4)
			<-acted
		}
		return nil
	}
}

func TestAcceptanceJoinedFinalization(t *testing.T) {
	cases := []struct {
		name   string
		act    func(f *accFixture, c net.Conn)
		accept bool
	}{
		{"close-frame", func(_ *accFixture, c net.Conn) { _ = nativeFixtureFrame(c, nil, 8) }, false},
		{"malformed-framing", func(_ *accFixture, c net.Conn) { _, _ = c.Write([]byte{0x81, 0x80}) }, false},
		{"socket-drop", func(_ *accFixture, c net.Conn) { _ = c.Close() }, false},
		{"clean-end", nil, true},
		{"post-fence-foreign-notice", func(f *accFixture, c net.Conn) {
			body, _ := json.Marshal(accNotice("turn/started", f.run.NativeSessionID, "turn-later", "inProgress"))
			_ = nativeFixtureFrame(c, body, 1)
		}, true},
		{"endpoint-swapped", func(f *accFixture, _ net.Conn) {
			_ = os.Rename(f.sockPath, f.sockPath+".old")
			l, err := net.Listen("unix", f.sockPath)
			if err == nil {
				_ = os.Chmod(f.sockPath, 0600)
				f.t.Cleanup(func() { _ = l.Close() })
			}
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newAccHarness(t)
			accAfterFence(h, c.act, nil)
			err := h.accept(accSent)
			if c.accept {
				accAccepted(t, h, err)
				if c.name == "post-fence-foreign-notice" {
					post := 0
					for _, r := range h.journal() {
						if r.Kind == "post-fence" {
							post++
						}
					}
					if post != 1 {
						t.Fatal("post-fence notice not journaled as history")
					}
				}
				return
			}
			accRefused(t, h, err, "")
			if h.submits != 1 {
				t.Fatal("finalization failure changed the effect count")
			}
		})
	}
}

// End-mode EOF drain: a close frame still pending when stdin ends is drained
// and refused. The fixture bridge stops reading the socket after the fence
// response (6 forwarded responses), so the frame is pending at the EOF.
func TestAcceptanceEOFDrainsPendingClose(t *testing.T) {
	old := "  ready,_,_=select.select([s,sys.stdin],[],[],10)"
	if strings.Count(canonicalCodexBridge, old) != 1 || strings.Count(canonicalCodexBridge, " def forward(body):\n") != 1 {
		t.Fatal("bridge fixture anchor drifted")
	}
	bridge := strings.Replace(canonicalCodexBridge, old, "  ready,_,_=select.select([s,sys.stdin] if FWD[0]<6 else [sys.stdin],[],[],10)", 1)
	bridge = strings.Replace(bridge, " def forward(body):\n", " def forward(body):\n  FWD[0]+=1\n", 1)
	bridge = strings.Replace(bridge, "def run():", "FWD=[0]\ndef run():", 1)
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			h := newAccHarness(t)
			h.acc.joined.script = "export HOME=" + shq(h.f.home) + "; exec python3 -c " + shq(bridge) + " end"
			if pending {
				accAfterFence(h, func(_ *accFixture, c net.Conn) { _ = nativeFixtureFrame(c, nil, 8) }, nil)
			}
			err := h.accept(accSent)
			if !pending {
				accAccepted(t, h, err)
				return
			}
			accRefused(t, h, err, "")
		})
	}
}

// R18f: a stalled loop lets the bridge's idle timeout end the generation.
func TestAcceptanceBridgeIdleTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the bridge idle timeout")
	}
	h := newAccHarness(t)
	h.binding = func(call int) error {
		if call == 2 {
			time.Sleep(11 * time.Second)
		}
		return nil
	}
	accRefused(t, h, h.accept(accSent), "")
}

// D1: invalid native JSON after the fence, bridge-valid, clean exit.
type accTap struct {
	mu     sync.Mutex
	r      io.Reader
	delay  time.Duration
	chunks []string
}

func (a *accTap) Read(p []byte) (int, error) {
	time.Sleep(a.delay)
	n, err := a.r.Read(p)
	a.mu.Lock()
	a.chunks = append(a.chunks, string(p[:n]))
	a.mu.Unlock()
	return n, err
}

func TestAcceptanceStrictDrain(t *testing.T) {
	invalid := []byte(`{"method":"turn/started","params":`)
	t.Run("buffered-in-scanner", func(t *testing.T) {
		h := newAccHarness(t)
		accAfterFence(h, nil, [][]byte{invalid})
		tap := &accTap{delay: 40 * time.Millisecond}
		h.acc.joined.tap = func(r io.Reader) io.Reader { tap.r = r; return tap }
		accRefused(t, h, h.accept(accSent), "drain")
		buffered := false
		tap.mu.Lock()
		for _, c := range tap.chunks {
			if strings.Contains(c, `"result"`) && strings.Contains(c, string(invalid)+"\n") && strings.Contains(c, accNewTurn) {
				buffered = true
			}
		}
		tap.mu.Unlock()
		if !buffered {
			t.Fatal("fixture did not place the invalid frame in the scanner buffer with the fence response")
		}
	})
	t.Run("in-pipe", func(t *testing.T) {
		h := newAccHarness(t)
		accAfterFence(h, func(_ *accFixture, c net.Conn) { _ = nativeFixtureFrame(c, invalid, 1) }, nil)
		accRefused(t, h, h.accept(accSent), "drain")
	})
	t.Run("valid-positive", func(t *testing.T) {
		h := newAccHarness(t)
		valid, _ := json.Marshal(map[string]any{"method": "item/started", "params": map[string]any{}})
		accAfterFence(h, func(_ *accFixture, c net.Conn) { _ = nativeFixtureFrame(c, valid, 1) }, [][]byte{valid})
		accAccepted(t, h, h.accept(accSent))
	})
}

// D2: clean exit with nonempty stderr is refused on raw bytes.
func TestAcceptanceRawDiagnostic(t *testing.T) {
	for name, stderr := range map[string]string{
		"allowlisted": "remote-control-endpoint-changed\n",
		"unknown":     "unexpected native detail",
		"overflow":    strings.Repeat("x", 200),
		"empty":       "",
	} {
		t.Run(name, func(t *testing.T) {
			h := newAccHarness(t)
			h.acc.joined.script = "export HOME=" + shq(h.f.home) + "; python3 -c " + shq(canonicalCodexBridge) + " end; rc=$?; printf %s " + shq(stderr) + " >&2; exit $rc"
			err := h.accept(accSent)
			if stderr == "" {
				accAccepted(t, h, err)
				return
			}
			accRefused(t, h, err, "bridge-diagnostic")
			if strings.Contains(err.Error(), "native detail") || strings.Contains(err.Error(), "xxxx") {
				t.Fatal("stderr bytes surfaced")
			}
		})
	}
	remote := Host{SSH: "agent@fixture", Name: "fixture-remote"}
	name, args := remote.joinedArgs("true")
	if name != "ssh" || !strings.Contains(strings.Join(args, " "), "-o LogLevel=ERROR") {
		t.Fatal("acceptance SSH invocation is not quiet")
	}
	if strings.Contains(strings.Join(remote.sshBase(), " "), "LogLevel") {
		t.Fatal("other SSH callers changed")
	}
}

// E2 / E4: one generation; a lost source revokes a candidate; no reconnect.
func TestAcceptanceSingleGeneration(t *testing.T) {
	t.Run("drop-mid-loop", func(t *testing.T) {
		h := newAccHarness(t)
		h.f.page = func(f *accFixture, i int) ([]any, any) {
			if i < 2 {
				return accDefaultPage(&accFixture{baseline: f.baseline, baseCur: f.baseCur}, i)
			}
			return accDefaultPage(f, i)
		}
		h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
			if full == 1 {
				return &accReply{after: func(c net.Conn) { _ = c.Close() }}
			}
			return nil
		}
		accRefused(t, h, h.accept(accSent), "")
		if h.f.connections() != 1 || h.submits != 1 {
			t.Fatalf("reconnect or resubmit: conns=%d submits=%d", h.f.connections(), h.submits)
		}
	})
	t.Run("candidate-then-source-lost", func(t *testing.T) {
		h := newAccHarness(t)
		h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
			if full == 0 {
				return &accReply{after: func(c net.Conn) { _ = c.Close() }}
			}
			return nil
		}
		accRefused(t, h, h.accept(accSent), "")
		if h.receipt().State == "native-accepted" || h.lastOutcome() == "native-accepted" {
			t.Fatal("candidate survived source loss")
		}
	})
}

// E3: cumulative retained notices, spread below the per-request frame bound.
func TestAcceptanceCumulativeNoticeLimit(t *testing.T) {
	for _, total := range []int{128, 133} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			h := newAccHarness(t)
			h.f.page = func(f *accFixture, i int) ([]any, any) {
				if i < 7 {
					return accDefaultPage(&accFixture{baseline: f.baseline, baseCur: f.baseCur}, i)
				}
				return accDefaultPage(f, i)
			}
			h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
				if full < 0 || full >= 7 {
					return nil
				}
				count := 19
				if total == 128 && full == 6 {
					count = 128 - 6*19
				}
				notices := make([]any, count)
				for i := range notices {
					notices[i] = accNotice("turn/started", f.run.NativeSessionID, accNewTurn, "inProgress")
				}
				return &accReply{pre: notices}
			}
			err := h.accept(accSent)
			if total == 128 {
				accAccepted(t, h, err)
				return
			}
			accRefused(t, h, err, "")
		})
	}
}

// R19: a source gap is sticky; a later valid page cannot clear it.
func TestAcceptanceStickySourceGap(t *testing.T) {
	h := newAccHarness(t)
	h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
		if full == 0 {
			return &accReply{raw: []byte(`{"id":%ID%,"result":{"data":[],"data":[]}}`)}
		}
		return nil
	}
	h.f.page = func(f *accFixture, i int) ([]any, any) { return accDefaultPage(f, i) }
	accRefused(t, h, h.accept(accSent), "")
}

// ---- pages and decoding (R20, R21, B1, B3, B4, R23) ----

func accIDs(prefix string, n int) []any {
	out := []any{}
	for i := 0; i < n; i++ {
		out = append(out, accBaseTurn(fmt.Sprintf("%s-%c", prefix, 'a'+i)))
	}
	return out
}

func TestAcceptancePagination(t *testing.T) {
	full := func(rows []any) []any {
		out := []any{}
		for _, r := range rows {
			m := map[string]any{}
			for k, v := range r.(map[string]any) {
				m[k] = v
			}
			m["itemsView"] = "full"
			out = append(out, m)
		}
		return out
	}
	abc := accIDs("base", 3)
	x := accBaseTurn("base-x")
	eight := accIDs("base", 8)
	cases := []struct {
		name     string
		baseline []any
		baseCur  any
		page     func(t any) ([]any, any)
		accept   bool
	}{
		{"short-exhausted-drop", abc, nil, func(t any) ([]any, any) { return append([]any{t}, full(abc[:1])...), nil }, false},
		{"complete", abc, nil, func(t any) ([]any, any) { return append([]any{t}, full(abc)...), nil }, true},
		{"rewritten", abc, nil, func(t any) ([]any, any) {
			return append([]any{t}, full([]any{abc[0], x, abc[2]})...), nil
		}, false},
		{"anchor-missing-null", abc, nil, func(t any) ([]any, any) { return []any{t}, nil }, false},
		{"anchor-missing-more", abc, nil, func(t any) ([]any, any) { return []any{t}, "cursor" }, false},
		{"full-baseline-capacity", eight, "cursor", func(t any) ([]any, any) { return append([]any{t}, full(eight[:7])...), "cursor" }, true},
		{"exhausted-baseline-capacity", eight, nil, func(t any) ([]any, any) { return append([]any{t}, full(eight[:7])...), "cursor" }, true},
		{"empty-more", []any{}, nil, func(t any) ([]any, any) { return []any{t}, "cursor" }, false},
		{"empty-exhausted", []any{}, nil, func(t any) ([]any, any) { return []any{t}, nil }, true},
		{"complete-but-cursor-more", abc, nil, func(t any) ([]any, any) { return append([]any{t}, full(abc)...), "cursor" }, false},
		{"duplicate-anchor", abc, nil, func(t any) ([]any, any) { return append([]any{t}, full([]any{abc[0], abc[0], abc[1]})...), nil }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newAccHarness(t)
			h.f.baseline, h.f.baseCur = c.baseline, c.baseCur
			h.f.page = func(f *accFixture, i int) ([]any, any) {
				if f.getSent() == "" {
					return full(f.baseline), f.baseCur
				}
				return c.page(accTurn(accNewTurn, "inProgress", accUser(f.getSent())))
			}
			err := h.accept(accSent)
			if c.accept {
				accAccepted(t, h, err)
				return
			}
			accRefused(t, h, err, "")
		})
	}
}

func TestAcceptanceStrictPages(t *testing.T) {
	turn := func(s string) map[string]any { return accTurn(accNewTurn, "inProgress", accUser(s)) }
	many := func(s string, n int) map[string]any {
		items := []any{accUser(s)}
		for len(items) < n {
			items = append(items, accAgent())
		}
		return accTurn(accNewTurn, "inProgress", items...)
	}
	cases := []struct {
		name   string
		page   func(s string) ([]any, any)
		accept bool
	}{
		{"missing-status", func(s string) ([]any, any) { r := turn(s); delete(r, "status"); return []any{r}, nil }, false},
		{"summary-view", func(s string) ([]any, any) { r := turn(s); r["itemsView"] = "summary"; return []any{r}, nil }, false},
		{"not-loaded-view", func(s string) ([]any, any) { r := turn(s); r["itemsView"] = "notLoaded"; return []any{r}, nil }, false},
		{"items-512", func(s string) ([]any, any) { return []any{many(s, 512)}, nil }, true},
		{"items-513", func(s string) ([]any, any) { return []any{many(s, 513)}, nil }, false},
		{"cursor-number", func(s string) ([]any, any) { return []any{turn(s)}, 5 }, false},
		{"cursor-empty", func(s string) ([]any, any) { return []any{turn(s)}, "" }, false},
		{"id-space", func(s string) ([]any, any) { r := turn(s); r["id"] = "bad id"; return []any{r}, nil }, false},
		{"id-slash", func(s string) ([]any, any) { r := turn(s); r["id"] = "a/b"; return []any{r}, nil }, false},
		{"id-empty", func(s string) ([]any, any) { r := turn(s); r["id"] = ""; return []any{r}, nil }, false},
		{"id-long", func(s string) ([]any, any) { r := turn(s); r["id"] = strings.Repeat("a", 300); return []any{r}, nil }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newAccHarness(t)
			h.f.baseline = []any{}
			h.f.page = func(f *accFixture, i int) ([]any, any) {
				if f.getSent() == "" {
					return []any{}, nil
				}
				return c.page(f.getSent())
			}
			err := h.accept(accSent)
			if c.accept {
				accAccepted(t, h, err)
				return
			}
			accRefused(t, h, err, "")
		})
	}
	for name, cursor := range map[string]any{"cursor-number-full-baseline": 5, "cursor-empty-full-baseline": ""} {
		t.Run(name, func(t *testing.T) {
			h := newAccHarness(t)
			eight := accIDs("base", 8)
			h.f.baseline, h.f.baseCur = eight, "cursor"
			h.f.page = func(f *accFixture, i int) ([]any, any) {
				rows := []any{}
				if f.getSent() != "" {
					rows = append(rows, accTurn(accNewTurn, "inProgress", accUser(f.getSent())))
				}
				for _, b := range eight[:8-len(rows)] {
					m := map[string]any{}
					for k, v := range b.(map[string]any) {
						m[k] = v
					}
					m["itemsView"] = "full"
					rows = append(rows, m)
				}
				if f.getSent() == "" {
					return rows, "cursor"
				}
				return rows, cursor
			}
			accRefused(t, h, h.accept(accSent), "")
		})
	}
	t.Run("baseline-turn-missing-items", func(t *testing.T) {
		h := newAccHarness(t)
		h.f.page = func(f *accFixture, i int) ([]any, any) {
			turns, cursor := accDefaultPage(f, i)
			if f.getSent() != "" {
				delete(turns[1].(map[string]any), "items")
			}
			return turns, cursor
		}
		accRefused(t, h, h.accept(accSent), "")
	})
	// The duplicate key sits in an otherwise-accepting candidate page.
	t.Run("duplicate-field", func(t *testing.T) {
		h := newAccHarness(t)
		h.f.baseline = []any{}
		h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
			if full >= 0 && f.getSent() != "" {
				data, _ := json.Marshal([]any{accTurn(accNewTurn, "inProgress", accUser(f.getSent()))})
				return &accReply{raw: []byte(`{"id":%ID%,"result":{"data":` + string(data) + `,"nextCursor":null,"nextCursor":null}}`)}
			}
			return nil
		}
		accRefused(t, h, h.accept(accSent), "")
	})
}

func TestAcceptanceBaselineVeto(t *testing.T) {
	for _, status := range []string{"active", "systemError", "notLoaded"} {
		t.Run(status, func(t *testing.T) {
			h := newAccHarness(t)
			h.f.status = status
			if err := h.accept(accSent); err == nil || h.submits != 0 {
				t.Fatal("non-idle baseline submitted")
			}
		})
	}
	for name, setup := range map[string]func(*accFixture){
		"short-with-cursor": func(f *accFixture) { f.baseCur = "cursor" },
		"duplicate-ids":     func(f *accFixture) { f.baseline = []any{accBaseTurn("base-1"), accBaseTurn("base-1")} },
	} {
		t.Run(name, func(t *testing.T) {
			h := newAccHarness(t)
			setup(h.f)
			if err := h.accept(accSent); err == nil || h.submits != 0 {
				t.Fatal("invalid baseline submitted")
			}
		})
	}
}

func TestAcceptanceBindingRecheck(t *testing.T) {
	for _, call := range []int{1, 2} {
		t.Run(fmt.Sprint(call), func(t *testing.T) {
			h := newAccHarness(t)
			h.binding = func(c int) error {
				if c == call {
					return errors.New("replaced occupant")
				}
				return nil
			}
			accRefused(t, h, h.accept(accSent), "binding-changed")
		})
	}
}

// T1: helper publication outcomes, asserting (helper, receipt, journal).
func TestAcceptancePublicationTable(t *testing.T) {
	type row struct {
		name    string
		setup   func(h *accHarness, expire func())
		post    bool
		helper  string
		receipt string
		journal string
	}
	rows := []row{
		{"terminal-before-finalization", func(h *accHarness, _ func()) {
			h.newTurn(func(s string) []any { return []any{accTurn(accNewTurn, "failed", accUser(s))} })
		}, false, "turn-failed", "unconfirmed", "unconfirmed"},
		{"c1", func(h *accHarness, expire func()) {
			h.acc.fs.hold = func(s string) {
				if s == "journal-post-fence" {
					expire()
				}
			}
		}, true, "deadline", "unconfirmed", "unconfirmed"},
		{"journal-outcome", func(h *accHarness, _ func()) { h.acc.fs.fail = accFailAt("journal-outcome") }, false, "journal", "unconfirmed", "!native-accepted"},
		{"receipt-before-rename", func(h *accHarness, _ func()) { h.acc.fs.fail = accFailAt("receipt-accepted-file") }, false, "receipt", "unconfirmed", "native-accepted"},
		{"receipt-dir-after-rename", func(h *accHarness, _ func()) { h.acc.fs.fail = accFailAt("receipt-accepted-dir") }, false, "receipt-persistence-uncertain", "native-accepted", "persistence-uncertain"},
		{"c2", func(h *accHarness, expire func()) {
			h.acc.fs.hold = func(s string) {
				if s == "receipt-accepted-dir" {
					expire()
				}
			}
		}, false, "deadline", "native-accepted", "late"},
		{"refusal-rename-then-dir-fail", func(h *accHarness, _ func()) {
			h.newTurn(func(s string) []any { return []any{accTurn(accNewTurn, "failed", accUser(s))} })
			h.acc.fs.fail = accFailAt("receipt-unconfirmed-dir")
		}, false, "turn-failed", "unconfirmed", "unconfirmed"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			h := newAccHarness(t)
			var mu sync.Mutex
			expired := false
			h.acc.now = func() time.Time {
				mu.Lock()
				defer mu.Unlock()
				if expired {
					return h.acc.deadline.Add(time.Second)
				}
				return time.Now()
			}
			r.setup(h, func() { mu.Lock(); expired = true; mu.Unlock() })
			if r.post {
				accAfterFence(h, func(f *accFixture, c net.Conn) {
					body, _ := json.Marshal(accNotice("turn/completed", f.run.NativeSessionID, accNewTurn, "completed"))
					_ = nativeFixtureFrame(c, body, 1)
				}, nil)
			}
			err := h.accept(accSent)
			if err == nil || !strings.Contains(err.Error(), r.helper) {
				t.Fatalf("helper: got %v, want %s", err, r.helper)
			}
			if got := h.receipt().State; got != r.receipt {
				t.Fatalf("receipt pathname: got %s, want %s", got, r.receipt)
			}
			// "none or partial": an uncommitted tail is never native-accepted.
			if got := h.lastOutcome(); r.journal == "!native-accepted" {
				if got == "native-accepted" || verifyDeliveryEvidence(h.dir, nil) {
					t.Fatalf("uncommitted journal outcome became evidence: %q", got)
				}
			} else if got != r.journal {
				t.Fatalf("journal last outcome: got %q, want %q", got, r.journal)
			}
		})
	}
}

// R26: closed codes only; no native, nonce, prompt or path value escapes.
func TestAcceptancePrivacy(t *testing.T) {
	var logs bytes.Buffer
	restore := accCaptureLog(&logs)
	defer restore()
	check := func(t *testing.T, h *accHarness, err error) {
		t.Helper()
		text := logs.String()
		if err != nil {
			text += err.Error()
		}
		for _, canary := range []string{h.acc.run.NativeSessionID, "0123456789abcdef0123456789abcdef", "staged fixture", h.dir, accNewTurn, "turn-competing", "native detail"} {
			if strings.Contains(text, canary) {
				t.Fatalf("private value escaped: %q", canary)
			}
		}
	}
	for name, setup := range map[string]func(h *accHarness){
		"accepted":    func(*accHarness) {},
		"persistence": func(h *accHarness) { h.acc.fs.fail = accFailAt("receipt-accepted-dir") },
		"malformed": func(h *accHarness) {
			h.f.reply = func(*accFixture, string, map[string]any, int) *accReply {
				return &accReply{raw: []byte(`{"id":%ID%,"result":{"data":1}}`)}
			}
		},
		"expired": func(h *accHarness) { h.acc.deadline = time.Now().Add(-time.Second) },
		"replaced": func(h *accHarness) {
			h.binding = func(int) error { return errors.New("replaced " + h.acc.run.NativeSessionID) }
		},
		"blocked": func(h *accHarness) { h.f.status = "active" },
		"stderr-bytes": func(h *accHarness) {
			h.acc.joined.script = "export HOME=" + shq(h.f.home) + "; python3 -c " + shq(canonicalCodexBridge) + " end; rc=$?; printf 'native detail' >&2; exit $rc"
		},
		"contradiction": func(h *accHarness) {
			h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
				if full == 0 {
					return &accReply{pre: []any{accNotice("turn/started", f.run.NativeSessionID, "turn-competing", "inProgress")}}
				}
				return nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			logs.Reset()
			h := newAccHarness(t)
			setup(h)
			check(t, h, h.accept(accSent))
		})
	}
	// The shadow readout stays closed while this path runs under a scope.
	s := newNativeEvidenceShadow(time.Now)
	h := newAccHarness(t)
	job := &Job{Issue: 7, Agent: "codex", DispatchKey: h.acc.key, Host: "fixture-host", Pane: "w1:p1", Workspace: "w1", RemoteControl: h.acc.run}
	h.host.ShadowScope = s.scope(job)
	accAccepted(t, h, h.accept(accSent))
	b, _ := json.Marshal(s.readout(time.Now()))
	for _, canary := range []string{h.acc.run.NativeSessionID, accNewTurn, "0123456789abcdef", h.dir} {
		if strings.Contains(string(b), canary) {
			t.Fatalf("readout carries a private value: %q", canary)
		}
	}
}

// F1/F3: absent native fields and malformed post-fence notices never count as
// success on the real transport and bridge.
func TestAcceptanceMissingNativeFieldsRefuse(t *testing.T) {
	t.Run("turn-error-absent", func(t *testing.T) {
		h := newAccHarness(t)
		h.newTurn(func(sent string) []any {
			turn := accTurn(accNewTurn, "inProgress", accUser(sent))
			delete(turn, "error")
			return []any{turn}
		})
		accRefused(t, h, h.accept(accSent), "")
		if verifyDeliveryEvidence(h.dir, nil) {
			t.Fatal("audit verifier accepted evidence for a refused delivery")
		}
	})
	t.Run("next-cursor-absent", func(t *testing.T) {
		h := newAccHarness(t)
		h.f.reply = func(f *accFixture, method string, _ map[string]any, full int) *accReply {
			if full < 0 {
				return nil
			}
			turns, _ := accDefaultPage(f, full)
			body, _ := json.Marshal(map[string]any{"data": turns})
			return &accReply{raw: append(append([]byte(`{"id":%ID%,"result":`), body...), '}')}
		}
		accRefused(t, h, h.accept(accSent), "")
	})
	for name, status := range map[string]any{"unknown": "unknown-status", "absent": nil, "empty": ""} {
		t.Run("post-fence-status-"+name, func(t *testing.T) {
			h := newAccHarness(t)
			n := accNotice("turn/completed", h.acc.run.NativeSessionID, accNewTurn, "completed")
			turn := n["params"].(map[string]any)["turn"].(map[string]any)
			if status == nil {
				delete(turn, "status")
			} else {
				turn["status"] = status
			}
			b, _ := json.Marshal(n)
			accAfterFence(h, nil, [][]byte{b})
			accRefused(t, h, h.accept(accSent), "drain")
		})
	}
}

// F2: the committed page record carries the decision evidence, and different
// observed inputs produce different committed records.
func TestAcceptanceJournalCarriesDecisionEvidence(t *testing.T) {
	var pages [][]byte
	for _, matching := range []bool{true, false} {
		h := newAccHarness(t)
		h.newTurn(func(sent string) []any {
			if !matching {
				sent = "different command"
			}
			return []any{accTurn(accNewTurn, "inProgress", accUser(sent))}
		})
		err := h.accept(accSent)
		if (err == nil) != matching {
			t.Fatalf("decision unexpected: %v", err)
		}
		for _, r := range h.journal() {
			if r.Kind != "page" {
				continue
			}
			if r.Page == nil || len(r.Page.Turns) == 0 {
				t.Fatal("committed page lacks decision evidence")
			}
			ev := r.Page.Turns[0]
			sum := sha256.Sum256([]byte(accSent))
			if matching && (ev.InputDigest != hex.EncodeToString(sum[:]) || ev.InputLength != len(accSent) || !ev.ErrorNull || ev.Status != "inProgress") {
				t.Fatalf("committed evidence incomplete: %+v", ev)
			}
			b, _ := json.Marshal(r)
			pages = append(pages, b)
			break
		}
	}
	if len(pages) != 2 || bytes.Equal(pages[0], pages[1]) {
		t.Fatal("different observed inputs share one committed decision record")
	}
}
