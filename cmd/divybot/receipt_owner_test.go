package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func ownerFixture(t *testing.T, root, key string, owner *receiptOwner) *durableMatrixReceipt {
	t.Helper()
	r, e := persistMatrixReceipt(root, key, "synthetic-command", receiptFor(MatrixConfig{}, syntheticRoute()), nil, owner)
	if e != nil {
		t.Fatal("reservation failed")
	}
	r.dispatch = &dispatchBinding{SchemaVersion: 1, RunID: "orchid-" + key, Issue: dispatchIssue{Repo: "example/inbox", Number: 1}, Source: "codex", Provider: "fixture-router", Profile: "leaf", Model: "fixture-model", Effort: "high"}
	return r
}
func checkOwnerTree(t *testing.T, root string, uid, gid int) {
	t.Helper()
	if e := filepath.Walk(root, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		st := info.Sys().(*syscall.Stat_t)
		mode := os.FileMode(0600)
		if info.IsDir() {
			mode = 0700
		}
		if int(st.Uid) != uid || int(st.Gid) != gid || info.Mode().Perm() != mode {
			t.Error("owner or private mode differs from configured contract")
		}
		return nil
	}); e != nil {
		t.Fatal("tree unreadable")
	}
}
func TestReceiptOwnerUnset(t *testing.T) {
	root := privateTestRoot(t)
	key := strings.Repeat("a", 64)
	r := ownerFixture(t, root, key, nil)
	if e := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "fixture-pane", WorkspaceID: "fixture-workspace"}); e != nil {
		t.Fatal("unset owner changed snapshot behavior")
	}
	checkOwnerTree(t, root, os.Getuid(), os.Getgid())
}
func TestReceiptOwnerSet(t *testing.T) {
	root := privateTestRoot(t)
	key := strings.Repeat("b", 64)
	calls := 0
	owner := &receiptOwner{uid: os.Getuid(), gid: os.Getgid()}
	owner.chown = func(path string, uid, gid int) error {
		calls++
		if uid != owner.uid || gid != owner.gid {
			t.Fatal("configured IDs were not forwarded")
		}
		if calls <= 4 {
			if _, e := os.Stat(filepath.Join(root, key, "record")); !os.IsNotExist(e) {
				t.Fatal("record published before transfer completed")
			}
		}
		return os.Chown(path, uid, gid)
	}
	r := ownerFixture(t, root, key, owner)
	if calls != 4 {
		t.Fatal("ownership must cover both files and both reservation directories")
	}
	for _, state := range []string{"reserved", "launching", "dispatched"} {
		if e := r.writeDispatch(state, &dispatchLocation{PaneID: "fixture-pane", WorkspaceID: "fixture-workspace"}); e != nil {
			t.Fatal("configured snapshot failed")
		}
	}
	if calls != 7 {
		t.Fatal("snapshot replacements did not receive configured ownership")
	}
	checkOwnerTree(t, filepath.Join(root, key), owner.uid, owner.gid)
}
func TestReceiptOwnerFailureHidesRecord(t *testing.T) {
	for failAt := 1; failAt <= 4; failAt++ {
		root := privateTestRoot(t)
		key := strings.Repeat("c", 64)
		calls := 0
		owner := &receiptOwner{uid: os.Getuid(), gid: os.Getgid(), chown: func(string, int, int) error {
			calls++
			if calls == failAt {
				return syscall.EPERM
			}
			return nil
		}}
		r, e := persistMatrixReceipt(root, key, "synthetic-command", receiptFor(MatrixConfig{}, syntheticRoute()), nil, owner)
		if e == nil || r != nil {
			t.Error("failed ownership transfer must refuse reservation")
		}
		if _, e := os.Stat(filepath.Join(root, key, "record")); !os.IsNotExist(e) {
			t.Error("failed ownership transfer exposed final record")
		}
		entries, e := os.ReadDir(filepath.Join(root, key))
		if e != nil || len(entries) != 0 {
			t.Error("failure must leave only an empty reservation fence")
		}
	}
}
func TestReceiptOwnerFailurePreservesSnapshot(t *testing.T) {
	root := privateTestRoot(t)
	key := strings.Repeat("d", 64)
	owner := &receiptOwner{uid: os.Getuid(), gid: os.Getgid()}
	r := ownerFixture(t, root, key, owner)
	if e := r.writeDispatch("reserved", nil); e != nil {
		t.Fatal("initial snapshot failed")
	}
	file := filepath.Join(root, key, "record", "dispatch.json")
	before, _ := os.ReadFile(file)
	owner.chown = func(string, int, int) error { return syscall.EPERM }
	if e := r.writeDispatch("launching", &dispatchLocation{PaneID: "fixture-pane", WorkspaceID: "fixture-workspace"}); e == nil {
		t.Error("failed snapshot ownership must refuse publication")
	}
	after, e := os.ReadFile(file)
	if e != nil || !bytes.Equal(before, after) {
		t.Error("failed ownership transfer replaced visible snapshot")
	}
}
func TestReceiptOwnerExistingReservation(t *testing.T) {
	root := privateTestRoot(t)
	key := strings.Repeat("e", 64)
	old := ownerFixture(t, root, key, nil)
	oldFile := old.file
	before, e := os.ReadFile(oldFile)
	if e != nil {
		t.Fatal("old receipt unreadable")
	}
	calls := 0
	owner := &receiptOwner{uid: os.Getuid(), gid: os.Getgid(), chown: func(path string, uid, gid int) error {
		calls++
		if strings.Contains(path, key) {
			t.Error("existing reservation ownership touched")
		}
		return os.Chown(path, uid, gid)
	}}
	if _, e := persistMatrixReceipt(root, key, "synthetic-command", receiptFor(MatrixConfig{}, syntheticRoute()), nil, owner); e == nil {
		t.Error("existing reservation replaced")
	}
	if calls != 0 {
		t.Error("existing reservation received ownership operations")
	}
	ownerFixture(t, root, strings.Repeat("f", 64), owner)
	after, e := os.ReadFile(oldFile)
	if e != nil || !bytes.Equal(before, after) || !old.claim("synthetic-command") {
		t.Error("original owner lost access to existing receipt")
	}
	checkOwnerTree(t, filepath.Join(root, key), os.Getuid(), os.Getgid())
}
func TestReceiptOwnerConfig(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	negative := -1
	for _, cfg := range []MatrixConfig{{ReceiptOwnerUID: &uid}, {ReceiptOwnerGID: &gid}, {ReceiptOwnerUID: &negative, ReceiptOwnerGID: &gid}} {
		if _, e := configuredReceiptOwner(cfg); !errors.Is(e, errMatrix) {
			t.Fatal("partial or invalid owner must refuse")
		}
	}
	cfg := MatrixConfig{ReceiptOwnerUID: &uid, ReceiptOwnerGID: &gid}
	owner, e := configuredReceiptOwner(cfg)
	if e != nil || owner.uid != uid || owner.gid != gid {
		t.Fatal("configured IDs lost")
	}
}

