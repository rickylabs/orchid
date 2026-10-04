package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ownerPortFixture(t *testing.T, tool string) (*ownerNativeGrantStore, ownerNativeInstallRequest, *Issue) {
	t.Helper()
	is := &Issue{Labels: []string{"fixture-target"}, ID: "synthetic-node", Number: 7, Title: "Synthetic complete owner brief", Body: "/swarm\ntier: feature\nrole: implementation\nprofile: leaf\n\nSynthetic full task with fenced detail and unicode λ."}
	uid := os.Getuid()
	options := ownerNativePortConfig{Socket: filepath.Join(privateTestRoot(t), "operator.sock"), StoreRoot: privateTestRoot(t), ApprovalRoot: privateTestRoot(t), OperatorUID: &uid}
	cfg := &Config{Inbox: "example/inbox", Targets: []Target{{Label: "fixture-target", Repo: "example/project"}}, Matrix: syntheticSource(t)}
	cfg.Matrix.ReceiptRoot = privateTestRoot(t)
	cfg.Matrix.TargetRevisions = map[string]string{"example/project": strings.Repeat("c", 40)}
	request := ownerNativeInstallRequest{SchemaVersion: 1, OperationID: "11111111-1111-4111-8111-111111111111", ApprovalRef: "fixture-approval", IssueNumber: 7, ExpectedIssueID: is.ID, Target: "example/project", ExpectedBriefDigest: briefDigest(*is), Tier: "feature", Role: "implementation", Profile: "leaf", NativeOverride: nativeOwnerFixture(tool)}
	approval := ownerNativeApproval{SchemaVersion: 1, Authorizer: "eric", Inbox: cfg.Inbox, MatrixRevision: cfg.Matrix.Revision, TargetRevision: cfg.Matrix.TargetRevisions[request.Target], Request: request}
	data, _ := json.Marshal(approval)
	if os.WriteFile(filepath.Join(options.ApprovalRoot, request.ApprovalRef+".json"), data, 0600) != nil {
		t.Fatal("approval fixture")
	}
	deps := configDeps()
	deps.command = func(context.Context, string, string, []byte, ...string) ([]byte, error) {
		return json.Marshal(map[string]any{"id": is.ID, "number": is.Number, "title": is.Title, "body": is.Body, "state": "OPEN", "labels": []any{map[string]any{"name": "fixture-target"}}})
	}
	deps.read = func(context.Context, string, string, string) (string, error) {
		return "| `routing` | matrix `implementation` row |", nil
	}
	store, err := newOwnerNativeGrantStore(context.Background(), cfg, options, deps)
	if err != nil {
		t.Fatal("private fixture bootstrap", err)
	}
	return store, request, is
}

func TestOwnerNativeLiveGrantInstallAllCLIs(t *testing.T) {
	for _, tool := range matrixTransports {
		t.Run(tool, func(t *testing.T) {
			s, request, _ := ownerPortFixture(t, tool)
			ack := s.install(context.Background(), request)
			if ack.State != "LIVE" || ack.OperationID != request.OperationID || ack.BriefDigest != request.ExpectedBriefDigest || ack.RecordChecksum == "" {
				t.Fatalf("owner grant not acknowledged LIVE: state=%s reason=%s", ack.State, ack.Reason)
			}
		})
	}
}

func ownerPortWriteApproval(t *testing.T, s *ownerNativeGrantStore, r ownerNativeInstallRequest) {
	t.Helper()
	a := ownerNativeApproval{SchemaVersion: 1, Authorizer: "eric", Inbox: s.cfg.Inbox, MatrixRevision: s.cfg.Matrix.Revision, TargetRevision: s.cfg.Matrix.TargetRevisions[r.Target], Request: r}
	raw, _ := json.Marshal(a)
	if os.WriteFile(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"), raw, 0600) != nil {
		t.Fatal("approval write")
	}
}

func ownerPortRequireFenced(t *testing.T, s *ownerNativeGrantStore, is Issue) {
	t.Helper()
	if _, err := s.matrixForIssue(is, "example/project"); err == nil {
		t.Fatal("owner intent fell through to default routing")
	}
	c := &Coord{cfg: s.cfg, ownerGrants: s}
	c.cfg.OwnerNativeGrantPort = &s.options
	effects := 0
	refused := 0
	_, ok := c.matrixAttempt(context.Background(), is.Number, is, Target{Repo: "example/project"}, nil, matrixAttemptDeps{
		report: func(matrixRefusal) { refused++ },
		read:   func(context.Context, string, string, string) (string, error) { effects++; return "", nil },
		launch: func(context.Context, int, Issue, Host, string, Overrides, *durableMatrixReceipt) error {
			effects++
			return nil
		},
	})
	if ok || refused != 1 || effects != 0 {
		t.Fatal("common admission escaped the owner fence")
	}
}

