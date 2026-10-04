package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

const ownerNativeFrameLimit = 1024 * 1024

type ownerNativeRPC struct {
	Method      string                     `json:"method"`
	Install     *ownerNativeInstallRequest `json:"install,omitempty"`
	OperationID string                     `json:"operation_id,omitempty"`
}

func ownerNativeDecodeRPC(raw []byte) (ownerNativeRPC, error) {
	var rpc ownerNativeRPC
	if len(raw) > ownerNativeFrameLimit || ownerNativeStrictJSON(raw, &rpc) != nil {
		return rpc, errMatrix
	}
	switch rpc.Method {
	case "install":
		if rpc.Install == nil || rpc.OperationID != "" || !ownerNativeRequestValid(*rpc.Install) {
			return rpc, errMatrix
		}
	case "readStatus":
		if rpc.Install != nil || !actionIDPattern.MatchString(rpc.OperationID) {
			return rpc, errMatrix
		}
	default:
		return rpc, errMatrix
	}
	// Null optional fields cannot silently select a different operation.
	var fields map[string]json.RawMessage
	if strictJSON(raw, &fields) != nil {
		return rpc, errMatrix
	}
	for _, v := range fields {
		if string(v) == "null" {
			return rpc, errMatrix
		}
	}
	return rpc, nil
}

func ownerNativeReadFrame(reader io.Reader) ([]byte, error) {
	raw, err := bufio.NewReader(io.LimitReader(reader, ownerNativeFrameLimit+1)).ReadBytes('\n')
	if err != nil || len(raw) > ownerNativeFrameLimit {
		return nil, errMatrix
	}
	return raw, nil
}

func (s *ownerNativeGrantStore) handle(ctx context.Context, conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	ack := ownerNativeFailure("", "REFUSED", "override-invalid")
	peerUID := s.peerUID
	if peerUID == nil {
		peerUID = ownerNativePeerUID
	}
	uid, err := peerUID(conn)
	operator := err == nil && uid == *s.options.OperatorUID
	switch {
	case !operator:
		log.Printf("owner grant REFUSED (override-invalid): the caller is not the operator")
	case !s.healthy():
		log.Printf("owner grant REFUSED (override-invalid): the private grant store is unhealthy")
	default:
		raw, e := ownerNativeReadFrame(conn)
		rpc, e2 := ownerNativeDecodeRPC(raw)
		if e != nil || e2 != nil {
			log.Printf("owner grant REFUSED (override-invalid): the request frame is malformed")
			break
		}
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if rpc.Method == "install" {
			ack = s.install(callCtx, *rpc.Install)
		} else {
			ack = s.readStatus(callCtx, rpc.OperationID)
		}
	}
	// A lost write is UNKNOWN to the caller; neither this handler nor status
	// installs a second operation or applies an issue label.
	_ = json.NewEncoder(conn).Encode(ack)
}

func (s *ownerNativeGrantStore) listen(ctx context.Context) (*net.UnixListener, error) {
	if !s.healthy() {
		return nil, errMatrix
	}
	// Never remove an existing endpoint, even if it appears stale.
	if _, err := os.Lstat(s.options.Socket); !os.IsNotExist(err) {
		return nil, errMatrix
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.options.Socket, Net: "unix"})
	if err != nil {
		return nil, errMatrix
	}
	listener.SetUnlinkOnClose(false)
	prepare := s.prepareSocket
	if prepare == nil {
		prepare = ownerNativeSetSocketOwner
	}
	created, err := os.Lstat(s.options.Socket)
	if err != nil || prepare(s.options.Socket, created, *s.options.OperatorUID) != nil {
		listener.Close()
		return nil, errMatrix
	}
	bound, err := os.Lstat(s.options.Socket)
	if err != nil || !os.SameFile(created, bound) || bound.Mode()&os.ModeSocket == 0 || bound.Mode().Perm() != 0600 || !ownerNativeOwned(bound, *s.options.OperatorUID) {
		listener.Close()
		return nil, errMatrix
	}
	go func() {
		<-ctx.Done()
		listener.Close()
		if current, e := os.Lstat(s.options.Socket); e == nil && os.SameFile(bound, current) {
			_ = os.Remove(s.options.Socket)
		}
	}()
	go func() {
		slots := make(chan struct{}, 16)
		for {
			conn, e := listener.AcceptUnix()
			if e != nil {
				return
			}
			select {
			case slots <- struct{}{}:
				go func() { defer func() { <-slots }(); s.handle(ctx, conn) }()
			default:
				conn.Close()
			}
		}
	}()
	return listener, nil
}

