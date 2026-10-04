package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Test fixtures fake herdr and gh by writing scripts into temporary
// directories. On a noexec TMPDIR the shell skips such a script and resolves
// the real binary further along PATH, so a fixture could reach the live herdr
// server or the authenticated gh account. TestMain refuses a TMPDIR that
// cannot execute fixtures and puts refusing shims for the guarded tools first
// on PATH, so a fixture that fails to run lands on a shim, never a real tool.

var guardedTools = []string{"herdr", "gh"}

const toolShimBody = "#!/bin/sh\necho '{\"error\":{\"code\":\"test-tool-denied\"}}'\nexit 97\n"

var toolShimDir string

// execCapable reports whether a fixture script written in dir can execute.
func execCapable(dir string, mode os.FileMode) bool {
	f, err := os.CreateTemp(dir, "exec-probe-")
	if err != nil {
		return false
	}
	path := f.Name()
	defer os.Remove(path)
	_, err = f.WriteString("#!/bin/sh\nexit 0\n")
	if closeErr := f.Close(); err != nil || closeErr != nil || os.Chmod(path, mode) != nil {
		return false
	}
	return exec.Command(path).Run() == nil // guard:exec-probe
}

func installToolShims(dir string) error {
	for _, tool := range guardedTools {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(toolShimBody), 0700); err != nil {
			return err
		}
	}
	return nil
}

func TestMain(m *testing.M) {
	tmp := os.TempDir()
	if !execCapable(tmp, 0700) {
		fmt.Fprintln(os.Stderr, "test guard: TMPDIR cannot execute test fixtures; fakes would fall through to real tools. Use an exec-capable TMPDIR.")
		os.Exit(2)
	}
	// Host.herdr puts /usr/local/bin ahead of PATH; a real tool there would
	// outrank the shims, so refuse rather than risk reaching it.
	for _, tool := range guardedTools {
		if _, err := os.Stat(filepath.Join("/usr/local/bin", tool)); err == nil {
			fmt.Fprintln(os.Stderr, "test guard: /usr/local/bin/"+tool+" would outrank the test shims; refusing to run.")
			os.Exit(2)
		}
	}
	dir, err := os.MkdirTemp(tmp, "orchid-tool-shims-")
	if err != nil || installToolShims(dir) != nil {
		fmt.Fprintln(os.Stderr, "test guard: tool shims unavailable")
		os.Exit(2)
	}
	toolShimDir = dir
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")) // guard:tool-shims
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestToolGuardFixturesCannotReachRealTools(t *testing.T) {
	// A stand-in for the real tools, placed right after the shims and ahead
	// of every real binary, so even a broken guard never reaches a live tool.
	realDir := t.TempDir()
	marker := filepath.Join(realDir, "reached")
	for _, tool := range guardedTools {
		if os.WriteFile(filepath.Join(realDir, tool), []byte("#!/bin/sh\ntouch "+shq(marker)+"\necho REAL\n"), 0700) != nil {
			t.Fatal("stand-in unavailable")
		}
	}
	rest := os.Getenv("PATH")
	path := realDir + string(os.PathListSeparator) + strings.TrimPrefix(rest, toolShimDir+string(os.PathListSeparator))
	if toolShimDir != "" && strings.HasPrefix(rest, toolShimDir+string(os.PathListSeparator)) {
		path = toolShimDir + string(os.PathListSeparator) + path
	}
	t.Setenv("PATH", path)
	// A fixture fake that cannot execute, as on a noexec TMPDIR.
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if os.MkdirAll(bin, 0700) != nil || os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\necho FAKE\n"), 0600) != nil {
		t.Fatal("fixture unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := Host{Name: "fixture-host", Home: home}.herdr(ctx, "agent", "get", "w1:p1")
	ghOut, _ := exec.CommandContext(ctx, "gh", "api", "user").CombinedOutput()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a fixture reached the real tool")
	}
	if !strings.Contains(out, "test-tool-denied") || !strings.Contains(string(ghOut), "test-tool-denied") {
		t.Fatalf("guarded tools did not resolve to the refusing shims: herdr=%q gh=%q", out, ghOut)
	}
	if execCapable(t.TempDir(), 0600) || !execCapable(t.TempDir(), 0700) {
		t.Fatal("exec probe does not distinguish a non-executable fixture")
	}
}
