package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A scripted canonical daemon over the real AF_UNIX socket and the real
// bridge (end mode). Only synthetic identifiers; no live endpoint.

type accReply struct {
	pre   []any          // notices written before the response
	raw   []byte         // replaces the response frame body (bridge-valid text)
	post  [][]byte       // text frames written in the same socket write as the response
	after func(net.Conn) // runs after the response write
}

type accFixture struct {
	t         *testing.T
	run       *remoteControlRun
	home      string
	sockPath  string
	listener  net.Listener
	mu        sync.Mutex
	conns     int
	sent      string
	sentFile  string // a fake herdr's recorded prompt, when set
	baseline  []any
	baseCur   any
	status    string
	fullReads int
	page      func(f *accFixture, i int) ([]any, any)
	reply     func(f *accFixture, method string, params map[string]any, full int) *accReply
	closed    chan struct{}
	closeOnce sync.Once
}

func accTurn(id, status string, items ...any) map[string]any {
	if items == nil {
		items = []any{}
	}
	return map[string]any{"id": id, "status": status, "itemsView": "full", "items": items, "error": nil}
}
func accUser(text string) map[string]any {
	return map[string]any{"type": "userMessage", "id": "user-item", "content": []any{map[string]any{"type": "text", "text": text, "text_elements": []any{}}}}
}
func accAgent() map[string]any {
	return map[string]any{"type": "agentMessage", "id": "agent-item", "text": "working on it"}
}
func accBaseTurn(id string) map[string]any {
	return map[string]any{"id": id, "status": "completed", "itemsView": "notLoaded", "items": []any{}, "error": nil}
}
func accNotice(method, thread, turn, status string) map[string]any {
	return map[string]any{"method": method, "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": status}}}
}

const accNewTurn = "turn-new"

func (f *accFixture) getSent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sentFile != "" {
		b, _ := os.ReadFile(f.sentFile)
		return string(b)
	}
	return f.sent
}
func (f *accFixture) setSent(s string) {
	f.mu.Lock()
	f.sent = s
	f.mu.Unlock()
}
func (f *accFixture) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

// Default page: the baseline until the submit, then the exact new turn.
func accDefaultPage(f *accFixture, i int) ([]any, any) {
	turns := []any{}
	if sent := f.getSent(); sent != "" {
		turns = append(turns, accTurn(accNewTurn, "inProgress", accUser(sent), accAgent()))
	}
	for _, b := range f.baseline {
		row := map[string]any{}
		for k, v := range b.(map[string]any) {
			row[k] = v
		}
		row["itemsView"] = "full"
		turns = append(turns, row)
	}
	return turns, f.baseCur
}

func newAccFixture(t *testing.T, run *remoteControlRun) *accFixture {
	t.Helper()
	root, err := os.MkdirTemp("", "rc-")
	if err != nil {
		t.Fatal("socket fixture unavailable")
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("CODEX_HOME", "")
	dir := filepath.Join(root, ".codex", "app-server-control")
	if os.MkdirAll(dir, 0700) != nil {
		t.Fatal("fixture directory unavailable")
	}
	f := &accFixture{t: t, run: run, home: root, sockPath: filepath.Join(dir, "app-server-control.sock"), status: "idle",
		baseline: []any{accBaseTurn("base-2"), accBaseTurn("base-1")}, page: accDefaultPage, closed: make(chan struct{})}
	f.listen()
	return f
}

func (f *accFixture) listen() {
	l, err := net.Listen("unix", f.sockPath)
	if err != nil || os.Chmod(f.sockPath, 0600) != nil {
		f.t.Fatal("fixture socket unavailable")
	}
	f.listener = l
	f.t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns++
			f.mu.Unlock()
			go f.serve(c)
		}
	}()
}