func TestOwnerNativeLiveGrantSnapshotAndRecovery(t *testing.T) {
	s, r, is := ownerPortFixture(t, "codex")
	before, _ := json.Marshal(s.cfg.Matrix)
	ack := s.install(context.Background(), r)
	if ack.State != "LIVE" {
		t.Fatal("control")
	}
	raw, err := os.ReadFile(s.recordPath(r.OperationID))
	if err != nil || shaText(raw) != ack.RecordChecksum {
		t.Fatal("ack is not durable exact content")
	}
	m, err := s.matrixForIssue(*is, r.Target)
	if err != nil || len(m.Grants) != 1 || m.Grants[0].NativeOverride.Route != r.NativeOverride.Route {
		t.Fatal("LIVE grant absent from admission")
	}
	m.Grants[0].NativeOverride.Route.Model = "different-caller-value"
	after, _ := json.Marshal(s.cfg.Matrix)
	if string(before) != string(after) {
		t.Fatal("startup config mutated")
	}
	if s.readStatus(context.Background(), r.OperationID).Route.Model != r.NativeOverride.Route.Model {
		t.Fatal("mutable snapshot leaked")
	}
	repeat := s.install(context.Background(), r)
	if repeat.State != "LIVE" || repeat.RecordChecksum != ack.RecordChecksum {
		t.Fatal("idempotent install changed authority")
	}
	restored, err := newOwnerNativeGrantStore(context.Background(), s.cfg, s.options, s.deps)
	if err != nil || restored.readStatus(context.Background(), r.OperationID).State != "LIVE" {
		t.Fatal("durable authority not recovered")
	}
	is.Body += "\nEdited full brief"
	if restored.readStatus(context.Background(), r.OperationID).State == "LIVE" {
		t.Fatal("stale LIVE acknowledgement")
	}
	ownerPortRequireFenced(t, restored, *is)
	ordinary := *is
	ordinary.Number++
	ordinary.ID = "ordinary-node"
	if _, err = restored.matrixForIssue(ordinary, r.Target); err != nil {
		t.Fatal("ordinary default path changed")
	}
}

func TestOwnerNativeLiveGrantAuthorityRefusals(t *testing.T) {
	for _, name := range []string{"other-actor", "wrong-inbox", "wrong-matrix-pin", "wrong-target-pin", "wrong-approved-route", "wrong-operation", "wrong-brief", "wrong-target", "wrong-profile", "disabled-target", "static-conflict", "bad-native", "wrong-schema", "unknown-field", "duplicate-field", "null-field", "case-alias", "case-extra-alias", "wrong-mode", "approval-symlink", "wrong-owner"} {
		t.Run(name, func(t *testing.T) {
			s, r, _ := ownerPortFixture(t, "claude")
			path := filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json")
			raw, _ := os.ReadFile(path)
			var a ownerNativeApproval
			_ = json.Unmarshal(raw, &a)
			rewrite := true
			switch name {
			case "other-actor":
				a.Authorizer = "owner"
			case "wrong-inbox":
				a.Inbox = "other/inbox"
			case "wrong-matrix-pin":
				a.MatrixRevision = strings.Repeat("d", 40)
			case "wrong-target-pin":
				a.TargetRevision = strings.Repeat("d", 40)
			case "wrong-approved-route":
				a.Request.NativeOverride.Route.Model = "different-route"
			case "wrong-operation":
				r.OperationID = "22222222-2222-4222-8222-222222222222"
			case "wrong-brief":
				r.ExpectedBriefDigest = strings.Repeat("a", 64)
			case "wrong-target":
				r.Target = "other/project"
			case "wrong-profile":
				r.Profile = "other-profile"
			case "disabled-target":
				s.cfg.Targets[0].Disabled = true
			case "static-conflict":
				s.cfg.Matrix.Grants = []MatrixGrant{{IssueID: "synthetic-node", Repo: r.Target, BriefDigest: r.ExpectedBriefDigest, NativeOverride: r.NativeOverride}}
			case "bad-native":
				r.NativeOverride.Authorizer = "owner"
			case "wrong-schema":
				a.SchemaVersion = 2
			case "unknown-field":
				raw = append([]byte(`{"untrusted":true,`), raw[1:]...)
				rewrite = false
			case "duplicate-field":
				raw = append([]byte(`{"authorizer":"eric",`), raw[1:]...)
				rewrite = false
			case "null-field":
				raw = []byte(strings.Replace(string(raw), `"authorizer":"eric"`, `"authorizer":null`, 1))
				rewrite = false
			case "case-alias":
				raw = []byte(strings.Replace(string(raw), `"authorizer"`, `"Authorizer"`, 1))
				rewrite = false
			case "case-extra-alias":
				raw = append([]byte(`{"Authorizer":"eric",`), raw[1:]...)
				rewrite = false
			case "wrong-mode":
				_ = os.Chmod(path, 0644)
				rewrite = false
			case "approval-symlink":
				target := filepath.Join(s.options.ApprovalRoot, "different.json")
				_ = os.Rename(path, target)
				_ = os.Symlink(target, path)
				rewrite = false
			case "wrong-owner":
				*s.options.OperatorUID = os.Getuid() + 1
				rewrite = false
			}
			if rewrite {
				raw, _ = json.Marshal(a)
			}
			if !containsString([]string{"wrong-mode", "approval-symlink", "wrong-owner"}, name) {
				_ = os.WriteFile(path, raw, 0600)
			}
			ack := s.install(context.Background(), r)
			if ack.State == "LIVE" {
				t.Fatal("unproven authority acknowledged LIVE")
			}
			records, _ := os.ReadDir(filepath.Join(s.options.StoreRoot, "records"))
			intents, _ := os.ReadDir(filepath.Join(s.options.StoreRoot, "intents"))
			if len(records) != 0 || len(intents) != 0 || len(s.active) != 0 {
				t.Fatal("refused request published authority")
			}
		})
	}
}

