//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// CI invokes only this synthetic fixture as root. No configured daemon, real
// operator approval, host path, network service or CLI agent is involved.
func TestOwnerNativeEndpointTwoUIDs(t *testing.T) {
	if os.Getuid() != 0 {
		if os.Getenv("OWNER_ENDPOINT_REQUIRE_ROOT") == "1" {
			t.Fatal("root proof lane did not run as root")
		}
		t.Skip("two-UID kernel fixture requires its isolated root CI step")
	}
	s, request, _ := ownerPortFixture(t, "claude")
	operator := 1000
	directory, err := os.MkdirTemp("", "owner-endpoint-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	if os.Chown(directory, operator, operator) != nil || os.Chmod(directory, 0700) != nil {
		t.Fatal("operator directory fixture")
	}
	s.options.Socket = filepath.Join(directory, "operator.sock")
	s.options.OperatorUID = &operator
	approval := filepath.Join(s.options.ApprovalRoot, request.ApprovalRef+".json")
	if os.Chown(s.options.ApprovalRoot, operator, operator) != nil || os.Chown(approval, operator, operator) != nil {
		t.Fatal("operator approval fixture")
	}
	requestPath := filepath.Join(directory, "request.json")
	raw, _ := json.Marshal(request)
	if os.WriteFile(requestPath, raw, 0600) != nil || os.Chown(requestPath, operator, operator) != nil {
		t.Fatal("operator request fixture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := s.listen(ctx)
	if err != nil {
		t.Fatal("daemon refused private operator-owned endpoint", err)
	}
	defer listener.Close()
	info, err := os.Lstat(s.options.Socket)
	if err != nil || !ownerNativeOwned(info, operator) || info.Mode().Perm() != 0600 {
		t.Fatal("socket is not private and operator-owned")
	}
	for _, path := range []string{s.options.StoreRoot, filepath.Join(s.options.StoreRoot, "records"), filepath.Join(s.options.StoreRoot, "intents")} {
		info, err := os.Lstat(path)
		if err != nil || !ownerNativeOwned(info, 0) || info.Mode().Perm() != 0700 {
			t.Fatal("daemon store ownership changed")
		}
	}
	child := func(mode string) {
		t.Helper()
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, "-test.run=^TestOwnerNativeEndpointOperatorChild$", "-test.count=1")
		cmd.Env = append(os.Environ(), "OWNER_ENDPOINT_CHILD="+mode, "OWNER_ENDPOINT_SOCKET="+s.options.Socket,
			"OWNER_ENDPOINT_REQUEST="+requestPath, "OWNER_ENDPOINT_OPERATION="+request.OperationID)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(operator), Gid: uint32(operator)}}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("operator %s: %v: %s", mode, err, output)
		}
	}
	t.Run("install", func(t *testing.T) { child("install") })
	t.Run("status", func(t *testing.T) { child("status") })
	t.Run("wrong-server-peer", func(t *testing.T) { child("wrong-server") })
	t.Run("wrong-client-peer", func(t *testing.T) {
		conn, err := net.Dial("unix", s.options.Socket) // Actual root peer, not the approved operator.
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_ = json.NewEncoder(conn).Encode(ownerNativeRPC{Method: "readStatus", OperationID: request.OperationID})
		var ack ownerNativeAck
		if json.NewDecoder(conn).Decode(&ack) != nil || ack.State != "REFUSED" {
			t.Fatal("server accepted wrong actual peer UID")
		}
	})
	for _, mode := range []os.FileMode{0770, 0707} {
		t.Run("directory-mode-"+strconv.FormatUint(uint64(mode), 8), func(t *testing.T) {
			if os.Chmod(directory, mode) != nil {
				t.Fatal("directory mode fixture")
			}
			defer os.Chmod(directory, 0700)
			if s.healthy() {
				t.Fatal("server accepted shared endpoint directory")
			}
			child("refused-path")
		})
	}
	for _, mode := range []os.FileMode{0660, 0606} {
		t.Run("socket-mode-"+strconv.FormatUint(uint64(mode), 8), func(t *testing.T) {
			if os.Chmod(s.options.Socket, mode) != nil {
				t.Fatal("socket mode fixture")
			}
			defer os.Chmod(s.options.Socket, 0600)
			child("refused-path")
		})
	}
	t.Run("swapped-socket", func(t *testing.T) { child("swapped") })
}

