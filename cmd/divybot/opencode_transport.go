package main

import (
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
)

// Provider IDs and models are data. These are syntax bounds, not a catalog.
var openCodeProviderID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var openCodeModelID = regexp.MustCompile(`^~?[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)

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
	if variant != "" && !validNativeEffort(variant) {
		return openCodeRoute{}, matrixReason("opencode-variant-unavailable")
	}
	return openCodeRoute{provider, id, variant}, nil
}

func (r openCodeRoute) qualifiedModel() string { return r.Provider + "/" + r.Model }

// The 2.x `model.list` API answer. Check the native providerID and id; reject
// duplicates, malformed/oversized catalogs, and a missing exact variant. Never
// pick a fallback.
func validateOpenCodeCatalog(raw []byte, route openCodeRoute) error {
	models, err := decodeOpenCodeModels(raw)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	found := false
	for _, model := range models {
		key := model.ProviderID + "/" + model.ID
		if seen[key] || !openCodeProviderID.MatchString(model.ProviderID) || !openCodeModelID.MatchString(model.ID) {
			return matrixReason("opencode-catalog-unavailable")
		}
		seen[key] = true
		if model.ProviderID != route.Provider || model.ID != route.Model {
			continue
		}
		found = true
		if route.Variant == "" {
			continue
		}
		var variants []struct {
			ID string `json:"id"`
		}
		if len(model.Variants) == 0 || json.Unmarshal(model.Variants, &variants) != nil {
			return matrixReason("opencode-variant-unavailable")
		}
		ok := false
		for _, variant := range variants {
			ok = ok || variant.ID == route.Variant
		}
		if !ok {
			return matrixReason("opencode-variant-unavailable")
		}
	}
	if !found {
		return matrixReason("opencode-model-unavailable")
	}
	return nil
}

// One 2.x server API operation on a private host this command owns:
// `--standalone` starts it and it exits with the command; the in-memory database
// opens no run or shared store, so it has no session to resume. No provider turn,
// credential output, or unbounded native diagnostics. Whole-process-group
// cancellation and the existing 1 MiB writer bound also apply over SSH.
func (h Host) openCodeAPI(ctx context.Context, cwd string, env map[string]string, operation string) ([]byte, error) {
	binary, err := h.openCodeBinary()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var script strings.Builder
	fmt.Fprintf(&script, "export HOME=%s; export TMPDIR=/tmp; export OPENCODE_DB=:memory:; ", shq(h.agentHome()))
	if value := env["OPENCODE_CONFIG_CONTENT"]; value != "" {
		fmt.Fprintf(&script, "export OPENCODE_CONFIG_CONTENT=%s; ", shq(value))
	}
	fmt.Fprintf(&script, "cd %s && exec %s api --standalone %s 2>/dev/null", shq(cwd), binary, shq(operation))
	if h.isLocal() {
		return matrixCommand(ctx, "", "bash", nil, "-c", script.String())
	}
	return matrixCommand(ctx, "", "ssh", nil, append(h.sshBase(), h.SSH, script.String())...)
}

func openCodeEnvironment(cwd string, route openCodeRoute) (map[string]string, error) {
	if !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd || cwd == "/" || !cleanText(cwd) || len(cwd) > 4096 {
		return nil, matrixReason("opencode-route-invalid")
	}
	// The 2.x TUI takes no --model or --agent; the overlay names both and changes
	// no permission rule. The variant is part of the 2.x model ID. The isolated
	// state keeps the TUI's recent-model choices from replacing the route.
	model := route.qualifiedModel()
	if route.Variant != "" {
		model += "#" + route.Variant
	}
	config, err := json.Marshal(map[string]any{"model": model, "default_agent": "build", "share": "disabled"})
	if err != nil {
		return nil, errMatrix
	}
	return map[string]string{"OPENCODE_CONFIG_CONTENT": string(config),
		"XDG_STATE_HOME": filepath.Join(cwd, ".divybot-opencode", "state")}, nil
}

func (h Host) prepareOpenCodeLaunch(ctx context.Context, cwd string, route openCodeRoute, env map[string]string, ownerChoice ...bool) error {
	// A trusted owner choice is attempted directly. Autonomous launches retain
	// their catalog policy; catalog output never authorizes an owner grant.
	if len(ownerChoice) == 0 || !ownerChoice[0] {
		// Avoid creating the private state root until the catalog has
		// independently admitted the route.
		catalog, err := h.openCodeAPI(ctx, cwd, map[string]string{"OPENCODE_CONFIG_CONTENT": env["OPENCODE_CONFIG_CONTENT"]}, "model.list")
		if err != nil {
			var reason matrixReason
			if errors.As(err, &reason) && reason == "opencode-binary-unconfigured" {
				return err
			}
			return matrixReason("opencode-catalog-unavailable")
		}
		if err := validateOpenCodeCatalog(catalog, route); err != nil {
			return err
		}
	}
	root := filepath.Join(cwd, ".divybot-opencode")
	// Never overwrite or traverse a pre-existing repository-supplied state path.
	script := "umask 077; test ! -e " + shq(root) + " && test ! -L " + shq(root) +
		" && mkdir " + shq(root) + " && mkdir -p " + shq(env["XDG_STATE_HOME"]) +
		" && printf '\\n.divybot-opencode/\\n' >> " + shq(filepath.Join(cwd, ".git", "info", "exclude"))
	if _, err := h.runRemote(ctx, script); err != nil {
		return matrixReason("opencode-state-unavailable")
	}
	return nil
}

// Native paste normalizes line endings. Do this before hashing and sending;
// observed text remains byte-exact, except the one trailing space the 2.1.2 TUI
// appends to a pasted prompt. Staging retains the original rendered goal.
func openCodeFirstPrompt(goal string) string {
	return strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(goal, "\r\n", "\n"), "\r", "\n"), "\n")
}

// This private correlation record is not an observed route or a public native
// identity. SessionID is chosen before launch, but the session is bound only
// after its own store shows the prompt and route. ExpectedPromptDigest binds the
// exact rendered first prompt; records without it are never automatically replayed.
type openCodeRun struct {
	ExpectedPromptDigest string        `json:"expectedPromptDigest,omitempty"`
	Route                openCodeRoute `json:"route"`
	Cwd                  string        `json:"cwd"`
	NotBefore            int64         `json:"notBefore"`
	SessionID            string        `json:"sessionId,omitempty"`
	CreatedAt            int64         `json:"createdAt,omitempty"`
	Failure              string        `json:"failure,omitempty"`
}

type openCodeObservationCalls struct {
	agent func(context.Context, string) (AgentInfo, error)
	store func(context.Context, *openCodeRun) ([]byte, error)
}

func (h Host) observeOpenCode(ctx context.Context, j *Job) (bool, bool, error) {
	return observeOpenCodeSession(ctx, j, openCodeObservationCalls{agent: h.agentInfoOf, store: h.openCodeStore})
}

func observeOpenCodeSession(ctx context.Context, j *Job, calls openCodeObservationCalls) (bool, bool, error) {
	run := j.OpenCode
	if ctx.Err() != nil || run == nil || run.Failure != "" {
		return false, false, matrixReason("opencode-output-unconfirmed")
	}
	before, err := calls.agent(ctx, j.Pane)
	if err != nil || !openCodeOccupant(before, j, run) {
		return false, false, matrixReason("opencode-output-unconfirmed")
	}
	raw, err := calls.store(ctx, run)
	if err != nil {
		return false, false, matrixReason("opencode-output-unconfirmed")
	}
	confirmed, completed, created, observationErr := inspectOpenCodeStore(raw, run)
	after, err := calls.agent(ctx, j.Pane)
	if err != nil || ctx.Err() != nil || !openCodeOccupant(after, j, run) || before.StateChangeSeq != after.StateChangeSeq {
		return false, false, matrixReason("opencode-output-unconfirmed")
	}
	if observationErr == nil && confirmed {
		run.CreatedAt = created
	}
	return confirmed, completed, observationErr
}

func openCodeOccupant(a AgentInfo, j *Job, run *openCodeRun) bool {
	return a.Agent == "opencode" && a.Name == j.Label && a.PaneID == j.Pane &&
		a.WorkspaceID == j.Workspace && a.Cwd == run.Cwd && a.InteractiveReady
}

func (h Host) injectOpenCodeGoal(ctx context.Context, j *Job, goal string, persist func() error) error {
	if j == nil || j.OpenCode == nil || strings.TrimSpace(goal) == "" || j.OpenCode.ExpectedPromptDigest != shaText([]byte(goal)) {
		return matrixReason("opencode-output-unconfirmed")
	}
	expected, err := h.agentInfoOf(ctx, j.Pane)
	if err != nil || !openCodeOccupant(expected, j, j.OpenCode) {
		return matrixReason("opencode-output-unconfirmed")
	}
	err = h.withPromptEvents(ctx, expected, func(ctx context.Context, p *promptEvents) error {
		if _, err := h.waitAgent(ctx, expected); err != nil {
			return matrixReason("opencode-output-unconfirmed")
		}
		return h.deliverOpenCodeGoal(ctx, j, goal, persist, p.wait)
	})
	if err != nil {
		var reason matrixReason
		if errors.As(err, &reason) {
			return err
		}
		return matrixReason("opencode-output-unconfirmed")
	}
	return nil
}

func (h Host) deliverOpenCodeGoal(ctx context.Context, j *Job, goal string, persist func() error, wait func(context.Context) bool) error {

	// The rendered goal is bound at launch, never reconstructed from Job.Goal's
	// truncated summary or recovered by reading a file in the model's first turn.
	if j == nil || j.OpenCode == nil || strings.TrimSpace(goal) == "" || j.OpenCode.ExpectedPromptDigest != shaText([]byte(goal)) {
		return matrixReason("opencode-output-unconfirmed")
	}
	before, err := h.agentInfoOf(ctx, j.Pane)
	if err != nil || j.OpenCode == nil || !openCodeOccupant(before, j, j.OpenCode) || before.AgentStatus != "idle" {
		return matrixReason("opencode-output-unconfirmed")
	}
	// The native host's clock supplies the freshness bound, not the dispatcher's
	// clock. A session created before this prompt cannot satisfy this launch.
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
	if ctx.Err() != nil {
		return matrixReason("opencode-output-unconfirmed")
	}
	if _, err := h.herdr(ctx, "agent", "prompt", j.Pane, goal); err != nil {
		return matrixReason("opencode-output-unconfirmed")
	}
	return waitOpenCodePrompt(ctx, func(ctx context.Context) (bool, bool, error) {
		return h.observeOpenCode(ctx, j)
	}, wait)
}

func waitOpenCodePrompt(ctx context.Context, observe func(context.Context) (bool, bool, error), wait func(context.Context) bool) error {
	for {
		if ctx.Err() != nil {
			return matrixReason("opencode-output-unconfirmed")
		}
		confirmed, _, err := observe(ctx)
		if ctx.Err() != nil {
			return matrixReason("opencode-output-unconfirmed")
		}
		if confirmed && err == nil {
			return nil
		}
		var reason matrixReason
		if errors.As(err, &reason) && (reason == "opencode-empty-answer" || reason == "opencode-route-mismatch" || reason == "opencode-provider-error") {
			return err
		}
		if !wait(ctx) {
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
