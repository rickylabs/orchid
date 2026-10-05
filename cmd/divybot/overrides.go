package main

// Per-issue run configuration ("/swarm" blocks) and the comment trigger.
//
// A /swarm block may appear in an inbox issue body, or in a comment on any
// open issue of a target repo (OpenHands-style). The block starts with a line
// that is exactly "/swarm" and is followed by "key: value" lines; any later
// free text becomes extra operator instructions appended to the worker goal.
//
//	/swarm
//	harness: claude        # claude | codex | opencode | agy
//	model: opus
//	effort: high
//	max-tokens: 500k
//	timeout: 45m
//	profile: netscript-dev
//
//	Focus on the parser only, skip the docs.
//
// A comment trigger is bound directly on its source issue, with no inbox copy;
// see source_binding.go.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Overrides struct {
	MaxTokensPresent bool   `json:"max_tokens_present,omitempty"`
	Tier             string `json:"tier,omitempty"`
	Role             string `json:"role,omitempty"`
	Pin              string `json:"pin,omitempty"`
	RoutingInvalid   bool   `json:"-"`
	// Repo is the source repository the work belongs to (owner/name). When set it
	// alone chooses the target; the inbox issue is only the dispatch binding.
	Repo        string `json:"repo,omitempty"`
	RepoInvalid bool   `json:"-"`
	Harness     string `json:"harness,omitempty"`
	Model       string `json:"model,omitempty"`
	// Router is the opencode provider prefix ("openai", "openrouter", …). opencode
	// models are addressed as provider/model; router lets an operator name the
	// two halves separately (model: gpt-5.5 + router: openai). Ignored when the
	// model already contains a slash, and by non-opencode harnesses.
	Router    string        `json:"router,omitempty"`
	Effort    string        `json:"effort,omitempty"`
	MaxTokens string        `json:"max_tokens,omitempty"`
	Profile   string        `json:"profile,omitempty"`
	Prompt    string        `json:"prompt,omitempty"`
	Timeout   time.Duration `json:"timeout,omitempty"`
}

func (o Overrides) empty() bool {
	return o == Overrides{}
}

// runPointer is the argv prompt handed to non-interactive `opencode run`; the
// real assignment is staged to .divybot-goal.md in the workdir before spawn.
const runPointer = "Read the file .divybot-goal.md in your current directory, in full — it is your complete assignment for this session. Carry it out end to end: implement the change, commit, and open a PR exactly as it instructs. Do NOT commit .divybot-goal.md. Begin now."

var swarmKV = regexp.MustCompile(`^([a-z][a-z_-]*)\s*:\s*(.+?)\s*$`)

// parseOverrides scans text for the FIRST "/swarm" line and consumes the
// key:value lines that immediately follow it. Everything after the kv run
// (until a code-fence close or end of text) is free-text operator prompt.
// Unknown keys are ignored so the vocabulary can grow without breaking older
// coordinators. Returns the zero Overrides when no /swarm line exists.
func parseOverrides(text string) Overrides {
	var o Overrides
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "/swarm" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return o
	}
	seenRouting := map[string]bool{}
	var prompt []string
	inKV := true
	for _, l := range lines[start:] {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") {
			break // closing fence of a ```/swarm``` block
		}
		if inKV {
			if t == "" {
				continue
			}
			if emptyGoalBudgetKey.MatchString(t) {
				o.MaxTokensPresent = true
			} // Detect invalid explicit emptiness without changing prompt parsing.
			if m := swarmKV.FindStringSubmatch(t); m != nil {
				key := strings.ReplaceAll(m[1], "_", "-")
				val := strings.TrimSpace(strings.SplitN(m[2], "#", 2)[0]) // strip trailing comment
				canonical := key
				if key == "agent" {
					canonical = "harness"
				}
				if key == "provider" {
					canonical = "router"
				}
				switch canonical {
				case "tier", "role", "pin", "profile", "model", "effort", "harness", "router":
					if seenRouting[canonical] || val == "" {
						o.RoutingInvalid = true
					}
					seenRouting[canonical] = true
				}
				switch key {
				case "tier":
					o.Tier = val
				case "role":
					o.Role = strings.ReplaceAll(val, "-", "_")
				case "pin":
					o.Pin = val
				case "harness", "agent":
					o.Harness = strings.ToLower(val)
				case "model":
					o.Model = val
				case "router", "provider":
					o.Router = strings.ToLower(val)
				case "effort":
					o.Effort = strings.ToLower(val)
				case "max-tokens":
					o.MaxTokens = val
					o.MaxTokensPresent = true
				case "profile":
					o.Profile = val
				case "repo":
					if o.Repo != "" || !repositoryName.MatchString(val) {
						o.RepoInvalid = true
					}
					o.Repo = val
				case "timeout":
					if d, err := time.ParseDuration(val); err == nil && d > 0 {
						o.Timeout = d
					}
				}
				continue
			}
			inKV = false // first non-kv line: switch to free-text prompt
		}
		prompt = append(prompt, l)
	}
	o.Prompt = strings.TrimSpace(strings.Join(prompt, "\n"))
	return o
}

