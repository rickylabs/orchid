package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Operator switches affect future launches. Existing runs retain their transport.
type RemoteControlConfig struct {
	Claude *bool `json:"claude,omitempty"`
	Codex  *bool `json:"codex,omitempty"`
}

func (r *RemoteControlConfig) defaults() {
	if r.Claude == nil {
		v := true
		r.Claude = &v
	}
	if r.Codex == nil {
		v := true
		r.Codex = &v
	}
}
func (h Host) remoteEnabled(agent string) bool {
	if h.RemoteControl == nil {
		return false
	}
	switch accountKey(agent) {
	case "claude":
		return h.RemoteControl.Claude != nil && *h.RemoteControl.Claude
	case "codex":
		return h.RemoteControl.Codex != nil && *h.RemoteControl.Codex
	}
	return false
}

// Private runtime binding; never serialized in the public dispatch projection.
type remoteControlRun struct {
	NativeSessionID string            `json:"nativeSessionId"`
	Cwd             string            `json:"cwd"`
	Name            string            `json:"name"`
	Model           string            `json:"model"`
	Effort          string            `json:"effort"`
	IdentitySource  string            `json:"identitySource"`
	HookConfirmed   bool              `json:"hookConfirmed"`
	TUIProcess      *remoteTUIProcess `json:"tuiProcess"`
	// The daemon version this run's TUI client was pinned to, and that client's directory.
	ClientVersion string `json:"-"`
	ClientBinary  string `json:"-"`
	ClientDir     string `json:"-"`
}

// Owner/operator-only decoration. This envelope must not enter the public tree.
type remoteControlObservation struct {
	SchemaVersion   int     `json:"schemaVersion"`
	RunID           string  `json:"runId"`
	NativeSessionID string  `json:"nativeSessionId"`
	Host            string  `json:"host"`
	PaneID          string  `json:"paneId"`
	WorkspaceID     string  `json:"workspaceId"`
	Vendor          string  `json:"vendor"`
	State           string  `json:"state"`
	Reason          *string `json:"reason"`
	SessionName     *string `json:"sessionName"`
	Link            *string `json:"link"`
	ObservedAt      string  `json:"observedAt"`
	ValidUntil      string  `json:"validUntil"`
}

const remoteControlFreshness = 30 * time.Second

func remoteSessionName(number int, title string) (string, error) {
	if number <= 0 || !utf8.ValidString(title) || strings.TrimSpace(title) == "" || strings.IndexFunc(title, unicode.IsControl) >= 0 {
		return "", goalError("remote-control-name-invalid")
	}
	runes := []rune(strings.TrimSpace(title))
	if len(runes) > 160 {
		runes = runes[:160]
	}
	return fmt.Sprintf("#%d %s", number, string(runes)), nil
}

func remoteCodexArgs(args []string, cwd, native string) ([]string, error) {
	if !privateNativeID(native) || !validRemoteCwd(cwd) {
		return nil, goalError("remote-control-binding-invalid")
	}
	// The native remote resume already owns its verified prepared cwd. Explicit
	// --cd invokes a separate daemon-global folder-trust lookup, ignoring the
	// per-thread trust map. Do not manufacture or answer that consent dialog.
	return append(append(append([]string{}, args...), "-c", `tui.status_line=["thread-id"]`, "--remote", "unix://"), "resume", native), nil
}
func validRemoteCwd(cwd string) bool {
	return filepath.IsAbs(cwd) && filepath.Clean(cwd) == cwd && cwd != string(filepath.Separator) && len(cwd) <= 4096 && utf8.ValidString(cwd) && strings.IndexFunc(cwd, unicode.IsControl) < 0
}

func remoteOccupant(raw json.RawMessage, kind, label, cwd, native string, location *dispatchLocation) bool {
	id, reason := nativeSessionFromResponse(raw, "agent_info", kind, label, location)
	var r struct {
		Agent AgentInfo `json:"agent"`
	}
	return reason == "" && id == native && decodeNativeJSON(raw, &r) == nil && r.Agent.Cwd == cwd &&
		(r.Agent.AgentStatus == "idle" || r.Agent.AgentStatus == "done" || r.Agent.AgentStatus == "working")
}

