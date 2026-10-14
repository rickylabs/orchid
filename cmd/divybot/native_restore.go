package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// Installation belongs to the launch account, independently of saved-machine
// command forwarding. Herdr's official integration records the native session
// reference and Herdr resumes it; Orchid never rebuilds the original prompt.
func (h Host) ensureNativeRestore(ctx context.Context, agent string) error {
	target := ""
	switch agent {
	case "opencode":
		target = "opencode"
	case "agy":
		target = "antigravity-cli"
	default:
		return nil
	}
	_, err := h.herdrAccountCommand(ctx, "integration", "install", target)
	return err
}

// Config/install operations are local to the target account and are not
// supported by --machine. Credential and filesystem operations stay here too.
func (h Host) herdrAccountCommand(ctx context.Context, args ...string) (string, error) {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shq(a)
	}
	script := fmt.Sprintf(`unset HERDR_SESSION HERDR_SOCKET_PATH; export HOME=%s; export PATH="$HOME/.local/bin:/usr/local/bin:$PATH"; herdr %s`, shq(h.agentHome()), strings.Join(q, " "))
	return h.runRemote(ctx, script)
}

// AGY's managed launch uses a private app_data_dir. Its integration's default
// "agy --conversation" command cannot find that store. Extend native restore
// through Herdr's documented resume-only report, without replacing the official
// identity reporter or inventing lifecycle state.
func reportAGYRestore(ctx context.Context, j *Job, store *nativeStore, id, home string,
	read func(context.Context, string) (AgentInfo, error), report func(context.Context, ...string) (string, error)) bool {
	if !agyBindingEligible(j) || !agyConversationID.MatchString(id) || !validAGYStore(store, "orchid-"+j.DispatchKey) {
		return false
	}
	before, err := read(ctx, j.Pane)
	matches := func(a AgentInfo) bool {
		var ref struct{ Source, Agent, Kind, Value string }
		return agyIdentityOccupant(a, j) && a.TerminalID != "" && json.Unmarshal(a.AgentSession, &ref) == nil && ref.Source == "herdr:antigravity_cli" && ref.Agent == "agy" && ref.Kind == "id" && ref.Value == id
	}
	if err != nil || !matches(before) || ctx.Err() != nil {
		return false
	}
	directory, err := agyStoreDirectory(before.Cwd, "orchid-"+j.DispatchKey)
	if err != nil || directory != store.Directory {
		return false
	}
	if j.NativeRestoreTerminal == before.TerminalID {
		return true
	}
	_, argv, err := managedInteractiveAgentArgs("agy", j.Overrides, before.Cwd)
	relative, relErr := filepath.Rel(filepath.Join(home, ".gemini"), store.Directory)
	if err != nil || relErr != nil {
		return false
	}
	argv = append([]string{"agy"}, argv...)
	argv = append(argv, "--gemini_dir", filepath.Join(home, ".gemini"), "--app_data_dir", relative, "--conversation", id)
	size := 0
	for _, arg := range argv {
		size += len(arg)
		if strings.ContainsRune(arg, '\'') || strings.IndexFunc(arg, unicode.IsControl) >= 0 {
			return false
		}
	}
	if len(argv) > 64 || size > 8192 {
		return false
	}
	args := []string{"pane", "report-agent-session", j.Pane, "--source", "orchid:restore", "--agent", "agy", "--"}
	out, err := report(ctx, append(args, argv...)...)
	raw, unwrapErr := herdrUnwrap(out)
	var result struct {
		Type string `json:"type"`
	}
	if err != nil || unwrapErr != nil || json.Unmarshal(raw, &result) != nil || result.Type != "ok" || ctx.Err() != nil {
		return false
	}
	after, err := read(ctx, j.Pane)
	if err != nil || !matches(after) || after.TerminalID != before.TerminalID || after.Cwd != before.Cwd || ctx.Err() != nil {
		return false
	}
	j.NativeRestoreTerminal = after.TerminalID
	return true
}

func (c *Coord) registerAGYRestore(ctx context.Context, host Host, j *Job, owner *receiptOwner) {
	r, id, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, owner, "agy")
	if err != nil || id == "" {
		return
	}
	if _, err = readAGYBindingAuthority(r, j); err != nil {
		return
	}
	store, err := readAGYStoreBinding(r)
	if err != nil {
		return
	}
	reportAGYRestore(ctx, j, store, id, host.agentHome(), host.agentInfoOf, host.herdr)
}
