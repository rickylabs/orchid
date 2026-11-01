package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unicode/utf8"
)

const nativeRunFrameLimit = 64 * 1024
const nativeRunConnections = 32

type nativeRunRPC struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func decodeNativeRunRPC(raw []byte) (nativeRunRPC, []nativeRunQuery, error) {
	var rpc nativeRunRPC
	if len(raw) > nativeRunFrameLimit || !utf8.Valid(raw) || strictJSON(raw, &rpc) != nil ||
		rpc.ID == "" || len(rpc.ID) > 64 {
		return rpc, nil, errMatrix
	}
	var fields map[string]json.RawMessage
	if strictJSON(raw, &fields) != nil || len(fields) != 3 {
		return rpc, nil, errMatrix
	}
	for _, name := range []string{"id", "method", "params"} {
		if v, ok := fields[name]; !ok || string(v) == "null" {
			return rpc, nil, errMatrix
		}
	}
	if rpc.Method == "run.subscribe" {
		var p map[string]json.RawMessage
		if strictJSON(rpc.Params, &p) != nil || p == nil || len(p) != 0 {
			return rpc, nil, errMatrix
		}
		return rpc, nil, nil
	}
	if rpc.Method != "run.lookup" {
		return rpc, nil, errMatrix
	}
	keys, err := decodeNativeRunKeys(rpc.Params)
	return rpc, keys, err
}

func decodeNativeRunKeys(raw json.RawMessage) ([]nativeRunQuery, error) {
	var p struct {
		Keys []nativeRunQuery `json:"keys"`
	}
	if len(raw) > nativeRunFrameLimit || ownerNativeStrictJSON(raw, &p) != nil || p.Keys == nil || len(p.Keys) > nativeRunBatchLimit {
		return nil, errMatrix
	}
	seen := map[string]bool{}
	for _, q := range p.Keys {
		if !validNativeRunQuery(q) || seen[q.Key] {
			return nil, errMatrix
		}
		seen[q.Key] = true
	}
	return p.Keys, nil
}

// The protocol has only two read methods. There is no path from client input to a coordinator action.
func (c *Coord) serveNativeRunConnection(ctx context.Context, conn *net.UnixConn, allowedUID int) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	uid, peerErr := ownerNativePeerUID(conn)
	if peerErr != nil || uid != allowedUID {
		return
	}
	raw, err := bufio.NewReader(io.LimitReader(conn, nativeRunFrameLimit+1)).ReadBytes('\n')
	if err != nil || len(raw) > nativeRunFrameLimit {
		_ = json.NewEncoder(conn).Encode(nativeRunFailure(""))
		return
	}
	rpc, _, err := decodeNativeRunRPC(raw)
	if err != nil {
		_ = json.NewEncoder(conn).Encode(nativeRunFailure(rpc.ID))
		return
	}
	enc := json.NewEncoder(conn)
	if rpc.Method == "run.lookup" {
		result, e := c.st.nativeRunsLookup(rpc.Params)
		if e != nil {
			_ = enc.Encode(nativeRunFailure(rpc.ID))
			return
		}
		_ = enc.Encode(map[string]any{"id": rpc.ID, "result": result})
		return
	}
	changed := make(chan struct{}, 1)
	w := c.st.nativeRuns
	w.mu.Lock()
	w.subs[changed] = struct{}{}
	ack := map[string]any{"type": "subscription_started", "generation": w.generation, "revision": w.revision}
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.subs, changed); w.mu.Unlock() }()
	if enc.Encode(map[string]any{"id": rpc.ID, "result": ack}) != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	peerClosed := make(chan struct{})
	go func() { var b [1]byte; _, _ = conn.Read(b[:]); close(peerClosed) }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-peerClosed:
			return
		case <-changed:
			w.mu.Lock()
			notice := map[string]any{"type": "runs_changed", "generation": w.generation, "revision": w.revision}
			w.mu.Unlock()
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if enc.Encode(map[string]any{"event": notice}) != nil {
				return
			}
		}
	}
}

// Native identity requires a canonical operator-provisioned parent and an authorized kernel peer.
func (c *Coord) listenNativeRuns(ctx context.Context, path string) (*net.UnixListener, error) {
	listener, _, err := c.listenNativeRunsDone(ctx, path)
	return listener, err
}

// errNativeRunLive marks an endpoint another listener still serves; it is never unlinked.
var errNativeRunLive = errors.New("native-run endpoint live")

