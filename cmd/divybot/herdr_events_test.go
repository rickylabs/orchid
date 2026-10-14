package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestHerdrSubscriptionReconcilesInflightEventsAndLoss(t *testing.T) {
	for attempt := 0; attempt < 2; attempt++ {
		client, server := net.Pipe()
		_ = client.SetDeadline(time.Now().Add(3 * time.Second))
		_ = server.SetDeadline(time.Now().Add(3 * time.Second))
		ctx, cancel := context.WithCancel(context.Background())
		subscribed := make(chan struct{})
		duringSnapshot := make(chan struct{})
		eventDrained := make(chan struct{})
		go func() {
			defer server.Close()
			var request struct {
				ID     string `json:"id"`
				Method string `json:"method"`
				Params struct {
					Subscriptions []map[string]string `json:"subscriptions"`
				} `json:"params"`
			}
			if json.NewDecoder(server).Decode(&request) != nil || request.Method != "events.subscribe" {
				return
			}
			encoded, _ := json.Marshal(request)
			t.Logf("subscription=%s", encoded)
			enc := json.NewEncoder(server)
			for _, subscription := range request.Params.Subscriptions {
				if subscription["type"] == "pane.agent_status_changed" && subscription["pane_id"] == "" {
					_ = enc.Encode(map[string]any{"id": "fleet", "error": map[string]any{"code": "invalid_params"}})
					return
				}
			}
			close(subscribed)
			_ = enc.Encode(map[string]any{"id": "fleet", "result": map[string]any{"type": "subscription_started"}})
			<-duringSnapshot
			_ = enc.Encode(map[string]any{"type": "pane_agent_status_changed"})
			// The next write can finish only after the reader consumed the event.
			_ = enc.Encode(map[string]any{"type": "pane_agent_detected"})
			close(eventDrained)
			<-ctx.Done()
		}()
		reads := 0
		var mu sync.Mutex
		var published []AgentInfo
		ready := make(chan struct{}, 1)
		done := make(chan error, 1)
		go func() {
			done <- reconcileHerdrEvents(ctx, []string{"w1:p1"}, client, func(context.Context) ([]AgentInfo, error) {
				select {
				case <-subscribed:
				default:
					return nil, errors.New("snapshot before subscription")
				}
				reads++
				if reads == 1 {
					close(duringSnapshot)
					<-eventDrained
				}
				return []AgentInfo{{StateChangeSeq: uint64(reads)}}, nil
			}, func(agents []AgentInfo, up bool) {
				if up {
					mu.Lock()
					published = agents
					mu.Unlock()
					select {
					case ready <- struct{}{}:
					default:
					}
				}
			}, nil)
		}()
		select {
		case <-ready:
			mu.Lock()
			got := published[0].StateChangeSeq
			mu.Unlock()
			if got < 2 {
				t.Errorf("in-flight event left stale snapshot: %d", got)
			}
		case err := <-done:
			t.Errorf("subscription ended before reconciliation: %v", err)
		case <-time.After(4 * time.Second):
			t.Error("subscription did not drain events during snapshot")
		}
		cancel()
		client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("subscription did not stop")
		}
	}
	// A loss response must invalidate state before a caller resubscribes.
	client, server := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	published := make(chan bool, 4)
	go func() {
		defer server.Close()
		var request any
		_ = json.NewDecoder(server).Decode(&request)
		enc := json.NewEncoder(server)
		_ = enc.Encode(map[string]any{"id": "fleet", "result": map[string]any{"type": "subscription_started"}})
		<-published
		_ = enc.Encode(map[string]any{"id": "fleet", "error": map[string]any{"code": "events_lost", "message": "lost"}})
	}()
	err := reconcileHerdrEvents(ctx, []string{"w1:p1"}, client, func(context.Context) ([]AgentInfo, error) { return nil, nil }, func(_ []AgentInfo, up bool) { published <- up }, nil)
	if err == nil {
		t.Fatal("events_lost was ignored")
	}
	select {
	case up := <-published:
		if up {
			t.Fatal("lost stream retained valid cache")
		}
	default:
		t.Fatal("lost stream did not invalidate cache")
	}
	// Pane creation/removal replaces scoped subscriptions; loss reboots discovery.
	ctx, cancelScopes := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelScopes()
	a := AgentInfo{PaneID: "w1:p1"}
	b := AgentInfo{PaneID: "w2:p1"}
	expected := [][]string{nil, {a.PaneID}, {a.PaneID, b.PaneID}, {b.PaneID}, nil, {b.PaneID}}
	snapshots := [][]AgentInfo{{a}, {a}, {a, b}, {a, b}, {b}, {b}, {b}, {b}}
	cycle, read := 0, 0
	var active chan struct{}
	var releaseMu sync.Mutex
	violations := make(chan string, 8)
	finished := make(chan struct{})
	var servers sync.WaitGroup
	go func() {
		defer close(finished)
		watchHerdrFleet(ctx, func(context.Context) (io.ReadWriteCloser, error) {
			index := cycle
			cycle++
			client, server := net.Pipe()
			release := make(chan struct{}, 1)
			releaseMu.Lock()
			active = release
			releaseMu.Unlock()
			servers.Add(1)
			go func() {
				defer servers.Done()
				defer server.Close()
				var request struct {
					Params struct {
						Subscriptions []map[string]string `json:"subscriptions"`
					} `json:"params"`
				}
				if json.NewDecoder(server).Decode(&request) != nil {
					return
				}
				var scopes []string
				for _, entry := range request.Params.Subscriptions {
					if entry["type"] == "pane.agent_status_changed" {
						scopes = append(scopes, entry["pane_id"])
					}
				}
				if index >= len(expected) || !slices.Equal(scopes, expected[index]) {
					violations <- fmt.Sprintf("scope cycle %d: %v", index, scopes)
					cancelScopes()
					return
				}
				enc := json.NewEncoder(server)
				_ = enc.Encode(map[string]any{"id": "fleet", "result": map[string]any{"type": "subscription_started"}})
				select {
				case <-ctx.Done():
					return
				case <-release:
				}
				switch index {
				case 1, 2:
					_ = enc.Encode(map[string]any{"type": "pane_updated"})
				case 3:
					_ = enc.Encode(map[string]any{"id": "fleet", "error": map[string]any{"code": "events_lost"}})
				}
				<-ctx.Done()
			}()
			return client, nil
		}, func(context.Context) ([]AgentInfo, error) {
			if read >= len(snapshots) {
				return nil, errors.New("unexpected snapshot")
			}
			agents := snapshots[read]
			read++
			return agents, nil
		}, func(_ []AgentInfo, up bool) {
			if !up {
				return
			}
			releaseMu.Lock()
			signal := active
			releaseMu.Unlock()
			select {
			case signal <- struct{}{}:
			default:
			}
			if cycle == len(expected) {
				cancelScopes()
			}
		})
	}()
	<-finished
	servers.Wait()
	if cycle != len(expected) || read != len(snapshots) {
		t.Fatalf("scope/loss recovery incomplete: connections=%d snapshots=%d", cycle, read)
	}
	select {
	case violation := <-violations:
		t.Fatal(violation)
	default:
	}

}

func TestHerdrSnapshotReadsNativeEnvelope(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' '{"id":"snapshot","result":{"type":"session_snapshot","snapshot":{"agents":[{"pane_id":"w1:p1","completion_seq":9}]}}}'
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	agents, err := (Host{Home: root}).agentSnapshot(context.Background())
	if err != nil || len(agents) != 1 || agents[0].CompletionSeq != 9 {
		t.Fatalf("snapshot lost native record: %+v %v", agents, err)
	}
}
