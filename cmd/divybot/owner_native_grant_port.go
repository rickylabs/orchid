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
	Method         string                     `json:"method"`
	Install        *ownerNativeInstallRequest `json:"install,omitempty"`
	OperationID    string                     `json:"operation_id,omitempty"`
	RecordChecksum string                     `json:"record_checksum,omitempty"`
}

func ownerNativeDecodeRPC(raw []byte) (ownerNativeRPC, error) {
	var rpc ownerNativeRPC
	if len(raw) > ownerNativeFrameLimit || ownerNativeStrictJSON(raw, &rpc) != nil {
		return rpc, errMatrix
	}
	switch rpc.Method {
	case "install":
		if rpc.Install == nil || rpc.OperationID != "" || rpc.RecordChecksum != "" || !ownerNativeRequestValid(*rpc.Install) {
			return rpc, errMatrix
		}
	case "readStatus":
		if rpc.Install != nil || rpc.RecordChecksum != "" || !actionIDPattern.MatchString(rpc.OperationID) {
			return rpc, errMatrix
		}
	case "revoke":
		if rpc.Install != nil || !actionIDPattern.MatchString(rpc.OperationID) || !digestPattern.MatchString(rpc.RecordChecksum) {
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
	if err == io.EOF && len(raw) == 0 {
		return nil, io.EOF
	}
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
		// Authenticated readiness checks verify the peer and close without an RPC.
		// Only clean EOF before any bytes is a probe; partial frames remain refused.
		if e == io.EOF {
			return
		}
		rpc, e2 := ownerNativeDecodeRPC(raw)
		if e != nil || e2 != nil {
			log.Printf("owner grant REFUSED (override-invalid): the request frame is malformed")
			break
		}
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		switch rpc.Method {
		case "install":
			ack = s.install(callCtx, *rpc.Install)
		case "revoke":
			ack = s.revoke(rpc.OperationID, rpc.RecordChecksum)
		case "readStatus":
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
		if len(args) > 0 && ((args[0] == "install" && ack.State == "LIVE") || (args[0] == "revoke" && ack.State == "REVOKED") || (args[0] == "status" && (ack.State == "LIVE" || ack.State == "REVOKED"))) {
			return 0
		}
		return 2
	}
	if len(args) == 0 || (args[0] != "install" && args[0] != "status" && args[0] != "revoke") {
		return emit()
	}
	fs := flag.NewFlagSet("owner-native-grant", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "private Unix socket")
	requestPath := fs.String("request", "", "private install request")
	operation := fs.String("operation", "", "existing operation UUID")
	checksum := fs.String("record-checksum", "", "exact installed record checksum")
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
		if *operation != "" || *checksum != "" {
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
		if args[0] == "revoke" {
			if !digestPattern.MatchString(*checksum) {
				return emit()
			}
			rpc.Method, rpc.RecordChecksum = "revoke", *checksum
		} else if *checksum != "" {
			return emit()
		}
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
	if rpc.Method == "revoke" && ack.State == "REVOKED" && ack.RecordChecksum != rpc.RecordChecksum {
		ack = ownerNativeFailure(rpc.OperationID, "UNKNOWN", "override-invalid")
	}
	return emit()
}

func ownerNativeAckValid(ack ownerNativeAck) bool {
	switch ack.State {
	case "REVOKED":
		return actionIDPattern.MatchString(ack.OperationID) && digestPattern.MatchString(ack.RecordChecksum) && ack.Reason == "" && ack.IssueID == "" && ack.IssueNumber == 0 && ack.Inbox == "" && ack.Target == "" && ack.BriefDigest == "" && ack.Profile == "" && ack.MatrixRevision == "" && ack.TargetRevision == "" && ack.Route == nil
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
	if is.Source != nil && (c.cfg.OwnerNativeGrantPort == nil || c.ownerGrants == nil) {
		return MatrixConfig{}, matrixReason("source-grant-missing") // a comment launches only on its grant
	}
	if c.cfg.OwnerNativeGrantPort == nil {
		return c.cfg.Matrix, nil
	}
	if c.ownerGrants == nil {
		return MatrixConfig{}, matrixReason("override-invalid")
	}
	return c.ownerGrants.matrixForIssueContext(ctx, is, repo)
}
