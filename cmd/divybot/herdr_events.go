package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"
)

const fleetRecoveryEvery = 5 * time.Minute

// Events invalidate the cache; their payloads never establish identity or state.
// Drain continuously while authoritative reads run, and repeat a read if an
// event arrived during it. Each new connection subscribes before its baseline.
func reconcileHerdrEvents(ctx context.Context, panes []string, stream io.ReadWriteCloser, snapshot func(context.Context) ([]AgentInfo, error), publish func([]AgentInfo, bool), refresh <-chan time.Time) error {
	defer stream.Close()
	defer publish(nil, false)
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stop()
	subscriptions := []map[string]string{}
	for _, kind := range []string{"pane.created", "pane.closed", "pane.updated", "pane.moved", "pane.exited", "pane.agent_detected"} {
		subscriptions = append(subscriptions, map[string]string{"type": kind})
	}
	for _, pane := range panes {
		subscriptions = append(subscriptions, map[string]string{"type": "pane.agent_status_changed", "pane_id": pane})
	}
	if conn, ok := stream.(net.Conn); ok {
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	}
	if err := json.NewEncoder(stream).Encode(map[string]any{"id": "fleet", "method": "events.subscribe", "params": map[string]any{"subscriptions": subscriptions}}); err != nil {
		return err
	}
	dec := json.NewDecoder(stream)
	var ack struct {
		ID     string `json:"id"`
		Result struct {
			Type string `json:"type"`
		} `json:"result"`
	}
	if err := dec.Decode(&ack); err != nil {
		return err
	}
	if ack.ID != "fleet" || ack.Result.Type != "subscription_started" {
		return errors.New("subscription not acknowledged")
	}
	if conn, ok := stream.(net.Conn); ok {
		_ = conn.SetDeadline(time.Time{})
	}
	dirty := make(chan struct{}, 1)
	failed := make(chan error, 1)
	var publicationMu sync.Mutex
	broken := false
	invalidate := func(err error) {
		publicationMu.Lock()
		defer publicationMu.Unlock()
		broken = true
		failed <- err
		publish(nil, false)
	}
	go func() {
		for {
			var event struct {
				Error *struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := dec.Decode(&event); err != nil {
				invalidate(err)
				return
			}
			if event.Error != nil {
				invalidate(fmt.Errorf("herdr subscription: %s", event.Error.Code))
				return
			}
			select {
			case dirty <- struct{}{}:
			default:
			}
		}
	}()
	for {
		// Clear prior invalidations before taking a snapshot, never afterward.
		select {
		case <-dirty:
		default:
		}
		agents, err := snapshot(ctx)
		if err != nil {
			return err
		}
		select {
		case err := <-failed:
			return err
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		select {
		case <-dirty:
			continue
		default:
		}
		publicationMu.Lock()
		if broken {
			publicationMu.Unlock()
			return <-failed
		}
		publish(agents, true)
		publicationMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-failed:
			return err
		case <-dirty:
		case <-refresh:
		}
	}
}

// Discover the same endpoint as the CLI instead of guessing session/config paths.
// Remote hosts use an owned SSH Unix-socket forward, with no remote helper.
func (h Host) herdrEventConnection(ctx context.Context) (io.ReadWriteCloser, error) {
	check, cancel := context.WithTimeout(ctx, 15*time.Second)
	var out string
	var err error
	if h.HerdrMachine != "" {
		// --machine status deliberately labels its socket "machine:<id>/<session>".
		// Streaming has no --machine wrapper in this release. Resolve the saved
		// account/session, then use the documented account-local discovery CLI.
		profile, profileErr := h.savedHerdrMachine(check)
		if profileErr != nil {
			cancel()
			return nil, profileErr
		}
		h.SSH = profile.Target
		out, err = h.herdrAccountCommand(check, "--session", profile.Session, "status", "server", "--json")
	} else {
		out, err = h.herdr(check, "status", "server", "--json")
	}
	cancel()
	if err != nil {
		return nil, errors.New("herdr endpoint unavailable")
	}
	var status struct {
		Socket  string `json:"socket"`
		Running bool   `json:"running"`
	}
	if json.Unmarshal([]byte(out), &status) != nil || !status.Running || !filepath.IsAbs(status.Socket) {
		return nil, errors.New("herdr endpoint invalid")
	}
	dial := func(ctx context.Context, path string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path)
	}
	if h.HerdrMachine == "" && h.isLocal() {
		return dial(ctx, status.Socket)
	}

	dir, err := os.MkdirTemp("", "orchid-herdr-")
	if err != nil {
		return nil, err
	}
	local := filepath.Join(dir, "socket")
	forwardCtx, stop := context.WithCancel(ctx)
	args := append(h.sshBase(), "-N", "-o", "ExitOnForwardFailure=yes", "-L", local+":"+status.Socket, h.SSH)
	started := make(chan error, 1)
	exited := make(chan struct{})
	go func() { runHerdrForward(forwardCtx, args, started); close(exited) }()
	if err := <-started; err != nil {
		stop()
		<-exited
		_ = os.RemoveAll(dir)
		return nil, err
	}
	cleanup := func() { stop(); <-exited; _ = os.RemoveAll(dir) }
	deadline, cancelDial := context.WithTimeout(ctx, 15*time.Second)
	defer cancelDial()
	for {
		conn, err := dial(deadline, local)
		if err == nil {
			return &herdrForward{Conn: conn, cleanup: cleanup}, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-deadline.Done():
			timer.Stop()
			cleanup()
			return nil, deadline.Err()
		case <-exited:
			timer.Stop()
			cleanup()
			return nil, errors.New("herdr forward ended")
		case <-timer.C:
		}
	}
}

