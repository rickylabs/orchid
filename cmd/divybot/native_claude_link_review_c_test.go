package main

// Review o89c probes, adopted unchanged as guards.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This writes only synthetic private fixture state. json.Unmarshal also lets
// this identical test file run on the old head, which has no capture field.
func auditCSetLaunch(t *testing.T, f *auditFixture, pid int) {
	t.Helper()
	raw, err := json.Marshal(f.j.RemoteControl)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["claudeProcess"] = map[string]any{"pid": pid, "start": procStart(t, pid)}
	raw, _ = json.Marshal(fields)
	if err := json.Unmarshal(raw, f.j.RemoteControl); err != nil {
		t.Fatal(err)
	}
}

func TestAuditO89CRewriteBeforeTail(t *testing.T) {
	f := auditNew(t)
	good := "https://claude.ai/code/" + fixtureBridge
	last := "A"
	if strings.HasSuffix(good, last) {
		last = "B"
	}
	other := good[:len(good)-1] + last
	first := auditStatus(f.j, good)
	changed := auditStatus(f.j, other)
	if len(first) != len(changed) {
		t.Fatal("control: rewrite lengths differ")
	}
	// Move the only bridge entry outside the cursor's 4 KiB suffix.
	filler := `{"type":"user","text":"` + strings.Repeat("x", 12000) + `"}`
	path := auditTranscript(t, f.h, f.j, first, filler)
	f.h.readClaudeBridge(f.j)
	f.observe()
	if f.link(t) == nil {
		t.Fatal("control: original agreeing transcript gave no link")
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// os.WriteFile keeps the inode. The suffix and total size are identical.
	if err := os.WriteFile(path, []byte(changed+"\n"+filler+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() {
		t.Fatal("control: inode or size changed")
	}
	f.h.readClaudeBridge(f.j)
	f.observe()
	if f.link(t) != nil {
		t.Error("rewritten latest typed entry disagrees, but unchanged suffix preserves and republishes the old link")
	}
}

func TestAuditO89CZombiePIDWhileRefreshPending(t *testing.T) {
	f := auditNew(t)
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	auditCSetLaunch(t, f, pid)
	rec, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": f.j.RemoteControl.NativeSessionID, "procStart": procStart(t, pid), "bridgeSessionId": fixtureBridge})
	if err := os.WriteFile(filepath.Join(f.h.Home, ".claude", "sessions", fmt.Sprintf("%d.json", pid)), rec, 0600); err != nil {
		t.Fatal(err)
	}
	auditTranscript(t, f.h, f.j, auditStatus(f.j, "https://claude.ai/code/"+fixtureBridge))
	changePID := func(block string) {
		f.herdr(t, f.j.RemoteControl.NativeSessionID, block, "/rc active")
		path := filepath.Join(f.h.Home, ".local", "bin", "herdr")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body = []byte(strings.Replace(string(body), fmt.Sprintf(`\"pid\":%d`, os.Getpid()), fmt.Sprintf(`\"pid\":%d`, pid), 1))
		if err := os.WriteFile(path, body, 0700); err != nil {
			t.Fatal(err)
		}
	}
	changePID("")
	f.h.readClaudeBridge(f.j)
	f.observe()
	if f.link(t) == nil {
		t.Fatal("control: live child did not publish link")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// An exited but unreaped process retains procfs and the same start time.
	zombie := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			t.Fatal("control: unreaped child procfs vanished")
		}
		fields := strings.Fields(string(raw)[strings.LastIndex(string(raw), ")")+1:])
		if len(fields) > 0 && fields[0] == "Z" {
			zombie = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !zombie {
		t.Fatal("control: child did not reach native zombie state")
	}
	release := filepath.Join(f.h.Home, "release-zombie-refresh")
	changePID(release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	f.c.observeRemoteControl(ctx, f.h, f.j)
	cancel()
	if f.link(t) != nil {
		t.Error("exited zombie PID got a newly fresh published link while refresh was pending")
	}
	if err := os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	f.c.shadow.bg.Wait()
	if f.link(t) != nil {
		t.Error("exited zombie PID retained a live link after refresh")
	}
}
