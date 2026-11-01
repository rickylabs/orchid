package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

const nativeRunSocketMode os.FileMode = 0660
const nativeRunPeerUIDEnv = "ORCHID_NATIVE_RUN_PEER_UID"

var errNativeRunParentMissing = errors.New("parent-directory-missing")
var errNativeRunPeerInvalid = errors.New("peer-uid-invalid")

// The operator provisions the parent; Orchid never creates it or changes its group/mode.
func nativeRunParent(path string, uid int) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return errNativeRunParentMissing
	}
	if err != nil || !info.IsDir() || !ownerNativeOwned(info, uid) {
		return errMatrix
	}
	mode := info.Mode() & (os.ModePerm | os.ModeSetgid | os.ModeSetuid | os.ModeSticky)
	if mode != 0700 && mode != os.ModeSetgid|0750 {
		return errMatrix
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return errMatrix
	}
	for p := path; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); !os.IsNotExist(err) {
			return errMatrix
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

// Parse once before bind; every connection is then checked against this exact kernel UID.
func nativeRunPeerUID() (int, error) {
	raw := os.Getenv(nativeRunPeerUIDEnv)
	if raw == "" {
		return os.Getuid(), nil
	}
	uid, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || uid == 1<<32-1 || strconv.FormatUint(uid, 10) != raw || int(uid) < 0 {
		return -1, errNativeRunPeerInvalid
	}
	return int(uid), nil
}
