package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Exact permissions are the observer's filesystem contract, independent of umask.
func TestNativeRunConfiguredSocketMode0660(t *testing.T) {
	c := nativeRunFixture(t)
	c.cfg.NativeRunSocket = filepath.Join(nativeRunPrivateDir(t), "configured.sock")
	stop := c.startNativeRunReads(context.Background())
	defer stop()
	info, err := os.Lstat(c.cfg.NativeRunSocket)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0660 {
		t.Fatalf("socket mode = %04o, want 0660", info.Mode().Perm())
	}
	if got := nativeRunDialLookup(t, c.cfg.NativeRunSocket); len(got.Records) != 1 {
		t.Fatal("configured endpoint did not serve native records")
	}
}

func TestNativeRunMissingParentNamedRefusalKeepsSupervising(t *testing.T) {
	path := filepath.Join(nativeRunPrivateDir(t), "not-provisioned", "read.sock")
	c, calls := nativeRunOnceCoord(t, path)
	if l, err := c.listenNativeRuns(context.Background(), path); err == nil || err.Error() != "parent-directory-missing" {
		if l != nil {
			l.Close()
		}
		t.Fatalf("missing parent reason = %v, want parent-directory-missing", err)
	}
	logs := nativeRunCaptureLog(t)
	c.runOnce(context.Background())
	if !strings.Contains(calls(), "issue list") || c.st.nativeRuns != nil {
		t.Fatal("missing parent prevented supervision or installed a source")
	}
	if !strings.Contains(logs.String(), "reason=parent-directory-missing; supervision continues") || strings.Contains(logs.String(), path) {
		t.Fatal("missing parent did not produce the path-free named refusal")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("Orchid created the operator-owned parent")
	}
}

func TestNativeRunSharedParentAndStaleSocket(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0660} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			c := nativeRunFixture(t)
			dir := nativeRunPrivateDir(t)
			if err := os.Chmod(dir, os.ModeSetgid|0750); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(dir)
			path := filepath.Join(dir, "read.sock")
			stale := nativeRunStaleSocket(t, path, mode)
			c.cfg.NativeRunSocket = path
			stop := c.startNativeRunReads(context.Background())
			defer stop()
			fresh, err := os.Lstat(path)
			if err != nil || os.SameFile(stale, fresh) || fresh.Mode().Perm() != 0660 {
				t.Fatal("stale endpoint not replaced by a mode-0660 socket", err)
			}
			if got := nativeRunDialLookup(t, path); len(got.Records) != 1 {
				t.Fatal("shared endpoint did not serve lookup")
			}
			after, _ := os.Stat(dir)
			if !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("operator parent changed")
			}
			stop()
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("owned shared endpoint not cleaned up")
			}
		})
	}
}

func TestNativeRunSharedParentRefusesUnsafeModesAndCheckout(t *testing.T) {
	c := nativeRunFixture(t)
	dir := nativeRunPrivateDir(t)
	for _, mode := range []os.FileMode{0750, os.ModeSetgid | 0770, os.ModeSetgid | 0755, os.ModeSetgid | 0700} {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		if l, err := c.listenNativeRuns(context.Background(), filepath.Join(dir, "read.sock")); err == nil {
			l.Close()
			t.Fatal("unsafe shared parent accepted", mode)
		}
	}
	if err := os.Chmod(dir, os.ModeSetgid|0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if l, err := c.listenNativeRuns(context.Background(), filepath.Join(dir, "read.sock")); err == nil {
		l.Close()
		t.Fatal("socket in checkout accepted")
	}
}

func TestNativeRunConfiguredPeerUID(t *testing.T) {
	for _, raw := range []string{"", strconv.Itoa(os.Getuid()), "0"} {
		t.Setenv(nativeRunPeerUIDEnv, raw)
		got, err := nativeRunPeerUID()
		want := os.Getuid()
		if raw == "0" {
			want = 0
		}
		if err != nil || got != want {
			t.Fatalf("configured peer %q: got %d, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"-1", "observer", "+1", "01", " 1", "4294967295", "4294967296"} {
		t.Setenv(nativeRunPeerUIDEnv, raw)
		c, calls := nativeRunOnceCoord(t, filepath.Join(nativeRunPrivateDir(t), "read.sock"))
		logs := nativeRunCaptureLog(t)
		c.runOnce(context.Background())
		if c.st.nativeRuns != nil || !strings.Contains(calls(), "issue list") || !strings.Contains(logs.String(), "reason=peer-uid-invalid; supervision continues") {
			t.Fatal("invalid peer UID did not refuse reads while continuing supervision", raw)
		}
	}
	t.Setenv(nativeRunPeerUIDEnv, strconv.Itoa(os.Getuid()+1))
	c := nativeRunFixture(t)
	c.cfg.NativeRunSocket = filepath.Join(nativeRunPrivateDir(t), "read.sock")
	stop := c.startNativeRunReads(context.Background())
	defer stop()
	conn, err := net.Dial("unix", c.cfg.NativeRunSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	fmt.Fprintln(conn, `{"id":"x","method":"run.lookup","params":{"keys":[]}}`)
	var got map[string]any
	if err := json.NewDecoder(conn).Decode(&got); err == nil {
		t.Fatal("unconfigured kernel UID received a response")
	}
}
