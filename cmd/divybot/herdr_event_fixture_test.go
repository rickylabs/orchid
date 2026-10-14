package main

import (
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
)

// The fake CLI uses a second connection to invalidate the subscribed native
// snapshot. No server, terminal or vendor agent is launched by this fixture.
func herdrEventFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	subscribers := map[net.Conn]*json.Encoder{}
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for c := range subscribers {
			c.Close()
		}
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				var q struct {
					Method string `json:"method"`
				}
				if json.NewDecoder(conn).Decode(&q) != nil {
					conn.Close()
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if q.Method == "events.subscribe" {
					enc := json.NewEncoder(conn)
					subscribers[conn] = enc
					enc.Encode(map[string]any{"id": "fleet", "result": map[string]string{"type": "subscription_started"}})
				} else {
					defer conn.Close()
					for _, enc := range subscribers {
						enc.Encode(map[string]string{"type": "pane_agent_status_changed"})
					}
				}
			}()
		}
	}()
	return path
}
