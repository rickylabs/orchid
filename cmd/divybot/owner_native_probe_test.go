package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOwnerNativeConnectOnlyProbe(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		wrongPeer   bool
		quiet       bool
	}{
		// The readiness client verifies the peer, then closes without sending bytes.
		{name: "authenticated-empty-EOF", quiet: true},
		{name: "partial-EOF", frame: `{"method":`},
		{name: "empty-line", frame: "\n"},
		{name: "missing-method", frame: "{}\n"},
		{name: "invalid-method", frame: "{\"method\":\"probe\"}\n"},
		{name: "unauthorized-empty-EOF", wrongPeer: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := ownerPortFixture(t, "claude")
			if tc.wrongPeer {
				s.peerUID = func(*net.UnixConn) (int, error) { return os.Getuid() + 1, nil }
			}
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.options.Socket, Net: "unix"})
			if err != nil {
				t.Fatal("fixture listener", err)
			}
			defer listener.Close()
			if err := os.Chmod(s.options.Socket, 0600); err != nil {
				t.Fatal("fixture socket mode", err)
			}
			client, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
			if err != nil {
				t.Fatal("fixture client", err)
			}
			defer client.Close()
			_ = client.SetDeadline(time.Now().Add(2 * time.Second))
			server, err := listener.AcceptUnix()
			if err != nil {
				t.Fatal("fixture accept", err)
			}
			if _, err := io.WriteString(client, tc.frame); err != nil {
				t.Fatal("fixture write", err)
			}
			if err := client.CloseWrite(); err != nil {
				t.Fatal("fixture EOF", err)
			}
			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(previous)
			s.handle(context.Background(), server)
			reply, err := io.ReadAll(client)
			if err != nil {
				t.Fatal("fixture reply", err)
			}
			if tc.quiet {
				if logs.Len() != 0 || len(reply) != 0 {
					t.Fatal("connect-only readiness probe produced a refusal")
				}
			} else {
				var ack ownerNativeAck
				if json.Unmarshal(reply, &ack) != nil || ack.State != "REFUSED" ||
					ack.Reason != "override-invalid" || !strings.Contains(logs.String(), "owner grant REFUSED") {
					t.Fatal("invalid request lost its visible refusal")
				}
			}
			if len(s.active) != 0 {
				t.Fatal("probe or invalid frame installed authority")
			}
		})
	}
}