func TestOwnerNativeLiveGrantIssueChecks(t *testing.T) {
	for _, name := range []string{"closed", "triggered", "null-body", "missing-body", "wrong-node", "wrong-number", "edit-before-publish", "edit-before-ack", "edit-during-snapshot", "wrong-target-label", "trigger-before-ack", "unreadable"} {
		t.Run(name, func(t *testing.T) {
			s, r, is := ownerPortFixture(t, "claude")
			reads := 0
			s.deps.command = func(_ context.Context, _ string, program string, _ []byte, args ...string) ([]byte, error) {
				reads++
				if program != "gh" || len(args) != 7 || args[0] != "issue" || args[1] != "view" || args[5] != "--json" {
					t.Fatal("port attempted an effect")
				}
				state := "OPEN"
				labels := []any{map[string]any{"name": "fixture-target"}}
				if name == "closed" {
					state = "CLOSED"
				}
				if name == "triggered" || (name == "trigger-before-ack" && reads == 3) {
					labels = []any{map[string]any{"name": "fixture-target"}, map[string]any{"name": "harness"}}
				}
				if (name == "edit-before-publish" && reads == 2) || (name == "edit-before-ack" && reads == 3) || (name == "edit-during-snapshot" && reads == 4) {
					is.Body += " edited"
				}
				if name == "wrong-target-label" {
					labels = []any{map[string]any{"name": "other-target"}}
				}
				payload := map[string]any{"id": is.ID, "number": is.Number, "title": is.Title, "body": is.Body, "state": state, "labels": labels}
				switch name {
				case "null-body":
					payload["body"] = nil
				case "missing-body":
					delete(payload, "body")
				case "wrong-node":
					payload["id"] = "wrong-node"
				case "wrong-number":
					payload["number"] = 8
				case "unreadable":
					return nil, errMatrix
				}
				return json.Marshal(payload)
			}
			ack := s.install(context.Background(), r)
			if ack.State == "LIVE" {
				t.Fatal("edited, active, incomplete or unreadable issue acknowledged LIVE")
			}
			if name == "edit-before-ack" {
				ownerPortRequireFenced(t, s, *is)
			}
			if !containsString([]string{"edit-before-ack", "edit-during-snapshot", "trigger-before-ack"}, name) {
				records, _ := os.ReadDir(filepath.Join(s.options.StoreRoot, "records"))
				if len(records) != 0 {
					t.Fatal("issue refusal published a record")
				}
			}
		})
	}
}