func ownerNativeGrantCLI(args []string, out io.Writer) int {
	ack := ownerNativeFailure("", "UNKNOWN", "override-invalid")
	emit := func() int {
		_ = json.NewEncoder(out).Encode(ack)
		if ack.State == "LIVE" {
			return 0
		}
		return 2
	}
	if len(args) == 0 || (args[0] != "install" && args[0] != "status") {
		return emit()
	}
	fs := flag.NewFlagSet("owner-native-grant", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private Unix socket")
	requestPath := fs.String("request", "", "private install request")
	operation := fs.String("operation", "", "existing operation UUID")
	serverUID := fs.Int("server-uid", os.Getuid(), "expected daemon UID")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *serverUID < 0 || !filepath.IsAbs(*socket) || filepath.Clean(*socket) != *socket || !ownerNativePrivateDir(filepath.Dir(*socket), os.Getuid()) {
		return emit()
	}
	info, e := os.Lstat(*socket)
	if e != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 || !ownerNativeOwned(info, os.Getuid()) {
		return emit()
	}
	rpc := ownerNativeRPC{Method: "readStatus", OperationID: *operation}
	if args[0] == "install" {
		if *operation != "" {
			return emit()
		}
		raw, err := ownerNativePrivateRead(*requestPath, os.Getuid())
		var req ownerNativeInstallRequest
		if err != nil || ownerNativeStrictJSON(raw, &req) != nil || !ownerNativeRequestValid(req) {
			return emit()
		}
		rpc = ownerNativeRPC{Method: "install", Install: &req}
		ack.OperationID = req.OperationID
	} else {
		if *requestPath != "" || !actionIDPattern.MatchString(*operation) {
			return emit()
		}
		ack.OperationID = *operation
	}
	conn, err := net.DialTimeout("unix", *socket, 30*time.Second)
	if err != nil {
		return emit()
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return emit()
	}
	uid, e := ownerNativePeerUID(unixConn)
	if e != nil || uid != *serverUID {
		return emit()
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if json.NewEncoder(conn).Encode(rpc) != nil {
		return emit()
	}
	raw, err := ownerNativeReadFrame(conn)
	var received ownerNativeAck
	if err != nil || ownerNativeStrictJSON(raw, &received) != nil || received.SchemaVersion != 1 || received.OperationID != ack.OperationID || !ownerNativeAckValid(received) {
		return emit()
	}
	ack = received
	return emit()
}

func ownerNativeAckValid(ack ownerNativeAck) bool {
	switch ack.State {
	case "LIVE":
		return ack.Reason == "" && cleanText(ack.IssueID) && ack.IssueNumber > 0 && repositoryName.MatchString(ack.Inbox) && repositoryName.MatchString(ack.Target) && digestPattern.MatchString(ack.BriefDigest) && profileStem.MatchString(ack.Profile) && sourceRevision.MatchString(ack.MatrixRevision) && sourceRevision.MatchString(ack.TargetRevision) && digestPattern.MatchString(ack.RecordChecksum) && ack.Route != nil && validOwnerNativeOverride(&ownerNativeOverride{Authorizer: "eric", Rationale: "Private acknowledged route", Route: *ack.Route})
	case "UNKNOWN", "REFUSED":
		return ack.Reason == "override-invalid" || ack.Reason == "grant-conflict"
	default:
		return false
	}
}

func (c *Coord) startOwnerNativeGrants(ctx context.Context) {
	if c.cfg.OwnerNativeGrantPort == nil {
		return
	}
	store, err := newOwnerNativeGrantStore(ctx, c.cfg, *c.cfg.OwnerNativeGrantPort, configDeps())
	if err != nil {
		return
	}
	if _, err = store.listen(ctx); err != nil {
		return
	}
	c.ownerGrants = store
}

func (c *Coord) matrixConfigForIssue(ctx context.Context, is Issue, repo string) (MatrixConfig, error) {
	if c.cfg.OwnerNativeGrantPort == nil {
		return c.cfg.Matrix, nil
	}
	if c.ownerGrants == nil {
		return MatrixConfig{}, matrixReason("override-invalid")
	}
	return c.ownerGrants.matrixForIssueContext(ctx, is, repo)
}
