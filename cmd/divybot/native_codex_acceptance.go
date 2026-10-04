package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

// Native acceptance of the Remote Control Codex prompt. The decision uses
// only canonical native evidence committed to a private journal: the exact
// transported bytes are the first and only input of one new turn on the
// prepared thread. One submit, no Enter, no replay and no pane read after
// the baseline. The receipt and journal are native evidence, never delivery
// authority; the caller's on-time commit (commitRemoteCodexDelivery) is.

const (
	acceptanceSchema    = 1
	acceptancePageK     = 8
	acceptanceMaxItems  = 512
	acceptanceMaxText   = 64 << 10
	acceptanceMaxRecord = 64 << 10
	deliveryCommitMark  = "native-v1"
	deliveryReceiptName = "delivery-command.json"
)

func deliveryGap(reason string) error { return goalError("delivery-source-gap-" + reason) }
func deliveryRefused(reason string) error {
	return goalError("delivery-unconfirmed-" + reason)
}

// Injectable durability faults for controls; nil uses the operating system.
type acceptanceFS struct {
	fail func(stage string) error // a non-nil error replaces that durable step
	hold func(stage string)       // test-only rendezvous before a durable step
}

func (fs acceptanceFS) step(stage string) error {
	if fs.hold != nil {
		fs.hold(stage)
	}
	if fs.fail != nil {
		return fs.fail(stage)
	}
	return nil
}
func (fs acceptanceFS) fileSync(stage string, f *os.File) error {
	if err := fs.step(stage); err != nil {
		return err
	}
	return f.Sync()
}
func (fs acceptanceFS) dirSync(stage, dir string) error {
	if err := fs.step(stage); err != nil {
		return err
	}
	return syncDirectory(dir)
}
func (fs acceptanceFS) move(stage, from, to string) error {
	if err := fs.step(stage); err != nil {
		return err
	}
	return os.Rename(from, to)
}

type acceptanceBinding struct {
	DispatchKey string            `json:"dispatchKey"`
	Thread      string            `json:"thread"`
	Host        string            `json:"host"`
	Pane        string            `json:"pane"`
	Workspace   string            `json:"workspace"`
	TUIProcess  *remoteTUIProcess `json:"tuiProcess"`
	Endpoint    string            `json:"endpoint"`
}

type acceptanceBaseline struct {
	IDs        []string `json:"ids"`
	CursorNull bool     `json:"cursorNull"`
}

type deliveryReceipt struct {
	SchemaVersion int                 `json:"schemaVersion"`
	CommandID     string              `json:"commandId"`
	Marker        string              `json:"marker"`
	Digest        string              `json:"digest"`
	Length        int                 `json:"length"`
	Binding       acceptanceBinding   `json:"binding"`
	Generation    string              `json:"generation"`
	Journal       string              `json:"journal"`
	Baseline      *acceptanceBaseline `json:"baseline"`
	Deadline      string              `json:"deadline"`
	State         string              `json:"state"`
	Reason        string              `json:"reason,omitempty"`
	TurnID        string              `json:"turnId,omitempty"`
	JournalSeq    int                 `json:"journalSeq,omitempty"`
	PrefixSha256  string              `json:"prefixSha256,omitempty"`
}

func randomHex() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func expectedOwnerUID(owner *receiptOwner) int {
	if owner != nil {
		return owner.uid
	}
	return os.Getuid()
}

func writeReceiptBody(f *os.File, r deliveryReceipt) error {
	b, err := json.Marshal(r)
	if err != nil || len(b) > acceptanceMaxRecord {
		return errors.New("delivery-receipt-invalid")
	}
	_, err = f.Write(b)
	return err
}

// Exclusive creation is the owned-operation lock: an existing receipt means
// a prior attempt, and never another submit.
func createDeliveryReceipt(dir string, r deliveryReceipt, owner *receiptOwner, fs acceptanceFS) error {
	path := filepath.Join(dir, deliveryReceiptName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600) // guard:receipt-excl
	if err != nil {
		return err
	}
	err = writeReceiptBody(f, r)
	if err == nil {
		err = fs.fileSync("receipt-prepared-file", f)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = transferReceiptOwner(owner, path)
	}
	if err == nil {
		err = fs.dirSync("receipt-prepared-dir", dir) // guard:receipt-prepared-dir
	}
	return err
}

