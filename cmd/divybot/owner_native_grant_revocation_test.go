package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOwnerNativeGrantRevocation(t *testing.T) {
	s, r, is := ownerPortFixture(t, "codex")
	var unauthorized atomic.Bool
	s.peerUID = func(conn *net.UnixConn) (int, error) {
		if unauthorized.Load() {
			return os.Getuid() + 1, nil
		}
		return ownerNativePeerUID(conn)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := s.listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	live := s.install(ctx, r)
	if live.State != "LIVE" {
		t.Fatal("install control")
	}
	call := func(checksum string) ownerNativeAck {
		t.Helper()
		conn, err := net.Dial("unix", s.options.Socket)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := json.NewEncoder(conn).Encode(map[string]any{
			"method": "revoke", "operation_id": r.OperationID, "record_checksum": checksum,
		}); err != nil {
			t.Fatal(err)
		}
		var ack ownerNativeAck
		if err := json.NewDecoder(conn).Decode(&ack); err != nil {
			t.Fatal(err)
		}
		return ack
	}
	if ack := call(strings.Repeat("d", 64)); ack.State != "REFUSED" || ack.Reason != "grant-conflict" {
		t.Fatalf("wrong record must conflict: %s %s", ack.State, ack.Reason)
	}
	if s.readStatus(ctx, r.OperationID).State != "LIVE" {
		t.Fatal("mismatched revocation changed authority")
	}
	ack := call(live.RecordChecksum)
	if ack.State != "REVOKED" || ack.OperationID != r.OperationID || ack.RecordChecksum != live.RecordChecksum || !ownerNativeAckValid(ack) {
		t.Fatalf("exact revocation not confirmed: %s", ack.State)
	}
	if again := call(live.RecordChecksum); again != ack {
		t.Fatal("revocation is not idempotent")
	}
	if s.readStatus(ctx, r.OperationID) != ack {
		t.Fatal("status does not confirm revocation")
	}
	ownerPortRequireFenced(t, s, *is)
	if s.install(ctx, r).State == "LIVE" {
		t.Fatal("install revived revoked grant")
	}
	restored, err := newOwnerNativeGrantStore(ctx, s.cfg, s.options, s.deps)
	if err != nil || restored.readStatus(ctx, r.OperationID) != ack {
		t.Fatal("revocation not recovered")
	}
	ownerPortRequireFenced(t, restored, *is)
	var out bytes.Buffer
	if ownerNativeGrantCLI([]string{"revoke", "-socket", s.options.Socket, "-operation", r.OperationID, "-record-checksum", live.RecordChecksum}, &out) != 0 {
		t.Fatal("revoke CLI failed")
	}
	out.Reset()
	if ownerNativeGrantCLI([]string{"status", "-socket", s.options.Socket, "-operation", r.OperationID}, &out) != 0 {
		t.Fatal("revoked status CLI failed")
	}
	for _, mode := range []string{"file-sync", "directory-sync", "link", "damaged-tombstone", "approval-and-issue-drift", "comment"} {
		t.Run(mode, func(t *testing.T) {
			store, request, issue := ownerPortFixture(t, "codex")
			if mode == "comment" {
				if err := os.Remove(filepath.Join(store.options.ApprovalRoot, request.ApprovalRef+".json")); err != nil {
					t.Fatal(err)
				}
				request.Trigger = ownerNativeCommentTrigger
				request.ExpectedBriefDigest = shaText([]byte("/swarm\nSynthetic comment"))
			}
			installed := store.install(ctx, request)
			if installed.State != "LIVE" {
				t.Fatal("install control")
			}
			switch mode {
			case "file-sync":
				store.fileSync = func(*os.File) error { return errMatrix }
			case "directory-sync":
				store.dirSync = func(string) error { return errMatrix }
			case "link":
				store.link = func(string, string) error { return errMatrix }
			case "damaged-tombstone":
				if err := os.WriteFile(store.revocationPath(request.OperationID), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "approval-and-issue-drift":
				if err := os.WriteFile(filepath.Join(store.options.ApprovalRoot, request.ApprovalRef+".json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				issue.Body += " edited"
				store.deps.command = func(context.Context, string, string, []byte, ...string) ([]byte, error) {
					t.Fatal("compensation must not depend on GitHub")
					return nil, errMatrix
				}
			}
			result := store.revoke(request.OperationID, installed.RecordChecksum)
			if mode == "approval-and-issue-drift" || mode == "comment" {
				if result.State != "REVOKED" || store.readStatus(ctx, request.OperationID) != result {
					t.Fatal("exact installed grant could not be compensated")
				}
				if mode == "comment" && store.commentGrantReady(request.Target, request.IssueNumber, issue.ID, request.ExpectedBriefDigest, 17, ownerGitHubID) {
					t.Fatal("revoked comment grant remained ready")
				}
				return
			}
			if result.State != "UNKNOWN" || store.readStatus(ctx, request.OperationID).State == "REVOKED" || store.readStatus(ctx, request.OperationID).State == "LIVE" {
				t.Fatal("failed publication claimed confirmed authority")
			}
			ownerPortRequireFenced(t, store, *issue)
			store.fileSync, store.dirSync, store.link = nil, nil, nil
			if mode == "damaged-tombstone" {
				recovered, err := newOwnerNativeGrantStore(ctx, store.cfg, store.options, store.deps)
				if err != nil {
					t.Fatal(err)
				}
				ownerPortRequireFenced(t, recovered, *issue)
				return
			}
			if store.revoke(request.OperationID, installed.RecordChecksum).State != "REVOKED" {
				t.Fatal("retry did not reconcile the same operation")
			}
		})
	}
	t.Run("another-operation", func(t *testing.T) {
		store, first, issue := ownerPortFixture(t, "codex")
		installed := store.install(ctx, first)
		if installed.State != "LIVE" {
			t.Fatal("first install control")
		}
		issue.Body += "\nNew approved brief"
		second := first
		second.OperationID = "22222222-2222-4222-8222-222222222222"
		second.ApprovalRef = "second-approval"
		second.ExpectedBriefDigest = briefDigest(*issue)
		ownerPortWriteApproval(t, store, second)
		if store.install(ctx, second).State != "LIVE" {
			t.Fatal("second install control")
		}
		if store.revoke(first.OperationID, installed.RecordChecksum).State != "REVOKED" || store.readStatus(ctx, second.OperationID).State != "LIVE" {
			t.Fatal("revocation affected another operation")
		}
		if _, err := store.matrixForIssue(*issue, second.Target); err != nil {
			t.Fatal("another approved brief lost admission")
		}
	})
	for _, raw := range []string{
		`{"method":"revoke"}`,
		`{"method":"revoke","operation_id":"` + r.OperationID + `"}`,
		`{"method":"revoke","operation_id":"` + r.OperationID + `","record_checksum":null}`,
		`{"method":"revoke","operation_id":"` + r.OperationID + `","record_checksum":"bad"}`,
		`{"method":"revoke","operation_id":"` + r.OperationID + `","record_checksum":"` + live.RecordChecksum + `","install":{}}`,
	} {
		if _, err := ownerNativeDecodeRPC([]byte(raw)); err == nil {
			t.Fatal("malformed revocation accepted")
		}
	}
	// Revocation uses the same authenticated operator channel as installation.
	unauthorized.Store(true)
	if call(live.RecordChecksum).State != "REFUSED" {
		t.Fatal("unauthorized revocation accepted")
	}
	var invalid bytes.Buffer
	if ownerNativeGrantCLI(nil, &invalid) == 0 {
		t.Fatal("empty CLI command accepted")
	}

}
