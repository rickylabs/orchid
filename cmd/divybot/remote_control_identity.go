package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

type remoteTUIProcess struct {
	PID         int    `json:"pid"`
	Fingerprint string `json:"fingerprint"`
}

// Retain a portable process-start/argv fingerprint alongside the one-shot TUI
// status proof. Pane/cwd/name reuse cannot reuse an earlier attachment proof.
const remoteTUIProcessPython = `import os,sys,subprocess,hashlib,json
try:
 pid=int(sys.argv[1])
 owner=subprocess.check_output(['ps','-p',str(pid),'-o','uid='],timeout=2).decode().strip()
 info=subprocess.check_output(['ps','-p',str(pid),'-o','lstart=','-o','command='],timeout=2)
 if int(owner)!=os.getuid() or not info.strip() or len(info)>16384:raise ValueError()
 print(json.dumps({'fingerprint':hashlib.sha256(info.strip()).hexdigest()}))
except Exception:sys.exit(2)
`

func (h Host) remoteTUIProcess(ctx context.Context, pane string) (*remoteTUIProcess, error) {
	out, err := h.herdr(ctx, "pane", "process-info", "--pane", pane)
	if err != nil {
		return nil, goalError("remote-control-identity-unconfirmed")
	}
	raw, err := herdrUnwrap(out)
	var row struct {
		ProcessInfo struct {
			Processes []struct {
				PID  int    `json:"pid"`
				Name string `json:"name"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	}
	if err != nil || decodeNativeJSON(raw, &row) != nil {
		return nil, goalError("remote-control-identity-unconfirmed")
	}
	pid := 0
	for _, p := range row.ProcessInfo.Processes {
		if p.Name == "codex" {
			if pid != 0 || p.PID <= 1 {
				return nil, goalError("remote-control-identity-unconfirmed")
			}
			pid = p.PID
		}
	}
	if pid == 0 {
		return nil, goalError("remote-control-identity-unconfirmed")
	}
	out, err = h.runRemote(ctx, "python3 -c "+shq(remoteTUIProcessPython)+" "+strconv.Itoa(pid))
	var result struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err != nil || len(out) > 256 || decodeNativeJSON([]byte(out), &result) != nil || !digestPattern.MatchString(result.Fingerprint) || ctx.Err() != nil {
		return nil, goalError("remote-control-identity-unconfirmed")
	}
	return &remoteTUIProcess{PID: pid, Fingerprint: result.Fingerprint}, nil
}

// 0.159.3 thread/loaded/list lists memory-resident threads, including one just
// prepared by Orchid. It exposes no client/pane association. Use the native TUI
// status card once, before any model turn, instead of treating loaded as attached.
func nativeCodexStatusMatches(screen, home string, run *remoteControlRun) bool {
	if run == nil || !privateNativeID(run.NativeSessionID) || len(screen) > 64*1024 || !utf8.ValidString(screen) {
		return false
	}
	if !emptyCodexComposer(screen) || !strings.Contains(screen, "/status") {
		return false
	}
	values := map[string]string{}
	for _, line := range strings.Split(screen, "\n") {
		line = strings.TrimSpace(strings.Trim(line, "│ "))
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		switch key {
		case "Session", "Directory", "Model", "Thread name":
			if _, duplicate := values[key]; duplicate {
				return false
			}
			values[key] = strings.TrimSpace(value)
		}
	}
	cwd := values["Directory"]
	if strings.HasPrefix(cwd, "~/") {
		cwd = filepath.Join(home, cwd[2:])
	}
	model := values["Model"]
	if model != run.Model && !strings.HasPrefix(model, run.Model+" (") {
		return false
	}
	if run.Effort != "" && !strings.Contains(model, "reasoning "+run.Effort) {
		return false
	}
	return values["Session"] == run.NativeSessionID && cwd == run.Cwd && values["Thread name"] == run.Name
}

func establishCodexStatusIdentity(ctx context.Context, expected AgentInfo, home string, run *remoteControlRun, d promptCalls) error {
	var before, previous promptSnapshot
	stable := false
	for ctx.Err() == nil {
		s, err := d.observe(ctx)
		if err != nil || !samePromptOccupant(expected, s.Agent) || !codexReady(s) || strings.Contains(s.Screen, "/status") {
			return goalError("remote-control-identity-unconfirmed")
		}
		if stable && previous.Agent.StateChangeSeq == s.Agent.StateChangeSeq && previous.Screen == s.Screen {
			before = s
			break
		}
		previous, stable = s, true
		if !d.wait(ctx) {
			return goalError("remote-control-identity-unconfirmed")
		}
	}
	if ctx.Err() != nil || !stable {
		return goalError("remote-control-identity-unconfirmed")
	}
	// One native status command, no Enter repair, model turn or consent command.
	if d.submit(ctx, "/status") != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	for ctx.Err() == nil {
		s, err := d.observe(ctx)
		if err != nil || !samePromptOccupant(before.Agent, s.Agent) || !codexReady(s) {
			return goalError("remote-control-identity-unconfirmed")
		}
		if nativeCodexStatusMatches(s.Screen, home, run) {
			if ctx.Err() != nil {
				break
			}
			run.IdentitySource = "codex-native-status"
			return nil
		}
		if !d.wait(ctx) {
			break
		}
	}
	return goalError("remote-control-identity-unconfirmed")
}

func (h Host) preGoalRemoteIdentity(ctx context.Context, kind, label string, run *remoteControlRun, location *dispatchLocation) error {
	if kind != "codex" {
		id, err := h.awaitRemoteIdentity(ctx, kind, label, run.Cwd, run.NativeSessionID, location)
		if err != nil {
			return err
		}
		run.NativeSessionID, run.IdentitySource, run.HookConfirmed = id, "herdr-session-start", true
		return nil
	}
	// The native status read proves TUI attachment even when an official hook
	// already exists. Keep the hook as a separate confirmation, never replace it.
	out, err := h.herdr(ctx, "agent", "get", location.PaneID)
	if err != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	raw, err := herdrUnwrap(out)
	if err != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	id, reason := nativeSessionFromResponse(raw, "agent_info", kind, label, location)
	if reason == "" && remoteOccupant(raw, kind, label, run.Cwd, run.NativeSessionID, location) && id == run.NativeSessionID {
		run.HookConfirmed = true
	} else if reason != nativeUnavailable {
		return goalError("remote-control-identity-unconfirmed")
	}
	var before struct {
		Agent AgentInfo `json:"agent"`
	}
	if decodeNativeJSON(raw, &before) != nil || !remoteStatusOccupant(raw, kind, label, run.Cwd, run.NativeSessionID, location) {
		return goalError("remote-control-identity-unconfirmed")
	}
	process, err := h.remoteTUIProcess(ctx, location.PaneID)
	if err != nil {
		return err
	}
	err = establishCodexStatusIdentity(ctx, before.Agent, h.agentHome(), run, promptCalls{
		observe: func(ctx context.Context) (promptSnapshot, error) { return h.promptSnapshot(ctx, location.PaneID) },
		submit: func(ctx context.Context, text string) error {
			_, err := h.herdr(ctx, "agent", "prompt", location.PaneID, text)
			return err
		},
		wait: goalWait,
	})
	current, currentErr := h.remoteTUIProcess(ctx, location.PaneID)
	if err != nil || currentErr != nil || *process != *current || ctx.Err() != nil {
		run.IdentitySource = ""
		return goalError("remote-control-identity-unconfirmed")
	}
	run.TUIProcess = process
	return nil
}

// The invocation-only native footer is the currently attached TUI identity.
// Require its entire ID at the bottom, after the composer, never an ID quoted
// in transcript prose. It grants no native work/completion proof.
func nativeCodexFooterIdentity(screen string) string {
	if len(screen) > 64*1024 || !utf8.ValidString(screen) || codexTrustDialog(screen) {
		return ""
	}
	lines := strings.Split(strings.TrimRight(screen, "\n\r \t"), "\n")
	if len(lines) < 2 {
		return ""
	}
	last := strings.Fields(lines[len(lines)-1])
	if len(last) == 0 || !actionIDPattern.MatchString(last[0]) {
		return ""
	}
	if !strings.Contains(strings.Join(lines[:len(lines)-1], "\n"), "›") {
		return ""
	}
	return last[0]
}

func (h Host) remoteAttachedStatus(ctx context.Context, label string, run *remoteControlRun, location *dispatchLocation) error {
	if run == nil || location == nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	if run.IdentitySource == "codex-native-status" {
		current, err := h.remoteTUIProcess(ctx, location.PaneID)
		if err != nil || run.TUIProcess == nil || *current != *run.TUIProcess {
			return goalError("remote-control-identity-unconfirmed")
		}
	}
	err := verifyCodexFooterAttachment(ctx, label, run, location, func() (promptSnapshot, error) { return h.promptSnapshot(ctx, location.PaneID) })
	if err != nil {
		return err
	}
	if run.IdentitySource == "codex-native-status" {
		current, err := h.remoteTUIProcess(ctx, location.PaneID)
		if err != nil || run.TUIProcess == nil || *current != *run.TUIProcess {
			return goalError("remote-control-identity-unconfirmed")
		}
	}
	if ctx.Err() != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	return nil
}

func verifyCodexFooterAttachment(ctx context.Context, label string, run *remoteControlRun, location *dispatchLocation, observe func() (promptSnapshot, error)) error {
	if ctx.Err() != nil || run == nil || location == nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	before, err := observe()
	if err != nil || !before.Stable {
		return goalError("remote-control-identity-unconfirmed")
	}
	raw, _ := json.Marshal(map[string]any{"type": "agent_info", "agent": before.Agent})
	if !remoteStatusOccupant(raw, "codex", label, run.Cwd, run.NativeSessionID, location) {
		return goalError("remote-control-identity-unconfirmed")
	}
	id := nativeCodexFooterIdentity(before.Screen)
	if id == "" {
		return goalError("remote-control-identity-unconfirmed")
	}
	if id != run.NativeSessionID {
		return goalError("remote-control-hook-mismatch")
	}
	after, err := observe()
	if err != nil || !after.Stable || !samePromptOccupant(before.Agent, after.Agent) || before.Agent.StateChangeSeq != after.Agent.StateChangeSeq || nativeCodexFooterIdentity(after.Screen) != id || ctx.Err() != nil {
		return goalError("remote-control-identity-unconfirmed")
	}
	raw, _ = json.Marshal(map[string]any{"type": "agent_info", "agent": after.Agent})
	if !remoteStatusOccupant(raw, "codex", label, run.Cwd, run.NativeSessionID, location) {
		return goalError("remote-control-identity-unconfirmed")
	}
	return nil
}