func TestOwnerNativeLiveGrantPublicationFailures(t *testing.T) {
	for _, name := range []string{"file-sync", "intent-directory-sync", "record-directory-sync", "record-link", "activation", "collision", "wrong-store-mode"} {
		t.Run(name, func(t *testing.T) {
			s, r, is := ownerPortFixture(t, "claude")
			switch name {
			case "file-sync":
				s.fileSync = func(*os.File) error { return errMatrix }
			case "intent-directory-sync":
				s.dirSync = func(string) error { return errMatrix }
			case "record-directory-sync":
				s.dirSync = func(path string) error {
					if filepath.Base(path) == "records" {
						return errMatrix
					}
					return syncDirectory(path)
				}
			case "record-link":
				s.link = func(from, to string) error {
					if filepath.Base(filepath.Dir(to)) == "records" {
						return errMatrix
					}
					return os.Link(from, to)
				}
			case "activation":
				s.activate = func() error { return errMatrix }
			case "collision":
				_ = os.WriteFile(s.recordPath(r.OperationID), []byte(`{"corrupt":true}`), 0600)
			case "wrong-store-mode":
				_ = os.Chmod(s.options.StoreRoot, 0755)
			}
			if ack := s.install(context.Background(), r); ack.State == "LIVE" {
				t.Fatal("uncertain publication acknowledged LIVE")
			}
			if s.readStatus(context.Background(), r.OperationID).State == "LIVE" {
				t.Fatal("status activated unacknowledged record")
			}
			if name != "collision" && name != "wrong-store-mode" {
				ownerPortRequireFenced(t, s, *is)
			}
			if name == "activation" || name == "record-directory-sync" {
				recovered, err := newOwnerNativeGrantStore(context.Background(), s.cfg, s.options, s.deps)
				if err != nil || recovered.readStatus(context.Background(), r.OperationID).State != "LIVE" {
					t.Fatal("independent recovery failed")
				}
			}
		})
	}
}

// Owner rule (2026-10-04): Eric's Approve in Cockpit is the approval; no
// operator file is written. The exact request still binds the current pins.
func TestOwnerNativeLiveGrantOwnerApproveNeedsNoFile(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
	r.ApprovalRef = "8f0c2a4e-1b7d-4c39-9e55-0a6b3c2d1e4f"
	r.NativeOverride = &ownerNativeOverride{Authorizer: "eric", Rationale: "Owner approved this launch in Cockpit.",
		Route: ownerNativeRoute{Harness: "claude", Provider: "anthropic", Model: "claude-opus-5-5", Effort: "high"}}
	var logs bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prior)
	ack := s.install(context.Background(), r)
	if ack.State != "LIVE" || ack.Route == nil || ack.Route.Model != "claude-opus-5-5" {
		t.Fatalf("owner Approve without a file refused: state=%s reason=%s log=%q", ack.State, ack.Reason, logs.String())
	}
	if !strings.Contains(logs.String(), "LIVE for claude claude-opus-5-5 (high)") {
		t.Fatalf("LIVE grant not logged: %q", logs.String())
	}
	if s.readStatus(context.Background(), r.OperationID).State != "LIVE" {
		t.Fatal("owner Approve status not LIVE")
	}
	if _, err := s.matrixForIssue(*is, r.Target); err != nil {
		t.Fatal("owner Approve grant not admitted")
	}
	recovered, err := newOwnerNativeGrantStore(context.Background(), s.cfg, s.options, s.deps)
	if err != nil || recovered.readStatus(context.Background(), r.OperationID).State != "LIVE" {
		t.Fatal("owner Approve grant not recovered")
	}
	// A later pin change fences the app approval exactly like a file approval.
	s.cfg.Matrix.Revision = strings.Repeat("d", 40)
	if s.readStatus(context.Background(), r.OperationID).State == "LIVE" {
		t.Fatal("app approval survived a matrix pin change")
	}
}

func TestOwnerNativeLiveGrantRefusalsLogOnePlainReason(t *testing.T) {
	for name, want := range map[string]string{
		"stale-brief":     "the brief changed after it was approved",
		"mismatched-file": "an operator approval file exists for this launch and does not match it",
		"wrong-profile":   "the brief's profile is not the approved profile",
		"static-conflict": "a startup grant already covers this issue and brief",
	} {
		t.Run(name, func(t *testing.T) {
			s, r, _ := ownerPortFixture(t, "claude")
			switch name {
			case "stale-brief":
				_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
				r.ExpectedBriefDigest = strings.Repeat("a", 64)
			case "mismatched-file":
				r.Tier = "other-tier"
			case "wrong-profile":
				_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
				r.Profile = "other-profile"
			case "static-conflict":
				s.cfg.Matrix.Grants = []MatrixGrant{{IssueID: "synthetic-node", Repo: r.Target, BriefDigest: r.ExpectedBriefDigest, NativeOverride: r.NativeOverride}}
			}
			var logs bytes.Buffer
			prior := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(prior)
			ack := s.install(context.Background(), r)
			line := "issue #7: owner grant " + r.OperationID + " REFUSED (override-invalid): " + want + "\n"
			if ack.State != "REFUSED" || ack.Reason != "override-invalid" || !strings.HasSuffix(logs.String(), line) || strings.Count(logs.String(), "\n") != 1 {
				t.Fatalf("refusal not explained once: state=%s reason=%s log=%q", ack.State, ack.Reason, logs.String())
			}
		})
	}
}