// clearStaleNativeRun removes an endpoint left by a crash, kill or --once exit.
// Only this UID's 0600/0660 socket that refuses connections is removed, and
// only if the probed inode is still the one at the path.
func clearStaleNativeRun(path string, uid int) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errMatrix
	}
	if (info.Mode().Perm() != 0600 && info.Mode().Perm() != nativeRunSocketMode) || !ownerNativeOwned(info, uid) {
		return errMatrix
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		_ = conn.Close()
		return errNativeRunLive
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return errMatrix // busy or unknown: treat as live
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return errMatrix
	}
	if os.Remove(path) != nil {
		return errMatrix
	}
	log.Printf("native-run-source replaced stale endpoint")
	return nil
}

// listenNativeRunsDone also returns a channel closed once the endpoint is
// released after ctx ends, so a short-lived caller can wait for its cleanup.
func (c *Coord) listenNativeRunsDone(ctx context.Context, path string) (*net.UnixListener, <-chan struct{}, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, nil, errMatrix
	}
	uid := os.Getuid()
	if err := nativeRunParent(filepath.Dir(path), uid); err != nil {
		return nil, nil, err
	}
	allowedUID, err := nativeRunPeerUID()
	if err != nil {
		return nil, nil, err
	}
	if err := clearStaleNativeRun(path, uid); err != nil {
		return nil, nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, nil, errMatrix
	}
	listener.SetUnlinkOnClose(false)
	created, err := os.Lstat(path)
	cleanup := func() {
		listener.Close()
		if current, e := os.Lstat(path); e == nil && created != nil && os.SameFile(current, created) {
			_ = os.Remove(path)
		}
	}
	if err != nil || ownerNativeSetSocketMode(path, created, uid, nativeRunSocketMode) != nil {
		cleanup()
		return nil, nil, errMatrix
	}
	owned, err := os.Lstat(path)
	if err != nil || !os.SameFile(created, owned) || owned.Mode()&os.ModeSocket == 0 {
		cleanup()
		return nil, nil, errMatrix
	}
	if owned.Mode().Perm() != nativeRunSocketMode || !ownerNativeOwned(owned, uid) {
		cleanup()
		return nil, nil, errMatrix
	}
	c.st.mu.Lock()
	c.st.nativeRunInbox = c.cfg.Inbox
	c.st.nativeRuns = &nativeRunWatch{generation: c.instance, subs: map[chan struct{}]struct{}{}}
	c.st.mu.Unlock()
	released := make(chan struct{})
	go func() {
		defer close(released)
		<-ctx.Done()
		_ = listener.Close()
		if current, err := os.Lstat(path); err == nil && os.SameFile(current, owned) {
			_ = os.Remove(path)
		}
	}()
	slots := make(chan struct{}, nativeRunConnections)
	go func() {
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
				go func() { defer func() { <-slots }(); c.serveNativeRunConnection(ctx, conn, allowedUID) }()
			default:
				_ = conn.Close()
			}
		}
	}()
	return listener, released, nil
}

// startNativeRunReads never gates supervision. Without a usable endpoint the
// native read is unavailable (no socket answers) and the coordinator keeps
// ticking and resuming runs. The returned stop releases the endpoint and
// waits for its owned-inode cleanup; it is safe to call more than once.
func (c *Coord) startNativeRunReads(ctx context.Context) (stop func()) {
	if c.cfg.NativeRunSocket == "" {
		return func() {}
	}
	readCtx, cancel := context.WithCancel(ctx)
	_, released, err := c.listenNativeRunsDone(readCtx, c.cfg.NativeRunSocket)
	if err != nil {
		cancel()
		reason := "endpoint-refused"
		if errors.Is(err, errNativeRunLive) {
			reason = "endpoint-live"
		} else if errors.Is(err, errNativeRunParentMissing) {
			reason = errNativeRunParentMissing.Error()
		} else if errors.Is(err, errNativeRunPeerInvalid) {
			reason = errNativeRunPeerInvalid.Error()
		}
		log.Printf("native-run-source unavailable reason=%s; supervision continues", reason)
		return func() {}
	}
	return func() { cancel(); <-released }
}

// runOnce is the --once pass: one tick, then the read endpoint is removed
// before exit so no stale socket is left for the next start.
func (c *Coord) runOnce(ctx context.Context) {
	stopReads := c.startNativeRunReads(ctx)
	defer stopReads()
	c.startOwnerNativeGrants(ctx)
	c.tick(ctx)
	c.st.save()
}
