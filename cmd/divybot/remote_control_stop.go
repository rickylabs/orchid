package main

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
)

type nativeRemoteChild struct {
	ID     string  `json:"id"`
	Parent *string `json:"parentThreadId"`
}

// The native ancestor filter is authoritative, but still recheck every chain.
// Never broaden to a cwd search or stop an unrelated thread.
func (p *goalRPC) remoteChildren() ([]nativeRemoteChild, error) {
	var page struct {
		Data []nativeRemoteChild `json:"data"`
		Next *string             `json:"nextCursor"`
	}
	page.Data = []nativeRemoteChild{}
	// Archive may acknowledge a timed-out shutdown. Include both catalog states;
	// neither archive nor omission from the loaded inventory proves work stopped.
	for _, archived := range []bool{false, true} {
		raw, err := p.request("thread/list", map[string]any{"ancestorThreadId": p.thread, "sourceKinds": []string{"subAgent"}, "limit": 64, "useStateDbOnly": true, "archived": archived})
		var current struct {
			Data []nativeRemoteChild `json:"data"`
		}
		if err != nil || decodeNativeJSON(raw, &current) != nil || current.Data == nil || len(current.Data) > 64 || !nativePageComplete(raw) {
			return nil, goalError("remote-control-child-work-unconfirmed")
		}
		page.Data = append(page.Data, current.Data...)
		if len(page.Data) > 64 {
			return nil, goalError("remote-control-child-work-unconfirmed")
		}
	}
	parents := map[string]string{}
	loaded, err := p.remoteLoadedChildren()
	if err != nil {
		return nil, err
	}
	for _, child := range loaded {
		present := false
		for _, stored := range page.Data {
			if stored.ID == child.ID {
				if stored.Parent == nil || child.Parent == nil || *stored.Parent != *child.Parent {
					return nil, goalError("remote-control-child-work-unconfirmed")
				}
				present = true
				break
			}
		}
		if !present {
			page.Data = append(page.Data, child)
		}
	}
	if len(page.Data) > 64 {
		return nil, goalError("remote-control-child-work-unconfirmed")
	}
	for _, child := range page.Data {
		if !privateNativeID(child.ID) || child.ID == p.thread || child.Parent == nil || !privateNativeID(*child.Parent) {
			return nil, goalError("remote-control-child-work-unconfirmed")
		}
		if _, duplicate := parents[child.ID]; duplicate {
			return nil, goalError("remote-control-child-work-unconfirmed")
		}
		parents[child.ID] = *child.Parent
	}
	for id := range parents {
		for depth := 0; id != p.thread; depth++ {
			if depth >= 64 {
				return nil, goalError("remote-control-child-work-unconfirmed")
			}
			parent, ok := parents[id]
			if !ok {
				return nil, goalError("remote-control-child-work-unconfirmed")
			}
			id = parent
		}
	}
	sort.Slice(page.Data, func(i, j int) bool { return page.Data[i].ID < page.Data[j].ID })
	return page.Data, nil
}

func (p *goalRPC) withRemoteChild(child nativeRemoteChild, use func() error) error {
	root := p.thread
	defer func() { p.thread = root }()
	p.thread = child.ID
	raw, err := p.request("thread/read", map[string]any{"threadId": child.ID, "includeTurns": false})
	var current struct {
		Thread nativeRemoteChild `json:"thread"`
	}
	if err != nil || decodeNativeJSON(raw, &current) != nil || current.Thread.ID != child.ID || current.Thread.Parent == nil || *current.Thread.Parent != *child.Parent {
		return goalError("remote-control-child-work-unconfirmed")
	}
	return use()
}

