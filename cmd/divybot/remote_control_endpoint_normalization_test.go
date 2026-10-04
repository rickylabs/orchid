package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteControlCanonicalAliasPreservesRawComponents(t *testing.T) {
	for _, mode := range []string{"direct-alias", "real-directory-dotdot", "relative-real-directory-dotdot", "missing-dotdot", "relative-missing-dotdot", "file-dotdot", "relative-file-dotdot", "symlink-dotdot", "relative-symlink-dotdot"} {
		t.Run(mode, func(t *testing.T) {
			kind := strings.TrimPrefix(mode, "relative-")
			var requests atomic.Int32
			h := canonicalFixtureHost(t, "valid", func(string, map[string]any) any { requests.Add(1); return map[string]string{"status": "connected"} })
			entry := filepath.Join(h.Home, ".codex", "app-server-control", "app-server-control.sock")
			target := filepath.Join(h.Home, "daemon.sock")
			if err := os.Rename(entry, target); err != nil {
				t.Fatal(err)
			}
			part := filepath.Join(h.Home, "component")
			link := target
			switch kind {
			case "real-directory-dotdot":
				if err := os.Mkdir(part, 0700); err != nil {
					t.Fatal(err)
				}
			case "file-dotdot":
				if err := os.WriteFile(part, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink-dotdot":
				other := filepath.Join(h.Home, "other")
				if err := os.MkdirAll(filepath.Join(other, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
				listener, err := net.Listen("unix", filepath.Join(other, "daemon.sock"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = listener.Close() })
				if err := os.Chmod(filepath.Join(other, "daemon.sock"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(other, "nested"), part); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "direct-alias" {
				link = part + "/../daemon.sock"
			} // preserve raw path components
			if strings.HasPrefix(mode, "relative-") {
				// Rel must not clean the link under test. Construct only the base,
				// then append the deliberately unresolved components verbatim.
				base, err := filepath.Rel(filepath.Dir(entry), h.Home)
				if err != nil {
					t.Fatal(err)
				}
				link = base + "/component/../daemon.sock"
			}
			if err := os.Symlink(link, entry); err != nil {
				t.Fatal(err)
			}
			canonical, canonicalErr := os.Stat(entry)
			if kind == "symlink-dotdot" {
				resolved, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if canonicalErr != nil || os.SameFile(canonical, resolved) {
					t.Fatal("fixture must name a different canonical kernel socket")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := h.withCanonicalConnection(ctx, privateTestID(t), func(p *goalRPC) error { return p.remoteConnected() })
			t.Logf("mode=%s canonicalExists=%v accepted=%v nativeRequests=%d", mode, canonicalErr == nil, err == nil, requests.Load())
			if kind == "direct-alias" || kind == "real-directory-dotdot" {
				if err != nil || requests.Load() != 1 {
					t.Fatal("safe one-hop control refused")
				}
			} else {
				reason := "remote-control-endpoint-dangling"
				if kind == "file-dotdot" {
					reason = "remote-control-endpoint-parent-unsafe"
				}
				if kind == "symlink-dotdot" {
					reason = "remote-control-endpoint-link-chain"
				}
				if err == nil || requests.Load() != 0 || closedGoalReason(err) != reason {
					t.Fatalf("unsafe alias components not refused before native requests: %s", closedGoalReason(err))
				}
			}
		})
	}
}

func TestRemoteControlRawComponentRecheckedAfterConnect(t *testing.T) {
	for _, relative := range []bool{false, true} {
		name := "absolute"
		if relative {
			name = "relative"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			h := canonicalFixtureHost(t, "valid", func(string, map[string]any) any { requests.Add(1); return map[string]string{"status": "connected"} })
			entry := filepath.Join(h.Home, ".codex", "app-server-control", "app-server-control.sock")
			target := filepath.Join(h.Home, "daemon.sock")
			part := filepath.Join(h.Home, "component")
			if err := os.Rename(entry, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(part, 0700); err != nil {
				t.Fatal(err)
			}
			base := h.Home
			if relative {
				var err error
				base, err = filepath.Rel(filepath.Dir(entry), h.Home)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(base+"/component/../daemon.sock", entry); err != nil {
				t.Fatal(err)
			}
			// Replace only a traversed directory that lexical normalization would
			// hide. Target, ultimate parent and canonical link remain unchanged.
			anchor := "s.connect(path)"
			if strings.Count(canonicalCodexBridge, anchor) != 1 {
				t.Fatal("connect fixture drifted")
			}
			mutation := ";os.rename(" + shq(part) + "," + shq(part+".old") + ");os.mkdir(" + shq(part) + ",0o700)"
			bridge := strings.Replace(canonicalCodexBridge, anchor, anchor+mutation, 1)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := h.withGoalScript(ctx, "export HOME="+shq(h.Home)+";exec python3 -c "+shq(bridge), privateTestID(t), func(p *goalRPC) error { return p.remoteConnected() })
			if err == nil || closedGoalReason(err) != "remote-control-endpoint-changed" || requests.Load() != 0 {
				t.Fatal("changed raw component passed post-connect checks")
			}
		})
	}
}