// The reaper gate must cover the entire owned subprocess lifetime, including Wait.
func runHerdrForward(ctx context.Context, args []string, started chan<- error) {
	defer children.hold()()
	cmd := exec.CommandContext(ctx, "ssh", args...)
	err := cmd.Start()
	started <- err
	if err == nil {
		_ = cmd.Wait()
	}
}

type herdrForward struct {
	net.Conn
	once    sync.Once
	cleanup func()
}

func (f *herdrForward) Close() error { err := f.Conn.Close(); f.once.Do(f.cleanup); return err }

func (h Host) agentSnapshot(ctx context.Context) ([]AgentInfo, error) {
	check, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	out, err := h.herdr(check, "api", "snapshot")
	if err != nil {
		return nil, errors.New("herdr snapshot unavailable")
	}
	raw, err := herdrUnwrap(out)
	if err != nil {
		return nil, err
	}
	var response struct {
		Type     string `json:"type"`
		Snapshot *struct {
			Agents []AgentInfo `json:"agents"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.Type != "session_snapshot" || response.Snapshot == nil || response.Snapshot.Agents == nil {
		return nil, errors.New("herdr snapshot invalid")
	}
	for i := range response.Snapshot.Agents {
		response.Snapshot.Agents[i].HerdrScope = h.herdrScope()
	}
	return response.Snapshot.Agents, nil
}

type herdrFleet struct {
	hosts  map[string]Host
	mu     sync.RWMutex
	agents map[string][]AgentInfo
	up     map[string]bool
	wake   chan struct{}
}

func (f *herdrFleet) publish(host string, agents []AgentInfo, up bool) {
	f.mu.Lock()
	wasUp, known := f.up[host]
	f.agents[host], f.up[host] = agents, up
	f.mu.Unlock()
	if known && !wasUp && !up {
		return
	}
	select {
	case f.wake <- struct{}{}:
	default:
	}
}
func (c *Coord) startHerdrFleet(ctx context.Context) {
	c.fleet = &herdrFleet{hosts: c.herdrObserverHosts(), agents: map[string][]AgentInfo{}, up: map[string]bool{}, wake: make(chan struct{}, 1)}
	for name, host := range c.fleet.hosts {
		go watchHerdrFleet(ctx, host.herdrEventConnection, host.agentSnapshot,
			func(agents []AgentInfo, up bool) { c.fleet.publish(name, agents, up) })
	}
}

var errHerdrScopesChanged = errors.New("herdr subscription scopes changed")

func herdrStatusPanes(agents []AgentInfo) []string {
	panes := []string{}
	for _, agent := range agents {
		if agent.PaneID != "" {
			panes = append(panes, agent.PaneID)
		}
	}
	sort.Strings(panes)
	return slices.Compact(panes)
}

// Global lifecycle subscriptions bootstrap discovery. Pane-scoped status
// subscriptions are then acknowledged before the next baseline. Any changed
// pane set replaces the subscription; rejection/loss returns to discovery so a
// closed pane can never keep the entire host stuck on invalid subscriptions.
func watchHerdrFleet(ctx context.Context, connect func(context.Context) (io.ReadWriteCloser, error), snapshot func(context.Context) ([]AgentInfo, error), publish func([]AgentInfo, bool)) {
	recovery := time.NewTicker(fleetRecoveryEvery)
	defer recovery.Stop()
	var panes []string
	for ctx.Err() == nil {
		stream, err := connect(ctx)
		if err == nil {
			subscribed := panes
			err = reconcileHerdrEvents(ctx, subscribed, stream, func(ctx context.Context) ([]AgentInfo, error) {
				agents, err := snapshot(ctx)
				if err != nil {
					return nil, err
				}
				latest := herdrStatusPanes(agents)
				if !slices.Equal(subscribed, latest) {
					panes = latest
					return nil, errHerdrScopesChanged
				}
				return agents, nil
			}, publish, recovery.C)
		} else {
			publish(nil, false)
		}
		if errors.Is(err, errHerdrScopesChanged) {
			continue
		}
		panes = nil
		retry := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			retry.Stop()
			return
		case <-retry.C:
		}
	}
}

// Only routine observations use the snapshot. Effect-time identity checks still
// perform independent authoritative reads before input or retirement.
func (c *Coord) fleetAgentResponse(ctx context.Context, h Host, pane string) (json.RawMessage, error) {
	if c.fleet == nil {
		out, err := h.herdr(ctx, "agent", "get", pane)
		if err != nil {
			return nil, err
		}
		return herdrUnwrap(out)
	}
	c.fleet.mu.RLock()
	defer c.fleet.mu.RUnlock()
	if !c.fleet.up[h.observerKey()] {
		return nil, errors.New("herdr observation unavailable")
	}
	var found *AgentInfo
	for _, a := range c.fleet.agents[h.observerKey()] {
		if a.PaneID == pane {
			if found != nil {
				return nil, errors.New("ambiguous herdr occupant")
			}
			copy := a
			found = &copy
		}
	}
	if found == nil {
		return nil, errors.New("herdr occupant unavailable")
	}
	return json.Marshal(map[string]any{"type": "agent_info", "agent": found})
}
