package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
)

// A prepared identity was already published only after native attachment proof.
// Keep the original receipt immutable: neither a footer nor an absent hook may
// replace it. Recheck all private dispatch/binding bytes around native proof.
func bindRemoteNativeGoal(ctx context.Context, r *durableMatrixReceipt, j *Job, proof func() error) (string, error) {
	if ctx.Err() != nil || r == nil || j == nil || j.RemoteControl == nil || r.dispatch == nil || r.dispatch.State != "dispatched" || r.dispatch.Source != "codex" || j.Agent != "codex" || r.dispatch.Issue.Number != j.Issue || r.dispatch.RunID != "orchid-"+j.DispatchKey || r.dispatch.Location == nil || r.dispatch.Location.PaneID != j.Pane || r.dispatch.Location.WorkspaceID != j.Workspace || r.dispatch.Host != j.Host {
		return "", goalError("goal-dispatch-binding-invalid")
	}
	read := func() (*dispatchBinding, map[string]json.RawMessage, error) {
		var d dispatchBinding
		var b map[string]json.RawMessage
		base := filepath.Dir(r.file)
		if readPrivateActionJSON(filepath.Join(base, "dispatch.json"), &d) != nil || readPrivateActionJSON(filepath.Join(base, "binding.json"), &b) != nil || !reflect.DeepEqual(&d, r.dispatch) {
			return nil, nil, goalError("goal-dispatch-binding-invalid")
		}
		var id, repo string
		if json.Unmarshal(b["NativeSessionID"], &id) != nil || json.Unmarshal(b["Repo"], &repo) != nil || id != j.RemoteControl.NativeSessionID || !privateNativeID(id) || repo != j.Repo {
			return nil, nil, goalError("goal-binding-invalid")
		}
		return &d, b, nil
	}
	dispatch, binding, err := read()
	if err != nil {
		return "", err
	}
	if err = proof(); err != nil {
		return "", err
	}
	after, next, err := read()
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(dispatch, after) || !reflect.DeepEqual(binding, next) {
		return "", goalError("goal-dispatch-binding-invalid")
	}
	return j.RemoteControl.NativeSessionID, nil
}
