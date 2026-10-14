package main

import (
	"context"
	"io"
	"strconv"
	"sync"
	"time"
)

// A subscription belongs to this launch's Host and discovered session socket.
// Reuse the fleet's subscribe-before-read/loss rules; terminal/session changes
// permanently refuse this delivery instead of following a replacement.
type promptEvents struct {
	ctx     context.Context
	mu      sync.Mutex
	latest  AgentInfo
	err     error
	ready   bool
	changed chan struct{}
	done    chan struct{}
	cancel  context.CancelFunc
}

func startPromptEvents(ctx context.Context, expected AgentInfo, stream io.ReadWriteCloser, read func(context.Context) (AgentInfo, error)) *promptEvents {
	ctx, cancel := context.WithCancel(ctx)
	p := &promptEvents{ctx: ctx, changed: make(chan struct{}, 1), done: make(chan struct{}), cancel: cancel}
	notify := func() {
		select {
		case p.changed <- struct{}{}:
		default:
		}
	}
	go func() {
		defer close(p.done)
		defer cancel()
		err := reconcileHerdrEvents(ctx, []string{expected.PaneID}, stream, func(ctx context.Context) ([]AgentInfo, error) {
			a, err := read(ctx)
			if err != nil || !samePromptOccupant(expected, a) {
				return nil, errPromptUnconfirmed
			}
			return []AgentInfo{a}, nil
		}, func(agents []AgentInfo, up bool) {
			p.mu.Lock()
			if !up {
				p.err = errPromptUnconfirmed
			} else if p.err == nil {
				p.latest, p.ready = agents[0], true
			}
			p.mu.Unlock()
			notify()
		}, nil)
		p.mu.Lock()
		if err != nil {
			p.err = errPromptUnconfirmed
		}
		p.mu.Unlock()
		notify()
	}()
	return p
}

func (p *promptEvents) close() { p.cancel(); <-p.done }

func (p *promptEvents) observe(ctx context.Context) (AgentInfo, error) {
	for {
		p.mu.Lock()
		a, ready, err := p.latest, p.ready, p.err
		p.mu.Unlock()
		if err != nil || ctx.Err() != nil {
			return AgentInfo{}, errPromptUnconfirmed
		}
		if ready {
			return a, nil
		}
		if !p.wait(ctx) {
			return AgentInfo{}, errPromptUnconfirmed
		}
	}
}

func (p *promptEvents) wait(ctx context.Context) bool {
	if p.ctx.Err() != nil {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-p.done:
		return false
	case <-p.changed:
		return true
	}
}

func (h Host) waitAgent(ctx context.Context, expected AgentInfo) (AgentInfo, error) {
	deadline, ok := ctx.Deadline()
	if !ok || ctx.Err() != nil || time.Until(deadline) < time.Millisecond {
		return AgentInfo{}, errPromptUnconfirmed
	}
	out, err := h.herdr(ctx, "agent", "wait", expected.Name, "--until", "idle", "--until", "done", "--until", "blocked", "--timeout", strconv.FormatInt(time.Until(deadline).Milliseconds(), 10))
	if err != nil {
		return AgentInfo{}, errPromptUnconfirmed
	}
	raw, err := herdrUnwrap(out)
	var result struct {
		Agent AgentInfo `json:"agent"`
	}
	if err != nil || decodeNativeJSON(raw, &result) != nil || !samePromptOccupant(expected, result.Agent) || !result.Agent.InteractiveReady ||
		(!result.Agent.NativeLifecycle && !h.CanonicalCodex) ||
		(result.Agent.AgentStatus != "idle" && result.Agent.AgentStatus != "done") {
		return AgentInfo{}, errPromptUnconfirmed
	}
	return result.Agent, nil
}

func nativePromptObserved(before, after AgentInfo) bool {
	if !samePromptOccupant(before, after) || !after.NativeLifecycle || after.StateChangeSeq <= before.StateChangeSeq {
		return false
	}
	return after.AgentStatus == "working" || after.AgentStatus == "blocked" ||
		((after.AgentStatus == "idle" || after.AgentStatus == "done") && after.CompletionSeq > before.CompletionSeq)
}

func awaitNativePrompt(ctx context.Context, before AgentInfo, observe func(context.Context) (AgentInfo, error), wait func(context.Context) bool) error {
	for {
		after, err := observe(ctx)
		if err != nil || !samePromptOccupant(before, after) {
			return errPromptUnconfirmed
		}
		if nativePromptObserved(before, after) && ctx.Err() == nil {
			return nil
		}
		if !wait(ctx) {
			return errPromptUnconfirmed
		}
	}
}

func (h Host) withPromptEvents(ctx context.Context, expected AgentInfo, use func(context.Context, *promptEvents) error) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	stream, err := h.herdrEventConnection(ctx)
	if err != nil {
		return errPromptUnconfirmed
	}
	p := startPromptEvents(ctx, expected, stream, func(ctx context.Context) (AgentInfo, error) { return h.agentInfoOf(ctx, expected.PaneID) })
	defer p.close()
	// This baseline can only publish after the subscription acknowledgement.
	if _, err := p.observe(ctx); err != nil {
		return err
	}
	return use(p.ctx, p)
}
