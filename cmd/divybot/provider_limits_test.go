package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func limitFixture(t *testing.T) (*Coord, time.Time) {
	t.Helper()
	root := privateTestRoot(t)
	output := privateTestRoot(t)
	cfg := &ProviderLimitsConfig{PrivateRoot: root, SnapshotFile: filepath.Join(output, "provider-limits.json"), Accounts: []providerLimitAccount{}, OpenRouterKeys: []providerLimitKey{}, ObservedRoutes: []string{"opencode-go/fixture", "codex/fixture", "openrouter/vendor/model"}}
	c := &Coord{cfg: &Config{ProviderLimits: cfg}}
	t.Cleanup(c.closeProviderLimits)
	return c, time.Now().Truncate(time.Millisecond).Add(-time.Minute)
}
func limitOutcome(provider, model, outcome, reason string, at time.Time) providerLimitOutcome {
	o := providerLimitOutcome{Provider: provider, Model: limitString(model), Outcome: outcome, Source: "provider-run", ObservedAt: limitInstant(at)}
	if model == "" {
		o.Model = nil
	}
	if reason != "" {
		o.Reason = limitString(reason)
	}
	return o
}
func TestProviderLimitsRefusalSurvivesRestartDeletionAndMetadata(t *testing.T) {
	c, at := limitFixture(t)
	cfg := c.cfg.ProviderLimits
	refusal := limitOutcome("opencode-go", "", "refused", "quota_exhausted", at)
	if c.recordLimitOutcome(refusal, "fixture-clock", "fixture-refusal", at) != nil {
		t.Fatal("record refusal")
	}
	if c.publishProviderLimits(context.Background(), nil, at.Add(time.Second)) != nil {
		t.Fatal("publish")
	}
	if c.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(time.Second)) != "quota-unavailable" {
		t.Fatal("provider-wide refusal did not block")
	}
	_ = os.Remove(cfg.SnapshotFile)
	c.closeProviderLimits()
	restarted := &Coord{cfg: c.cfg}
	t.Cleanup(restarted.closeProviderLimits)
	if restarted.publishProviderLimits(context.Background(), nil, at.Add(2*time.Second)) != nil {
		t.Fatal("restart history")
	}
	raw, e := readLimitPrivate(cfg.SnapshotFile)
	if e != nil {
		t.Fatal(e)
	}
	var doc providerLimitSnapshot
	_ = strictJSON(raw, &doc)
	if len(doc.Outcomes) != 1 || doc.Outcomes[0].Outcome != "refused" {
		t.Fatal("metadata or deletion erased refusal")
	}
	wrong := limitOutcome("codex", "fixture", "succeeded", "", at.Add(3*time.Second))
	if restarted.recordLimitOutcome(wrong, "fixture-clock", "fixture-other", at.Add(3*time.Second)) != nil {
		t.Fatal("other success")
	}
	if restarted.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(4*time.Second)) == "" {
		t.Fatal("unrelated provider cleared refusal")
	}
	good := limitOutcome("opencode-go", "fixture", "succeeded", "", at.Add(4*time.Second))
	if restarted.recordLimitOutcome(good, "fixture-clock", "fixture-success", at.Add(4*time.Second)) != nil {
		t.Fatal("actual success")
	}
	if restarted.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(5*time.Second)) != "" {
		t.Fatal("strictly later verified success did not clear")
	}
	raw, e = readLimitPrivate(cfg.SnapshotFile)
	if e != nil {
		t.Fatal(e)
	}
	_ = strictJSON(raw, &doc)
	if len(doc.Outcomes) != 3 {
		t.Fatal("success tombstone not immediately republished")
	}
	if proof := os.Getenv("ORCHID_PROVIDER_LIMITS_PROOF"); proof != "" {
		if os.WriteFile(proof, raw, 0600) != nil {
			t.Fatal("write synthetic proof")
		}
	}
}
func TestProviderLimitsIngestionOrderAndReplay(t *testing.T) {
	c, at := limitFixture(t)
	refusal := limitOutcome("opencode-go", "fixture", "refused", "quota_exhausted", at.Add(5*time.Second))
	if c.recordLimitOutcome(refusal, "clock", "refusal", at.Add(12*time.Second)) != nil {
		t.Fatal("delayed refusal")
	}
	stored := c.limits.ledger.Entries[limitScope(refusal)]
	if stored.Outcome.ObservedAt != refusal.ObservedAt || stored.NativeAt != refusal.ObservedAt {
		t.Fatal("receipt time replaced actual native refusal time")
	}
	fresh := limitOutcome("opencode-go", "fixture", "succeeded", "", at.Add(15*time.Second))
	_ = c.recordLimitOutcome(fresh, "foreign-clock", "foreign", at.Add(16*time.Second))
	if c.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(17*time.Second)) == "" {
		t.Fatal("cross-clock success cleared refusal")
	}
	_ = c.recordLimitOutcome(fresh, "clock", "same-clock", at.Add(18*time.Second))
	if c.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(19*time.Second)) != "" {
		t.Fatal("later native inference did not clear")
	}
	_ = c.recordLimitOutcome(refusal, "clock", "refusal", at.Add(20*time.Second))
	if c.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(21*time.Second)) != "" {
		t.Fatal("replayed earlier refusal resurrected after success")
	}
	if c.limits.ledger.Entries[limitScope(fresh)].Outcome.ObservedAt != fresh.ObservedAt {
		t.Fatal("success native clock changed")
	}
	older := limitOutcome("opencode-go", "", "refused", "quota_exhausted", at.Add(4*time.Second))
	_ = c.recordLimitOutcome(older, "clock", "delayed-global", at.Add(22*time.Second))
	if c.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(23*time.Second)) != "" {
		t.Fatal("delayed global refusal ignored verified later native success")
	}
}
func TestProviderLimitsExactBindingRebindAndStorageFences(t *testing.T) {
	c, at := limitFixture(t)
	cfg := c.cfg.ProviderLimits
	a := "aref:v1:codex:" + strings.Repeat("a", 43)
	b := "aref:v1:codex:" + strings.Repeat("b", 43)
	cfg.Accounts = []providerLimitAccount{{"codex", a, "fixture-host", []string{"codex/fixture"}}}
	o := limitOutcome("codex", "fixture", "refused", "quota_exhausted", at)
	o.AccountRef = &a
	if c.recordLimitOutcome(o, "clock", "refusal", at) != nil {
		t.Fatal("account refusal")
	}
	if c.providerLimitLaunchReason("codex-run", Overrides{Model: "fixture"}, at) == "" {
		t.Fatal("bound native alias bypassed account refusal")
	}
	cfg.Accounts[0].AccountRef = b
	if c.providerLimitLaunchReason("codex", Overrides{Model: "fixture"}, at) != "" {
		t.Fatal("old account refusal blocked explicit new binding")
	}
	if len(c.limits.ledger.Entries) != 1 {
		t.Fatal("rebind erased old refusal")
	}
	cfg.Accounts = append(cfg.Accounts, providerLimitAccount{"codex", a, "fixture-host", []string{"codex/fixture"}})
	if cfg.valid() {
		t.Fatal("ambiguous binding accepted")
	}
	cfg.Accounts = cfg.Accounts[:1]
	if c.publishProviderLimits(context.Background(), nil, at.Add(time.Second)) != nil {
		t.Fatal("publish old refusal")
	}
	c.closeProviderLimits()
	_ = os.Chmod(limitLedgerPath(cfg), 0644)
	restart := &Coord{cfg: c.cfg}
	t.Cleanup(restart.closeProviderLimits)
	if restart.providerLimitLaunchReason("codex", Overrides{Model: "fixture"}, at.Add(2*time.Second)) != "receipt-persistence-failed" {
		t.Fatal("unsafe ledger opened admission")
	}
}
func TestProviderLimitsLeaseAndOfflineCompaction(t *testing.T) {
	c, at := limitFixture(t)
	cfg := c.cfg.ProviderLimits
	o := limitOutcome("opencode-go", "fixture", "succeeded", "", at)
	if c.recordLimitOutcome(o, "clock", "success", at) != nil {
		t.Fatal("record")
	}
	if compactProviderLimitLedger(cfg, at.Add(time.Second)) == nil {
		t.Fatal("compaction raced live writer")
	}
	c.closeProviderLimits()
	cfg.ObservedRoutes = []string{}
	if compactProviderLimitLedger(cfg, at.Add(time.Second)) != nil {
		t.Fatal("offline compaction")
	}
	raw, e := readLimitPrivate(limitLedgerPath(cfg))
	var ledger providerLimitLedger
	if e != nil || strictJSON(raw, &ledger) != nil || len(ledger.Entries) != 0 || len(ledger.Retired) != 1 {
		t.Fatal("archive lost watermark")
	}
	cfg.ObservedRoutes = []string{"opencode-go/fixture"}
	restart := &Coord{cfg: c.cfg}
	t.Cleanup(restart.closeProviderLimits)
	if restart.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(2*time.Second)) != "receipt-persistence-failed" {
		t.Fatal("retired scope reopened without replay fence")
	}
	var out, err bytes.Buffer
	if providerLimitsCLI([]string{"compact", "--bad", "secret-private-path"}, &out, &err) != 2 || strings.Contains(err.String(), "secret") {
		t.Fatal("unsafe CLI diagnostic")
	}
}

