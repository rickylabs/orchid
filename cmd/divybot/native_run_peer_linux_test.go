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

func TestNativeRunSameUIDKernelPeer(t *testing.T) {
	_, _, socket := nativeRunSocketFixture(t)
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	uid, err := ownerNativePeerUID(conn)
	if err != nil || uid != os.Getuid() {
		t.Fatal("kernel peer identity not sameUID")
	}
	conn.SetDeadline(time.Now().Add(time.Second))
	json.NewEncoder(conn).Encode(map[string]any{"id": "x", "method": "run.lookup", "params": map[string]any{"keys": []nativeRunQuery{{"701", "codex-701"}}}})
	var got struct {
		Result nativeRunLookup `json:"result"`
	}
	if json.NewDecoder(conn).Decode(&got) != nil || len(got.Result.Records) != 1 {
		t.Fatal("authorized kernel peer refused")
	}
}

func TestNativeRunForeignUIDKernelPeer(t *testing.T) {
	if os.Getuid() != 0 {
		if os.Getenv("NATIVE_RUN_REQUIRE_ROOT") == "1" {
			t.Fatal("root proof lane did not run as root")
		}
		t.Skip("twoUID kernel rejection requires isolated root CI lane")
	}
	c := nativeRunFixture(t)
	dir, err := os.MkdirTemp("", "native-run-peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	os.Chmod(dir, 0700)
	socket := filepath.Join(dir, "read.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := c.listenNativeRuns(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// Deliberately bypass pathname DAC after startup so the independent kernel
	// peer rejection, rather than directory permissions, is exercised.
	os.Chmod(dir, 0777)
	os.Chmod(socket, 0666)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Go's build output may have a 0700 parent, so copy the executable
	// into a root-owned traversable fixture. Otherwise the child fails at
	// exec rather than proving the socket's independent kernel UID check.
	executableDir, err := os.MkdirTemp("", "native-run-executable-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(executableDir)
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
	cmd := exec.Command(childBinary, "-test.run=^TestNativeRunForeignUIDChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "NATIVE_RUN_FOREIGN_SOCKET="+socket)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("foreign kernel peer fixture: %v: %s", err, output)
	}
}
func TestNativeRunForeignUIDChild(t *testing.T) {
	socket := os.Getenv("NATIVE_RUN_FOREIGN_SOCKET")
	if socket == "" {
		t.Skip("child fixture")
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	json.NewEncoder(conn).Encode(map[string]any{"id": "x", "method": "run.lookup", "params": map[string]any{"keys": []nativeRunQuery{{"701", "codex-701"}}}})
	var got map[string]json.RawMessage
	if json.NewDecoder(conn).Decode(&got) == nil {
		t.Fatal("foreign UID obtained response")
	}
}
