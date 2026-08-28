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
// Comment-triggered runs are mirrored into the inbox (like assignmentTick)
// with the comment appended to the mirrored body, so the whole existing
// spawn/supervise pipeline applies unchanged.

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

type Overrides struct {
	Harness   string        `json:"harness,omitempty"`
	Model     string        `json:"model,omitempty"`
	// Router is the opencode provider prefix ("openai", "n5air", …). opencode
	// models are addressed as provider/model; router lets an operator name the
	// two halves separately (model: gpt-5.5 + router: openai). Ignored when the
	// model already contains a slash, and by non-opencode harnesses.
	Router string `json:"router,omitempty"`
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

// Default models per harness when the /swarm block names none. Kept in sync
// with the NetScript agentic toolchain defaults (WSL) and the current
// OpenRouter catalog — refresh these as new flagships land.
const (
	defaultClaudeModel   = "opus"                     // Claude Opus 5 alias
	defaultCodexModel    = "gpt-5.6-sol"              // codex CLI flagship
	defaultOpencodeModel = "openrouter/z-ai/glm-5.3"  // OpenRouter flagship (2026-08)
)

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
			if m := swarmKV.FindStringSubmatch(t); m != nil {
				key := strings.ReplaceAll(m[1], "_", "-")
				val := strings.TrimSpace(strings.SplitN(m[2], "#", 2)[0]) // strip trailing comment
				switch key {
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
				case "profile":
					o.Profile = val
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

// buildAgentCmd renders the launch command for a harness, applying overrides.
func buildAgentCmd(agent string, o Overrides) string {
	// opencode addresses models as provider/model; a "router:" key supplies the
	// provider half when the model was given bare.
	ocModel := o.Model
	if ocModel != "" && o.Router != "" && !strings.Contains(ocModel, "/") {
		ocModel = o.Router + "/" + ocModel
	}
	switch agent {
	case "codex":
		// Real interactive codex TUI (herdr detects it as agent "codex" and
		// `agent prompt --wait` drives it — verified on this host 2026-08-28).
		// First run in a workdir shows a directory-trust prompt that leaves the
		// agent "blocked"; injectGoal clears it with an Enter and retries.
		m := o.Model
		if m == "" {
			m = defaultCodexModel
		}
		return "codex --dangerously-bypass-approvals-and-sandbox -m " + shq(m)
	case "codex-run":
		// Non-interactive codex: `codex exec` with the pointer as argv. RunMode
		// supervision (PR path + deadline) only.
		m := o.Model
		if m == "" {
			m = defaultCodexModel
		}
		cmd := "codex exec --dangerously-bypass-approvals-and-sandbox -m " + shq(m)
		cmd += " " + shq(runPointer)
		return "bash -c " + shq(cmd+`; echo "[divybot] codex exec exited: $?"; exec sleep 2147483647`)
	case "opencode":
		// Interactive opencode TUI (herdr-supervised). The goal pointer is
		// injected via herdr's native `agent prompt --wait`, which confirms
		// submission server-side; the full goal is staged to .divybot-goal.md
		// before spawn.
		if ocModel == "" {
			ocModel = defaultOpencodeModel
		}
		return "opencode --model " + shq(ocModel)
	case "opencode-run":
		// Explicit fallback: non-interactive `opencode run` with the pointer as
		// argv — no TUI, no injection, invisible to herdr agent detection (job
		// runs in RunMode: PR-path + deadline supervision only). The trailing
		// sleep holds the pane open so the transcript stays readable until
		// teardown closes the workspace.
		if ocModel == "" {
			ocModel = defaultOpencodeModel
		}
		cmd := "opencode run --model " + shq(ocModel)
		cmd += " " + shq(runPointer)
		return "bash -c " + shq(cmd+`; echo "[divybot] opencode run exited: $?"; exec sleep 2147483647`)
	case "agy":
		// Antigravity CLI (agy 1.1.22+): supports the same auto-approve flag as
		// claude, plus real --model and --effort flags (agy models: gemini-3.x
		// families with per-effort variants).
		cmd := "agy --dangerously-skip-permissions"
		if o.Model != "" {
			cmd += " --model " + shq(o.Model)
		}
		if o.Effort != "" {
			cmd += " --effort " + shq(o.Effort)
		}
		return cmd
	default: // claude
		m := o.Model
		if m == "" {
			m = defaultClaudeModel
		}
		return "claude --dangerously-skip-permissions --model " + shq(m)
	}
}

// goalPreamble renders operator directives from a /swarm block as a block
// prepended to the worker goal. Empty when there is nothing to say.
func (o Overrides) goalPreamble() string {
	var b strings.Builder
	if o.Profile != "" {
		fmt.Fprintf(&b, "FIRST read profiles/%s.md in the repo root (if present) — it defines your working process for this task, including any evaluation models to use.\n", o.Profile)
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

// commentTick scans recent issue comments on every target repo for "/swarm"
// trigger comments and mirrors each into the inbox (once), carrying the
// comment text so parseOverrides sees it at spawn. Only comments authored by
// the bot login are honored — anyone else commenting /swarm on a public repo
// must not be able to spend the swarm's quota.
func (c *Coord) commentTick(ctx context.Context) {
	bot := c.cfg.BotLogin
	if bot == "" {
		return
	}
	c.st.mu.Lock()
	if c.st.SeenSwarm == nil {
		c.st.SeenSwarm = map[string]bool{}
	}
	c.st.mu.Unlock()
	have, err := inboxMirrored(ctx, c.cfg.Inbox)
	if err != nil {
		log.Printf("swarm-comments: inbox list failed, skipping: %v", err)
		return
	}
	since := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	for _, t := range c.cfg.Targets {
		if t.Repo == "" || t.Repo == c.cfg.Inbox || t.Label == "" {
			continue
		}
		var comments []struct {
			ID       int64                  `json:"id"`
			Body     string                 `json:"body"`
			IssueURL string                 `json:"issue_url"`
			HTMLURL  string                 `json:"html_url"`
			Author   struct{ Login string } `json:"user"`
		}
		if err := ghJSON(ctx, &comments, "api",
			fmt.Sprintf("repos/%s/issues/comments?since=%s&per_page=100", t.Repo, since)); err != nil {
			log.Printf("swarm-comments: list %s failed: %v", t.Repo, err)
			continue
		}
		for _, cm := range comments {
			if !strings.HasPrefix(strings.TrimSpace(cm.Body), "/swarm") {
				continue
			}
			key := fmt.Sprintf("%s#c%d", t.Repo, cm.ID)
			c.st.mu.Lock()
			seen := c.st.SeenSwarm[key]
			if !seen {
				c.st.SeenSwarm[key] = true
			}
			c.st.mu.Unlock()
			if seen {
				continue
			}
			if cm.Author.Login != bot {
				log.Printf("swarm-comments: ignoring /swarm from non-bot @%s on %s", cm.Author.Login, cm.HTMLURL)
				continue
			}
			// PR conversation comments share the issues/comments feed — a
			// /swarm on a PR is out of scope for spawning.
			if strings.Contains(cm.HTMLURL, "/pull/") {
				continue
			}
			num := 0
			if i := strings.LastIndex(cm.IssueURL, "/"); i >= 0 {
				fmt.Sscanf(cm.IssueURL[i+1:], "%d", &num)
			}
			if num == 0 {
				continue
			}
			ref := fmt.Sprintf("%s#%d", t.Repo, num)
			if have[ref] {
				continue // already mirrored (assignment or an earlier /swarm)
			}
			var is struct {
				Title string `json:"title"`
				Body  string `json:"body"`
				State string `json:"state"`
			}
			if err := ghJSON(ctx, &is, "api", fmt.Sprintf("repos/%s/issues/%d", t.Repo, num)); err != nil || is.State != "open" {
				continue
			}
			title := fmt.Sprintf("[%s] %s", ref, is.Title)
			body := fmt.Sprintf(
				"Triggered by a /swarm comment on [%s](%s).\n\n---\n\n%s\n\n---\n\n%s",
				ref, cm.HTMLURL, truncate(is.Body, 2000), truncate(cm.Body, 1500))
			out, err := run(ctx, "gh", "issue", "create",
				"--repo", c.cfg.Inbox, "--label", t.Label, "--title", title, "--body", body)
			if err != nil {
				log.Printf("swarm-comments: gh issue create failed for %s: %v: %s", ref, err, strings.TrimSpace(out))
				continue
			}
			have[ref] = true
			log.Printf("swarm-comments: opened inbox issue for %s → %s", ref, strings.TrimSpace(out))
		}
	}
}