type limitRoundTripper func(*http.Request) (*http.Response, error)

func (f limitRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestProviderLimitsOpenRouterOwnKeyMetadataOnly(t *testing.T) {
	at := time.Now()
	key := providerLimitKey{"public-alias", "PRIVATE_CREDENTIAL_ENV", []string{"openrouter/vendor/model"}}
	for _, tc := range []struct {
		name, body, state string
		status            int
		used              float64
	}{
		{"own cap", `{"data":{"limit":10,"limit_remaining":1,"usage":90000,"label":"PRIVATE-CREDENTIAL","limit_reset":"weekly"}}`, "known", 200, 9},
		{"zero cap", `{"data":{"limit":0,"limit_remaining":0}}`, "known", 200, 0},
		{"unlimited", `{"data":{"limit":null,"limit_remaining":null,"usage":2}}`, "known", 200, 2},
		{"missing", `{"data":{"limit":10}}`, "unknown", 200, 0},
		{"duplicate", `{"data":{"limit":10,"limit":20,"limit_remaining":1}}`, "unknown", 200, 0},
		{"metadata402", `{"error":{"code":402}}`, "unknown", 402, 0},
		{"metadata429", `{"error":{"code":429}}`, "unknown", 429, 0},
		{"bad remaining", `{"data":{"limit":10,"limit_remaining":11}}`, "unknown", 200, 0},
		{"oversized", strings.Repeat("x", 64*1024+1), "unknown", 200, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: limitRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != providerLimitKeyEndpoint || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer fixture-credential" {
					t.Fatal("wrong endpoint/authorization")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}}, nil
			})}
			row := collectOpenRouterLimit(context.Background(), key, "fixture-credential", client, at)
			if row.State != tc.state || calls != 1 {
				t.Fatal("metadata classification")
			}
			if row.Used != nil && *row.Used != tc.used {
				t.Fatal("account/lifetime usage supplied cap usage")
			}
			b, _ := json.Marshal(row)
			if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "credential") {
				t.Fatal("credential/environment/raw label leaked")
			}
		})
	}
	calls := 0
	client := &http.Client{Transport: limitRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": []string{"https://foreign.invalid/secret"}}, Request: r}, nil
	})}
	if row := collectOpenRouterLimit(context.Background(), key, "fixture-credential", client, at); row.State != "unknown" || calls != 1 {
		t.Fatal("redirect followed")
	}
}
func TestProviderLimitsNativeMissingWindowAndSourceTime(t *testing.T) {
	c, at := limitFixture(t)
	five, seven, _, ok := parseHostQuota("codex", `123 {"payload":{"type":"token_count","rate_limits":{"primary":{"window_minutes":300,"resets_at":456},"secondary":{"used_percent":100,"window_minutes":10080,"resets_at":789}}}}`)
	if !ok || five.ResetsAt != 0 || seven.UsedPct != 100 {
		t.Fatal("missing percentage invented zero")
	}
	rows := nativeLimitMeters(c.cfg.ProviderLimits, map[string]quota{"codex": {ok: true, at: at, five: five, seven: seven}})
	for _, r := range rows {
		if r.Provider == "codex" && r.Window == "weekly" && (r.ObservedAt == nil || *r.ObservedAt != limitInstant(at)) {
			t.Fatal("native source time refreshed")
		}
		if r.Provider == "codex" && r.Window == "5h" && (r.State != "unknown" || r.UsedPercent != nil) {
			t.Fatal("missing native window invented usage")
		}
	}
}
func TestProviderLimitsNativeErrorAndSuccessAfterOwnershipFences(t *testing.T) {
	c, at := limitFixture(t)
	run := &openCodeRun{Route: openCodeRoute{"opencode-go", "fixture", "high"}, Cwd: "/fixture/checkout", SessionID: "ses_fixture", NotBefore: 1000, ExpectedPromptDigest: shaText([]byte(runPointer))}
	j := &Job{Agent: "opencode", Label: "fixture-agent", Pane: "fixture-pane", Workspace: "fixture-workspace", Host: "fixture-clock", OpenCode: run}
	raw := openCodeExportFixture(run, "OK")
	var export map[string]any
	_ = json.Unmarshal(raw, &export)
	messages := export["messages"].([]any)
	assistant := messages[1].(map[string]any)["info"].(map[string]any)
	assistant["error"] = map[string]any{"name": "APIError", "data": map[string]any{"statusCode": 429, "responseBody": `{"name":"GoUsageLimitError","metadata":{"private":"do-not-publish"}}`, "responseHeaders": map[string]any{"retry-after": "60"}}}
	raw, _ = json.Marshal(export)
	a := AgentInfo{Agent: j.Agent, Name: j.Label, PaneID: j.Pane, WorkspaceID: j.Workspace, Cwd: run.Cwd, InteractiveReady: true, StateChangeSeq: 1}
	calls := openCodeObservationCalls{agent: func(context.Context, string) (AgentInfo, error) { return a, nil }, sessions: func(context.Context, *openCodeRun) ([]openCodeSession, error) {
		return []openCodeSession{{ID: run.SessionID, Directory: run.Cwd, Created: 2000}}, nil
	}, export: func(context.Context, *openCodeRun, string) ([]byte, error) { return raw, nil }}
	_, _, e, success := observeOpenCodeSessionLimits(context.Background(), j, calls)
	var native *providerLimitNativeError
	if !errors.As(e, &native) || native.reason != "quota_exhausted" || native.resetsAt == nil || success != nil {
		t.Fatal("actual Go quota not normalized")
	}
	c.recordOpenCodeLimit(j, e, nil, at)
	if c.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at) == "" {
		t.Fatal("actual Go refusal lost")
	}
	reads := 0
	calls.agent = func(context.Context, string) (AgentInfo, error) {
		reads++
		copy := a
		copy.StateChangeSeq = uint64(reads)
		return copy, nil
	}
	_, _, e, _ = observeOpenCodeSessionLimits(context.Background(), j, calls)
	if errors.As(e, &native) {
		t.Fatal("replaced occupant published quota evidence")
	}
	delete(assistant, "error")
	assistant["id"] = "fixture-later-assistant"
	assistant["time"].(map[string]any)["created"] = 2003
	assistant["time"].(map[string]any)["completed"] = 2004
	messages[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["messageID"] = "fixture-later-assistant"
	raw, _ = json.Marshal(export)
	calls.agent = func(context.Context, string) (AgentInfo, error) { return a, nil }
	confirmed, completed, e, success := observeOpenCodeSessionLimits(context.Background(), j, calls)
	if !confirmed || !completed || e != nil || success == nil {
		t.Fatal("verified completed inference absent")
	}
	c.recordOpenCodeLimit(j, nil, success, at.Add(time.Second))
	if c.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(2*time.Second)) != "" {
		t.Fatal("verified later inference failed to clear")
	}
}

