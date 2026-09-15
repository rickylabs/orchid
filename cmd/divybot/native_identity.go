package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Native session IDs are private join keys, never public run identifiers.
type nativeIdentityReason string

const (
	nativeUnavailable nativeIdentityReason = "native-session-unavailable"
	nativeUnsupported nativeIdentityReason = "native-session-contract-unsupported"
	nativeInvalid     nativeIdentityReason = "native-session-evidence-invalid"
)

// Herdr protocol 22 AgentInfo.agent_session is supplied by integrations, not
// screen detection. The bundled Codex integration v8 accepts SessionStart stdin,
// checks inherited CODEX_THREAD_ID, and sends pane.report_agent_session over IPC.
// Never accept the path variant, arbitrary sources, or a different pane occupant.
func nativeSessionFromStart(raw json.RawMessage, kind, label string, location *dispatchLocation) (string, nativeIdentityReason) {
	return nativeSessionFromResponse(raw, "agent_started", kind, label, location)
}

func nativeSessionFromResponse(raw json.RawMessage, responseType, kind, label string, location *dispatchLocation) (string, nativeIdentityReason) {
	if kind != "codex" {
		return "", nativeUnsupported
	}
	var response struct {
		Type  string `json:"type"`
		Agent struct {
			Kind      string `json:"agent"`
			Name      string `json:"name"`
			Pane      string `json:"pane_id"`
			Workspace string `json:"workspace_id"`
			Ready     bool   `json:"interactive_ready"`
			Session   *struct {
				Source string `json:"source"`
				Agent  string `json:"agent"`
				Kind   string `json:"kind"`
				Value  string `json:"value"`
			} `json:"agent_session"`
		} `json:"agent"`
	}
	if len(raw) > 1024*1024 || decodeNativeJSON(raw, &response) != nil {
		return "", nativeInvalid
	}
	a := response.Agent
	if response.Type != responseType || location == nil || a.Kind != kind || a.Name != label || a.Pane != location.PaneID || a.Workspace != location.WorkspaceID || !a.Ready {
		return "", nativeInvalid
	}
	if a.Session == nil {
		return "", nativeUnavailable
	}
	s := a.Session
	if s.Source != "herdr:codex" || s.Agent != kind || s.Kind != "id" || !privateNativeID(s.Value) {
		return "", nativeInvalid
	}
	return s.Value, ""
}

func privateNativeID(id string) bool {
	return len(id) > 0 && len(id) <= 256 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\x00\r\n\t /\\")
}

// Reuse the existing duplicate-key/trailing-document guard while allowing the
// extra metadata fields carried by native response schemas.
func decodeNativeJSON(raw []byte, out any) error {
	var fields map[string]json.RawMessage
	if strictJSON(raw, &fields) != nil {
		return errMatrix
	}
	return json.Unmarshal(raw, out)
}

// Extend binding.json, not the public dispatch projection or matrix observation.
// Caller holds r.mu. No native identity is stored on an inconclusive lookup.
func (r *durableMatrixReceipt) writeNativeIdentityLocked(identity *string) (err error) {
	dir := filepath.Dir(r.file)
	path := filepath.Join(dir, "binding.json")
	defer func() {
		// If a failed invalidation cannot replace the binding, remove it rather
		// than retain a usable join key for an uncertain launch. The fence stays.
		if identity == nil && err != nil {
			_ = os.Remove(path)
		}
	}()
	original, e := os.ReadFile(path)
	if identity == nil && os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return errMatrix
	}
	var binding map[string]json.RawMessage
	if decodeNativeJSON(original, &binding) != nil {
		return errMatrix
	}
	if binding == nil {
		binding = map[string]json.RawMessage{}
	}
	if identity == nil {
		if _, ok := binding["NativeSessionID"]; !ok {
			return nil
		}
		delete(binding, "NativeSessionID")
	} else {
		if r.dispatch == nil || r.dispatch.State != "dispatched" {
			return errMatrix
		}
		raw, e := json.Marshal(identity)
		if e != nil {
			return errMatrix
		}
		binding["NativeSessionID"] = raw
	}
	data, e := json.Marshal(binding)
	if e != nil {
		return errMatrix
	}
	f, e := os.CreateTemp(dir, ".binding-")
	if e != nil {
		return errMatrix
	}
	defer os.Remove(f.Name())
	_, written := f.Write(data)
	owned := transferReceiptOwner(r.owner, f.Name())
	synced := f.Sync()
	closed := f.Close()
	if written != nil || owned != nil || synced != nil || closed != nil {
		return errMatrix
	}
	if os.Rename(f.Name(), path) != nil {
		return errMatrix
	}
	if syncDirectory(dir) != nil {
		// A failed directory sync cannot certify the publication. Erase the entire
		// binding rather than leave a successful-looking identity; the fence stays.
		_ = os.Remove(path)
		return errMatrix
	}
	return nil
}
func (r *durableMatrixReceipt) writeNativeIdentity(identity *string) error {
	if r == nil {
		return errMatrix
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeNativeIdentityLocked(identity)
}
