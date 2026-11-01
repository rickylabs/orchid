//go:build linux

package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNativeRunSharedSocketInheritsParentGroup(t *testing.T) {
	dir := nativeRunPrivateDir(t)
	if err := os.Chmod(dir, os.ModeSetgid|0750); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := nativeRunFixture(t)
	c.cfg.NativeRunSocket = filepath.Join(dir, "read.sock")
	stop := c.startNativeRunReads(context.Background())
	defer stop()
	socket, err := os.Lstat(c.cfg.NativeRunSocket)
	if err != nil {
		t.Fatal(err)
	}
	if socket.Sys().(*syscall.Stat_t).Gid != parent.Sys().(*syscall.Stat_t).Gid {
		t.Fatal("socket did not retain provisioned parent group")
	}
	if socket.Mode().Perm() != 0660 {
		t.Fatalf("socket mode = %04o, want 0660", socket.Mode().Perm())
	}
}

// The required root CI lane proves real cross-UID admission, plus independent kernel rejection.
func TestNativeRunConfiguredUIDKernelPeer(t *testing.T) {
	if os.Getuid() != 0 {
		if os.Getenv("NATIVE_RUN_REQUIRE_ROOT") == "1" {
			t.Fatal("root proof lane did not run as root")
		}
		t.Skip("configured-UID kernel proof requires isolated root CI lane")
	}
	t.Setenv(nativeRunPeerUIDEnv, "65534")
	dir := nativeRunPrivateDir(t)
	if err := os.Chown(dir, 0, 65534); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, os.ModeSetgid|0750); err != nil {
		t.Fatal(err)
	}
	c := nativeRunFixture(t)
	c.cfg.NativeRunSocket = filepath.Join(dir, "read.sock")
	stop := c.startNativeRunReads(context.Background())
	defer stop()
	info, err := os.Lstat(c.cfg.NativeRunSocket)
	if err != nil || info.Mode().Perm() != 0660 || info.Sys().(*syscall.Stat_t).Gid != 65534 {
		t.Fatal("cross-UID fixture lacks inherited observer group", err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executableDir := nativeRunPrivateDir(t)
	if err := os.Chmod(executableDir, 0755); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	childBinary := filepath.Join(executableDir, "peer.test")
	if err := os.WriteFile(childBinary, contents, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(childBinary, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(childBinary, "-test.run=^TestNativeRunConfiguredUIDChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "NATIVE_RUN_CONFIGURED_SOCKET="+c.cfg.NativeRunSocket)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configured kernel peer: %v: %s", err, out)
	}
	// Root can bypass pathname DAC but is not the configured observer UID.
	conn, err := net.Dial("unix", c.cfg.NativeRunSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	json.NewEncoder(conn).Encode(map[string]any{"id": "denied", "method": "run.lookup", "params": map[string]any{"keys": []nativeRunQuery{}}})
	var got map[string]any
	if json.NewDecoder(conn).Decode(&got) == nil {
		t.Fatal("unconfigured kernel UID obtained a response despite pathname access")
	}
}

func TestNativeRunConfiguredUIDChild(t *testing.T) {
	socket := os.Getenv("NATIVE_RUN_CONFIGURED_SOCKET")
	if socket == "" {
		t.Skip("child fixture")
	}
	// The consumer opens a subscription, then a separate baseline lookup socket.
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	json.NewEncoder(conn).Encode(map[string]any{"id": "watch", "method": "run.subscribe", "params": map[string]any{}})
	var ack struct {
		Result struct {
			Type string `json:"type"`
		} `json:"result"`
	}
	if err := json.NewDecoder(conn).Decode(&ack); err != nil || ack.Result.Type != "subscription_started" {
		t.Fatal("configured observer subscription refused", err)
	}
	if got := nativeRunDialLookup(t, socket); len(got.Records) != 1 {
		t.Fatal("configured observer lookup refused")
	}
}
