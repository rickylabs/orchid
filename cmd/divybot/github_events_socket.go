package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const githubEventConnections = 32

// GitHub's HTTPS sender reaches this same-UID HTTP socket through the operator's TLS proxy.
// No public listener, secret value or private endpoint is supplied in repository configuration.
func (c *Coord) startGitHubEvents(ctx context.Context) {
	path, secretPath := os.Getenv("ORCHID_GITHUB_WEBHOOK_SOCKET"), os.Getenv("ORCHID_GITHUB_WEBHOOK_SECRET_FILE")
	if path == "" && secretPath == "" {
		return
	}
	secret, err := privateFileBytes(secretPath, os.Getuid(), ownerNativeOpen)
	if err != nil || len(secret) < 16 || len(secret) > 4096 {
		log.Printf("github-supervision unavailable reason=secret-refused; recovery continues")
		return
	}
	listener, err := listenGitHubEvents(path)
	if err != nil {
		log.Printf("github-supervision unavailable reason=endpoint-refused; recovery continues")
		return
	}
	ingress := newGitHubEventIngress(secret, c.githubEventRepository, func() {
		select {
		case c.fleet.wake <- struct{}{}:
		default: // A pending tick will read all current authority; no queue grows per event.
		}
	})
	server := &http.Server{Handler: ingress, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 8 * 1024, ErrorLog: log.New(io.Discard, "", 0)}
	go func() {
		<-ctx.Done()
		_ = server.Close()
		_ = listener.Close()
	}()
	go func() {
		err := server.Serve(&githubBoundedListener{Listener: listener, ctx: ctx, slots: make(chan struct{}, githubEventConnections)})
		if err != nil && !errors.Is(err, http.ErrServerClosed) && ctx.Err() == nil {
			log.Printf("github-supervision unavailable reason=serve-failed; recovery continues")
		}
	}()
	log.Printf("github-supervision listening; authenticated native events wake existing supervision")
}

type githubOwnedListener struct {
	*net.UnixListener
	path string
	info os.FileInfo
	once sync.Once
}

func (l *githubOwnedListener) Close() error {
	err := l.UnixListener.Close()
	l.once.Do(func() {
		if current, e := os.Lstat(l.path); e == nil && l.info != nil && os.SameFile(l.info, current) {
			_ = os.Remove(l.path)
		}
	})
	return err
}

// Collision is unknown ownership, even for a stale same-UID socket; never unlink it.
func listenGitHubEvents(path string) (*githubOwnedListener, error) {
	uid := os.Getuid()
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !ownerNativePrivateDir(filepath.Dir(path), uid) {
		return nil, errMatrix
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, errMatrix
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, errMatrix
	}
	l.SetUnlinkOnClose(false)
	info, err := os.Lstat(path)
	owned := &githubOwnedListener{UnixListener: l, path: path, info: info}
	if err != nil || ownerNativeSetSocketOwner(path, info, uid) != nil {
		_ = owned.Close()
		return nil, errMatrix
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) || current.Mode()&os.ModeSocket == 0 || current.Mode().Perm() != 0600 || !ownerNativeOwned(current, uid) {
		_ = owned.Close()
		return nil, errMatrix
	}
	return owned, nil
}

type githubBoundedListener struct {
	net.Listener
	ctx   context.Context
	slots chan struct{}
}

func (l *githubBoundedListener) Accept() (net.Conn, error) {
	select {
	case <-l.ctx.Done():
		return nil, l.ctx.Err()
	case l.slots <- struct{}{}:
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &githubSlotConnection{Conn: conn, release: func() { <-l.slots }}, nil
}

type githubSlotConnection struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *githubSlotConnection) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
