package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func trustWriteOK() any {
	return map[string]string{"status": "ok", "version": "fixture-version"}
}

func TestCodexCheckoutTrustWriteAndRemoval(t *testing.T) {
	// Dots, quotes, backslashes and Unicode must remain one native key segment.
	cwd := `/fixture/repo.v2/quote"slash\雪<>&/issue-7`
	var writes []map[string]any
	p, _ := remoteFixturePort(func(method string, params map[string]any) any {
		if method != codexConfigValueWrite {
			t.Fatalf("unexpected effect: %s", method)
		}
		writes = append(writes, params)
		return trustWriteOK()
	}, "")
	if err := p.setCheckoutTrust(cwd, true); err != nil {
		t.Fatal(err)
	}
	if err := p.setCheckoutTrust(cwd, false); err != nil {
		t.Fatal(err)
	}
	key := `projects."/fixture/repo.v2/quote\"slash\\雪<>&/issue-7"`
	want := []map[string]any{
		{"keyPath": key, "mergeStrategy": "upsert", "value": map[string]any{"trust_level": "trusted"}},
		{"keyPath": key, "mergeStrategy": "upsert", "value": nil},
	}
	if !reflect.DeepEqual(writes, want) {
		t.Fatalf("trust effects escaped the exact checkout: %#v", writes)
	}
}

func TestCodexCheckoutTrustRefusesUnconfirmedWrites(t *testing.T) {
	for _, result := range []any{
		map[string]any{}, nil,
		map[string]string{"status": "okOverridden", "version": "fixture-version"},
		map[string]string{"status": "ok"},
		map[string]string{"status": "failed", "version": "fixture-version"},
	} {
		for _, grant := range []bool{true, false} {
			p, _ := remoteFixturePort(func(string, map[string]any) any { return result }, "")
			if err := p.setCheckoutTrust("/fixture/issue-7", grant); err == nil {
				t.Fatal("an unconfirmed config write was accepted")
			}
		}
	}
}

func TestCodexCheckoutTrustCapabilityIsNarrow(t *testing.T) {
	p, w := remoteFixturePort(func(string, map[string]any) any { return trustWriteOK() }, "")
	for _, cwd := range []string{"", "/", "relative", "/fixture/../issue-7", "/fixture/line\nbreak", strings.Repeat("/a", 2100)} {
		if p.setCheckoutTrust(cwd, true) == nil || p.setCheckoutTrust(cwd, false) == nil {
			t.Fatal("unscoped trust path accepted")
		}
	}
	for _, value := range []any{
		map[string]any{"keyPath": "projects", "value": map[string]any{}, "mergeStrategy": "replace"},
		codexCheckoutTrustWrite{cwd: "/fixture/issue-7", KeyPath: "projects", MergeStrategy: "upsert"},
		codexCheckoutTrustWrite{cwd: "/fixture/issue-7", KeyPath: codexCheckoutTrustKey("/fixture/issue-7"), MergeStrategy: "replace"},
		codexCheckoutTrustWrite{cwd: "/fixture/issue-7", KeyPath: codexCheckoutTrustKey("/fixture/issue-7"), MergeStrategy: "upsert", Value: map[string]string{"other": "trusted"}},
	} {
		if _, err := p.request(codexConfigValueWrite, value); err == nil {
			t.Fatal("generic config mutation gained checkout trust capability")
		}
	}
	if len(w.requests) != 0 {
		t.Fatal("refused config write reached the transport")
	}
}

func TestCodexCheckoutTrustPreparationWritesOnce(t *testing.T) {
	for _, status := range []string{"ok", "okOverridden", "failed"} {
		t.Run(status, func(t *testing.T) {
			run := syntheticRemoteRun(t)
			run.ClientVersion = canonicalFixtureVersion
			expected := *run
			writes := make(chan map[string]any, 4)
			h := canonicalFixtureHost(t, "valid", func(method string, params map[string]any) any {
				switch method {
				case "remoteControl/status/read":
					return map[string]string{"status": "connected"}
				case "thread/start", "thread/read":
					return remoteThreadFixture(&expected)
				case codexConfigValueWrite:
					writes <- params
					return map[string]string{"status": status, "version": "fixture-version"}
				}
				return map[string]any{}
			})
			ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
			defer done()
			err := h.prepareRemoteCodex(ctx, run, nil)
			if (err == nil) != (status == "ok") {
				t.Fatalf("unconfirmed daemon trust allowed TUI startup: %v", err)
			}
			if len(writes) != 1 {
				t.Fatal("preparation did not make exactly one config write")
			}
			write := <-writes
			if write["keyPath"] != codexCheckoutTrustKey(run.Cwd) {
				t.Fatal("preparation trusted a different checkout")
			}
		})
	}
}

func TestCodexCheckoutCleanupRemovesTrustBeforeDirectory(t *testing.T) {
	for _, status := range []string{"ok", "okOverridden"} {
		t.Run(status, func(t *testing.T) {
			cwd := filepath.Join(t.TempDir(), "issue-7")
			if err := os.Mkdir(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			writes := make(chan bool, 2)
			h := canonicalFixtureHost(t, "valid", func(method string, params map[string]any) any {
				_, exists := params["value"]
				_, statErr := os.Stat(cwd)
				writes <- method == codexConfigValueWrite && params["keyPath"] == codexCheckoutTrustKey(cwd) && exists && params["value"] == nil && statErr == nil
				return map[string]string{"status": status, "version": "fixture-version"}
			})
			// Cleanup must not depend on a now-archived thread or changed roots.
			run := syntheticRemoteRun(t)
			run.Cwd = cwd
			j := &Job{Issue: 7, Agent: "codex", Workdir: cwd, RemoteControl: run}
			h = h.forJob(j)
			h.WorkdirRoot = "/different-root"
			ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
			defer done()
			err := h.cleanupCheckout(ctx, j)
			if (err == nil) != (status == "ok") || len(writes) != 1 || !<-writes {
				t.Fatal("cleanup lost the exact checkout or native removal ordering")
			}
			_, statErr := os.Stat(cwd)
			if status == "ok" && !os.IsNotExist(statErr) || status != "ok" && statErr != nil {
				t.Fatal("checkout deletion did not respect confirmed trust removal")
			}
		})
	}
}