// Strict private read: regular, no symlink, 0600, expected owner, bounded.
func readDeliveryReceipt(dir string, owner *receiptOwner) (deliveryReceipt, error) {
	var r deliveryReceipt
	f, err := os.OpenFile(filepath.Join(dir, deliveryReceiptName), os.O_RDONLY|syscall.O_NOFOLLOW, 0) // guard:receipt-nofollow
	if err != nil {
		return r, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return r, err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || !ok || int(sys.Uid) != expectedOwnerUID(owner) { // guard:receipt-owner-mode
		return r, errors.New("delivery-receipt-invalid")
	}
	if st.Size() > acceptanceMaxRecord { // guard:receipt-size
		return r, errors.New("delivery-receipt-invalid")
	}
	b := make([]byte, acceptanceMaxRecord+1)
	n, _ := f.Read(b)
	if n > acceptanceMaxRecord {
		return r, errors.New("delivery-receipt-invalid")
	}
	if strictJSON(b[:n], &r) != nil { // guard:receipt-strict
		return r, errors.New("delivery-receipt-invalid")
	}
	return r, nil
}

// temp → fsync → rename → directory fsync. renamed reports whether the
// pathname now names the new body, whatever the directory fsync returned.
func updateDeliveryReceipt(dir string, r deliveryReceipt, owner *receiptOwner, fs acceptanceFS, stage string) (renamed bool, err error) {
	f, err := os.CreateTemp(dir, ".delivery-command-")
	if err != nil {
		return false, err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0600); err == nil {
		err = writeReceiptBody(f, r)
	}
	if err == nil {
		err = fs.fileSync(stage+"-file", f)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = transferReceiptOwner(owner, temp)
	}
	if err != nil {
		return false, err
	}
	if err = fs.move(stage+"-rename", temp, filepath.Join(dir, deliveryReceiptName)); err != nil {
		return false, err
	}
	return true, fs.dirSync(stage+"-dir", dir)
}

type journalRecord struct {
	Seq        int      `json:"seq"`
	Kind       string   `json:"kind"`
	Generation string   `json:"generation,omitempty"`
	Command    string   `json:"command,omitempty"`
	Turn       string   `json:"turn,omitempty"`
	Status     string   `json:"status,omitempty"`
	Class      string   `json:"class,omitempty"`
	Turns      []string `json:"turns,omitempty"`
	Outcome    string   `json:"outcome,omitempty"`
}

// Append-only contiguous journal. A record is a decision input only after its
// fsync returned; nothing reads an uncommitted tail.
type deliveryJournal struct {
	path       string
	f          *os.File
	fs         acceptanceFS
	generation string
	command    string
	seq        int
	digest     hash.Hash
	dev, ino   uint64
	notices    []remoteNativeTurn // committed scoped notices: decision inputs
	post       []remoteNativeTurn // committed post-fence notices: history only
}

func createDeliveryJournal(dir, name, generation, command string, owner *receiptOwner, fs acceptanceFS) (*deliveryJournal, error) {
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND|syscall.O_NOFOLLOW, 0600) // guard:journal-excl
	if err != nil {
		return nil, err
	}
	j := &deliveryJournal{path: path, f: f, fs: fs, generation: generation, command: command, digest: sha256.New()}
	st, err := f.Stat()
	sys, ok := st.Sys().(*syscall.Stat_t)
	if err != nil || !ok {
		f.Close()
		return nil, errors.New("delivery-journal-invalid")
	}
	j.dev, j.ino = uint64(sys.Dev), uint64(sys.Ino)
	if err = transferReceiptOwner(owner, path); err == nil {
		err = j.append(journalRecord{Kind: "header", Generation: generation, Command: command})
	}
	if err == nil {
		err = fs.dirSync("journal-dir", dir)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return j, nil
}

func (j *deliveryJournal) append(r journalRecord) error {
	r.Seq = j.seq + 1
	b, err := json.Marshal(r)
	if err != nil || len(b) > acceptanceMaxRecord {
		return errors.New("delivery-journal-invalid")
	}
	b = append(b, '\n')
	if _, err = j.f.Write(b); err != nil {
		return err
	}
	if err = j.fs.fileSync("journal-"+r.Kind, j.f); err != nil { // guard:journal-fsync
		return err
	}
	j.seq = r.Seq
	j.digest.Write(b)
	return nil
}

func (j *deliveryJournal) close() {
	if j != nil && j.f != nil {
		_ = j.f.Close()
	}
}

// Re-verify the committed prefix by path before terminal writes: the same
// file (device and inode), the registered header, contiguous seq and bytes.
func (j *deliveryJournal) verify() (string, error) {
	f, err := os.OpenFile(j.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	sys, ok := st.Sys().(*syscall.Stat_t)
	if err != nil || !ok || uint64(sys.Dev) != j.dev || uint64(sys.Ino) != j.ino { // guard:journal-inode
		return "", errors.New("delivery-journal-identity")
	}
	body, err := readJournalBytes(f)
	if err != nil {
		return "", err
	}
	records, prefix, err := parseJournal(body, j.seq)
	if err != nil || len(records) != j.seq || records[0].Generation != j.generation || records[0].Command != j.command { // guard:journal-header
		return "", errors.New("delivery-journal-identity")
	}
	if prefix != hex.EncodeToString(j.digest.Sum(nil)) { // guard:journal-prefix
		return "", errors.New("delivery-journal-identity")
	}
	return prefix, nil
}

func readJournalBytes(f *os.File) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(f); err != nil || buf.Len() > 16<<20 {
		return nil, errors.New("delivery-journal-invalid")
	}
	return buf.Bytes(), nil
}

// Parse the first n contiguous records and return the sha256 of their bytes.
func parseJournal(body []byte, n int) ([]journalRecord, string, error) {
	var records []journalRecord
	h := sha256.New()
	scan := bufio.NewScanner(bytes.NewReader(body))
	scan.Buffer(make([]byte, 4096), acceptanceMaxRecord+1)
	for len(records) < n && scan.Scan() {
		var r journalRecord
		if strictJSON(scan.Bytes(), &r) != nil || r.Seq != len(records)+1 { // guard:journal-seq
			return nil, "", errors.New("delivery-journal-invalid")
		}
		if len(records) == 0 && r.Kind != "header" {
			return nil, "", errors.New("delivery-journal-invalid")
		}
		h.Write(append(append([]byte(nil), scan.Bytes()...), '\n'))
		records = append(records, r)
	}
	if len(records) != n {
		return nil, "", errors.New("delivery-journal-invalid")
	}
	return records, hex.EncodeToString(h.Sum(nil)), nil
}

// Audit integrity only; never delivery authority.
func verifyDeliveryEvidence(dir string, owner *receiptOwner) bool {
	r, err := readDeliveryReceipt(dir, owner)
	if err != nil || r.State != "native-accepted" || r.JournalSeq < 1 || !privateNativeID(r.Journal) || filepath.Base(r.Journal) != r.Journal {
		return false
	}
	f, err := os.OpenFile(filepath.Join(dir, r.Journal), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	body, err := readJournalBytes(f)
	if err != nil {
		return false
	}
	records, prefix, err := parseJournal(body, r.JournalSeq) // guard:verify-seq
	if err != nil || records[0].Generation != r.Generation || records[0].Command != r.CommandID {
		return false
	}
	return prefix == r.PrefixSha256 // guard:verify-digest
}

// ---- native page decoding and classification ----

type nativeTurnRow struct {
	ID        string
	Status    string
	ItemsView string
	Failed    bool
	Items     []json.RawMessage
}

type turnPage struct {
	Turns      []nativeTurnRow
	CursorNull bool
}

func decodeTurnPage(raw json.RawMessage, full bool) (turnPage, error) {
	var v struct {
		Data       *[]json.RawMessage `json:"data"`
		NextCursor json.RawMessage    `json:"nextCursor"`
	}
	var page turnPage
	if decodeNativeJSON(raw, &v) != nil || v.Data == nil || len(*v.Data) > acceptancePageK { // guard:page-strict
		return page, deliveryGap("page")
	}
	switch c := strings.TrimSpace(string(v.NextCursor)); {
	case c == "" || c == "null":
		page.CursorNull = true
	default:
		var s string
		if json.Unmarshal(v.NextCursor, &s) != nil || s == "" || len(s) > 1024 { // guard:page-cursor
			return page, deliveryGap("cursor")
		}
	}
	seen := map[string]bool{}
	for _, item := range *v.Data {
		var t struct {
			ID        *string            `json:"id"`
			Status    *string            `json:"status"`
			ItemsView string             `json:"itemsView"`
			Error     json.RawMessage    `json:"error"`
			Items     *[]json.RawMessage `json:"items"`
		}
		if decodeNativeJSON(item, &t) != nil || t.ID == nil || t.Status == nil || t.Items == nil {
			return page, deliveryGap("page")
		}
		if !privateNativeID(*t.ID) { // guard:page-id
			return page, deliveryGap("id")
		}
		if seen[*t.ID] { // guard:page-unique
			return page, deliveryGap("id")
		}
		seen[*t.ID] = true
		switch *t.Status {
		case "inProgress", "completed", "failed", "interrupted":
		default:
			return page, deliveryGap("page")
		}
		if full && t.ItemsView != "full" { // guard:page-full
			return page, deliveryGap("page")
		}
		if len(*t.Items) > acceptanceMaxItems { // guard:page-items
			return page, deliveryGap("bounds")
		}
		e := strings.TrimSpace(string(t.Error))
		page.Turns = append(page.Turns, nativeTurnRow{ID: *t.ID, Status: *t.Status, ItemsView: t.ItemsView, Failed: e != "" && e != "null", Items: *t.Items})
	}
	return page, nil
}

func pageIDs(p turnPage) []string {
	ids := make([]string, 0, len(p.Turns))
	for _, t := range p.Turns {
		ids = append(ids, t.ID)
	}
	return ids
}

func baselineFromPage(p turnPage) (*acceptanceBaseline, error) {
	b := len(p.Turns)
	if b == 0 && !p.CursorNull || b < acceptancePageK && !p.CursorNull {
		return nil, deliveryGap("baseline")
	}
	return &acceptanceBaseline{IDs: pageIDs(p), CursorNull: p.CursorNull}, nil
}

// classifyPage returns the number of new turns or a HISTORY-AMBIGUOUS error.
func classifyPage(base *acceptanceBaseline, p turnPage) (int, error) {
	K, b := acceptancePageK, len(base.IDs)
	ids := pageIDs(p)
	if b == 0 {
		if !p.CursorNull { // guard:empty-cursor
			return 0, deliveryRefused("history-ambiguous")
		}
		return len(ids), nil
	}
	n, found := -1, 0
	for i, id := range ids {
		if id == base.IDs[0] {
			n, found = i, found+1
		}
	}
	if found != 1 { // guard:anchor-once
		return 0, deliveryRefused("history-ambiguous")
	}
	v := len(ids) - n
	m := min(v, b)
	if !reflect.DeepEqual(ids[n:n+m], base.IDs[:m]) { // guard:suffix-identity
		return 0, deliveryRefused("history-ambiguous")
	}
	if base.CursorNull {
		if v != min(b, K-n) || p.CursorNull != (n+b <= K) { // guard:suffix-complete
			return 0, deliveryRefused("history-ambiguous")
		}
	} else if v != K-n || p.CursorNull {
		return 0, deliveryRefused("history-ambiguous")
	}
	return n, nil
}

// Exact input: the first item is the only user message, with exactly one
// text part whose bytes are the transported argument.
func candidateRefusal(t nativeTurnRow, digest string, length int) string {
	if t.Status != "inProgress" && t.Status != "completed" { // guard:candidate-status
		return "turn-failed"
	}
	if t.Failed { // guard:candidate-error
		return "turn-failed"
	}
	if len(t.Items) == 0 {
		return "input-mismatch"
	}
	users := 0
	for i, raw := range t.Items {
		var item struct {
			Type    string            `json:"type"`
			Content []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return "input-mismatch"
		}
		if item.Type == "userMessage" {
			users++
		}
		if i == 0 && item.Type != "userMessage" { // guard:candidate-first
			return "input-mismatch"
		}
		if i == 0 {
			if len(item.Content) != 1 { // guard:candidate-single-part
				return "input-mismatch"
			}
			var part struct {
				Type string  `json:"type"`
				Text *string `json:"text"`
			}
			if json.Unmarshal(item.Content[0], &part) != nil || part.Type != "text" || part.Text == nil { // guard:candidate-text
				return "input-mismatch"
			}
			if len(*part.Text) > acceptanceMaxText { // guard:candidate-bound
				return "bounds"
			}
			sum := sha256.Sum256([]byte(*part.Text))
			if hex.EncodeToString(sum[:]) != digest || len(*part.Text) != length { // guard:candidate-digest
				return "input-mismatch"
			}
		}
	}
	if users != 1 { // guard:candidate-only-input
		return "input-mismatch"
	}
	return ""
}

// ---- the acceptance operation ----

type codexAcceptance struct {
	root     string
	key      string
	owner    *receiptOwner
	run      *remoteControlRun
	pane     string
	ws       string
	host     string
	deadline time.Time
	now      func() time.Time
	every    time.Duration
	fs       acceptanceFS
	joined   joinedOptions
	binding  func(context.Context) error // structured occupant/process check; no pane reads
}

type acceptanceRun struct {
	acc      *codexAcceptance
	dir      string
	receipt  deliveryReceipt
	journal  *deliveryJournal
	accepted string
}

func (a *codexAcceptance) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *codexAcceptance) expired(ctx context.Context) bool {
	return ctx.Err() != nil || !a.clock().Before(a.deadline)
}

// accept runs the whole operation for the exact transported string sent.
func (h Host) acceptCodexPrompt(ctx context.Context, a *codexAcceptance, sent string, submit func(context.Context) error) error {
	if a == nil || a.run == nil || h.RemoteRun == nil || a.run.NativeSessionID != h.RemoteRun.NativeSessionID || !digestPattern.MatchString(a.key) || !privateReceiptRoot(a.root) {
		return deliveryRefused("binding-invalid")
	}
	dir := filepath.Join(a.root, a.key, "record")
	if !privateReceiptRoot(dir) {
		return deliveryRefused("binding-invalid")
	}
	command, err := randomHex()
	generation, gerr := randomHex()
	if err != nil || gerr != nil {
		return deliveryRefused("binding-invalid")
	}
	sum := sha256.Sum256([]byte(sent))
	marker := ""
	if lines := strings.SplitN(sent, "\n", 2); len(lines) > 0 {
		marker = shaText([]byte(lines[0]))
	}
	r := &acceptanceRun{acc: a, dir: dir, receipt: deliveryReceipt{
		SchemaVersion: acceptanceSchema, CommandID: command, Marker: marker,
		Digest: hex.EncodeToString(sum[:]), Length: len(sent), // guard:exact-argument
		Binding: acceptanceBinding{DispatchKey: a.key, Thread: a.run.NativeSessionID, Host: a.host, Pane: a.pane, Workspace: a.ws,
			TUIProcess: a.run.TUIProcess, Endpoint: "canonical-codex-app-server"},
		Generation: generation, Journal: "delivery-journal-" + generation + ".jsonl",
		Deadline: a.deadline.UTC().Format(time.RFC3339Nano), State: "prepared",
	}}
	// 1–2: exclusive durable prepared receipt, then strict re-read.
	if err := createDeliveryReceipt(dir, r.receipt, a.owner, a.fs); err != nil {
		return deliveryRefused("receipt")
	}
	if got, err := readDeliveryReceipt(dir, a.owner); err != nil || !reflect.DeepEqual(got, r.receipt) { // guard:receipt-verify
		return deliveryRefused("receipt")
	}
	j, err := createDeliveryJournal(dir, r.receipt.Journal, generation, command, a.owner, a.fs)
	if err != nil {
		r.terminal("journal")
		return deliveryRefused("journal")
	}
	r.journal = j
	defer j.close()
	notice := func(n remoteNativeTurn) error {
		if n.ThreadID != a.run.NativeSessionID {
			return nil
		}
		if err := j.append(journalRecord{Kind: "notice", Turn: n.ID, Status: n.Status}); err != nil {
			return deliveryGap("journal")
		}
		j.notices = append(j.notices, n)
		return nil
	}
	drain := func(m map[string]json.RawMessage) error {
		if _, response := m["id"]; response {
			return deliveryGap("drain")
		}
		var method string
		if json.Unmarshal(m["method"], &method) != nil {
			return deliveryGap("drain")
		}
		if method != "turn/started" && method != "turn/completed" {
			return nil
		}
		var n struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if decodeNativeJSON(m["params"], &n) != nil || !privateNativeID(n.ThreadID) || !privateNativeID(n.Turn.ID) {
			return deliveryGap("drain")
		}
		if n.ThreadID != a.run.NativeSessionID {
			return nil
		}
		if err := j.append(journalRecord{Kind: "post-fence", Turn: n.Turn.ID, Status: n.Turn.Status}); err != nil {
			return deliveryGap("journal")
		}
		j.post = append(j.post, remoteNativeTurn{n.ThreadID, n.Turn.ID, n.Turn.Status}) // guard:post-fence-history
		return nil
	}
	h.RemoteRun = a.run
	submitted := false
	err = h.withJoinedCanonicalConnection(ctx, a.run.NativeSessionID, a.joined, notice, func(p *goalRPC) error {
		return r.loop(ctx, p, sent, func(ctx context.Context) error {
			submitted = true
			return submit(ctx)
		})
	}, drain)
	if err != nil {
		reason := "source-gap"
		var g goalError
		if errors.As(err, &g) && strings.HasPrefix(string(g), "delivery-unconfirmed-") {
			reason = strings.TrimPrefix(string(g), "delivery-unconfirmed-")
		}
		r.terminal(reason)
		if !submitted {
			return deliveryRefused("pre-effect")
		}
		return err
	}
	return r.publish(ctx)
}

// Best-effort refusal records; the receipt names the last renamed state.
func (r *acceptanceRun) terminal(reason string) { r.refuse(reason, true) }

func (r *acceptanceRun) refuse(reason string, journal bool) {
	if journal && r.journal != nil {
		_ = r.journal.append(journalRecord{Kind: "outcome", Outcome: "unconfirmed", Class: reason})
	}
	if r.receipt.State == "prepared" || r.receipt.State == "attempted" {
		next := r.receipt
		next.State, next.Reason = "unconfirmed", reason
		if renamed, _ := updateDeliveryReceipt(r.dir, next, r.acc.owner, r.acc.fs, "receipt-unconfirmed"); renamed {
			r.receipt = next
		}
	}
}

func (r *acceptanceRun) loop(ctx context.Context, p *goalRPC, sent string, submit func(context.Context) error) error {
	a, j := r.acc, r.journal
	// Baseline: native idle thread and an anchored or empty-complete page.
	raw, err := p.request("thread/read", map[string]any{"threadId": p.thread, "includeTurns": false})
	var thread struct {
		Thread struct {
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if err != nil || json.Unmarshal(raw, &thread) != nil {
		return deliveryGap("baseline")
	}
	if thread.Thread.Status.Type != "idle" { // guard:baseline-idle
		return deliveryRefused("baseline-active")
	}
	raw, err = p.request("thread/turns/list", map[string]any{"threadId": p.thread, "limit": acceptancePageK, "sortDirection": "desc", "itemsView": "notLoaded"})
	if err != nil {
		return deliveryGap("baseline")
	}
	page, err := decodeTurnPage(raw, false)
	if err != nil {
		return deliveryGap("baseline")
	}
	base, err := baselineFromPage(page)
	if err != nil {
		return err
	}
	if err := j.append(journalRecord{Kind: "baseline", Turns: base.IDs, Class: map[bool]string{true: "cursor-null", false: "cursor-more"}[base.CursorNull]}); err != nil {
		return deliveryGap("journal")
	}
	// 3: attempted, fully durable, before the effect.
	next := r.receipt
	next.State, next.Baseline = "attempted", base
	if renamed, err := updateDeliveryReceipt(r.dir, next, a.owner, a.fs, "receipt-attempted"); err != nil { // guard:attempted-durable
		if renamed {
			r.receipt = next
		}
		return deliveryRefused("receipt")
	}
	r.receipt = next
	if a.expired(ctx) {
		return deliveryRefused("deadline")
	}
	// 4: the single effect. A failed or lost acknowledgement is terminal.
	if err := submit(ctx); err != nil { // guard:lost-ack
		return deliveryRefused("submit-uncertain")
	}
	read := func(kind string) (turnPage, int, error) {
		raw, err := p.request("thread/turns/list", map[string]any{"threadId": p.thread, "limit": acceptancePageK, "sortDirection": "desc", "itemsView": "full"})
		if err != nil {
			return turnPage{}, 0, err
		}
		page, err := decodeTurnPage(raw, true)
		if err != nil {
			return turnPage{}, 0, err // guard:sticky-gap
		}
		n, cerr := classifyPage(base, page)
		class := "complete"
		if cerr != nil {
			class = "history-ambiguous"
		}
		if err := j.append(journalRecord{Kind: kind, Class: class, Turns: pageIDs(page)[:max(n, 0)]}); err != nil {
			return turnPage{}, 0, deliveryGap("journal")
		}
		return page, n, cerr
	}
	reconcile := func(turn string) error {
		for _, n := range j.notices { // guard:sticky-notices
			if n.ID != turn || (n.Status != "inProgress" && n.Status != "completed") {
				return deliveryRefused("contradiction")
			}
		}
		return nil
	}
	for {
		if a.expired(ctx) {
			return deliveryRefused("deadline")
		}
		page, n, err := read("page")
		if err != nil {
			return err
		}
		if n == 0 { // VALID-PENDING: the only retryable state
			if !goalSleep(ctx, a.every) {
				return deliveryRefused("deadline")
			}
			continue
		}
		if n != 1 { // guard:single-new-turn
			return deliveryRefused("multiple-new-turns")
		}
		t := page.Turns[0]
		if reason := candidateRefusal(t, r.receipt.Digest, r.receipt.Length); reason != "" {
			return deliveryRefused(reason)
		}
		if err := reconcile(t.ID); err != nil {
			return err
		}
		if err := a.binding(ctx); err != nil { // step 2
			return deliveryRefused("binding-changed")
		}
		fence, fn, err := read("fence") // guard:fence
		if err != nil {
			return err
		}
		if fn != 1 || fence.Turns[0].ID != t.ID || candidateRefusal(fence.Turns[0], r.receipt.Digest, r.receipt.Length) != "" {
			return deliveryRefused("contradiction")
		}
		if err := reconcile(t.ID); err != nil { // guard:final-reconcile
			return err
		}
		if err := a.binding(ctx); err != nil { // guard:binding-recheck
			return deliveryRefused("binding-changed")
		}
		if a.expired(ctx) {
			return deliveryRefused("deadline")
		}
		r.accepted = t.ID
		return nil
	}
}

func goalSleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// §3.7: C1, journal outcome, receipt native-accepted, C2. Native evidence only.
func (r *acceptanceRun) publish(ctx context.Context) error {
	a, j := r.acc, r.journal
	if r.accepted == "" || a.expired(ctx) { // guard:c1
		r.terminal("deadline")
		return deliveryRefused("deadline")
	}
	if _, err := j.verify(); err != nil {
		r.terminal("journal")
		return deliveryGap("journal")
	}
	if err := j.append(journalRecord{Kind: "outcome", Outcome: "native-accepted", Turn: r.accepted}); err != nil {
		r.terminal("journal")
		return deliveryRefused("journal")
	}
	prefix, err := j.verify()
	if err != nil {
		r.terminal("journal")
		return deliveryGap("journal")
	}
	next := r.receipt
	next.State, next.TurnID, next.JournalSeq, next.PrefixSha256 = "native-accepted", r.accepted, j.seq, prefix
	renamed, err := updateDeliveryReceipt(r.dir, next, a.owner, a.fs, "receipt-accepted")
	if err != nil {
		if renamed { // guard:accepted-dir-fsync
			r.receipt = next
			_ = j.append(journalRecord{Kind: "outcome", Outcome: "persistence-uncertain"})
			return deliveryRefused("receipt-persistence-uncertain")
		}
		r.refuse("receipt", false) // the journal keeps native-accepted as evidence
		return deliveryRefused("receipt")
	}
	r.receipt = next
	if a.expired(ctx) { // guard:c2
		_ = j.append(journalRecord{Kind: "outcome", Outcome: "late"})
		return deliveryRefused("deadline")
	}
	return nil
}