func TestOwnerNativeEndpointOperatorChild(t *testing.T) {
	mode := os.Getenv("OWNER_ENDPOINT_CHILD")
	if mode == "" {
		t.Skip("operator subprocess only")
	}
	if os.Getuid() != 1000 {
		t.Fatal("fixture did not change the actual kernel UID")
	}
	socket, operation := os.Getenv("OWNER_ENDPOINT_SOCKET"), os.Getenv("OWNER_ENDPOINT_OPERATION")
	args := []string{"status", "-socket", socket, "-operation", operation, "-server-uid", "0"}
	if mode == "install" {
		args = []string{"install", "-socket", socket, "-request", os.Getenv("OWNER_ENDPOINT_REQUEST"), "-server-uid", "0"}
	}
	if mode == "wrong-server" {
		args[len(args)-1] = "1001"
	}
	if mode == "swapped" {
		if os.Remove(socket) != nil {
			t.Fatal("replace fixture socket")
		}
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
		if err != nil || os.Chmod(socket, 0600) != nil {
			t.Fatal("replacement fixture socket")
		}
		listener.SetUnlinkOnClose(false)
		defer listener.Close()
		finished := make(chan bool, 1)
		go func() {
			conn, err := listener.AcceptUnix()
			if err != nil {
				finished <- false
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			var frame ownerNativeRPC
			received := json.NewDecoder(conn).Decode(&frame) == nil
			// Any syntactically valid response still cannot authenticate this UID1000 server as root.
			_ = json.NewEncoder(conn).Encode(ownerNativeFailure(operation, "REFUSED", "grant-conflict"))
			finished <- received
		}()
		var out bytes.Buffer
		_ = ownerNativeGrantCLI(args, &out)
		var ack ownerNativeAck
		if json.Unmarshal(out.Bytes(), &ack) != nil || ack.State != "UNKNOWN" {
			t.Fatal("client trusted a socket swapped by the operator")
		}
		select {
		case sent := <-finished:
			if sent {
				t.Fatal("client sent authority to unverified server")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("client never exercised replacement peer authentication")
		}
		return
	}
	var out bytes.Buffer
	exit := ownerNativeGrantCLI(args, &out)
	var ack ownerNativeAck
	if json.Unmarshal(out.Bytes(), &ack) != nil {
		t.Fatal("invalid CLI response")
	}
	if mode == "install" || mode == "status" {
		if exit != 0 || ack.State != "LIVE" {
			t.Fatal("operator cannot reach authenticated root daemon")
		}
	} else if exit == 0 || ack.State != "UNKNOWN" {
		t.Fatal("invalid peer/path was trusted")
	}
}

func TestOwnerNativeSocketOwnershipNoFollow(t *testing.T) {
	for _, change := range []string{"valid", "replaced", "regular", "symlink", "foreign-owner"} {
		t.Run(change, func(t *testing.T) {
			if change == "foreign-owner" && os.Getuid() != 0 {
				t.Skip("actual foreign socket owner is covered by isolated root CI")
			}
			path := filepath.Join(privateTestRoot(t), "operator.sock")
			var listener *net.UnixListener
			var err error
			if change == "regular" {
				err = os.WriteFile(path, []byte("synthetic"), 0640)
			} else {
				listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err == nil {
					listener.SetUnlinkOnClose(false)
					defer listener.Close()
				}
			}
			if err != nil || os.Chmod(path, 0640) != nil {
				t.Fatal("socket type fixture")
			}
			created, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if change == "replaced" {
				if os.Rename(path, path+".old") != nil {
					t.Fatal("replacement fixture")
				}
				replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				replacement.SetUnlinkOnClose(false)
				defer replacement.Close()
			} else if change == "foreign-owner" {
				if os.Chown(path, 1000, -1) != nil {
					t.Fatal("foreign owner fixture")
				}
			} else if change == "symlink" {
				alias := path + ".alias"
				if os.Symlink(path, alias) != nil {
					t.Fatal("symlink fixture")
				}
				path = alias
			}
			if (ownerNativeSetSocketOwner(path, created, os.Getuid()) == nil) != (change == "valid") {
				t.Fatal("socket inode ownership guard failed")
			}
			if change == "valid" {
				info, err := os.Lstat(path)
				if err != nil || info.Mode().Perm() != 0600 || !ownerNativeOwned(info, os.Getuid()) {
					t.Fatal("held socket permissions not applied")
				}
			}
		})
	}
}

func TestOwnerNativeSocketRegistrationGuards(t *testing.T) {
	for _, change := range []string{"valid", "prepare-error", "replaced", "shared", "missing", "wrong-owner"} {
		t.Run(change, func(t *testing.T) {
			if change == "wrong-owner" && os.Getuid() != 0 {
				t.Skip("actual foreign ownership is covered by isolated root CI")
			}
			s, _, _ := ownerPortFixture(t, "claude")
			s.prepareSocket = func(path string, created os.FileInfo, uid int) error {
				if ownerNativeSetSocketOwner(path, created, uid) != nil {
					return errMatrix
				}
				switch change {
				case "prepare-error":
					return errMatrix
				case "replaced":
					if os.Rename(path, path+".old") != nil {
						t.Fatal("replacement fixture")
					}
					replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
					if err != nil || os.Chmod(path, 0600) != nil {
						t.Fatal("replacement listener fixture")
					}
					replacement.SetUnlinkOnClose(false)
					t.Cleanup(func() { replacement.Close() })
				case "shared":
					return os.Chmod(path, 0660)
				case "missing":
					return os.Remove(path)
				case "wrong-owner":
					return os.Chown(path, 1000, -1)
				}
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			listener, err := s.listen(ctx)
			if listener != nil {
				defer listener.Close()
			}
			if (err == nil) != (change == "valid") {
				t.Fatal("unverified socket registered")
			}
		})
	}
}
