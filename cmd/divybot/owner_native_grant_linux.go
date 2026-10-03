//go:build linux

package main

import (
	"net"
	"os"
	"syscall"
)

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
