package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGitHubEventSocketOwnershipAndCollision(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "hook")
	if err := os.WriteFile(path, []byte("foreign resource"), 0600); err != nil {
		t.Fatal(err)
	}
	if l, err := listenGitHubEvents(path); err == nil {
		l.Close()
		t.Fatal("replaced a foreign resource")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "foreign resource" {
		t.Fatal("collision changed the existing resource")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := listenGitHubEvents(path); err == nil {
		t.Fatal("bound in a public directory")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	l, err := listenGitHubEvents(path)
	if err != nil {
		t.Fatal("private endpoint refused")
	}
	defer l.Close()
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatal("endpoint is not a private socket")
	}
	if _, err := listenGitHubEvents(path); err == nil {
		t.Fatal("replaced a live endpoint")
	}
	if err := os.Rename(path, path+".owned"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "replacement" {
		t.Fatal("shutdown removed a replacement inode")
	}
}

func TestGitHubEventBadConfigurationKeepsRecoveryAvailable(t *testing.T) {
	for _, test := range []string{"disabled", "missing-secret", "public-secret", "short-secret", "missing-endpoint"} {
		t.Run(test, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			path, secret := filepath.Join(root, "hook"), filepath.Join(root, "secret")
			t.Setenv("ORCHID_GITHUB_WEBHOOK_SOCKET", path)
			t.Setenv("ORCHID_GITHUB_WEBHOOK_SECRET_FILE", secret)
			if test == "disabled" || test == "missing-endpoint" {
				t.Setenv("ORCHID_GITHUB_WEBHOOK_SOCKET", "")
			}
			if test == "disabled" {
				t.Setenv("ORCHID_GITHUB_WEBHOOK_SECRET_FILE", "")
			} else if test != "missing-secret" {
				body, mode := githubFixtureSecret, os.FileMode(0600)
				if test == "public-secret" {
					mode = 0644
				}
				if test == "short-secret" {
					body = "short"
				}
				if err := os.WriteFile(secret, []byte(body), mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(secret, mode); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := newCoord(&Config{Inbox: "example/inbox"})
			c.startHerdrFleet(ctx)
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("bad or disabled configuration bound an endpoint")
			}
			// The existing owner and its repair path remain usable after ingress refusal.
			c.fleet.publish("fixture", nil, false)
			githubPickup(t, c)
		})
	}
}

func TestGitHubEventConnectionSlotsReleaseAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	underlying := &githubFixtureListener{}
	l := &githubBoundedListener{Listener: underlying, ctx: ctx, slots: make(chan struct{}, 1)}
	conn, err := l.Accept()
	if err != nil || len(l.slots) != 1 {
		t.Fatal("connection did not reserve its bound")
	}
	conn.Close()
	conn.Close()
	if len(l.slots) != 0 {
		t.Fatal("close did not release exactly one slot")
	}
	conn, err = l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() { _, err := l.Accept(); done <- err }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation admitted another connection")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown stranded a full listener")
	}
}

type githubFixtureListener struct{}

func (*githubFixtureListener) Accept() (net.Conn, error) {
	server, client := net.Pipe()
	client.Close()
	return server, nil
}
func (*githubFixtureListener) Close() error   { return nil }
func (*githubFixtureListener) Addr() net.Addr { return &net.UnixAddr{} }
