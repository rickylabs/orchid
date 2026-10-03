//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
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