func TestReceiptOwnerCrossUID(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("INCONCLUSIVE: cross-UID handoff requires privilege; same-owner syscall and failure controls run separately")
	}
	root := privateTestRoot(t)
	key := strings.Repeat("9", 64)
	// Synthetic numeric identities, confined to this disposable fixture.
	owner := &receiptOwner{uid: 1, gid: 1}
	r := ownerFixture(t, root, key, owner)
	if e := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "fixture-pane", WorkspaceID: "fixture-workspace"}); e != nil {
		t.Fatal("cross-UID snapshot failed")
	}
	checkOwnerTree(t, filepath.Join(root, key), 1, 1)
}

func TestReceiptOwnerSyncFailureHidesRecord(t *testing.T) {
	root := privateTestRoot(t)
	key := strings.Repeat("8", 64)
	owner := &receiptOwner{uid: os.Getuid(), gid: os.Getgid(), sync: func(string) error { return syscall.EIO }}
	if r, e := persistMatrixReceipt(root, key, "synthetic-command", receiptFor(MatrixConfig{}, syntheticRoute()), nil, owner); e == nil || r != nil {
		t.Error("unsynced ownership must refuse reservation")
	}
	if _, e := os.Stat(filepath.Join(root, key, "record")); !os.IsNotExist(e) {
		t.Error("unsynced ownership exposed final record")
	}
}
