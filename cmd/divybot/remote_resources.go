package main

import (
	"context"
	"encoding/json"
	"time"
)

// Native archive owns MCP shutdown. Never infer child-process ownership from
// process names, cwd, or the shared daemon's process tree. Retirement calls this
// only after its durable completion/stop fence and paired seat/process absence.
func (p *goalRPC) releaseRemoteResources() error {
	children, err := p.remoteChildren()
	if err != nil {
		return err
	}
	if p.remoteResourcesAbsent(children) {
		return nil
	} // retry after archive/save interruption
	if err = p.remoteWorkIdle(true); err != nil {
		return err
	}
	// Archive also shuts down the native descendant tree. All descendants must
	// have passed the native terminal-work checks before this mutation.
	raw, err := p.request("thread/archive", map[string]any{"threadId": p.thread})
	var result map[string]json.RawMessage
	if err != nil || decodeNativeJSON(raw, &result) != nil || result == nil || len(result) != 0 {
		return goalError("remote-control-resource-release-unconfirmed")
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 0; attempt < 20 && ctx.Err() == nil; attempt++ {
		if p.remoteResourcesAbsent(children) {
			return nil
		}
		if !goalWait(ctx) {
			break
		}
	}
	return goalError("remote-control-resource-release-unconfirmed")
}

// Archive acknowledgement alone is insufficient: wait for native runtime
// unloading of the exact owned tree. Persisted conversation history is retained.
func (p *goalRPC) remoteResourcesAbsent(children []nativeRemoteChild) bool {
	ids := []string{p.thread}
	for _, child := range children {
		ids = append(ids, child.ID)
	}
	root := p.thread
	defer func() { p.thread = root }()
	for _, id := range ids {
		p.thread = id
		raw, err := p.request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
		var row struct {
			Thread struct {
				ID     string `json:"id"`
				Status struct {
					Type string `json:"type"`
				} `json:"status"`
			} `json:"thread"`
		}
		if err != nil || decodeNativeJSON(raw, &row) != nil || row.Thread.ID != id || row.Thread.Status.Type != "notLoaded" {
			return false
		}
	}
	p.thread = root
	raw, err := p.request("thread/loaded/list", map[string]any{"limit": 64})
	var page struct {
		Data []string `json:"data"`
	}
	if err != nil || decodeNativeJSON(raw, &page) != nil || page.Data == nil || len(page.Data) > 64 || !nativePageComplete(raw) {
		return false
	}
	for _, loaded := range page.Data {
		for _, owned := range ids {
			if loaded == owned {
				return false
			}
		}
	}
	return p.reconcileNativeLifecycle() == nil
}

func (h Host) releaseRemoteResources(ctx context.Context, j *Job) error {
	if j == nil || j.Agent != "codex" || j.RemoteControl == nil {
		return nil
	}
	check, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return h.withCanonicalConnection(check, j.RemoteControl.NativeSessionID, func(p *goalRPC) error {
		if err := p.verifyRemoteThread(j.RemoteControl, false); err != nil {
			return err
		}
		return p.releaseRemoteResources()
	})
}
