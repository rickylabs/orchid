package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOwnerNativeRPCStrictFrames(t *testing.T) {
	s, r, _ := ownerPortFixture(t, "claude")
	good, _ := json.Marshal(ownerNativeRPC{Method: "install", Install: &r})
	if _, err := ownerNativeDecodeRPC(good); err != nil {
		t.Fatal("valid frame")
	}
	for _, raw := range [][]byte{
		[]byte(`null`), []byte(`{}`), append(good, []byte(`{}`)...),
		[]byte(strings.Replace(string(good), `"method":"install"`, `"method":"readStatus"`, 1)),
		[]byte(strings.Replace(string(good), `"method":"install"`, `"Method":"install"`, 1)),
		append([]byte(`{"unknown":true,`), good[1:]...),
		append([]byte(`{"method":"install",`), good[1:]...),
		[]byte(strings.Replace(string(good), `"profile":"leaf"`, `"profile":null`, 1)),
		[]byte(strings.Replace(string(good), `"expected_issue_id":"synthetic-node",`, "", 1)),
		[]byte(strings.Replace(string(good), `"schema_version":1`, `"schema_version":2`, 1)),
		[]byte(`{"method":"readStatus","operation_id":"11111111-1111-4111-8111-111111111111","install":null}`),
		bytes.Repeat([]byte("a"), ownerNativeFrameLimit+1), {0xff},
	} {
		if _, err := ownerNativeDecodeRPC(raw); err == nil {
			t.Fatal("invalid authority frame accepted")
		}
	}
	if _, err := ownerNativeReadFrame(strings.NewReader(string(good))); err == nil {
		t.Fatal("unterminated frame accepted")
	}
	if _, err := ownerNativeReadFrame(bytes.NewReader(bytes.Repeat([]byte("a"), ownerNativeFrameLimit+1))); err == nil {
		t.Fatal("oversized frame accepted")
	}
	if len(s.active) != 0 {
		t.Fatal("decoder published a grant")
	}
}

func TestOwnerNativeRPCRealUnixAndLostReply(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	var triggered atomic.Bool
	s.deps.command = func(context.Context, string, string, []byte, ...string) ([]byte, error) {
		labels := []any{map[string]any{"name": "fixture-target"}}
		if triggered.Load() {
			labels = append(labels, map[string]any{"name": "harness"})
		}
		return json.Marshal(map[string]any{"id": is.ID, "number": is.Number, "title": is.Title, "body": is.Body, "state": "OPEN", "labels": labels})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := s.listen(ctx)
	if err != nil {
		t.Fatal("listen", err)
	}
	defer listener.Close()
	path := filepath.Join(s.options.ApprovalRoot, "request.json")
	raw, _ := json.Marshal(r)
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("request")
	}
	var output bytes.Buffer
	if ownerNativeGrantCLI([]string{"install", "-socket", s.options.Socket, "-request", path}, &output) != 0 {
		t.Fatalf("install CLI failed: %s", output.String())
	}
	var first ownerNativeAck
	if json.Unmarshal(output.Bytes(), &first) != nil || first.State != "LIVE" {
		t.Fatal("install ack")
	}
	output.Reset()
	if ownerNativeGrantCLI([]string{"status", "-socket", s.options.Socket, "-operation", r.OperationID}, &output) != 0 {
		t.Fatal("status CLI")
	}
	var status ownerNativeAck
	_ = json.Unmarshal(output.Bytes(), &status)
	if status.RecordChecksum != first.RecordChecksum {
		t.Fatal("status changed immutable operation")
	}
	// Reply loss never requires another install; independently readStatus proves LIVE.
	conn, err := net.Dial("unix", s.options.Socket)
	if err != nil {
		t.Fatal("connect")
	}
	_ = json.NewEncoder(conn).Encode(ownerNativeRPC{Method: "install", Install: &r})
	conn.Close()
	if s.readStatus(context.Background(), r.OperationID).State != "LIVE" {
		t.Fatal("reply loss removed authority")
	}
	entries, _ := os.ReadDir(filepath.Join(s.options.StoreRoot, "records"))
	if len(entries) != 1 {
		t.Fatal("reply loss duplicated grant")
	}
	// Once the normal consumer triggers, status remains proof, but install is inert-only.
	triggered.Store(true)
	if s.install(context.Background(), r).State == "LIVE" {
		t.Fatal("install accepted triggered issue")
	}
	if s.readStatus(context.Background(), r.OperationID).State != "LIVE" {
		t.Fatal("normal trigger invalidated status")
	}
}

