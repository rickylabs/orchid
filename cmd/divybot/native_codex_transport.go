package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
)

// One owned canonical generation for native prompt acceptance. Unlike
// withGoalScript, finalization is deliberate: close stdin, drain the same
// scanner to EOF, join the bridge and require a clean end. The bridge runs in
// its opt-in end mode; every other caller keeps the unchanged bridge.

const joinedDrainFrames = 256

func canonicalEndScript(home string) string {
	return fmt.Sprintf("export HOME=%s; exec python3 -c %s end", shq(home), shq(canonicalCodexBridge))
}

// This operation alone asks SSH for errors only, so a benign host-key notice
// cannot become stderr on an otherwise clean bridge end.
func (h Host) joinedArgs(script string) (string, []string) {
	if h.isLocal() {
		return "bash", []string{"-c", script}
	}
	return "ssh", append(append(h.sshBase(), "-o", "LogLevel=ERROR"), h.SSH, script) // guard:ssh-quiet
}

type joinedOptions struct {
	script string                    // "" selects the canonical end-mode bridge
	tap    func(io.Reader) io.Reader // test-only read-boundary witness
}

func (h Host) withJoinedCanonicalConnection(ctx context.Context, thread string, opt joinedOptions, notice func(remoteNativeTurn) error,
	use func(*goalRPC) error, drain func(map[string]json.RawMessage) error) (err error) {
	if ctx.Err() != nil || !privateNativeID(thread) || h.RemoteRun == nil || thread != h.RemoteRun.NativeSessionID {
		return goalError("remote-control-binding-invalid")
	}
	script := opt.script
	if script == "" {
		script = canonicalEndScript(h.agentHome())
	}
	defer children.hold()()
	name, args := h.joinedArgs(script)
	cmd := exec.CommandContext(ctx, name, args...)
	in, e := cmd.StdinPipe()
	if e != nil {
		return goalError("goal-transport-unavailable")
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		return goalError("goal-transport-unavailable")
	}
	var diagnostic remoteBridgeDiagnostic
	cmd.Stderr = &diagnostic
	if cmd.Start() != nil {
		return goalError("goal-transport-unavailable")
	}
	joined := false
	join := func() {
		if !joined {
			_ = in.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			joined = true
		}
	}
	defer join()
	var reader io.Reader = out
	if opt.tap != nil {
		reader = opt.tap(out)
	}
	p := newGoalRPC(in, reader, thread)
	p.ctx = ctx
	p.onTurnNotice = notice
	shadow := h.ShadowScope.open(shadowCodexCanonical, thread)
	p.shadow = shadow
	defer func() { shadow.close(err) }()
	if e := p.initialize(); e != nil {
		return e
	}
	if e := p.verifyRemoteThread(h.RemoteRun, false); e != nil {
		return e
	}
	if e := use(p); e != nil {
		return e
	}
	if ctx.Err() != nil {
		return deliveryGap("bridge-end")
	}
	// Deliberate local shutdown: the only path to a clean end-mode exit.
	if in.Close() != nil {
		return deliveryGap("bridge-end")
	}
	for frames := 0; ; frames++ {
		if frames >= joinedDrainFrames {
			return deliveryGap("drain")
		}
		if !p.scan.Scan() { // guard:drain-scanner
			if p.scan.Err() != nil {
				return deliveryGap("drain")
			}
			break
		}
		var m map[string]json.RawMessage
		if decodeNativeJSON(p.scan.Bytes(), &m) != nil || m == nil { // guard:drain-decode
			return deliveryGap("drain")
		}
		if drain(m) != nil {
			return deliveryGap("drain")
		}
	}
	waitErr := cmd.Wait()
	joined = true
	if waitErr != nil { // guard:joined-wait
		return deliveryGap("bridge-end")
	}
	if len(diagnostic.body) != 0 || diagnostic.overflow { // guard:raw-diagnostic
		return deliveryGap("bridge-diagnostic")
	}
	if ctx.Err() != nil {
		return deliveryGap("bridge-end")
	}
	return nil
}