// A deferred hook may be absent before the first turn, but a conflicting hook
// is never interchangeable with the explicit native status proof.
func remoteStatusOccupant(raw json.RawMessage, kind, label, cwd, native string, location *dispatchLocation) bool {
	id, reason := nativeSessionFromResponse(raw, "agent_info", kind, label, location)
	var v struct {
		Agent AgentInfo `json:"agent"`
	}
	return kind == "codex" && (reason == nativeUnavailable || reason == "" && id == native) &&
		decodeNativeJSON(raw, &v) == nil && v.Agent.Cwd == cwd &&
		(v.Agent.AgentStatus == "idle" || v.Agent.AgentStatus == "done" || v.Agent.AgentStatus == "working" || v.Agent.AgentStatus == "blocked")
}

// Only native UI status in an exact, stable occupant can establish connection.
// Never send /remote-control: in an unconnected session it can enable/reconnect.
func claudeRemoteConnected(screen string) bool {
	if len(screen) == 0 || len(screen) > 64*1024 || !utf8.ValidString(screen) {
		return false
	}
	for _, refusal := range []string{"Enable Remote Control", "Remote Control failed", "Remote Control failure", "Remote Control not started", "Remote Control is disabled", "Remote Control is unavailable", "Couldn't reconnect", "couldn't reconnect", "requires a", "not enabled", "reconnect"} {
		if strings.Contains(screen, refusal) {
			return false
		}
	}
	// Match the footer line, not quoted task prose. Goal has not been sent yet.
	for _, line := range strings.Split(screen, "\n") {
		line = strings.TrimSpace(line)
		// Installed 2.1.288 hides the verbose footer after repeated impressions.
		// Its bridge-status renderer requires the current connected session.
		if strings.HasPrefix(line, "/remote-control is active · Continue here, on your phone, or at ") {
			return true
		}
		if strings.HasPrefix(line, "/rc active") && (len(line) == len("/rc active") || strings.Contains(" ·|", line[len("/rc active"):len("/rc active")+1])) {
			return true
		}
	}
	return false
}

func awaitRemoteProof(ctx context.Context, kind, label, cwd, native string, location *dispatchLocation,
	read func(context.Context) (json.RawMessage, error), proof func(context.Context) (bool, error), wait func(context.Context) bool) error {
	return awaitRemoteProofWithOccupant(ctx, func(raw json.RawMessage) bool { return remoteOccupant(raw, kind, label, cwd, native, location) }, read, proof, wait)
}
func awaitRemoteProofWithOccupant(ctx context.Context, occupant func(json.RawMessage) bool,
	read func(context.Context) (json.RawMessage, error), proof func(context.Context) (bool, error), wait func(context.Context) bool) error {
	for ctx.Err() == nil {
		before, err := read(ctx)
		if err != nil || !occupant(before) {
			return goalError("remote-control-identity-unconfirmed")
		}
		connected, err := proof(ctx)
		after, readErr := read(ctx)
		if ctx.Err() != nil || readErr != nil || !occupant(after) {
			return goalError("remote-control-identity-unconfirmed")
		}
		var a, b struct {
			Agent AgentInfo `json:"agent"`
		}
		if decodeNativeJSON(before, &a) != nil || decodeNativeJSON(after, &b) != nil || !reflect.DeepEqual(a.Agent, b.Agent) {
			return goalError("remote-control-identity-unconfirmed")
		}
		if err == nil && connected {
			return nil
		}
		if !wait(ctx) {
			break
		}
	}
	return goalError("remote-control-unconfirmed")
}