func TestOwnerNativeLiveGrantDamagedAuthorityFences(t *testing.T) {
	for _, name := range []string{"missing-record", "corrupt-record", "missing-intent", "corrupt-intent", "missing-approval", "empty-intent-directory", "wrong-record-mode", "intent-directory-symlink", "changed-node", "changed-title", "changed-target", "revoked-target", "changed-pin"} {
		t.Run(name, func(t *testing.T) {
			s, r, is := ownerPortFixture(t, "claude")
			if s.install(context.Background(), r).State != "LIVE" {
				t.Fatal("control")
			}
			intentPath := filepath.Join(s.intentDir(r.IssueNumber), ownerNativeOperationKey(r.OperationID)+".json")
			switch name {
			case "missing-record":
				_ = os.Remove(s.recordPath(r.OperationID))
			case "corrupt-record":
				_ = os.WriteFile(s.recordPath(r.OperationID), []byte("bad"), 0600)
			case "missing-intent":
				_ = os.Remove(intentPath)
			case "corrupt-intent":
				_ = os.WriteFile(intentPath, []byte("bad"), 0600)
			case "missing-approval":
				_ = os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json"))
			case "empty-intent-directory":
				_ = os.Remove(intentPath)
			case "wrong-record-mode":
				_ = os.Chmod(s.recordPath(r.OperationID), 0644)
			case "intent-directory-symlink":
				dir := s.intentDir(r.IssueNumber)
				_ = os.Rename(dir, dir+"-old")
				_ = os.Symlink(dir+"-old", dir)
			case "changed-node":
				is.ID = "different-node"
			case "changed-title":
				is.Title += " edited"
			case "changed-target":
				r.Target = "other/project"
			case "revoked-target":
				s.cfg.Targets[0].Disabled = true
			case "changed-pin":
				s.cfg.Matrix.Revision = strings.Repeat("d", 40)
			}
			if name == "changed-target" {
				if _, err := s.matrixForIssue(*is, r.Target); err == nil {
					t.Fatal("target redirect bypassed fence")
				}
				return
			}
			ownerPortRequireFenced(t, s, *is)
			if !containsString([]string{"changed-node", "changed-title"}, name) && s.readStatus(context.Background(), r.OperationID).State == "LIVE" {
				t.Fatal("damaged proof stayed LIVE")
			}
			if containsString([]string{"missing-record", "missing-intent", "empty-intent-directory"}, name) {
				recovered, err := newOwnerNativeGrantStore(context.Background(), s.cfg, s.options, s.deps)
				if err != nil {
					t.Fatal("recovery")
				}
				ownerPortRequireFenced(t, recovered, *is)
			}
		})
	}
}

func TestOwnerNativeLiveGrantCommonAdmissionAllCLIs(t *testing.T) {
	for _, tool := range matrixTransports {
		t.Run(tool, func(t *testing.T) {
			s, r, is := ownerPortFixture(t, tool)
			s.cfg.OwnerNativeGrantPort = &s.options
			s.cfg.Governor.WeeklyCeiling = 92
			s.cfg.OpenCode = OpenCodeConfig{Providers: map[string]OpenCodeProvider{"fixture-provider": {MaxActive: 1}}}
			s.cfg.UnmeteredTransports = UnmeteredTransportLimits{"agy": {MaxActive: 1}}
			if s.install(context.Background(), r).State != "LIVE" {
				t.Fatal("authority control")
			}
			c := &Coord{cfg: s.cfg, ownerGrants: s}
			now := time.Now()
			q := quota{ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(time.Hour).Unix()}}
			c.gov.q = map[string]quota{"claude": q, "codex": q}
			budget := map[string]int{"claude": 1, "codex": 1, "agy": 1, "opencode": 1, "opencode:fixture-provider": 1}
			persisted, launched := 0, 0
			var refusal matrixRefusal
			d := matrixAttemptDeps{
				report: func(r matrixRefusal) { refusal = r },
				read: func(context.Context, string, string, string) (string, error) {
					return "| `routing` | matrix `implementation` row |", nil
				},
				resolve: func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) {
					t.Fatal("live native grant fell back to logical matrix")
					return matrixRoute{}, errMatrix
				},
				host: func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, true },
				persist: func(root, key, command string, receipt matrixReceipt, binding any, owners ...*receiptOwner) (*durableMatrixReceipt, error) {
					persisted++
					return persistMatrixReceipt(root, key, command, receipt, binding, owners...)
				},
				launch: func(_ context.Context, _ int, _ Issue, _ Host, agent string, o Overrides, receipt *durableMatrixReceipt) error {
					launched++
					if agent != tool || o.Model != r.NativeOverride.Route.Model || !receipt.claim(mustBuildAgentCmd(t, tool, o)) {
						t.Fatal("native receipt/command not preserved")
					}
					return nil
				},
			}
			_, ok := c.matrixAttempt(context.Background(), is.Number, *is, Target{Repo: r.Target}, budget, d)
			if !ok || persisted != 1 || launched != 1 {
				t.Fatalf("live common admission failed: %s", refusal.ReasonCode)
			}
			// A stale poll snapshot must not authorize after GitHub's complete brief changed.
			old := *is
			is.Body += "\nCurrent edited task"
			_, ok = c.matrixAttempt(context.Background(), old.Number, old, Target{Repo: r.Target}, budget, d)
			if ok || persisted != 1 || launched != 1 {
				t.Fatal("stale poll reached launch")
			}
			// Action retry uses the same accessor and cannot revive the stale owner grant.
			d.retry = &retryExpectation{}
			_, ok = c.matrixAttempt(context.Background(), old.Number, old, Target{Repo: r.Target}, budget, d)
			if ok || persisted != 1 || launched != 1 {
				t.Fatal("action retry escaped the live owner fence")
			}
		})
	}
}

