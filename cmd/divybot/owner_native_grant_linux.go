//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// The endpoint directory belongs to the operator, so its pathname can change.
// Hold the exact no-follow inode before changing ownership or permissions;
// never chmod/chown a replacement pathname with daemon privileges.
func ownerNativeSetSocketOwner(path string, created os.FileInfo, uid int) error {
	const oPath = 0x200000 // Linux O_PATH, absent from syscall on amd64/386.
	fd, err := syscall.Open(path, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return errMatrix
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	held, err := f.Stat()
	if err != nil || held.Mode()&os.ModeSocket == 0 || !os.SameFile(created, held) || !ownerNativeOwned(held, os.Getuid()) {
		return errMatrix
	}
	const atEmptyPath = 0x1000
	// Fchownat addresses the held inode. Linux's O_PATH descriptor cannot be
	// fchmod'ed; its kernel-owned proc reference addresses that same inode.
	if syscall.Fchownat(fd, "", uid, -1, atEmptyPath) != nil || os.Chmod(fmt.Sprintf("/proc/self/fd/%d", fd), 0600) != nil {
		return errMatrix
	}
	return nil
}

func ownerNativeOwned(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uid >= 0 && uint64(stat.Uid) == uint64(uid)
}

func ownerNativeOpen(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func ownerNativePeerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return -1, err
	}
	var cred *syscall.Ucred
	var readErr error
	err = raw.Control(func(fd uintptr) {
		cred, readErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil {
		return -1, err
	}
	if readErr != nil || cred == nil {
		return -1, errMatrix
	}
	return int(cred.Uid), nil
}

// heldChildAbsent answers whether name is absent from the private root,
// judged relative to one held descriptor of that root, never by re-walking
// its pathname: the root is opened no-follow as a directory and must be 0700
// and owned by uid; a missing entry in that held directory means absent only
// if the held directory is still the one at the root path afterwards. ok=false when any
// of this cannot be established (the root missing, replaced, unsafe or
// unreadable right now), so callers keep waiting. between runs after the root
// is held and before the lookup (tests only; nil in production).
func heldChildAbsent(root, name string, uid int, between func()) (absent, ok bool) {
	if name == "" || filepath.Base(name) != name {
		return false, false
	}
	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false, false
	}
	held := os.NewFile(uintptr(fd), root)
	defer held.Close()
	info, err := held.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !ownerNativeOwned(info, uid) {
		return false, false
	}
	if between != nil {
		between()
	}
	// Open (never follow, never block) relative to the held root, then close.
	child, err := syscall.Openat(fd, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0) // guard:held-root-lookup
	switch err {
	case nil:
		_ = syscall.Close(child)
		return false, true
	case syscall.ENOENT:
		// Absent only if the held directory is still the one at the root path:
		// a root replaced during the lookup says nothing about its new contents.
		if now, err := os.Lstat(root); err != nil || !os.SameFile(info, now) { // guard:held-root-still-root
			return false, false
		}
		return true, true
	default: // a symlink, a permission or IO failure: not provably absent
		return false, false
	}
}
