package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Review o89b probes, adopted as guards. The only change: the fixtures record
// the Claude process captured at launch, as the managed launch now does.
type auditFixture struct {
	c   *Coord
	h   Host
	j   *Job
	row string
}

func auditTranscript(t *testing.T, h Host, j *Job, lines ...string) string {
	t.Helper()
	dir := filepath.Join(h.Home, ".claude", "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(j.RemoteControl.Cwd, "-"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, j.RemoteControl.NativeSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func auditStatus(j *Job, url string) string {
	b, _ := json.Marshal(map[string]any{"type": "system", "subtype": "bridge_status", "sessionId": j.RemoteControl.NativeSessionID, "url": url})
	return string(b)
}
func auditNew(t *testing.T) *auditFixture {
	t.Helper()
	r := registrationReceipt(t, "claude", Overrides{})
	r.dispatch.Host = "fixture-host"
	if err := r.writeDispatch("dispatched", &dispatchLocation{PaneID: "w1:p1", WorkspaceID: "w1"}); err != nil {
		t.Fatal(err)
	}
	run := syntheticRemoteRun(t)
	j := launchedSelf(t, &Job{Issue: 7, Agent: "claude", Repo: "fixture/repo", Label: "fixture-agent", Host: "fixture-host", Pane: "w1:p1", Workspace: "w1", DispatchKey: strings.Repeat("d", 64), RemoteControl: run})
	binding, _ := json.Marshal(map[string]string{"Repo": j.Repo, "NativeSessionID": run.NativeSessionID})
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.file), "binding.json"), binding, 0600); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	rec := map[string]any{"pid": self, "sessionId": run.NativeSessionID, "procStart": procStart(t, self), "bridgeSessionId": fixtureBridge}
	h := claudeBridgeHost(t, []int{self}, map[int]any{self: rec})
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox", Matrix: MatrixConfig{ReceiptRoot: filepath.Dir(filepath.Dir(filepath.Dir(r.file)))}}, shadow: newNativeEvidenceShadow(time.Now), claudeLinks: &claudeLinkStore{}}
	h.ShadowScope, h.ClaudeLinks = c.shadow.scope(j), c.claudeLinks
	f := &auditFixture{c: c, h: h, j: j, row: filepath.Join(filepath.Dir(r.file), "remote-control.json")}
	f.herdr(t, run.NativeSessionID, "", "/rc active")
	return f
}
func (f *auditFixture) herdr(t *testing.T, native, block, pane string) {
	t.Helper()
	agent := claudeAgentInfo(t, native)
	a := agent["agent"].(map[string]any)
	a["cwd"], a["agent_status"], a["state_change_seq"] = f.j.RemoteControl.Cwd, "idle", 1
	info, _ := json.Marshal(map[string]any{"result": agent})
	procs := fmt.Sprintf(`{"result":{"process_info":{"foreground_processes":[{"pid":%d}]}}}`, os.Getpid())
	script := "#!/usr/bin/env python3\nimport sys,os,time\na=sys.argv[1:]\nif a[:2]==['pane','process-info']:\n"
	if block != "" {
		script += " while not os.path.exists(" + fmt.Sprintf("%q", block) + "):time.sleep(0.01)\n"
	}
	script += " print(" + fmt.Sprintf("%q", procs) + ")\nelif a[:2]==['agent','get']:\n"
	if native == "" {
		script += " sys.exit(1)\n"
	} else {
		script += " print(" + fmt.Sprintf("%q", string(info)) + ")\n"
	}
	script += "elif a[:2]==['pane','read']:\n open(" + fmt.Sprintf("%q", filepath.Join(f.h.Home, "screen-read")) + ",'w').write('read')\n print(" + fmt.Sprintf("%q", pane) + ")\nelse:sys.exit(1)\n"
	writeFixture(t, filepath.Join(f.h.Home, ".local", "bin", "herdr"), script)
}
func (f *auditFixture) observe() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	f.c.observeRemoteControl(ctx, f.h, f.j)
	cancel()
	f.c.shadow.bg.Wait()
}
func (f *auditFixture) link(t *testing.T) *string {
	t.Helper()
	data, err := os.ReadFile(f.row)
	if os.IsNotExist(err) {
		return nil
	}
	var row remoteControlObservation
	if err != nil || json.Unmarshal(data, &row) != nil {
		t.Fatal("unreadable observation")
	}
	return row.Link
}
func TestAuditO89LateTranscriptEntry(t *testing.T) {
	good := "https://claude.ai/code/" + fixtureBridge
	for _, encoded := range []bool{false, true} {
		t.Run(fmt.Sprintf("escaped-%v", encoded), func(t *testing.T) {
			f := auditNew(t)
			latest := auditStatus(f.j, "https://claude.ai/code/session_Different")
			if encoded {
				latest = strings.Replace(latest, `bridge_status`, `bridge\u005fstatus`, 1)
			}
			auditTranscript(t, f.h, f.j, auditStatus(f.j, good), latest)
			f.h.readClaudeBridge(f.j)
			f.observe()
			if f.link(t) != nil {
				t.Error("latest typed transcript entry disagrees but earlier link was published")
			}
		})
	}
}
func TestAuditO89FirstBridgeLessProcessCannotBeReplaced(t *testing.T) {
	f := auditNew(t)
	auditTranscript(t, f.h, f.j, auditStatus(f.j, "https://claude.ai/code/"+fixtureBridge))
	self, parent := os.Getpid(), os.Getppid()
	path := filepath.Join(f.h.Home, ".claude", "sessions", fmt.Sprintf("%d.json", self))
	initial, _ := json.Marshal(map[string]any{"pid": self, "sessionId": f.j.RemoteControl.NativeSessionID, "procStart": procStart(t, self)})
	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	f.h.readClaudeBridge(f.j)
	if f.c.claudeLinks.fresh(f.j.DispatchKey, f.j.RemoteControl.NativeSessionID, time.Now()) != nil {
		t.Fatal("control: bridge-less process has a link")
	}
	replacement, _ := json.Marshal(map[string]any{"pid": parent, "sessionId": f.j.RemoteControl.NativeSessionID, "procStart": procStart(t, parent), "bridgeSessionId": fixtureBridge})
	if err := os.WriteFile(filepath.Join(f.h.Home, ".claude", "sessions", fmt.Sprintf("%d.json", parent)), replacement, 0600); err != nil {
		t.Fatal(err)
	}
	info := fmt.Sprintf(`{"result":{"process_info":{"foreground_processes":[{"pid":%d}]}}}`, parent)
	writeFixture(t, filepath.Join(f.h.Home, ".local", "bin", "herdr"), "#!/bin/sh\nprintf '%s\\n' '"+info+"'\n")
	f.h.readClaudeBridge(f.j)
	if f.c.claudeLinks.fresh(f.j.DispatchKey, f.j.RemoteControl.NativeSessionID, time.Now()) != nil {
		t.Error("later process took the pin and supplied a link after first bound process lacked bridge")
	}
	f.herdr(t, f.j.RemoteControl.NativeSessionID, "", "/rc active")
	herdrPath := filepath.Join(f.h.Home, ".local", "bin", "herdr")
	body, err := os.ReadFile(herdrPath)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), fmt.Sprintf(`\"pid\":%d`, self), fmt.Sprintf(`\"pid\":%d`, parent), 1))
	if err := os.WriteFile(herdrPath, body, 0700); err != nil {
		t.Fatal(err)
	}
	f.observe()
	if f.link(t) != nil {
		t.Error("replacement process link was served on the original job's published row")
	}
}

// A native hook can still describe the same session while its process has
// exited and the next asynchronous lookup has not finished. The published
// file itself must not refresh a target for that ended process.
func TestAuditO89EndedPIDWhileRefreshPending(t *testing.T) {
	f := auditNew(t)
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	f.j.RemoteControl.ClaudeProcess = &claudeProcess{PID: pid, Start: procStart(t, pid)}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
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
		t.Fatal("control: live launched PID did not publish a link")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("control: sleep unexpectedly completed successfully")
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d/stat", pid)); !os.IsNotExist(err) {
		t.Fatal("control: PID has not ended")
	}
	release := filepath.Join(f.h.Home, "release-ended-refresh")
	changePID(release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	f.c.observeRemoteControl(ctx, f.h, f.j)
	cancel()
	if f.link(t) != nil {
		t.Error("ended launched PID got a newly fresh published link while refresh was pending")
	}
	if err := os.WriteFile(release, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	f.c.shadow.bg.Wait()
	if f.link(t) != nil {
		t.Error("ended launched PID retained a link after failed refresh")
	}
}
