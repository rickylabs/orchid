package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

func openCodeBindingEligible(j *Job) bool {
	if j == nil || j.Agent != "opencode" || j.RunMode || j.GoalDelivery != "confirmed" || j.OpenCode == nil {
		return false
	}
	r := j.OpenCode
	if r.Failure != "" || !privateNativeID(r.SessionID) || !strings.HasPrefix(r.SessionID, "ses_") ||
		!digestPattern.MatchString(r.ExpectedPromptDigest) || r.NotBefore < 1 || r.CreatedAt < r.NotBefore {
		return false
	}
	for _, id := range r.ExcludedIDs {
		if id == r.SessionID {
			return false
		}
	}
	return true
}

// The exact whole-brief reservation and independently selected route remain authority.
// Native prompt/output proof alone cannot replace a changed private launch receipt.
func readOpenCodeBindingAuthority(r *durableMatrixReceipt, j *Job) (string, error) {
	path := filepath.Join(filepath.Dir(r.file), "binding.json")
	s, err := os.Lstat(path)
	if err != nil || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || s.Size() > 1048576 {
		return "", errMatrix
	}
	raw, err := os.ReadFile(path)
	var binding struct {
		IssueID, Repo, BriefDigest, Host string
		Route                            struct{ Transport, Provider, Model, Effort string }
	}
	if err != nil || decodeNativeJSON(raw, &binding) != nil || binding.IssueID == "" || binding.Repo != j.Repo || binding.Host != j.Host ||
		!digestPattern.MatchString(binding.BriefDigest) || shaText([]byte(binding.IssueID+"\x00"+binding.Repo+"\x00"+binding.BriefDigest)) != j.DispatchKey ||
		binding.Route.Transport != "opencode" || binding.Route.Provider != r.dispatch.Provider || binding.Route.Model != r.dispatch.Model ||
		binding.Route.Effort != r.dispatch.Effort {
		return "", errMatrix
	}
	route, err := resolveOpenCodeRoute(Overrides{Model: binding.Route.Model, Router: binding.Route.Provider, Effort: binding.Route.Effort})
	if err != nil || route != j.OpenCode.Route {
		return "", errMatrix
	}
	return shaText(raw), nil
}

func retryOpenCodeNativeBinding(ctx context.Context, root, inbox string, owner *receiptOwner, j *Job,
	readAgent func(context.Context, string) (AgentInfo, error), observe func(context.Context, *Job) (bool, bool, error)) bool {
	if ctx.Err() != nil || !openCodeBindingEligible(j) {
		return false
	}
	r, existing, err := loadNativeBindingReceipt(root, j.DispatchKey, j, inbox, owner, "opencode")
	if err != nil || existing != "" || r.dispatch.Location == nil || r.dispatch.Host != j.Host ||
		r.dispatch.Location.PaneID != j.Pane || r.dispatch.Location.WorkspaceID != j.Workspace {
		return false
	}
	beforeBinding, err := readOpenCodeBindingAuthority(r, j)
	if err != nil {
		return false
	}
	proof := *j.OpenCode
	before, err := readAgent(ctx, j.Pane)
	if err != nil || ctx.Err() != nil || !openCodeOccupant(before, j, &proof) {
		return false
	}
	confirmed, _, err := observe(ctx, j)
	if err != nil || ctx.Err() != nil || !confirmed || !openCodeBindingEligible(j) || !reflect.DeepEqual(proof, *j.OpenCode) {
		return false
	}
	after, err := readAgent(ctx, j.Pane)
	if err != nil || ctx.Err() != nil || !openCodeOccupant(after, j, &proof) || before.StateChangeSeq != after.StateChangeSeq {
		return false
	}
	verified, existing, err := loadNativeBindingReceipt(root, j.DispatchKey, j, inbox, owner, "opencode")
	if err != nil || existing != "" || !reflect.DeepEqual(r.dispatch, verified.dispatch) {
		return false
	}
	currentBinding, err := readOpenCodeBindingAuthority(verified, j)
	if err != nil || currentBinding != beforeBinding || ctx.Err() != nil {
		return false
	}
	id := proof.SessionID
	return publishOpenCodeNativeIdentity(ctx, id, verified.writeNativeIdentity)
}

// Invalidate a receipt written after cancellation rather than certify late proof.
func publishOpenCodeNativeIdentity(ctx context.Context, id string, write func(*string) error) bool {
	return publishNativeIdentity(ctx, id, write)
}

func (c *Coord) bindOpenCodeLiveIdentity(ctx context.Context, host Host, j *Job) {
	if c.dry || !openCodeBindingEligible(j) {
		return
	}
	owner, err := configuredReceiptOwner(c.cfg.Matrix)
	if err != nil {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if retryOpenCodeNativeBinding(readCtx, c.cfg.Matrix.ReceiptRoot, c.cfg.Inbox, owner, j, host.agentInfoOf, host.observeOpenCode) {
		log.Printf("issue #%d: OpenCode native identity bound", j.Issue)
	}
}
