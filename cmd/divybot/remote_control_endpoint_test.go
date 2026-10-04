package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteControlCanonicalEndpoint(t *testing.T) {
	for _, test := range []struct{ mode, reason string }{
		{"direct", ""}, {"absolute", ""}, {"relative", ""}, {"parent-0755", ""}, {"single-read", ""}, {"dial-resolved", ""},
		{"foreign-link", "remote-control-endpoint-entry-owner"},
		{"non-socket", "remote-control-endpoint-not-socket"},
		{"socket-0660", "remote-control-endpoint-socket-mode"},
		{"foreign-target", "remote-control-endpoint-socket-owner"},
		{"chain", "remote-control-endpoint-link-chain"},
		{"dangling", "remote-control-endpoint-dangling"},
		{"parent-writable", "remote-control-endpoint-parent-unsafe"},
		{"parent-link", "remote-control-endpoint-link-chain"},
		{"ancestor-link", "remote-control-endpoint-link-chain"},
		{"entry-replaced", "remote-control-endpoint-changed"},
		{"target-replaced", "remote-control-endpoint-changed"},
		{"parent-replaced", "remote-control-endpoint-changed"},
		{"parent-mode-changed", "remote-control-endpoint-changed"},
		{"canonical-directory-replaced", "remote-control-endpoint-changed"},
		{"canonical-directory-mode-changed", "remote-control-endpoint-changed"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			var requests atomic.Int32
			h := canonicalFixtureHost(t, "valid", func(string, map[string]any) any {
				requests.Add(1)
				return map[string]string{"status": "connected"}
			})
			entry := filepath.Join(h.Home, ".codex", "app-server-control", "app-server-control.sock")
			parent := filepath.Join(h.Home, "daemon")
			if os.Mkdir(parent, 0700) != nil {
				t.Fatal("fixture parent unavailable")
			}
			target := filepath.Join(parent, "daemon.sock")
			bridge := canonicalCodexBridge
			if test.mode != "direct" {
				if os.Rename(entry, target) != nil {
					t.Fatal("fixture socket relocation unavailable")
				}
				link := target
				if test.mode == "relative" {
					var err error
					link, err = filepath.Rel(filepath.Dir(entry), target)
					if err != nil {
						t.Fatal("relative fixture unavailable")
					}
				}
				if test.mode == "non-socket" {
					link = filepath.Join(parent, "plain")
					if os.WriteFile(link, []byte("fixture"), 0600) != nil {
						t.Fatal("non-socket fixture unavailable")
					}
				}
				if test.mode == "socket-0660" && os.Chmod(target, 0660) != nil {
					t.Fatal("socket mode fixture unavailable")
				}
				if test.mode == "chain" {
					link = filepath.Join(parent, "second")
					if os.Symlink(target, link) != nil {
						t.Fatal("chain fixture unavailable")
					}
				}
				if test.mode == "dangling" {
					link = filepath.Join(parent, "missing")
				}
				if test.mode == "parent-0755" || test.mode == "parent-writable" {
					mode := os.FileMode(0755)
					if test.mode == "parent-writable" {
						mode = 0770
					}
					if os.Chmod(parent, mode) != nil {
						t.Fatal("parent mode fixture unavailable")
					}
				}
				if test.mode == "parent-link" || test.mode == "ancestor-link" {
					alias := filepath.Join(h.Home, "alias")
					if os.Symlink(parent, alias) != nil {
						t.Fatal("parent alias fixture unavailable")
					}
					link = filepath.Join(alias, "daemon.sock")
					if test.mode == "ancestor-link" {
						if os.Mkdir(filepath.Join(parent, "nested"), 0700) != nil || os.Rename(target, filepath.Join(parent, "nested", "daemon.sock")) != nil {
							t.Fatal("ancestor fixture unavailable")
						}
						link = filepath.Join(alias, "nested", "daemon.sock")
					}
				}
				if os.Symlink(link, entry) != nil {
					t.Fatal("canonical alias fixture unavailable")
				}
			}
			if test.mode == "foreign-link" || test.mode == "foreign-target" {
				// Substitute only lstat's UID input: all successful controls and
				// peer checks use the real kernel socket. No chown privilege needed.
				foreign := entry
				if test.mode == "foreign-target" {
					foreign = target
				}
				injection := "original_lstat=os.lstat\ndef foreign_stat(path):\n result=original_lstat(path)\n if path==" + shq(foreign) + ":\n  fields=list(result);fields[4]=os.getuid()+1;return os.stat_result(fields)\n return result\nos.lstat=foreign_stat\ndef run():"
				bridge = strings.Replace(bridge, "def run():", injection, 1)
			}
			if test.mode == "single-read" {
				injection := "original_readlink=os.readlink\nreads=0\ndef one_readlink(path):\n global reads\n reads+=1\n if reads!=1:raise ValueError()\n return original_readlink(path)\nos.readlink=one_readlink\ndef run():"
				bridge = strings.Replace(bridge, "def run():", injection, 1)
			}
			if test.mode == "dial-resolved" {
				injection := "class PinnedSocket(socket.socket):\n def connect(self,path):\n  if path!=" + shq(target) + ":raise ValueError()\n  return super().connect(path)\nsocket.socket=PinnedSocket\ndef run():"
				bridge = strings.Replace(bridge, "def run():", injection, 1)
			}
			mutation := ""
			switch test.mode {
			case "entry-replaced":
				mutation = "os.rename(" + shq(entry) + "," + shq(entry+".old") + "); os.symlink(" + shq(target) + "," + shq(entry) + ")"
			case "target-replaced":
				mutation = "os.rename(" + shq(target) + "," + shq(target+".old") + "); os.symlink(" + shq(target+".old") + "," + shq(target) + ")"
			case "parent-replaced":
				mutation = "os.rename(" + shq(parent) + "," + shq(parent+".old") + "); os.mkdir(" + shq(parent) + ",0o700)"
			case "parent-mode-changed":
				mutation = "os.chmod(" + shq(parent) + ",0o770)"
			case "canonical-directory-replaced":
				canonical := filepath.Dir(entry)
				mutation = "os.rename(" + shq(canonical) + "," + shq(canonical+".old") + "); os.mkdir(" + shq(canonical) + ",0o700)"
			case "canonical-directory-mode-changed":
				mutation = "os.chmod(" + shq(filepath.Dir(entry)) + ",0o755)"
			}
			if mutation != "" {
				anchor := "s.connect(path)"
				if strings.Count(bridge, anchor) != 1 {
					t.Fatal("connect boundary fixture drifted")
				}
				bridge = strings.Replace(bridge, anchor, anchor+"; "+mutation, 1)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := h.withGoalScript(ctx, "export HOME="+shq(h.Home)+"; exec python3 -c "+shq(bridge), privateTestID(t), func(p *goalRPC) error { return p.remoteConnected() })
			if test.reason == "" {
				if err != nil || requests.Load() != 1 {
					t.Fatal("owned canonical endpoint did not connect")
				}
			} else if err == nil || closedGoalReason(err) != test.reason || requests.Load() != 0 {
				t.Fatalf("unsafe endpoint was not refused before native requests with named reason: got %s", closedGoalReason(err))
			}
		})
	}
}

func TestRemoteControlEndpointDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name, text, reason string
	}{
		{"closed", "remote-control-endpoint-link-chain\n", "remote-control-endpoint-link-chain"},
		{"raw", "untrusted native stderr", ""},
		{"prefix", "remote-control-endpoint-made-up\n", ""},
		{"multiple", "remote-control-endpoint-link-chain\nremote-control-endpoint-dangling\n", ""},
		{"overflow", "remote-control-endpoint-link-chain\n" + strings.Repeat("x", 256), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var d remoteBridgeDiagnostic
			// Check both fragmented input and bounded discard semantics.
			for _, b := range []byte(test.text) {
				if n, err := d.Write([]byte{b}); n != 1 || err != nil {
					t.Fatal("bounded stderr collection blocked the helper")
				}
			}
			err := d.reason()
			if len(d.body) > 128 || (err == nil) != (test.reason == "") || err != nil && err.Error() != test.reason {
				t.Fatal("untrusted stderr escaped the closed diagnostic boundary")
			}
		})
	}
	t.Run("overflow-whole", func(t *testing.T) {
		var d remoteBridgeDiagnostic
		input := bytes.Repeat([]byte("x"), 4096)
		if n, err := d.Write(input); n != len(input) || err != nil || len(d.body) != 128 || !d.overflow || d.reason() != nil {
			t.Fatal("stderr capacity did not consume and discard excessive input")
		}
	})
}