func (f *accFixture) serve(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(60 * time.Second))
	reader := bufio.NewReader(connection)
	key := ""
	for lines := 0; lines < 64; lines++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Sec-WebSocket-Key:") {
			key = strings.TrimSpace(strings.TrimPrefix(line, "Sec-WebSocket-Key:"))
		}
		if line == "" {
			break
		}
	}
	digest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if _, err := fmt.Fprintf(connection, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(digest[:])); err != nil {
		return
	}
	for frames := 0; frames < 4096; frames++ {
		b, opcode, err := readNativeFixtureFrame(reader)
		if err != nil {
			return
		}
		if opcode == 8 {
			f.closeOnce.Do(func() { close(f.closed) })
			return
		}
		if opcode == 10 {
			continue
		}
		var q struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(b, &q) != nil {
			return
		}
		if q.ID == 0 {
			continue
		}
		var result any = map[string]any{}
		full := -1
		switch q.Method {
		case "initialize":
			result = map[string]string{"userAgent": "fixture"}
		case "thread/read":
			thread := remoteThreadFixture(f.run)
			thread["thread"].(map[string]any)["status"] = map[string]string{"type": f.status}
			result = thread
		case "thread/turns/list":
			if q.Params["itemsView"] == "notLoaded" {
				result = map[string]any{"data": f.baseline, "nextCursor": f.baseCur}
			} else {
				f.mu.Lock()
				full = f.fullReads
				f.fullReads++
				f.mu.Unlock()
				turns, cursor := f.page(f, full)
				result = map[string]any{"data": turns, "nextCursor": cursor}
			}
		}
		var r *accReply
		if f.reply != nil {
			r = f.reply(f, q.Method, q.Params, full)
		}
		if r == nil {
			r = &accReply{}
		}
		for _, notice := range r.pre {
			body, _ := json.Marshal(notice)
			if nativeFixtureFrame(connection, body, 1) != nil {
				return
			}
		}
		body := r.raw
		if body != nil {
			body = bytes.ReplaceAll(body, []byte("%ID%"), []byte(fmt.Sprint(q.ID)))
		}
		if body == nil {
			body, _ = json.Marshal(response(q.ID, result))
		}
		var out bytes.Buffer
		_ = nativeFixtureFrame(&out, body, 1)
		for _, extra := range r.post {
			_ = nativeFixtureFrame(&out, extra, 1)
		}
		if _, err := connection.Write(out.Bytes()); err != nil {
			return
		}
		if r.after != nil {
			r.after(connection)
		}
	}
}

// ---- acceptance harness ----

type accHarness struct {
	t         *testing.T
	f         *accFixture
	host      Host
	acc       *codexAcceptance
	dir       string
	submits   int
	submitErr error
	onSubmit  func()
	binding   func(call int) error
	calls     int
	after     func(t *testing.T)
}

func newAccHarness(t *testing.T) *accHarness {
	t.Helper()
	run := syntheticRemoteRun(t)
	f := newAccFixture(t, run)
	root := filepath.Join(t.TempDir(), "receipts")
	key := strings.Repeat("c", 64)
	dir := filepath.Join(root, key, "record")
	for _, d := range []string{root, filepath.Join(root, key), dir} {
		if os.Mkdir(d, 0700) != nil || os.Chmod(d, 0700) != nil {
			t.Fatal("receipt fixture unavailable")
		}
	}
	h := &accHarness{t: t, f: f, dir: dir}
	h.host = Host{Home: f.home, Name: "fixture-host", CanonicalCodex: true, RemoteRun: run}
	h.acc = &codexAcceptance{root: root, key: key, run: run, pane: "w1:p1", ws: "w1", host: "fixture-host",
		deadline: time.Now().Add(20 * time.Second), every: 5 * time.Millisecond,
		binding: func(ctx context.Context) error {
			h.calls++
			if h.binding != nil {
				return h.binding(h.calls)
			}
			return nil
		}}
	return h
}

func (h *accHarness) submit(sent string) func(context.Context) error {
	return func(context.Context) error {
		h.submits++
		h.f.setSent(sent)
		if h.onSubmit != nil {
			h.onSubmit()
		}
		return h.submitErr
	}
}

func (h *accHarness) accept(sent string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return h.host.acceptCodexPrompt(ctx, h.acc, sent, h.submit(sent))
}

func (h *accHarness) receipt() deliveryReceipt {
	r, _ := readDeliveryReceipt(h.dir, nil)
	return r
}

func (h *accHarness) journal() []journalRecord {
	r := h.receipt()
	if r.Journal == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(h.dir, r.Journal))
	if err != nil {
		return nil
	}
	var out []journalRecord
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var rec journalRecord
		if json.Unmarshal([]byte(line), &rec) == nil {
			out = append(out, rec)
		}
	}
	return out
}

func (h *accHarness) lastOutcome() string {
	out := ""
	for _, r := range h.journal() {
		if r.Kind == "outcome" {
			out = r.Outcome
		}
	}
	return out
}

const accSent = "Assignment delivery marker: orch-goal-0123456789abcdef0123456789abcdef\nRead the staged fixture assignment.\nAssignment delivery marker: orch-goal-0123456789abcdef0123456789abcdef"

var _ = io.Discard

func accCaptureLog(w io.Writer) func() {
	prev := log.Writer()
	log.SetOutput(w)
	return func() { log.SetOutput(prev) }
}