func TestOwnerNativeLiveGrantHistoryAndBounds(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	if s.install(context.Background(), r).State != "LIVE" {
		t.Fatal("control")
	}
	is.Body += "\nNew owner-approved brief with ``` fenced λ detail"
	next := r
	next.OperationID = "22222222-2222-4222-8222-222222222222"
	next.ApprovalRef = "next-approval"
	next.ExpectedBriefDigest = briefDigest(*is)
	ownerPortWriteApproval(t, s, next)
	if s.install(context.Background(), next).State != "LIVE" {
		t.Fatal("new exact brief approval refused")
	}
	if os.Remove(filepath.Join(s.options.ApprovalRoot, r.ApprovalRef+".json")) != nil {
		t.Fatal("history fixture")
	}
	if s.readStatus(context.Background(), next.OperationID).State != "LIVE" {
		t.Fatal("unselected history blocked current approval")
	}
	recovered, err := newOwnerNativeGrantStore(context.Background(), s.cfg, s.options, s.deps)
	if err != nil || recovered.readStatus(context.Background(), next.OperationID).State != "LIVE" {
		t.Fatal("history recovery")
	}
	for n := 0; n < 33; n++ {
		_ = os.WriteFile(filepath.Join(s.intentDir(r.IssueNumber), fmt.Sprintf(".owner-native-leftover-%d", n)), []byte("partial"), 0600)
	}
	if _, err := ownerNativePrivateEntries(s.intentDir(r.IssueNumber), 32); err == nil {
		t.Fatal("unbounded directory accepted")
	}
	ownerPortRequireFenced(t, s, *is)
}

func TestOwnerNativePrivateFilesystemChecks(t *testing.T) {
	root := privateTestRoot(t)
	path := filepath.Join(root, "record.json")
	raw := []byte(`{"synthetic":true}`)
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("fixture")
	}
	if _, err := ownerNativePrivateRead(path, os.Getuid()); err != nil {
		t.Fatal("private read")
	}
	if _, err := ownerNativePrivateRead(path, os.Getuid()+1); err == nil {
		t.Fatal("foreign owner accepted")
	}
	if os.Chmod(path, 0644) != nil {
		t.Fatal("fixture")
	}
	if _, err := ownerNativePrivateRead(path, os.Getuid()); err == nil {
		t.Fatal("public file accepted")
	}
	_ = os.Chmod(path, 0600)
	sym := filepath.Join(root, "symlink.json")
	_ = os.Symlink(path, sym)
	if _, err := ownerNativePrivateRead(sym, os.Getuid()); err == nil {
		t.Fatal("symlink followed")
	}
	if os.WriteFile(path, make([]byte, ownerNativeFrameLimit+1), 0600) != nil {
		t.Fatal("oversize fixture")
	}
	if _, err := ownerNativePrivateRead(path, os.Getuid()); err == nil {
		t.Fatal("unbounded file accepted")
	}
	_ = os.WriteFile(path, []byte{0xff}, 0600)
	if _, err := ownerNativePrivateRead(path, os.Getuid()); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if os.Mkdir(filepath.Join(root, ".git"), 0700) != nil {
		t.Fatal("git fixture")
	}
	if ownerNativePrivateDir(root, os.Getuid()) {
		t.Fatal("authority directory inside Git accepted")
	}
}