func TestProviderLimitsProducedCockpitCompatibility(t *testing.T) {
	c, at := limitFixture(t)
	cfg := c.cfg.ProviderLimits
	cfg.Accounts = []providerLimitAccount{{"codex", "aref:v1:codex:" + strings.Repeat("a", 43), "fixture-host", []string{"codex/fixture"}}}
	cfg.OpenRouterKeys = []providerLimitKey{{"small-cap", "FIXTURE_SMALL_LIMIT_KEY", []string{"openrouter/vendor/model"}}, {"large-cap", "FIXTURE_LARGE_LIMIT_KEY", []string{"openrouter/vendor/other"}}}
	cfg.ObservedRoutes = append(cfg.ObservedRoutes, "openrouter/vendor/other")
	t.Setenv("FIXTURE_SMALL_LIMIT_KEY", "fixture-small")
	t.Setenv("FIXTURE_LARGE_LIMIT_KEY", "fixture-large")
	client := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = client })
	http.DefaultClient = &http.Client{Transport: limitRoundTripper(func(r *http.Request) (*http.Response, error) {
		body := `{"data":{"limit":10,"limit_remaining":0}}`
		if r.Header.Get("Authorization") == "Bearer fixture-large" {
			body = `{"data":{"limit":100,"limit_remaining":80}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	quotas := map[string]quota{"codex": {ok: true, at: at, sourceHost: "fixture-host", five: RateLimit{UsedPct: 90, ResetsAt: at.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 100, ResetsAt: at.Add(7 * 24 * time.Hour).Unix()}}}
	if c.publishProviderLimits(context.Background(), quotas, at.Add(time.Second)) != nil {
		t.Fatal("source publication")
	}
	global := limitOutcome("codex", "", "refused", "quota_exhausted", at.Add(2*time.Second))
	if c.recordLimitOutcome(global, "fixture-clock", "fixture-wide-refusal", at.Add(2*time.Second)) != nil {
		t.Fatal("global source refusal")
	}
	raw, e := readLimitPrivate(cfg.SnapshotFile)
	if e != nil {
		t.Fatal(e)
	}
	var doc providerLimitSnapshot
	if strictJSON(raw, &doc) != nil || len(doc.Outcomes) != 1 || doc.Outcomes[0].AccountRef != nil {
		t.Fatal("global scope lost")
	}
	if target := os.Getenv("ORCHID_PROVIDER_LIMITS_COMPATIBILITY_PROOF"); target != "" {
		if os.WriteFile(target, raw, 0600) != nil {
			t.Fatal("proof output")
		}
	}
}

func TestProviderLimitsSameScopeAndEqualNativeTimes(t *testing.T) {
	for _, kind := range []string{"key", "account", "model", "equal"} {
		t.Run(kind, func(t *testing.T) {
			c, at := limitFixture(t)
			refused := limitOutcome("openrouter", "vendor/model", "refused", "payment_required", at)
			refused.KeyName = limitString("small-cap")
			if c.recordLimitOutcome(refused, "clock", "refused", at.Add(10*time.Second)) != nil {
				t.Fatal("refusal")
			}
			succeeded := refused
			succeeded.Outcome = "succeeded"
			succeeded.Reason = nil
			succeeded.ObservedAt = limitInstant(at.Add(time.Second))
			switch kind {
			case "key":
				succeeded.KeyName = limitString("large-cap")
			case "account":
				succeeded.AccountRef = limitString("paccount_" + strings.Repeat("b", 64))
			case "model":
				succeeded.Model = limitString("vendor/other")
			case "equal":
				succeeded.ObservedAt = refused.ObservedAt
			}
			_ = c.recordLimitOutcome(succeeded, "clock", "success", at.Add(20*time.Second))
			if len(activeLimitRefusals(c.limits.ledger.Entries)) != 1 {
				t.Fatal("different scope or equal native time cleared")
			}
		})
	}
}
func TestProviderLimitsCapacityNeverEvictsRefusals(t *testing.T) {
	c, at := limitFixture(t)
	c.cfg.ProviderLimits.ObservedRoutes = []string{}
	if c.publishProviderLimits(context.Background(), nil, at) != nil {
		t.Fatal("initialize")
	}
	for i := 0; len(c.limits.ledger.Reserved) < providerLimitMaxScopes; i++ {
		o := limitOutcome("opencode-go", fmt.Sprintf("fixture-%d", i), "refused", "quota_exhausted", at)
		c.limits.ledger.Sequence++
		scope := limitScope(o)
		c.limits.ledger.Reserved[scope] = true
		c.limits.ledger.Entries[scope] = providerLimitEntry{o, "clock", fmt.Sprintf("event-%d", i), c.limits.ledger.Sequence, o.ObservedAt, o.ObservedAt}
	}
	if len(c.limits.ledger.Reserved)*2 > providerLimitMaxRows {
		t.Fatal("reserved scopes exceed capacity for both rate and blocking evidence")
	}
	if writeLimitLedger(limitLedgerPath(c.cfg.ProviderLimits), ".fixture-", nil, c.limits.ledger) != nil {
		t.Fatal("fixture bounded ledger")
	}
	before, _ := readLimitPrivate(limitLedgerPath(c.cfg.ProviderLimits))
	if c.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/new-owner-pick"}, at.Add(time.Second)) != "receipt-persistence-failed" {
		t.Fatal("capacity overflow admitted unrecordable work")
	}
	after, _ := readLimitPrivate(limitLedgerPath(c.cfg.ProviderLimits))
	if !bytes.Equal(before, after) || len(activeLimitRefusals(c.limits.ledger.Entries)) != providerLimitMaxScopes-6 {
		t.Fatal("capacity evicted refusal")
	}
	c.closeProviderLimits()
	restarted := &Coord{cfg: c.cfg}
	t.Cleanup(restarted.closeProviderLimits)
	if restarted.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture-0"}, at.Add(2*time.Second)) != "quota-unavailable" {
		t.Fatal("restart lost full ledger refusal")
	}
}
func TestProviderLimitsSelectedNativeHostAndNoCredentialAlias(t *testing.T) {
	c, at := limitFixture(t)
	ref := "aref:v1:claude:" + strings.Repeat("a", 43)
	c.cfg.ProviderLimits.Accounts = []providerLimitAccount{{"claude", ref, "fixture-selected", []string{"claude/fixture"}}}
	root := t.TempDir()
	logfile := filepath.Join(root, "reads")
	fixture := fmt.Sprintf("%d %s", time.Now().Unix(), claudeLine)
	script := "#!/bin/sh\necho selected-read >> " + shq(logfile) + "\nprintf '%s\n' " + shq(fixture) + "\n"
	if os.WriteFile(filepath.Join(root, "ssh"), []byte(script), 0700) != nil {
		t.Fatal("script")
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	c.cfg.Hosts = []Host{{SSH: "fixture-other"}, {SSH: "fixture-selected"}}
	qs := c.sampleQuota(context.Background())
	log, _ := os.ReadFile(logfile)
	if strings.Count(string(log), "selected-read") != 1 || qs["claude"].sourceHost != "fixture-selected" {
		t.Fatal("sampled another account host or duplicated poller")
	}
	rows := nativeLimitMeters(c.cfg.ProviderLimits, map[string]quota{"claude": {ok: true, at: at, sourceHost: "fixture-other", five: RateLimit{UsedPct: 100, ResetsAt: at.Add(time.Hour).Unix()}}})
	for _, row := range rows {
		if row.Provider == "claude" && row.State != "unknown" {
			t.Fatal("different host blended into bound account")
		}
	}
	c.cfg.ProviderLimits.OpenRouterKeys = []providerLimitKey{{"a", "FIXTURE_KEY_A", []string{}}, {"b", "FIXTURE_KEY_B", []string{}}}
	t.Setenv("FIXTURE_KEY_A", "fixture-same")
	t.Setenv("FIXTURE_KEY_B", "fixture-same")
	if c.publishProviderLimits(context.Background(), nil, at) == nil {
		t.Fatal("two aliases claimed identical credential")
	}
}

func TestProviderLimitsCompactionKeepsClearingTombstone(t *testing.T) {
	c, at := limitFixture(t)
	refused := limitOutcome("opencode-go", "", "refused", "quota_exhausted", at)
	success := limitOutcome("opencode-go", "fixture", "succeeded", "", at.Add(time.Second))
	if c.recordLimitOutcome(refused, "clock", "refusal", at) != nil || c.recordLimitOutcome(success, "clock", "success", at.Add(2*time.Second)) != nil {
		t.Fatal("record actual chronology")
	}
	c.closeProviderLimits()
	c.cfg.ProviderLimits.ObservedRoutes = []string{}
	if compactProviderLimitLedger(c.cfg.ProviderLimits, at.Add(3*time.Second)) != nil {
		t.Fatal("compact")
	}
	restarted := &Coord{cfg: c.cfg}
	t.Cleanup(restarted.closeProviderLimits)
	if restarted.publishProviderLimits(context.Background(), nil, at.Add(4*time.Second)) != nil {
		t.Fatal("republish")
	}
	if restarted.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(5*time.Second)) != "" {
		t.Fatal("compaction resurrected cleared global refusal")
	}
	raw, _ := readLimitPrivate(c.cfg.ProviderLimits.SnapshotFile)
	var doc providerLimitSnapshot
	if strictJSON(raw, &doc) != nil || len(doc.Outcomes) != 2 {
		t.Fatal("public source lost clearing tombstone")
	}
}

func TestProviderLimitsReleasedLeaseCannotWrite(t *testing.T) {
	c, at := limitFixture(t)
	if c.publishProviderLimits(context.Background(), nil, at) != nil {
		t.Fatal("initial source")
	}
	before, _ := readLimitPrivate(c.cfg.ProviderLimits.SnapshotFile)
	c.closeProviderLimits()
	if c.publishProviderLimits(context.Background(), nil, at.Add(time.Second)) == nil {
		t.Fatal("released writer lease published")
	}
	after, _ := readLimitPrivate(c.cfg.ProviderLimits.SnapshotFile)
	if !bytes.Equal(before, after) {
		t.Fatal("late shutdown callback wrote source without lease")
	}
}

func TestProviderLimitsBindingRemovalSurvivesRestart(t *testing.T) {
	c, at := limitFixture(t)
	cfg := c.cfg.ProviderLimits
	old := "aref:v1:codex:" + strings.Repeat("a", 43)
	fresh := "aref:v1:codex:" + strings.Repeat("b", 43)
	cfg.Accounts = []providerLimitAccount{{"codex", old, "fixture-host", []string{"codex/fixture"}}}
	refused := limitOutcome("codex", "fixture", "refused", "quota_exhausted", at)
	refused.AccountRef = &old
	if c.recordLimitOutcome(refused, "clock", "refused", at) != nil || c.publishProviderLimits(context.Background(), nil, at.Add(time.Second)) != nil {
		t.Fatal("old bound source")
	}
	before, _ := readLimitPrivate(cfg.SnapshotFile)
	cfg.Accounts = []providerLimitAccount{{"codex", fresh, "fixture-host", []string{}}}
	c.closeProviderLimits()
	restarted := &Coord{cfg: c.cfg}
	t.Cleanup(restarted.closeProviderLimits)
	if restarted.publishProviderLimits(context.Background(), nil, at.Add(2*time.Second)) != nil {
		t.Fatal("new unbound source")
	}
	after, _ := readLimitPrivate(cfg.SnapshotFile)
	var doc providerLimitSnapshot
	if strictJSON(after, &doc) != nil {
		t.Fatal("new source")
	}
	retired := 0
	for _, m := range doc.Meters {
		if sameLimitString(m.AccountRef, &old) {
			retired++
			if len(m.LaunchModels) != 0 || m.State != "unknown" {
				t.Fatal("old route binding survived")
			}
		}
	}
	if retired != 2 || len(doc.Outcomes) != 1 || !sameLimitString(doc.Outcomes[0].AccountRef, &old) {
		t.Fatal("binding retirement erased identity/refusal")
	}
	if restarted.providerLimitLaunchReason("codex", Overrides{Model: "fixture"}, at.Add(3*time.Second)) != "" {
		t.Fatal("unbound new account inherited old refusal")
	}
	if target := os.Getenv("ORCHID_PROVIDER_LIMITS_BINDING_PROOF"); target != "" {
		if os.WriteFile(target+"-before.json", before, 0600) != nil || os.WriteFile(target+"-after.json", after, 0600) != nil {
			t.Fatal("binding proof")
		}
	}
}

func TestProviderLimitsRateObservationNeverBlocksOrClearsHardRefusal(t *testing.T) {
	for _, reason := range []string{"quota_exhausted", "payment_required"} {
		t.Run(reason, func(t *testing.T) {
			c, at := limitFixture(t)
			rate := limitOutcome("opencode-go", "fixture", "refused", "rate_limited", at)
			rate.ResetsAt = limitString(limitInstant(at.Add(time.Hour)))
			if err := c.recordLimitOutcome(rate, "clock", "rate-only", at); err != nil {
				t.Fatal(err)
			}
			if got := c.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at); got != "" {
				t.Fatalf("rate observation blocked: %s", got)
			}
			hard := limitOutcome("opencode-go", "fixture", "refused", reason, at.Add(time.Second))
			if err := c.recordLimitOutcome(hard, "clock", "hard", at.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			rate.ObservedAt = limitInstant(at.Add(2 * time.Second))
			if err := c.recordLimitOutcome(rate, "clock", "later-rate", at.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			if got := c.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(2*time.Second)); got != "quota-unavailable" {
				t.Fatalf("later 429 erased %s refusal: %s", reason, got)
			}
			if err := c.publishProviderLimits(context.Background(), nil, at.Add(3*time.Second)); err != nil {
				t.Fatal(err)
			}
			raw, err := readLimitPrivate(c.cfg.ProviderLimits.SnapshotFile)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot providerLimitSnapshot
			if strictJSON(raw, &snapshot) != nil || len(snapshot.Outcomes) != 2 {
				t.Fatal("hard/rate evidence was discarded")
			}
			for _, o := range snapshot.Outcomes {
				if limitRateObservation(o) && (o.ObservedAt != rate.ObservedAt || !sameLimitString(o.ResetsAt, rate.ResetsAt)) {
					t.Fatal("rate native time/reset lost")
				}
			}
			c.closeProviderLimits()
			restart := &Coord{cfg: c.cfg}
			t.Cleanup(restart.closeProviderLimits)
			if got := restart.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(4*time.Second)); got != "quota-unavailable" {
				t.Fatalf("restart erased hard refusal: %s", got)
			}
			success := limitOutcome("opencode-go", "fixture", "succeeded", "", at.Add(5*time.Second))
			if err := restart.recordLimitOutcome(success, "clock", "success", at.Add(5*time.Second)); err != nil {
				t.Fatal(err)
			}
			if got := restart.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(5*time.Second)); got != "" {
				t.Fatalf("verified success did not clear: %s", got)
			}
			// A new rate observation remains informational after clearance and restart.
			rate.ObservedAt = limitInstant(at.Add(6 * time.Second))
			if err := restart.recordLimitOutcome(rate, "clock", "fresh-rate", at.Add(6*time.Second)); err != nil {
				t.Fatal(err)
			}
			if got := restart.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(6*time.Second)); got != "" {
				t.Fatalf("new rate observation blocked: %s", got)
			}
			rate.ObservedAt = limitInstant(at.Add(4 * time.Second))
			if err := restart.recordLimitOutcome(rate, "foreign-clock", "older-rate", at.Add(7*time.Second)); err != nil {
				t.Fatal("older informational observation fenced dispatch")
			}
			if got := restart.providerLimitLaunchReason("opencode", Overrides{Model: "opencode-go/fixture"}, at.Add(7*time.Second)); got != "" {
				t.Fatal("older 429 fenced admission")
			}
		})
	}
}
