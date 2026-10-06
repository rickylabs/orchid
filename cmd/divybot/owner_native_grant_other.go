//go:build !linux

package main

import (
	"net"
	"os"
)

// The private port is unavailable where authenticated local peer credentials
// and no-follow file opens are not implemented.
func ownerNativeOwned(os.FileInfo, int) bool                   { return false }
func ownerNativeSetSocketOwner(string, os.FileInfo, int) error { return errMatrix }
func ownerNativeOpen(string) (*os.File, error)                 { return nil, errMatrix }
func ownerNativePeerUID(*net.UnixConn) (int, error)            { return -1, errMatrix }

// No held no-follow directory lookup here: absence is never established.
func heldChildAbsent(string, string, int, func()) (bool, bool) { return false, false }
