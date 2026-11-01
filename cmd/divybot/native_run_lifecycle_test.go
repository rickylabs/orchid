package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func nativeRunPrivateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nrl-")
	if err != nil || os.Chmod(dir, 0700) != nil {
		t.Fatal("private directory")
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// A crashed or killed coordinator leaves its bound socket inode behind.
func nativeRunStaleSocket(t *testing.T, path string, mode os.FileMode) os.FileInfo {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	if os.Chmod(path, mode) != nil || l.Close() != nil {
		t.Fatal("stale socket fixture")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatal("stale socket fixture")
	}
	return info
}

func nativeRunDialLookup(t *testing.T, path string) nativeRunLookup {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	json.NewEncoder(conn).Encode(map[string]any{"id": "l", "method": "run.lookup", "params": map[string]any{"keys": []nativeRunQuery{{"701", "codex-701"}}}})
	var got struct {
		Result nativeRunLookup `json:"result"`
	}
	if json.NewDecoder(conn).Decode(&got) != nil {
		t.Fatal("lookup unanswered")
	}
	return got.Result
}

func nativeRunKept(t *testing.T, path string, want os.FileInfo) {
	t.Helper()
	got, err := os.Lstat(path)
	if err != nil || !os.SameFile(got, want) {
		t.Fatal("endpoint inode was unlinked or replaced")
	}
}

func TestNativeRunStaleCrashSocketReplaced(t *testing.T) {
	c := nativeRunFixture(t)
	path := filepath.Join(nativeRunPrivateDir(t), "read.sock")
	stale := nativeRunStaleSocket(t, path, 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := c.listenNativeRuns(ctx, path)
	if err != nil {
		t.Fatal("stale crash socket blocked the read endpoint", err)
	}
	defer listener.Close()
	fresh, err := os.Lstat(path)
	if err != nil || os.SameFile(fresh, stale) || fresh.Mode().Perm() != 0660 {
		t.Fatal("stale inode not replaced by a private endpoint")
	}
	if got := nativeRunDialLookup(t, path); len(got.Records) != 1 {
		t.Fatal("replacement endpoint does not serve lookups")
	}
}

func TestNativeRunUnsafeStaleEndpointKept(t *testing.T) {
	c := nativeRunFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := nativeRunPrivateDir(t)
	for _, mode := range []os.FileMode{0644, 0666, 0700} {
		path := filepath.Join(dir, "mode.sock")
		stale := nativeRunStaleSocket(t, path, mode)
		if l, err := c.listenNativeRuns(ctx, path); err == nil {
			l.Close()
			t.Fatal("non-private stale socket replaced", mode)
		}
		nativeRunKept(t, path, stale)
		os.Remove(path)
	}
	// A socket owned by another UID is never touched, even when it refuses connections.
	path := filepath.Join(dir, "foreign.sock")
	stale := nativeRunStaleSocket(t, path, 0600)
	if clearStaleNativeRun(path, os.Getuid()+1) == nil {
		t.Fatal("foreign-owned socket accepted for removal")
	}
	nativeRunKept(t, path, stale)
	// A symlink to a stale socket is not a socket at the endpoint path.
	link := filepath.Join(dir, "link.sock")
	if os.Symlink(path, link) != nil {
		t.Fatal("symlink fixture")
	}
	linkInfo, _ := os.Lstat(link)
	if l, err := c.listenNativeRuns(ctx, link); err == nil {
		l.Close()
		t.Fatal("symlinked endpoint replaced")
	}
	nativeRunKept(t, link, linkInfo)
	nativeRunKept(t, path, stale)
}

func TestNativeRunLiveSocketNeverUnlinked(t *testing.T) {
	owner := nativeRunFixture(t)
	path := filepath.Join(nativeRunPrivateDir(t), "read.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := owner.listenNativeRuns(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	live, _ := os.Lstat(path)
	second := nativeRunFixture(t)
	if l, err := second.listenNativeRuns(ctx, path); !errors.Is(err, errNativeRunLive) {
		if l != nil {
			l.Close()
		}
		t.Fatal("live endpoint was not refused as live", err)
	}
	nativeRunKept(t, path, live)
	if got := nativeRunDialLookup(t, path); len(got.Records) != 1 {
		t.Fatal("live owner stopped serving after a conflicting start")
	}
}

type nativeRunLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *nativeRunLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *nativeRunLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// nativeRunOnceCoord is a --once coordinator whose tick reaches the fake gh.
func nativeRunOnceCoord(t *testing.T, socket string) (*Coord, func() string) {
	t.Helper()
	root := privateTestRoot(t)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shq(calls) + "\nprintf '[]\\n'\n"
	if os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700) != nil {
		t.Fatal("fake gh")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := &Coord{cfg: &Config{Inbox: "example/inbox", NativeRunSocket: socket, Matrix: MatrixConfig{ReceiptRoot: root},
		Targets: []Target{{Label: "orchid", Repo: "example/project"}}},
		st: loadState(filepath.Join(root, "state.json")), instance: "once-generation"}
	return c, func() string { raw, _ := os.ReadFile(calls); return string(raw) }
}

func nativeRunCaptureLog(t *testing.T) *nativeRunLog {
	t.Helper()
	out := &nativeRunLog{}
	prev := log.Writer()
	log.SetOutput(out)
	t.Cleanup(func() { log.SetOutput(prev) })
	return out
}

func TestNativeRunOnceTicksAndRemovesEndpoint(t *testing.T) {
	path := filepath.Join(nativeRunPrivateDir(t), "read.sock")
	nativeRunStaleSocket(t, path, 0600) // left behind by an earlier --once or crash
	for pass := 0; pass < 2; pass++ {
		c, calls := nativeRunOnceCoord(t, path)
		c.runOnce(context.Background())
		if !strings.Contains(calls(), "issue list") {
			t.Fatal("--once pass did not tick", pass, calls())
		}
		if c.st.nativeRuns == nil {
			t.Fatal("--once did not serve native reads", pass)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("--once exit left its endpoint behind", pass)
		}
	}
}

func TestNativeRunUnavailableReadKeepsSupervising(t *testing.T) {
	dir := nativeRunPrivateDir(t)
	path := filepath.Join(dir, "read.sock")
	owner := nativeRunFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := owner.listenNativeRuns(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	live, _ := os.Lstat(path)
	unsafe := filepath.Join(t.TempDir(), "public", "read.sock")
	if os.MkdirAll(filepath.Dir(unsafe), 0700) != nil || os.Chmod(filepath.Dir(unsafe), 0755) != nil {
		t.Fatal("unsafe parent fixture")
	}
	for _, tc := range []struct{ socket, reason string }{{path, "endpoint-live"}, {unsafe, "endpoint-refused"}} {
		logs := nativeRunCaptureLog(t)
		c, calls := nativeRunOnceCoord(t, tc.socket)
		stop := c.startNativeRunReads(context.Background())
		stop()
		stop() // idempotent no-op
		c.runOnce(context.Background())
		if !strings.Contains(calls(), "issue list") {
			t.Fatal("unavailable native read stopped the supervision tick", tc.reason)
		}
		if !strings.Contains(logs.String(), "native-run-source unavailable reason="+tc.reason) {
			t.Fatal("unavailable native read not reported", tc.reason, logs.String())
		}
		if c.st.nativeRuns != nil {
			t.Fatal("unavailable start installed a read source", tc.reason)
		}
	}
	nativeRunKept(t, path, live)
	if got := nativeRunDialLookup(t, path); len(got.Records) != 1 {
		t.Fatal("conflicting --once disturbed the live endpoint")
	}
	if _, err := os.Lstat(unsafe); !os.IsNotExist(err) {
		t.Fatal("endpoint created under an unsafe parent")
	}
}
