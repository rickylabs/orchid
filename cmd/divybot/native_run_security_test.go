package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func nativeRunSocketFixture(t *testing.T) (*Coord, context.CancelFunc, string) {
	t.Helper()
	c := nativeRunFixture(t)
	dir, err := os.MkdirTemp("", "native-run-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if os.Chmod(dir, 0700) != nil {
		t.Fatal("private directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	socket := filepath.Join(dir, "read.sock")
	listener, err := c.listenNativeRuns(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return c, cancel, socket
}

func TestNativeRunSocketStrictLookupAndAckBaseline(t *testing.T) {
	c, _, socket := nativeRunSocketFixture(t)
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	if enc.Encode(map[string]any{"id": "watch", "method": "run.subscribe", "params": map[string]any{}}) != nil {
		t.Fatal("subscribe write")
	}
	var ack struct {
		Result struct {
			Type       string `json:"type"`
			Generation string `json:"generation"`
			Revision   uint64 `json:"revision"`
		} `json:"result"`
	}
	if dec.Decode(&ack) != nil || ack.Result.Type != "subscription_started" {
		t.Fatal("missing subscription ack")
	}
	// Save after ack but before baseline: the invalidation cannot be lost.
	c.st.mu.Lock()
	c.st.Jobs[701].NativeSessionID = "restored-native"
	err = c.st.saveLocked()
	c.st.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer lookup.Close()
	lookup.SetDeadline(time.Now().Add(3 * time.Second))
	json.NewEncoder(lookup).Encode(map[string]any{"id": "baseline", "method": "run.lookup", "params": map[string]any{"keys": []nativeRunQuery{{"701", "codex-701"}}}})
	var baseline struct {
		Result nativeRunLookup `json:"result"`
	}
	if json.NewDecoder(lookup).Decode(&baseline) != nil || len(baseline.Result.Records) != 1 || baseline.Result.Records[0].NativeSessionID != "restored-native" || baseline.Result.Generation != ack.Result.Generation || baseline.Result.Revision <= ack.Result.Revision {
		t.Fatal("baseline missed acknowledged save")
	}
	var change struct {
		Event struct {
			Type     string `json:"type"`
			Revision uint64 `json:"revision"`
		} `json:"event"`
	}
	if dec.Decode(&change) != nil || change.Event.Type != "runs_changed" || change.Event.Revision < baseline.Result.Revision {
		t.Fatal("intervening save notification lost")
	}
}

func TestNativeRunSocketPermissionsAndOwnedCleanup(t *testing.T) {
	_, cancel, socket := nativeRunSocketFixture(t)
	info, err := os.Lstat(socket)
	if err != nil || info.Mode().Perm() != 0600 || !ownerNativeOwned(info, os.Getuid()) {
		t.Fatal("socket is not private and sameUID owned")
	}
	if os.Remove(socket) != nil || os.WriteFile(socket, []byte("foreign-replacement"), 0600) != nil {
		t.Fatal("replacement fixture")
	}
	cancel()
	// Wait for the listener shutdown without assuming scheduler timing.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, e := net.Dial("unix", socket)
		if e != nil {
			break
		}
		conn.Close()
		time.Sleep(time.Millisecond)
	}
	raw, e := os.ReadFile(socket)
	if e != nil || string(raw) != "foreign-replacement" {
		t.Fatal("shutdown removed replacement inode")
	}
}

func TestNativeRunUnsafeParentAndExistingPath(t *testing.T) {
	c := nativeRunFixture(t)
	dir, err := os.MkdirTemp("", "native-run-parent-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, mode := range []os.FileMode{0755, 0711, 0500} {
		os.Chmod(dir, mode)
		if listener, err := c.listenNativeRuns(ctx, filepath.Join(dir, "refused.sock")); err == nil {
			listener.Close()
			t.Fatal("unsafe parent accepted", mode)
		}
	}
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "existing.sock")
	os.WriteFile(path, []byte("existing"), 0600)
	if listener, err := c.listenNativeRuns(ctx, path); err == nil {
		listener.Close()
		t.Fatal("existing endpoint overwritten")
	}
	link := dir + "-symlink"
	if os.Symlink(dir, link) != nil {
		t.Fatal("symlink fixture")
	}
	defer os.Remove(link)
	if listener, err := c.listenNativeRuns(ctx, filepath.Join(link, "refused.sock")); err == nil {
		listener.Close()
		t.Fatal("symlink parent accepted")
	}
}

func TestNativeRunSocketRefusesMalformedAndOversize(t *testing.T) {
	_, _, socket := nativeRunSocketFixture(t)
	for _, raw := range []string{"{\n", `{"id":"x","method":"agent.stop","params":{}}` + "\n", string(make([]byte, nativeRunFrameLimit+1)) + "\n"} {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = conn.Write([]byte(raw))
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		conn.Close()
		var got map[string]json.RawMessage
		if err != nil || json.Unmarshal(line, &got) != nil || got["error"] == nil || got["result"] != nil {
			t.Fatal("invalid frame accepted")
		}
	}
}