func TestOwnerNativeLiveGrantStartupCompatibility(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	input, _ := json.Marshal(MatrixGrant{Tier: r.Tier, Role: r.Role, NativeOverride: r.NativeOverride})
	grant, problems := bindMatrixGrant(*is, r.Target, input)
	if len(problems) > 0 {
		t.Fatal("fixture")
	}
	s.cfg.Matrix.Grants = []MatrixGrant{grant}
	if matrix, err := s.matrixForIssue(*is, r.Target); err != nil || len(matrix.Grants) != 1 {
		t.Fatal("startup grant lost")
	}
	is.Body += "\nEdited startup owner brief"
	ownerPortRequireFenced(t, s, *is)
}

func TestOwnerNativeLiveGrantCallerDetachment(t *testing.T) {
	s, r, _ := ownerPortFixture(t, "claude")
	ack := s.install(context.Background(), r)
	if ack.State != "LIVE" {
		t.Fatal("control")
	}
	expected := r.NativeOverride.Route
	r.NativeOverride.Route.Model = "mutated-caller-value"
	current := s.readStatus(context.Background(), r.OperationID)
	if current.State != "LIVE" || *current.Route != expected {
		t.Fatal("caller mutated active authority")
	}
}

func TestOwnerNativeLiveGrantPublicationReadback(t *testing.T) {
	s, _, _ := ownerPortFixture(t, "claude")
	dir := filepath.Join(s.options.StoreRoot, "records")
	path := filepath.Join(dir, "synthetic.json")
	if err := s.publish(path, []byte("first")); err != nil {
		t.Fatal("control")
	}
	if err := s.publish(path, []byte("second")); err == nil {
		t.Fatal("immutable collision overwritten")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "first" {
		t.Fatal("immutable bytes changed")
	}
	badPath := filepath.Join(dir, "readback.json")
	s.link = func(from, to string) error {
		if err := os.Link(from, to); err != nil {
			return err
		}
		return os.WriteFile(to, []byte("different bytes"), 0600)
	}
	if err := s.publish(badPath, []byte("expected bytes")); err == nil {
		t.Fatal("damaged publication readback accepted")
	}
}

func TestOwnerNativePrivateReadIdentityAndParent(t *testing.T) {
	root := privateTestRoot(t)
	path := filepath.Join(root, "expected.json")
	other := filepath.Join(root, "different.json")
	_ = os.WriteFile(path, []byte("expected"), 0600)
	_ = os.WriteFile(other, []byte("different"), 0600)
	if _, err := ownerNativePrivateReadWithOpen(path, os.Getuid(), func(string) (*os.File, error) { return ownerNativeOpen(other) }); err == nil {
		t.Fatal("different inode accepted after lstat")
	}
	sym := filepath.Join(root, "alias.json")
	_ = os.Symlink(path, sym)
	if file, err := ownerNativeOpen(sym); err == nil {
		file.Close()
		t.Fatal("no-follow open followed symlink")
	}
	_ = os.Chmod(root, 0755)
	if _, err := ownerNativePrivateRead(path, os.Getuid()); err == nil {
		t.Fatal("unsafe parent accepted")
	}
}

func TestOwnerNativeLiveGrantBuilderPolicy(t *testing.T) {
	for _, name := range []string{"profile", "routing", "source", "static-conflict"} {
		t.Run(name, func(t *testing.T) {
			s, r, is := ownerPortFixture(t, "claude")
			switch name {
			case "profile":
				r.Profile = "other-profile"
			case "routing":
				is.Body = "/swarm\ntier: feature\nrole: implementation\nprofile: leaf\nharness: opencode\n\nExact approved task"
				r.ExpectedBriefDigest = briefDigest(*is)
			case "source":
				s.cfg.Matrix.Source = filepath.Join(privateTestRoot(t), "missing-checkout")
			case "static-conflict":
				input, _ := json.Marshal(MatrixGrant{Tier: r.Tier, Role: r.Role, NativeOverride: r.NativeOverride})
				g, p := bindMatrixGrant(*is, r.Target, input)
				if len(p) > 0 {
					t.Fatal("fixture")
				}
				s.cfg.Matrix.Grants = []MatrixGrant{g}
			}
			ownerPortWriteApproval(t, s, r)
			if s.install(context.Background(), r).State == "LIVE" {
				t.Fatal("invalid approved scope reached LIVE")
			}
			entries, _ := os.ReadDir(filepath.Join(s.options.StoreRoot, "records"))
			if len(entries) != 0 {
				t.Fatal("builder policy refusal published a record")
			}
		})
	}
}

func TestOwnerNativeLiveGrantBootstrapGuards(t *testing.T) {
	s, _, _ := ownerPortFixture(t, "claude")
	deps := s.deps
	deps.read = nil
	if _, err := newOwnerNativeGrantStore(context.Background(), s.cfg, s.options, deps); err == nil {
		t.Fatal("missing approval builder dependency accepted")
	}
	s.cfg.Matrix.Source = filepath.Join(privateTestRoot(t), "missing-checkout")
	if _, err := newOwnerNativeGrantStore(context.Background(), s.cfg, s.options, s.deps); err == nil {
		t.Fatal("invalid pinned configuration bootstrapped")
	}
}

func TestOwnerNativeLiveGrantRecoveredScopeGuards(t *testing.T) {
	for _, name := range []string{"version", "inbox", "pin", "approval-bytes", "profile", "static-conflict", "generated-binding"} {
		t.Run(name, func(t *testing.T) {
			s, r, _ := ownerPortFixture(t, "claude")
			if s.install(context.Background(), r).State != "LIVE" {
				t.Fatal("control")
			}
			record, _, err := s.readRecordLocked(r.OperationID)
			if err != nil || !s.recordValid(record) {
				t.Fatal("valid recovered record")
			}
			switch name {
			case "version":
				record.SchemaVersion = 2
			case "inbox":
				record.Inbox = "other/inbox"
			case "pin":
				record.MatrixRevision = strings.Repeat("d", 40)
			case "approval-bytes":
				record.ApprovalDigest = strings.Repeat("a", 64)
			case "profile":
				record.Request.Profile = "other-profile"
				ownerPortWriteApproval(t, s, record.Request)
				approval, _ := s.approval(record.Request)
				record.ApprovalDigest = approval
			case "static-conflict":
				s.cfg.Matrix.Grants = []MatrixGrant{record.Grant}
			case "generated-binding":
				record.Grant.Repo = "other/project"
			}
			if s.recordValid(record) {
				t.Fatal("invalid recovered scope became usable")
			}
		})
	}
}

func TestOwnerNativeLiveGrantRequiredIssueFields(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	is.Body = ""
	r.ExpectedBriefDigest = briefDigest(*is)
	ownerPortWriteApproval(t, s, r)
	s.deps.command = func(context.Context, string, string, []byte, ...string) ([]byte, error) {
		return json.Marshal(map[string]any{"id": is.ID, "number": is.Number, "title": is.Title, "body": nil, "state": "OPEN", "labels": []any{map[string]any{"name": "fixture-target"}}})
	}
	if s.install(context.Background(), r).State == "LIVE" {
		t.Fatal("null body substituted for complete empty body")
	}
}

func TestOwnerNativeLiveGrantPrivateRecordProof(t *testing.T) {
	s, r, _ := ownerPortFixture(t, "claude")
	if s.install(context.Background(), r).State != "LIVE" {
		t.Fatal("control")
	}
	raw, _ := json.Marshal(ownerNativeIntent{SchemaVersion: 1, OperationID: r.OperationID, RecordChecksum: strings.Repeat("a", 64)})
	_ = os.WriteFile(filepath.Join(s.intentDir(r.IssueNumber), ownerNativeOperationKey(r.OperationID)+".json"), raw, 0600)
	if _, _, err := s.readRecordLocked(r.OperationID); err == nil {
		t.Fatal("private record accepted a different durable index checksum")
	}
}

func TestOwnerNativeLiveGrantUnavailableStoreNoFallback(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	_ = os.Chmod(s.options.StoreRoot, 0755)
	if _, err := s.matrixForIssue(*is, r.Target); err == nil {
		t.Fatal("unsafe configured store fell back to ordinary admission")
	}
}

func TestOwnerNativeLiveGrantValidJSONTamperRecovery(t *testing.T) {
	s, r, is := ownerPortFixture(t, "claude")
	if s.install(context.Background(), r).State != "LIVE" {
		t.Fatal("control")
	}
	raw, _ := os.ReadFile(s.recordPath(r.OperationID))
	var record ownerNativeGrantRecord
	_ = json.Unmarshal(raw, &record)
	record.Issue.Labels = append(record.Issue.Labels, "unrelated-label")
	tampered, _ := json.Marshal(record)
	_ = os.WriteFile(s.recordPath(r.OperationID), tampered, 0600)
	recovered, err := newOwnerNativeGrantStore(context.Background(), s.cfg, s.options, s.deps)
	if err != nil {
		t.Fatal("recover")
	}
	if recovered.readStatus(context.Background(), r.OperationID).State == "LIVE" {
		t.Fatal("different record bytes recovered as LIVE")
	}
	ownerPortRequireFenced(t, recovered, *is)
}