func TestOwnerNativeRPCPeerRefusalAndSocketCollision(t *testing.T) {
	s, r, _ := ownerPortFixture(t, "claude")
	uid := os.Getuid() + 1
	s.peerUID = func(*net.UnixConn) (int, error) { return uid, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := s.listen(ctx)
	if err != nil {
		t.Fatal("listen", err)
	}
	defer listener.Close()
	if _, err = s.listen(ctx); err == nil {
		t.Fatal("existing endpoint overwritten")
	}
	conn, err := net.Dial("unix", s.options.Socket)
	if err != nil {
		t.Fatal("connect")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_ = json.NewEncoder(conn).Encode(ownerNativeRPC{Method: "install", Install: &r})
	raw, err := ownerNativeReadFrame(conn)
	var ack ownerNativeAck
	if err != nil || json.Unmarshal(raw, &ack) != nil || ack.State != "REFUSED" || len(s.active) != 0 {
		t.Fatal("unauthenticated peer reached authority")
	}
	var out bytes.Buffer
	if ownerNativeGrantCLI([]string{"status", "-socket", s.options.Socket, "-operation", r.OperationID, "-server-uid", strconv.Itoa(uid)}, &out) == 0 {
		t.Fatal("client trusted wrong server UID")
	}
}

func TestOwnerNativeRPCConcurrentOperations(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	issues := map[int]Issue{7: *is}
	requests := []ownerNativeInstallRequest{r}
	for n := 8; n < 12; n++ {
		issue := *is
		issue.Number = n
		issue.ID = fmt.Sprintf("synthetic-node-%d", n)
		issue.Body += fmt.Sprintf("\nTask %d", n)
		issues[n] = issue
		req := r
		req.OperationID = fmt.Sprintf("%08d-1111-4111-8111-111111111111", n)
		req.ApprovalRef = fmt.Sprintf("fixture-approval-%d", n)
		req.IssueNumber = n
		req.ExpectedIssueID = issue.ID
		req.ExpectedBriefDigest = briefDigest(issue)
		ownerPortWriteApproval(t, s, req)
		requests = append(requests, req)
	}
	s.deps.command = func(_ context.Context, _ string, program string, _ []byte, args ...string) ([]byte, error) {
		n, _ := strconv.Atoi(args[2])
		issue, ok := issues[n]
		if !ok || program != "gh" {
			return nil, errMatrix
		}
		return json.Marshal(map[string]any{"id": issue.ID, "number": n, "title": issue.Title, "body": issue.Body, "state": "OPEN", "labels": []any{map[string]any{"name": "fixture-target"}}})
	}
	var wg sync.WaitGroup
	errors := make(chan string, 100)
	for _, request := range requests {
		for repeat := 0; repeat < 3; repeat++ {
			wg.Add(1)
			go func(req ownerNativeInstallRequest) {
				defer wg.Done()
				if s.install(context.Background(), req).State != "LIVE" {
					errors <- "install"
				}
				if s.readStatus(context.Background(), req.OperationID).State != "LIVE" {
					errors <- "status"
				}
				if _, err := s.matrixForIssue(issues[req.IssueNumber], req.Target); err != nil {
					errors <- "admission"
				}
			}(request)
		}
	}
	wg.Wait()
	close(errors)
	for reason := range errors {
		t.Error(reason)
	}
	entries, _ := os.ReadDir(filepath.Join(s.options.StoreRoot, "records"))
	if len(entries) != len(requests) {
		t.Fatal("concurrent install lost or duplicated operations")
	}
}

func TestOwnerNativeRPCUnavailableAndDuplicateScope(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	if s.install(context.Background(), r).State != "LIVE" {
		t.Fatal("control")
	}
	second := r
	second.OperationID = "22222222-2222-4222-8222-222222222222"
	second.ApprovalRef = "second-approval"
	ownerPortWriteApproval(t, s, second)
	if ack := s.install(context.Background(), second); ack.State != "REFUSED" || ack.Reason != "grant-conflict" {
		t.Fatal("duplicate scope did not refuse a known conflict")
	}
	changed := r
	changed.NativeOverride = nativeOwnerFixture("codex")
	ownerPortWriteApproval(t, s, changed)
	if ack := s.install(context.Background(), changed); ack.State != "REFUSED" || ack.Reason != "grant-conflict" {
		t.Fatal("changed operation did not refuse a known conflict")
	}
	// A configured but unavailable port cannot fall back to startup/default routing.
	c := &Coord{cfg: s.cfg}
	c.cfg.OwnerNativeGrantPort = &s.options
	if _, err := c.matrixConfigForIssue(context.Background(), *is, r.Target); err == nil {
		t.Fatal("unavailable owner authority fell back")
	}
	c.cfg.OwnerNativeGrantPort = nil
	if _, err := c.matrixConfigForIssue(context.Background(), *is, r.Target); err != nil {
		t.Fatal("disabled port changed ordinary routing")
	}
}

func TestOwnerNativeRPCPrivateConfig(t *testing.T) {
	s, _, _ := ownerPortFixture(t, "claude")
	s.cfg.OwnerNativeGrantPort = &s.options
	path := filepath.Join(privateTestRoot(t), "config.json")
	raw, _ := json.Marshal(s.cfg)
	_ = os.WriteFile(path, raw, 0600)
	if _, err := loadConfig(path); err != nil {
		t.Fatal("valid optional bootstrap", err)
	}
	if _, _, problems := readMatrixConfigFile(path); len(problems) > 0 {
		t.Fatal("valid matrix bootstrap", problems)
	}
	for _, replacement := range []string{`null`, `{"unknown":true}`, `{"socket":null}`, `{"socket":"relative","store_root":"relative","approval_root":"relative","operator_uid":0}`} {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		fields["owner_native_grant_port"] = json.RawMessage(replacement)
		bad, _ := json.Marshal(fields)
		_ = os.WriteFile(path, bad, 0600)
		if _, err := loadConfig(path); err == nil {
			t.Fatal("runtime accepted invalid private config")
		}
		cfg, _, problems := readMatrixConfigFile(path)
		if len(problems) == 0 && len(validateMatrixConfig(context.Background(), cfg)) == 0 {
			t.Fatal("matrix validate accepted invalid private config")
		}
	}
}

func TestOwnerNativeRPCConfigShape(t *testing.T) {
	var options ownerNativePortConfig
	for _, raw := range []string{
		`{"socket":"x","store_root":"y","approval_root":"z","operator_uid":null}`,
		`{"socket":"x","store_root":"y","approval_root":"z"}`,
		`{"socket":"x","store_root":"y","approval_root":"z","operator_uid":0,"Socket":"x"}`,
		`{"socket":"x","socket":"x","store_root":"y","approval_root":"z","operator_uid":0}`,
	} {
		if ownerNativeStrictJSON([]byte(raw), &options) == nil {
			t.Fatal("private bootstrap shape accepted")
		}
	}
}

func TestOwnerNativeRPCRequestBounds(t *testing.T) {
	_, base, _ := ownerPortFixture(t, "claude")
	for _, name := range []string{"schema", "operation", "approval", "issue-number", "node", "target", "digest", "tier", "role", "profile", "route"} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var r ownerNativeInstallRequest
			_ = json.Unmarshal(raw, &r)
			switch name {
			case "schema":
				r.SchemaVersion = 2
			case "operation":
				r.OperationID = "different"
			case "approval":
				r.ApprovalRef = "../unsafe"
			case "issue-number":
				r.IssueNumber = 0
			case "node":
				r.ExpectedIssueID = ""
			case "target":
				r.Target = "invalid"
			case "digest":
				r.ExpectedBriefDigest = "invalid"
			case "tier":
				r.Tier = "!"
			case "role":
				r.Role = "!"
			case "profile":
				r.Profile = "../unsafe"
			case "route":
				r.NativeOverride.Authorizer = "owner"
			}
			if ownerNativeRequestValid(r) {
				t.Fatal("unbounded or incomplete private scope accepted")
			}
		})
	}
}
