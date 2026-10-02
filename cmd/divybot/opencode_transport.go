package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Provider IDs and models are data. These are syntax bounds, not a catalog.
var openCodeProviderID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var openCodeModelID = regexp.MustCompile(`^~?[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
var openCodeVariantID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type openCodeRoute struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Variant  string `json:"variant"`
}

func resolveOpenCodeRoute(o Overrides) (openCodeRoute, error) {
	model := o.Model
	if !strings.Contains(model, "/") && o.Router != "" {
		model = o.Router + "/" + model
	}
	provider, id, ok := strings.Cut(model, "/")
	if !ok || !openCodeProviderID.MatchString(provider) || !openCodeModelID.MatchString(id) ||
		(o.Router != "" && o.Router != provider) {
		return openCodeRoute{}, matrixReason("opencode-route-invalid")
	}
	variant := o.Effort
	if variant == "provider_default" || variant == "none" {
		variant = ""
	}
	if variant != "" && !openCodeVariantID.MatchString(variant) {
		return openCodeRoute{}, matrixReason("opencode-variant-unavailable")
	}
	return openCodeRoute{provider, id, variant}, nil
}

func (r openCodeRoute) qualifiedModel() string { return r.Provider + "/" + r.Model }

// `models --verbose` is a sequence of qualified IDs and JSON metadata. Check the
// native providerID and id independently of the ID header; reject duplicates,
// malformed/oversized catalogs, and a missing exact variant. Never pick a fallback.
func validateOpenCodeCatalog(raw []byte, route openCodeRoute) error {
	bad := matrixReason("opencode-catalog-unavailable")
	if len(raw) == 0 || len(raw) > 1024*1024 || !utf8.Valid(raw) {
		return bad
	}
	seen := map[string]bool{}
	found := false
	for len(bytes.TrimSpace(raw)) > 0 {
		raw = bytes.TrimSpace(raw)
		line, rest, ok := bytes.Cut(raw, []byte("\n"))
		if !ok || seen[string(line)] || len(seen) >= 1024 {
			return bad
		}
		seen[string(line)] = true
		decoder := json.NewDecoder(bytes.NewReader(rest))
		var body json.RawMessage
		if decoder.Decode(&body) != nil {
			return bad
		}
		var model struct {
			ID         string                     `json:"id"`
			ProviderID string                     `json:"providerID"`
			Variants   map[string]json.RawMessage `json:"variants"`
		}
		if decodeNativeJSON(body, &model) != nil || model.ProviderID != route.Provider ||
			!openCodeModelID.MatchString(model.ID) || string(line) != model.ProviderID+"/"+model.ID {
			return bad
		}
		if model.ID == route.Model {
			if route.Variant != "" {
				variant, ok := model.Variants[route.Variant]
				var options map[string]json.RawMessage
				if !ok || strictJSON(variant, &options) != nil || options == nil {
					return matrixReason("opencode-variant-unavailable")
				}
			}
			found = true
		}
		raw = rest[decoder.InputOffset():]
	}
	if !found {
		return matrixReason("opencode-model-unavailable")
	}
	return nil
}

// No provider turn, credential output, or unbounded native diagnostics. Use the
// same HOME, cwd and process overlay as the launched TUI. Whole-process-group
// cancellation and the existing 1 MiB writer bound also apply over SSH.
func (h Host) openCodeRead(ctx context.Context, cwd string, env map[string]string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var script strings.Builder
	fmt.Fprintf(&script, "export HOME=%s; export PATH=\"$HOME/.opencode/bin:$HOME/.local/bin:/usr/local/bin:$PATH\"; export TMPDIR=/tmp; ", shq(h.agentHome()))
	for _, key := range []string{"OPENCODE_CONFIG_CONTENT", "XDG_STATE_HOME"} {
		if value := env[key]; value != "" {
			fmt.Fprintf(&script, "export %s=%s; ", key, shq(value))
		}
	}
	fmt.Fprintf(&script, "cd %s && exec opencode --pure", shq(cwd))
	for _, arg := range args {
		script.WriteString(" " + shq(arg))
	}
	script.WriteString(" 2>/dev/null")
	if h.isLocal() {
		return matrixCommand(ctx, "", "bash", nil, "-c", script.String())
	}
	return matrixCommand(ctx, "", "ssh", nil, append(h.sshBase(), h.SSH, script.String())...)
}

func openCodeEnvironment(cwd string, route openCodeRoute) (map[string]string, error) {
	if !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd || cwd == "/" || !cleanText(cwd) || len(cwd) > 4096 {
		return nil, matrixReason("opencode-route-invalid")
	}
	// Agent model precedes the TUI --model fallback. The overlay sets both and
	// changes no permission rule. The isolated state prevents inherited variants.
	config, err := json.Marshal(map[string]any{"model": route.qualifiedModel(), "default_agent": "build",
		"agent": map[string]any{"build": map[string]any{"model": route.qualifiedModel()}}, "share": "disabled"})
	if err != nil {
		return nil, errMatrix
	}
	return map[string]string{"OPENCODE_CONFIG_CONTENT": string(config),
		"XDG_STATE_HOME": filepath.Join(cwd, ".divybot-opencode", "state")}, nil
}

func (h Host) prepareOpenCodeLaunch(ctx context.Context, cwd string, route openCodeRoute, env map[string]string) error {
	// Catalog commands do not select a variant. Avoid creating the private state
	// root until the catalog has independently admitted the route.
	catalog, err := h.openCodeRead(ctx, cwd, map[string]string{"OPENCODE_CONFIG_CONTENT": env["OPENCODE_CONFIG_CONTENT"]}, "models", route.Provider, "--verbose")
	if err != nil {
		return matrixReason("opencode-catalog-unavailable")
	}
	if err := validateOpenCodeCatalog(catalog, route); err != nil {
		return err
	}
	agent, err := h.openCodeRead(ctx, cwd, map[string]string{"OPENCODE_CONFIG_CONTENT": env["OPENCODE_CONFIG_CONTENT"]}, "debug", "agent", "build")
	var selected struct {
		Name, Mode string
		Hidden     bool
		Model      struct{ ProviderID, ModelID string }
	}
	if err != nil || len(agent) > 1024*1024 || decodeNativeJSON(agent, &selected) != nil || selected.Name != "build" || selected.Hidden ||
		(selected.Mode != "primary" && selected.Mode != "all") || selected.Model.ProviderID != route.Provider || selected.Model.ModelID != route.Model {
		return matrixReason("opencode-route-invalid")
	}
	state, _ := json.Marshal(map[string]any{"recent": []any{}, "favorite": []any{},
		"variant": map[string]string{route.qualifiedModel(): route.Variant}})
	root := filepath.Join(cwd, ".divybot-opencode")
	file := filepath.Join(env["XDG_STATE_HOME"], "opencode", "model.json")
	// Never overwrite or traverse a pre-existing repository-supplied state path.
	script := "umask 077; test ! -e " + shq(root) + " && test ! -L " + shq(root) +
		" && mkdir " + shq(root) + " && mkdir -p " + shq(filepath.Dir(file)) +
		" && printf '%s' " + shq(string(state)) + " > " + shq(file) +
		" && printf '\\n.divybot-opencode/\\n' >> " + shq(filepath.Join(cwd, ".git", "info", "exclude"))
	if _, err := h.runRemote(ctx, script); err != nil {
		return matrixReason("opencode-state-unavailable")
	}
	return nil
}

// This private correlation record is not an observed route or a public native
// identity. A session is bound only after native prompt/route evidence agrees.
// ExpectedPromptDigest binds the exact rendered first prompt; records without
// it cannot confirm legacy pointer delivery and are never automatically replayed.
type openCodeRun struct {
	ExpectedPromptDigest string        `json:"expectedPromptDigest,omitempty"`
	Route                openCodeRoute `json:"route"`
	Cwd                  string        `json:"cwd"`
	ExcludedIDs          []string      `json:"excludedIds"`
	NotBefore            int64         `json:"notBefore"`
	SessionID            string        `json:"sessionId,omitempty"`
	CreatedAt            int64         `json:"createdAt,omitempty"`
	Failure              string        `json:"failure,omitempty"`
}

type openCodeSession struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Created   int64  `json:"created"`
}

func decodeOpenCodeSessions(raw []byte) ([]openCodeSession, error) {
	// Installed 1.18.34 returns zero bytes (rc 0) for an empty session index.
	// This supplies no session/answer evidence and can never confirm a run.
	if len(raw) == 0 {
		return []openCodeSession{}, nil
	}
	var sessions []openCodeSession
	if len(raw) > 1024*1024 || !utf8.Valid(raw) || decodeNativeJSONList(raw, &sessions) != nil || sessions == nil || len(sessions) > 128 {
		return nil, matrixReason("opencode-output-unconfirmed")
	}
	seen := map[string]bool{}
	for _, s := range sessions {
		if !privateNativeID(s.ID) || !strings.HasPrefix(s.ID, "ses_") || seen[s.ID] || !filepath.IsAbs(s.Directory) || s.Created <= 0 {
			return nil, matrixReason("opencode-output-unconfirmed")
		}
		seen[s.ID] = true
	}
	return sessions, nil
}

func decodeNativeJSONList(raw []byte, out any) error {
	var records []json.RawMessage
	if strictJSON(raw, &records) != nil {
		return errMatrix
	}
	return json.Unmarshal(raw, out)
}

func (h Host) openCodeSessions(ctx context.Context, run *openCodeRun) ([]openCodeSession, error) {
	env, err := openCodeEnvironment(run.Cwd, run.Route)
	if err != nil {
		return nil, err
	}
	raw, err := h.openCodeRead(ctx, run.Cwd, env, "session", "list", "--format", "json", "--max-count", "128")
	if err != nil {
		return nil, matrixReason("opencode-output-unconfirmed")
	}
	return decodeOpenCodeSessions(raw)
}

func selectOpenCodeSession(sessions []openCodeSession, run *openCodeRun) (string, error) {
	var id string
	for _, session := range sessions {
		if session.Directory != run.Cwd || session.Created < run.NotBefore || containsString(run.ExcludedIDs, session.ID) {
			continue
		}
		if id != "" || (run.SessionID != "" && run.SessionID != session.ID) {
			return "", matrixReason("opencode-output-unconfirmed")
		}
		id = session.ID
	}
	return id, nil
}

type openCodeMessage struct {
	Info struct {
		ID, Role, SessionID, ParentID, ProviderID, ModelID, Variant, Finish string
		Error                                                               json.RawMessage
		Model                                                               struct{ ProviderID, ModelID, Variant string }
		Time                                                                struct{ Created, Completed int64 }
	} `json:"info"`
	Parts []struct {
		Type, Text, SessionID, MessageID string
		Synthetic                        bool
	} `json:"parts"`
}

// Inspect native stored messages, never the requested argv, a footer, an exit
// status or Herdr idle/done. Output from a different parent cannot satisfy a turn.
func inspectOpenCodeExport(raw []byte, run *openCodeRun) (confirmed bool, completed bool, err error) {
	if run == nil || run.NotBefore < 1 || !privateNativeID(run.SessionID) || !strings.HasPrefix(run.SessionID, "ses_") {
		return false, false, matrixReason("opencode-output-unconfirmed")
	}
	var record struct {
		Info struct {
			ID, Directory string
			Time          struct{ Created int64 }
		}
		Messages []openCodeMessage
	}
	bad := matrixReason("opencode-output-unconfirmed")
	if len(raw) > 1024*1024 || !utf8.Valid(raw) || decodeNativeJSON(raw, &record) != nil ||
		record.Info.ID != run.SessionID || record.Info.Directory != run.Cwd || record.Info.Time.Created <= 0 || record.Info.Time.Created < run.NotBefore || (run.CreatedAt != 0 && record.Info.Time.Created != run.CreatedAt) ||
		len(record.Messages) == 0 || len(record.Messages) > 1024 {
		return false, false, bad
	}
	users := map[string]bool{}
	latestUser := ""
	first := true
	seen := map[string]bool{}
	for _, message := range record.Messages {
		info := message.Info
		if info.ID == "" || seen[info.ID] || info.SessionID != run.SessionID || info.Time.Created < record.Info.Time.Created || len(message.Parts) > 1024 {
			return false, false, bad
		}
		seen[info.ID] = true
		var text strings.Builder
		for _, part := range message.Parts {
			if part.SessionID != run.SessionID || part.MessageID != info.ID {
				return false, false, bad
			}
			if part.Type == "text" && !part.Synthetic {
				text.WriteString(part.Text)
			}
		}
		switch info.Role {
		case "user":
			confirmed, completed = false, false
			if info.Model.ProviderID != run.Route.Provider || info.Model.ModelID != run.Route.Model || info.Model.Variant != run.Route.Variant {
				return false, false, matrixReason("opencode-route-mismatch")
			}
			if first && shaText([]byte(text.String())) != run.ExpectedPromptDigest {
				return false, false, bad
			}
			first = false
			users[info.ID] = true
			latestUser = info.ID
		case "assistant":
			if !users[info.ParentID] || info.ParentID != latestUser || info.ProviderID != run.Route.Provider || info.ModelID != run.Route.Model || info.Variant != run.Route.Variant {
				return false, false, matrixReason("opencode-route-mismatch")
			}
			if len(info.Error) > 0 && string(info.Error) != "null" {
				return false, false, matrixReason("opencode-provider-error")
			}
			confirmed = true
			completed = false // An earlier assistant cannot complete a later streaming response.
			if info.Time.Completed > 0 && info.Finish != "" && info.Finish != "tool-calls" {
				if strings.TrimSpace(text.String()) == "" {
					return false, false, matrixReason("opencode-empty-answer")
				}
				completed = true
			}
		default:
			return false, false, bad
		}
	}
	return confirmed, completed, nil
}

func (h Host) observeOpenCode(ctx context.Context, j *Job) (bool, bool, error) {
	run := j.OpenCode
	if run == nil || run.Failure != "" {
		return false, false, matrixReason("opencode-output-unconfirmed")
	}
	before, err := h.agentInfoOf(ctx, j.Pane)
	if err != nil || !openCodeOccupant(before, j, run) {
		return false, false, matrixReason("opencode-output-unconfirmed")
	}
	sessions, err := h.openCodeSessions(ctx, run)
	if err != nil {
		return false, false, err
	}
	id, err := selectOpenCodeSession(sessions, run)
	if err != nil || id == "" {
		return false, false, err
	}
	copy := *run
	copy.SessionID = id
	for _, session := range sessions {
		if session.ID == id {
			copy.CreatedAt = session.Created
		}
	}
	if run.CreatedAt != 0 && run.CreatedAt != copy.CreatedAt {
		return false, false, matrixReason("opencode-output-unconfirmed")
	}
	env, err := openCodeEnvironment(run.Cwd, run.Route)
	if err != nil {
		return false, false, err
	}
	raw, err := h.openCodeRead(ctx, run.Cwd, env, "export", id)
	if err != nil {
		return false, false, matrixReason("opencode-output-unconfirmed")
	}
	confirmed, completed, observationErr := inspectOpenCodeExport(raw, &copy)
	after, err := h.agentInfoOf(ctx, j.Pane)
	if err != nil || !openCodeOccupant(after, j, run) || before.StateChangeSeq != after.StateChangeSeq {
		return false, false, matrixReason("opencode-output-unconfirmed")
	}
	if observationErr == nil && confirmed {
		run.SessionID = id
		run.CreatedAt = copy.CreatedAt
	}
	return confirmed, completed, observationErr
}

func openCodeOccupant(a AgentInfo, j *Job, run *openCodeRun) bool {
	return a.Agent == "opencode" && a.Name == j.Label && a.PaneID == j.Pane &&
		a.WorkspaceID == j.Workspace && a.Cwd == run.Cwd && a.InteractiveReady
}

func (h Host) injectOpenCodeGoal(ctx context.Context, j *Job, goal string, persist func() error) error {
	// The rendered goal is bound at launch, never reconstructed from Job.Goal's
	// truncated summary or recovered by reading a file in the model's first turn.
	if j == nil || j.OpenCode == nil || strings.TrimSpace(goal) == "" || j.OpenCode.ExpectedPromptDigest != shaText([]byte(goal)) {
		return matrixReason("opencode-output-unconfirmed")
	}
	before, err := h.agentInfoOf(ctx, j.Pane)
	if err != nil || j.OpenCode == nil || !openCodeOccupant(before, j, j.OpenCode) || before.AgentStatus != "idle" {
		return matrixReason("opencode-output-unconfirmed")
	}
	sessions, err := h.openCodeSessions(ctx, j.OpenCode)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.Directory == j.OpenCode.Cwd {
			j.OpenCode.ExcludedIDs = append(j.OpenCode.ExcludedIDs, session.ID)
		}
	}
	// The native host's clock supplies the freshness bound, not the dispatcher's
	// clock. An old, truncated-out session cannot satisfy this launch.
	script := "cd " + shq(j.OpenCode.Cwd) + " && exec date '+%s' 2>/dev/null"
	var clock []byte
	if h.isLocal() {
		clock, err = matrixCommand(ctx, "", "bash", nil, "-c", script)
	} else {
		clock, err = matrixCommand(ctx, "", "ssh", nil, append(h.sshBase(), h.SSH, script)...)
	}
	seconds, clockErr := strconv.ParseInt(strings.TrimSpace(string(clock)), 10, 64)
	if err != nil || clockErr != nil || seconds < 1 || seconds > 100000000000 {
		return matrixReason("opencode-output-unconfirmed")
	}
	j.OpenCode.NotBefore = seconds * 1000
	after, readErr := h.agentInfoOf(ctx, j.Pane)
	if readErr != nil || !openCodeOccupant(after, j, j.OpenCode) || after.AgentStatus != "idle" || after.StateChangeSeq != before.StateChangeSeq || persist == nil || persist() != nil {
		return matrixReason("opencode-output-unconfirmed")
	}
	// Baseline was read before this single effect. No Enter nudge or resubmission.
	if _, err := h.herdr(ctx, "agent", "prompt", j.Pane, goal); err != nil {
		return matrixReason("opencode-output-unconfirmed")
	}
	for {
		confirmed, _, err := h.observeOpenCode(ctx, j)
		if confirmed && err == nil {
			return nil
		}
		var reason matrixReason
		if errors.As(err, &reason) && (reason == "opencode-empty-answer" || reason == "opencode-route-mismatch" || reason == "opencode-provider-error") {
			return err
		}
		if !nativePromptWait(ctx) {
			return matrixReason("opencode-output-unconfirmed")
		}
	}
}

func (c *Coord) blockOpenCode(n int, j *Job, err error) {
	reason := "opencode-output-unconfirmed"
	var code matrixReason
	if errors.As(err, &code) && (code == "opencode-empty-answer" || code == "opencode-route-mismatch" || code == "opencode-provider-error") {
		reason = string(code)
	}
	c.st.mu.Lock()
	if j.OpenCode == nil {
		j.OpenCode = &openCodeRun{}
	}
	j.OpenCode.Failure = reason
	_ = c.st.saveLocked()
	c.st.mu.Unlock()
	c.st.blockLaunch(n, reason)
	// Fixed vocabulary only. Raw provider errors, native IDs and text stay private.
	logOpenCodeBlock(n, reason)
}

func logOpenCodeBlock(n int, reason string) {
	log.Printf("issue #%d: OpenCode BLOCKED reason=%s; no automatic replay or completion", n, reason)
}
