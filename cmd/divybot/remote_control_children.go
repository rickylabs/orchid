package main

import "encoding/json"

func nativePageComplete(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	return decodeNativeJSON(raw, &fields) == nil && string(fields["nextCursor"]) == "null"
}

type remoteTreeMember struct {
	ID      string  `json:"id"`
	Parent  *string `json:"parentThreadId"`
	Session string  `json:"sessionId"`
}

// Ephemeral subagents have no persisted catalog row. Read the native loaded
// inventory too. Metadata reads confer no write authority: only the native
// session-tree key plus a fully checked parent chain admits a descendant.
func (p *goalRPC) remoteLoadedChildren() ([]nativeRemoteChild, error) {
	read := func(id string) (remoteTreeMember, error) {
		root := p.thread
		p.thread = id
		defer func() { p.thread = root }()
		raw, err := p.request("thread/read", map[string]any{"threadId": id, "includeTurns": false})
		var row struct {
			Thread remoteTreeMember `json:"thread"`
		}
		var envelope map[string]json.RawMessage
		var fields map[string]json.RawMessage
		if err != nil || decodeNativeJSON(raw, &row) != nil || row.Thread.ID != id || !privateNativeID(row.Thread.Session) || decodeNativeJSON(raw, &envelope) != nil || decodeNativeJSON(envelope["thread"], &fields) != nil || fields["parentThreadId"] == nil {
			return remoteTreeMember{}, goalError("remote-control-child-work-unconfirmed")
		}
		return row.Thread, nil
	}
	root, err := read(p.thread)
	if err != nil {
		return nil, err
	}
	raw, err := p.request("thread/loaded/list", map[string]any{"limit": 64})
	var page struct {
		Data []string `json:"data"`
	}
	if err != nil || decodeNativeJSON(raw, &page) != nil || page.Data == nil || len(page.Data) > 64 || !nativePageComplete(raw) {
		return nil, goalError("remote-control-child-work-unconfirmed")
	}
	seen := map[string]bool{}
	children := []nativeRemoteChild{}
	for _, id := range page.Data {
		if !privateNativeID(id) || seen[id] {
			return nil, goalError("remote-control-child-work-unconfirmed")
		}
		seen[id] = true
		if id == p.thread {
			continue
		}
		child, err := read(id)
		if err != nil {
			return nil, err
		}
		if child.Session != root.Session {
			continue
		}
		if child.Parent == nil || !privateNativeID(*child.Parent) {
			return nil, goalError("remote-control-child-work-unconfirmed")
		}
		children = append(children, nativeRemoteChild{ID: id, Parent: child.Parent})
	}
	return children, nil
}