func (p *goalRPC) remoteWorkIdle(allowEmpty bool) error {
	if err := p.remoteThreadIdle(allowEmpty); err != nil {
		return err
	}
	children, err := p.remoteChildren()
	if err != nil {
		return err
	}
	for _, child := range children {
		if err = p.withRemoteChild(child, func() error {
			goal, e := p.get()
			if e != nil {
				return e
			}
			if goal != nil && goal.Status != "complete" && goal.Status != "paused" {
				return goalError("remote-control-child-work-unconfirmed")
			}
			return p.remoteThreadIdle(false)
		}); err != nil {
			return err
		}
	}
	// Recheck the root after children; an intervening new turn cannot certify idle.
	if err = p.remoteThreadIdle(allowEmpty); err != nil {
		return err
	}
	current, err := p.remoteChildren()
	if err != nil || !reflect.DeepEqual(current, children) {
		return goalError("remote-control-child-work-unconfirmed")
	}
	return p.reconcileNativeLifecycle()
}

func (p *goalRPC) stopRemoteWork(intent *goalIntent) error {
	// Stop root work first so it cannot create another descendant during cleanup.
	if err := p.stopRemoteThread(intent); err != nil {
		return err
	}
	children, err := p.remoteChildren()
	if err != nil {
		return err
	}
	for _, child := range children {
		if err = p.withRemoteChild(child, func() error {
			goal, e := p.get()
			if e != nil {
				return e
			}
			var childIntent *goalIntent
			if goal != nil {
				childIntent = &goalIntent{Objective: goal.Objective, TokenBudget: goal.TokenBudget}
			}
			return p.stopRemoteThread(childIntent)
		}); err != nil {
			return err
		}
	}
	return p.remoteWorkIdle(true)
}

