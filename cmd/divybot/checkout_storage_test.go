package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckoutStoragePreservesExistingPaths(t *testing.T) {
	root := t.TempDir()
	legacy, disk := filepath.Join(root, "legacy"), filepath.Join(root, "disk")
	h := Host{WorkdirRoot: legacy, DiskWorkdirRoot: disk}
	ctx := context.Background()
	old := filepath.Join(legacy, "issue-7")
	next := filepath.Join(disk, "issue-7")
	check := func(want string) {
		t.Helper()
		got, err := h.checkoutDirectory(ctx, 7)
		if err != nil || got != want {
			t.Fatalf("checkout selection: %q %v", got, err)
		}
	}
	check(old) // missing mount keeps compatibility fallback
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(disk, 0700); err != nil {
		t.Fatal(err)
	}
	check(old) // adding the disk mount never moves in-flight or finished work
	if err := os.RemoveAll(old); err != nil {
		t.Fatal(err)
	}
	check(next)
	if err := os.MkdirAll(next, 0700); err != nil {
		t.Fatal(err)
	}
	check(next)
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.checkoutDirectory(ctx, 7); err == nil {
		t.Fatal("ambiguous existing checkouts accepted")
	}
	if got := h.jobCheckout(&Job{Issue: 7, Workdir: old}); got != old {
		t.Fatal("cleanup followed new config instead of recorded path")
	}
	h.DiskWorkdirRoot = "relative"
	if _, err := h.checkoutDirectory(ctx, 8); err == nil {
		t.Fatal("relative disk root accepted")
	}
}
