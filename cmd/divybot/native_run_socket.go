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
	if rpc.Method == "run.subscribe" {
		var p map[string]json.RawMessage
		if strictJSON(rpc.Params, &p) != nil || p == nil || len(p) != 0 {
			return rpc, nil, errMatrix
		}
		return rpc, nil, nil
	}
	var p struct {
		Keys []nativeRunQuery `json:"keys"`
	}
	if rpc.Method != "run.lookup" || ownerNativeStrictJSON(rpc.Params, &p) != nil ||
		p.Keys == nil || len(p.Keys) > nativeRunBatchLimit {
		return rpc, nil, errMatrix
	}
	seen := map[string]bool{}
	for _, q := range p.Keys {
		if !validNativeRunQuery(q) || seen[q.Key] {
			return rpc, nil, errMatrix
		}
		seen[q.Key] = true
	}
	return rpc, p.Keys, nil
}

// The protocol has only two read methods. There is no path from client input to a coordinator action.
func (c *Coord) serveNativeRunConnection(ctx context.Context, conn *net.UnixConn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	raw, err := bufio.NewReader(io.LimitReader(conn, nativeRunFrameLimit+1)).ReadBytes('\n')
	if err != nil || len(raw) > nativeRunFrameLimit {
		_ = json.NewEncoder(conn).Encode(nativeRunFailure(""))
		return
	}
	rpc, keys, err := decodeNativeRunRPC(raw)
	if err != nil {
		_ = json.NewEncoder(conn).Encode(nativeRunFailure(rpc.ID))
		return
	}
	enc := json.NewEncoder(conn)
	if rpc.Method == "run.lookup" {
		_ = enc.Encode(map[string]any{"id": rpc.ID, "result": c.lookupNativeRuns(keys)})
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

// Native identity is private: require a private canonical parent and never replace a foreign socket.
func (c *Coord) listenNativeRuns(ctx context.Context, path string) (*net.UnixListener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errMatrix
	}
	parent, err := os.Lstat(filepath.Dir(path))
	canonical, realErr := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil || realErr != nil || !parent.IsDir() || parent.Mode().Perm()&0077 != 0 || canonical != filepath.Dir(path) {
		return nil, errMatrix
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, errMatrix
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	owned, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	c.st.mu.Lock()
	c.st.nativeRuns = &nativeRunWatch{generation: c.instance, subs: map[chan struct{}]struct{}{}}
	c.st.mu.Unlock()
	go func() {
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
				go func() { defer func() { <-slots }(); c.serveNativeRunConnection(ctx, conn) }()
			default:
				_ = conn.Close()
			}
		}
	}()
	return listener, nil
}

func (c *Coord) startNativeRunReads(ctx context.Context) bool {
	if c.cfg.NativeRunSocket == "" {
		return true
	}
	if _, err := c.listenNativeRuns(ctx, c.cfg.NativeRunSocket); err != nil {
		log.Printf("native-run-source unavailable")
		return false
	}
	return true
}