// Do not confuse a TUI process with work owned by a shared native daemon.
// Every query/mutation remains scoped to the prepared native thread. Native
// archive/interrupt acknowledgements never substitute for terminal readback.
func (p *goalRPC) remoteThreadIdle(allowEmpty bool) error {
	raw, err := p.request("thread/read", map[string]any{"threadId": p.thread, "includeTurns": false})
	var head struct {
		Thread struct {
			ID     string `json:"id"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if err != nil || decodeNativeJSON(raw, &head) != nil || head.Thread.ID != p.thread || head.Thread.Status.Type != "idle" {
		return goalError("remote-control-work-unconfirmed")
	}
	for _, method := range []string{"thread/queue/list", "thread/backgroundTerminals/list"} {
		raw, err = p.request(method, map[string]any{"threadId": p.thread, "limit": 1})
		var page struct {
			Data []json.RawMessage `json:"data"`
			Next *string           `json:"nextCursor"`
		}
		if err != nil || decodeNativeJSON(raw, &page) != nil || page.Data == nil || len(page.Data) != 0 || page.Next != nil || !nativePageComplete(raw) {
			return goalError("remote-control-work-unconfirmed")
		}
	}
	raw, err = p.request("thread/turns/list", map[string]any{"threadId": p.thread, "limit": 1, "sortDirection": "desc", "itemsView": "full"})
	var page struct {
		Data []json.RawMessage `json:"data"`
	}
	if err != nil || decodeNativeJSON(raw, &page) != nil || page.Data == nil || len(page.Data) > 1 {
		return goalError("remote-control-work-unconfirmed")
	}
	if len(page.Data) == 0 {
		if allowEmpty {
			p.recordNativeTurnProof("", "")
			return nil
		}
		return goalError("remote-control-work-unconfirmed")
	}
	var turn struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		ItemsView string `json:"itemsView"`
		Items     []struct {
			Type              string   `json:"type"`
			Status            string   `json:"status"`
			ReceiverThreadIDs []string `json:"receiverThreadIds"`
			AgentThreadID     string   `json:"agentThreadId"`
		} `json:"items"`
	}
	if decodeNativeJSON(page.Data[0], &turn) != nil || !privateNativeID(turn.ID) || turn.ItemsView != "full" || turn.Items == nil || len(turn.Items) > 512 {
		return goalError("remote-control-work-unconfirmed")
	}
	switch turn.Status {
	case "completed", "interrupted", "failed":
	default:
		return goalError("remote-control-work-unconfirmed")
	}
	for _, item := range turn.Items {
		switch item.Type {
		case "commandExecution", "mcpToolCall", "dynamicToolCall", "fileChange", "collabAgentToolCall":
			switch item.Status {
			case "completed", "interrupted", "failed", "declined":
			default:
				return goalError("remote-control-work-unconfirmed")
			}
		}
	}
	p.recordNativeTurnProof(turn.ID, turn.Status)
	return nil
}

func (p *goalRPC) stopRemoteThread(intent *goalIntent) error {
	goal, err := p.get()
	if err != nil {
		return err
	}
	if goal != nil {
		if intent == nil || !sameGoalIntent(goal, *intent) {
			return goalError("remote-control-goal-mismatch")
		}
		// Pause with the existing equality/notification proof before cancellation.
		if goal.Status != "complete" && goal.Status != "paused" {
			if _, err = transitionDispatchGoal(p, *intent, "paused"); err != nil {
				return err
			}
		}
	}
	raw, err := p.request("thread/turns/list", map[string]any{"threadId": p.thread, "limit": 1, "sortDirection": "desc", "itemsView": "notLoaded"})
	var page struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err != nil || decodeNativeJSON(raw, &page) != nil || page.Data == nil || len(page.Data) > 1 {
		return goalError("remote-control-work-unconfirmed")
	}
	if len(page.Data) == 1 && page.Data[0].Status == "inProgress" {
		if !privateNativeID(page.Data[0].ID) {
			return goalError("remote-control-work-unconfirmed")
		}
		if _, err = p.request("turn/interrupt", map[string]any{"threadId": p.thread, "turnId": page.Data[0].ID}); err != nil {
			return err
		}
	}
	// Background process termination is itself scoped, followed by an empty list.
	raw, err = p.request("thread/backgroundTerminals/list", map[string]any{"threadId": p.thread, "limit": 64})
	var terminals struct {
		Data []struct {
			ProcessID string `json:"processId"`
		} `json:"data"`
		Next *string `json:"nextCursor"`
	}
	if err != nil || decodeNativeJSON(raw, &terminals) != nil || terminals.Data == nil || terminals.Next != nil || len(terminals.Data) > 64 || !nativePageComplete(raw) {
		return goalError("remote-control-work-unconfirmed")
	}
	for _, terminal := range terminals.Data {
		if !privateNativeID(terminal.ProcessID) {
			return goalError("remote-control-work-unconfirmed")
		}
		response, e := p.request("thread/backgroundTerminals/terminate", map[string]any{"threadId": p.thread, "processId": terminal.ProcessID})
		var result struct {
			Terminated bool `json:"terminated"`
		}
		if e != nil || decodeNativeJSON(response, &result) != nil || !result.Terminated {
			return goalError("remote-control-work-unconfirmed")
		}
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	idle := false
	for attempt := 0; attempt < 20 && ctx.Err() == nil; attempt++ {
		if err = p.remoteThreadIdle(true); err == nil {
			idle = true
			break
		}
		if !goalWait(ctx) {
			break
		}
	}
	if !idle || ctx.Err() != nil {
		return goalError("remote-control-work-unconfirmed")
	}
	goal, err = p.get()
	if err != nil {
		return err
	}
	if goal != nil && (intent == nil || !sameGoalIntent(goal, *intent) || (goal.Status != "complete" && goal.Status != "paused")) {
		return goalError("remote-control-goal-mismatch")
	}
	return p.reconcileNativeLifecycle()
}

func (h Host) stopRemoteRun(ctx context.Context, j *Job) error {
	if j == nil || j.Agent != "codex" || j.RemoteControl == nil {
		return nil
	}
	if !privateNativeID(j.RemoteControl.NativeSessionID) {
		return goalError("remote-control-binding-invalid")
	}
	var intent *goalIntent
	if j.NativeGoal != nil {
		intent = &j.NativeGoal.Intent
	}
	return h.withCanonicalConnection(ctx, j.RemoteControl.NativeSessionID, func(p *goalRPC) error {
		if err := p.verifyRemoteThread(j.RemoteControl, false); err != nil {
			return err
		}
		return p.stopRemoteWork(intent)
	})
}