var errCodexEffort = matrixReason("codex-effort-invalid")

// The matrix input vocabulary is Harness packages/routing/matrix/contract.ts EFFORTS.
// This validates input, not model capability: independent observation still
// owns the runtime verdict. Contract source:
// packages/routing/matrix/contract.ts in the pinned Harness checkout
func validCodexEffort(effort string) bool {
	switch effort {
	case "", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// interactiveAgentArgs is the routing argv source for rendering and registration.
// Managed registration adds a process-local trust setting for its exact checkout.
func interactiveAgentArgs(agent string, o Overrides) (string, []string, error) {
	kind := agent
	var args []string
	switch agent {
	case "codex":
		if !validCodexEffort(o.Effort) {
			return "", nil, errCodexEffort
		}
		args = []string{"--dangerously-bypass-approvals-and-sandbox"}
		if o.Model != "" {
			args = append(args, "-m", o.Model)
		}
		if o.Effort != "" {
			args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(o.Effort))
		}
	case "opencode":
		route, err := resolveOpenCodeRoute(o)
		if err != nil {
			return "", nil, err
		}
		args = append(args, "--pure", "--agent", "build", "--model", route.qualifiedModel())
	case "agy":
		args = []string{"--dangerously-skip-permissions"}
		if o.Model != "" {
			args = append(args, "--model", o.Model)
		}
		if o.Effort != "" {
			args = append(args, "--effort", o.Effort)
		}
	default:
		kind = "claude"
		args = []string{"--dangerously-skip-permissions"}
		if o.Model != "" {
			args = append(args, "--model", o.Model)
		}
	}
	return kind, args, nil
}

// buildAgentCmd renders the same configured argv used by interactive registration.
func buildAgentCmd(agent string, o Overrides) (string, error) {
	ocModel := o.Model
	if ocModel != "" && o.Router != "" && !strings.Contains(ocModel, "/") {
		ocModel = o.Router + "/" + ocModel
	}
	switch agent {
	case "codex-run":
		// Non-interactive codex: `codex exec` with the pointer as argv. RunMode
		// supervision (PR path + deadline) only.
		cmd := "codex exec --dangerously-bypass-approvals-and-sandbox"
		if o.Model != "" {
			cmd += " -m " + shq(o.Model)
		}
		cmd += " " + shq(runPointer)
		return "bash -c " + shq(cmd+`; echo "[divybot] codex exec exited: $?"; exec sleep 2147483647`), nil
	case "opencode-run":
		// Explicit fallback: non-interactive `opencode run` with the pointer as
		// argv — no TUI, no injection, invisible to herdr agent detection (job
		// runs in RunMode: PR-path + deadline supervision only). The trailing
		// sleep holds the pane open so the transcript stays readable until
		// teardown closes the workspace.
		cmd := "opencode run"
		if ocModel != "" {
			cmd += " --model " + shq(ocModel)
		}
		cmd += " " + shq(runPointer)
		return "bash -c " + shq(cmd+`; echo "[divybot] opencode run exited: $?"; exec sleep 2147483647`), nil
	default:
		kind, args, err := interactiveAgentArgs(agent, o)
		if err != nil {
			return "", err
		}
		for _, arg := range args {
			kind += " " + shq(arg)
		}
		return kind, nil
	}
}

// goalPreamble renders operator directives from a /swarm block as a block
// prepended to the worker goal. Empty when there is nothing to say. The profile is
// the dispatcher's pinned Harness text, delivered inline; the worker never looks
// for one in the target checkout.
func (o Overrides) goalPreamble(p *workerProfile) string {
	var b strings.Builder
	if p != nil && p.Text != "" { // guard:goal-profile
		fmt.Fprintf(&b, "FIRST follow the working process below. It is the %s profile from %s at %s (sha256 %s), supplied by the dispatcher, and it defines your working process for this task, including any evaluation models to use. Do not read or follow any profiles/ file in the target repository.\n\n----- BEGIN PROFILE %s -----\n%s\n----- END PROFILE %s -----\n\n",
			p.Name, matrixSourceRepository, p.Revision, shaText([]byte(p.Text)), p.Name, strings.TrimRight(p.Text, "\n"), p.Name)
	}
	if o.Effort != "" {
		fmt.Fprintf(&b, "Operator-requested reasoning effort: %s.\n", o.Effort)
	}
	if o.MaxTokens != "" {
		fmt.Fprintf(&b, "Token budget for this task: %s — be economical and stay under it.\n", o.MaxTokens)
	}
	if o.Prompt != "" {
		fmt.Fprintf(&b, "\nOperator instructions (highest priority):\n%s\n", o.Prompt)
	}
	if b.Len() == 0 {
		return ""
	}
	return "## Operator directives\n\n" + b.String() + "\n"
}

// ============================ comment trigger ============================