func (h Host) remoteProof(ctx context.Context, kind, label string, r *remoteControlRun, location *dispatchLocation) error {
	if r == nil || (r.IdentitySource != "herdr-session-start" && r.IdentitySource != "codex-native-status") {
		return goalError("remote-control-identity-unconfirmed")
	}
	return awaitRemoteProofWithOccupant(ctx, func(raw json.RawMessage) bool {
		if r.IdentitySource == "codex-native-status" {
			return remoteStatusOccupant(raw, kind, label, r.Cwd, r.NativeSessionID, location)
		}
		return remoteOccupant(raw, kind, label, r.Cwd, r.NativeSessionID, location)
	},
		func(ctx context.Context) (json.RawMessage, error) {
			out, err := h.herdr(ctx, "agent", "get", location.PaneID)
			if err != nil {
				return nil, goalError("remote-control-identity-unconfirmed")
			}
			return herdrUnwrap(out)
		}, func(ctx context.Context) (bool, error) {
			if kind == "claude" {
				screen, err := h.visiblePromptScreen(ctx, location.PaneID)
				return err == nil && claudeRemoteConnected(screen), err
			}
			var connected bool
			if err := h.remoteAttachedStatus(ctx, label, r, location); err != nil {
				return false, err
			}
			if r.IdentitySource == "codex-native-status" {
				current, e := h.remoteTUIProcess(ctx, location.PaneID)
				if e != nil || r.TUIProcess == nil || *current != *r.TUIProcess {
					return false, goalError("remote-control-identity-unconfirmed")
				}
			}
			err := h.withCanonicalConnection(ctx, r.NativeSessionID, func(p *goalRPC) error {
				if err := p.remoteConnected(); err != nil {
					return err
				}
				if err := p.verifyRemoteThread(r, false); err != nil {
					return err
				}
				connected = true
				return nil
			})
			if r.IdentitySource == "codex-native-status" {
				current, e := h.remoteTUIProcess(ctx, location.PaneID)
				if e != nil || r.TUIProcess == nil || *current != *r.TUIProcess {
					return false, goalError("remote-control-identity-unconfirmed")
				}
			}
			return connected, err
		}, goalWait)
}

func (h Host) awaitRemoteIdentity(ctx context.Context, kind, label, cwd, expected string, location *dispatchLocation) (string, error) {
	for ctx.Err() == nil {
		out, err := h.herdr(ctx, "agent", "get", location.PaneID)
		if err != nil {
			return "", goalError("remote-control-identity-unconfirmed")
		}
		raw, err := herdrUnwrap(out)
		if err != nil {
			return "", goalError("remote-control-identity-unconfirmed")
		}
		id, reason := nativeSessionFromResponse(raw, "agent_info", kind, label, location)
		if reason == "" {
			if (expected != "" && id != expected) || !remoteOccupant(raw, kind, label, cwd, id, location) {
				return "", goalError("remote-control-identity-unconfirmed")
			}
			return id, nil
		}
		if reason != nativeUnavailable || !goalWait(ctx) {
			break
		}
	}
	return "", goalError("remote-control-identity-unconfirmed")
}

func (h Host) forJob(j *Job) Host {
	h.CanonicalCodex = j != nil && j.Agent == "codex" && j.RemoteControl != nil
	if h.CanonicalCodex {
		h.RemoteRun = j.RemoteControl
	} else {
		h.RemoteRun = nil
	}
	return h
}

func writeRemoteObservation(ctx context.Context, r *durableMatrixReceipt, kind string, run *remoteControlRun, state, reason string, observedName *string) error {
	if ctx.Err() != nil || r == nil || r.dispatch == nil || r.dispatch.State != "dispatched" || r.dispatch.Source != kind || r.dispatch.ParentRunID != nil || r.dispatch.Location == nil || run == nil || !privateNativeID(run.NativeSessionID) || (kind != "claude" && kind != "codex") ||
		(state != "connected" && state != "unconfirmed") || (state == "connected" && reason != "") || (state == "unconfirmed" && (reason != "remote-control-unconfirmed" || observedName != nil)) || (observedName != nil && *observedName != run.Name) {
		return goalError("remote-control-binding-invalid")
	}
	now := time.Now().UTC()
	vendor := "claude"
	if kind == "codex" {
		vendor = "chatgpt"
	}
	var refusal *string
	if reason != "" {
		refusal = &reason
	}
	row := remoteControlObservation{SchemaVersion: 1, RunID: r.dispatch.RunID, NativeSessionID: run.NativeSessionID,
		Host: r.dispatch.Host, PaneID: r.dispatch.Location.PaneID, WorkspaceID: r.dispatch.Location.WorkspaceID,
		Vendor: vendor, State: state, Reason: refusal, SessionName: observedName,
		ObservedAt: now.Format(time.RFC3339Nano), ValidUntil: now.Add(remoteControlFreshness).Format(time.RFC3339Nano)}
	path := filepath.Join(filepath.Dir(r.file), "remote-control.json")
	if err := writePrivateJSON(path, ".remote-control-", r.owner, row); err != nil {
		return err
	}
	if ctx.Err() != nil {
		_ = os.Remove(path)
		_ = syncDirectory(filepath.Dir(path))
		return goalError("remote-control-unconfirmed")
	}
	return nil
}
