package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// All billing/policy values and identities here are synthetic, never live spend.
func budgetString(v string) *string { return &v }
func budgetFlag(v bool) *bool       { return &v }
func budgetFixture(t *testing.T) (*ProviderBudgetConfig, time.Time, map[string]any, func()) {
	t.Helper()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	uid := os.Geteuid()
	cfg := &ProviderBudgetConfig{SourceFile: filepath.Join(t.TempDir(), "checkpoint.json"), SourceHash: strings.Repeat("a", 64), SourceOwnerUID: &uid, MaxAge: "1m",
		Models: []ProviderModelBudget{{Provider: "github-copilot", Model: "vendor/native-model", AccountRef: "paccount_" + strings.Repeat("b", 64), Unit: "premium_requests", Period: "day", Limit: budgetString("2"), IncludedEntitlement: budgetString("2"), MaxOverage: budgetString("0"), Expensive: budgetFlag(true)}}}
	meter := map[string]any{"provider": "github-copilot", "model": "vendor/native-model", "accountRef": cfg.Models[0].AccountRef, "unit": "premium_requests", "period": "day", "from": "2026-10-02T00:00:00.000Z", "through": "2026-10-03T00:00:00.000Z", "observedAt": now.Format(transportAvailabilityTime), "reportedThrough": nil, "source": "github-billing", "state": "known", "reason": nil, "grossQuantity": "2", "includedQuantity": "2", "netQuantity": "0", "grossUsd": "0", "includedUsd": "0", "netUsd": "0", "pricePerUnitUsd": nil}
	state := map[string]any{"sourceHash": cfg.SourceHash, "snapshot": map[string]any{"schemaVersion": 2, "generatedAt": now.Format(transportAvailabilityTime), "account": map[string]any{}, "providers": map[string]any{"schemaVersion": 1, "generatedAt": now.Format(transportAvailabilityTime), "meters": []any{meter}, "prices": []any{}, "history": []any{}, "coverage": map[string]any{}}}}
	write := func() {
		b, _ := json.Marshal(state)
		if err := os.WriteFile(cfg.SourceFile, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	return cfg, now, state, write
}
func budgetSnapshot(s map[string]any) map[string]any { return s["snapshot"].(map[string]any) }
func budgetProviders(s map[string]any) map[string]any {
	return budgetSnapshot(s)["providers"].(map[string]any)
}
func budgetRow(s map[string]any) map[string]any {
	return budgetProviders(s)["meters"].([]any)[0].(map[string]any)
}
func budgetDecisionReason(t *testing.T, cfg *ProviderBudgetConfig, now time.Time) string {
	t.Helper()
	rows, err := buildProviderBudgetDecisions(cfg, now, now.Add(time.Minute).Format(transportAvailabilityTime))
	if err != nil || len(rows) != 1 || rows[0].Reason == nil || rows[0].Available {
		t.Fatal("decision did not remain closed")
	}
	return *rows[0].Reason
}

func TestProviderBudgetPolicy(t *testing.T) {
	for _, name := range []string{"valid", "invalid-path", "unclean-path", "root-path", "long-path", "control-path", "invalid-hash", "missing-owner", "invalid-owner", "owner-overflow", "invalid-age", "zero-age", "no-models", "too-many", "provider", "model", "long-model", "own-prefix", "double-prefix", "account", "missing-expensive", "period", "unit", "duplicate", "limit", "entitlement", "overage", "unknown", "unknown-with-limit"} {
		t.Run(name, func(t *testing.T) {
			cfg, _, _, _ := budgetFixture(t)
			switch name {
			case "invalid-path":
				cfg.SourceFile = "relative"
			case "unclean-path":
				cfg.SourceFile = "/synthetic/../source"
			case "root-path":
				cfg.SourceFile = "/"
			case "long-path":
				cfg.SourceFile = "/" + strings.Repeat("x", 4096)
			case "control-path":
				cfg.SourceFile = "/synthetic/\nsource"
			case "invalid-hash":
				cfg.SourceHash = "unknown"
			case "missing-owner":
				cfg.SourceOwnerUID = nil
			case "invalid-owner":
				v := -1
				cfg.SourceOwnerUID = &v
			case "owner-overflow":
				v := int(4294967295)
				cfg.SourceOwnerUID = &v
			case "invalid-age":
				cfg.MaxAge = "later"
			case "zero-age":
				cfg.MaxAge = "0s"
			case "no-models":
				cfg.Models = nil
			case "too-many":
				p := cfg.Models[0]
				cfg.Models = nil
				for i := 0; i < 1025; i++ {
					p.Model = fmt.Sprintf("native-%d", i)
					cfg.Models = append(cfg.Models, p)
				}
			case "provider":
				cfg.Models[0].Provider = "Bad/Provider"
			case "model":
				cfg.Models[0].Model = "bad model"
			case "long-model":
				cfg.Models[0].Model = strings.Repeat("x", 257)
			case "own-prefix":
				cfg.Models[0].Model = "github-copilot/vendor/native-model"
			case "double-prefix":
				cfg.Models[0].Model = "github-copilot/github-copilot/vendor/native-model"
			case "account":
				cfg.Models[0].AccountRef = "private-account"
			case "missing-expensive":
				cfg.Models[0].Expensive = nil
			case "period":
				cfg.Models[0].Period = "history"
			case "unit":
				cfg.Models[0].Unit = "tokens"
			case "duplicate":
				cfg.Models = append(cfg.Models, cfg.Models[0])
			case "limit":
				cfg.Models[0].Limit = budgetString("-1")
			case "entitlement":
				cfg.Models[0].IncludedEntitlement = budgetString("1.0")
			case "overage":
				cfg.Models[0].MaxOverage = budgetString("1000000000")
			case "unknown", "unknown-with-limit":
				cfg.Models[0].Unit = "unknown"
				cfg.Models[0].Limit = nil
				cfg.Models[0].IncludedEntitlement = nil
				cfg.Models[0].MaxOverage = nil
				if name == "unknown-with-limit" {
					cfg.Models[0].Limit = budgetString("0")
				}
			}
			expected := name == "valid" || name == "unknown"
			if cfg.valid() != expected {
				t.Fatal("policy validation disagreed with control")
			}
			raw, _ := json.Marshal(map[string]any{"provider_budgets": cfg})
			file := filepath.Join(t.TempDir(), "config.json")
			writeFixture(t, file, string(raw))
			_, err := loadConfig(file)
			if (err == nil) != expected {
				t.Fatal("runtime config disagreed")
			}
			_, _, problems := readMatrixConfigFile(file)
			if (len(problems) == 0) != expected {
				t.Fatal("diagnostic config disagreed")
			}
		})
	}
	for _, model := range []string{"flat", "vendor/native-model", "~vendor/native-model", "github-copilotish/native-model", "github-copilot", "vendor/github-copilot/native-model", "Vendor/native-model"} {
		cfg, _, _, _ := budgetFixture(t)
		cfg.Models[0].Model = model
		if !cfg.valid() {
			t.Fatal("native identity changed")
		}
	}
	for _, raw := range []string{`null`, `{}`, `{"a":1,"a":2}`, `{"a":1,"b":2,"PRIVATE":true}`} {
		if _, err := decodeProviderBudgetConfig([]byte(raw)); err == nil {
			t.Fatal("unknown shape accepted")
		}
	}
	cfg, _, _, _ := budgetFixture(t)
	raw, _ := json.Marshal(cfg)
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	for _, key := range []string{"limit", "included_entitlement", "max_overage", "expensive"} {
		saved := object["models"].([]any)[0].(map[string]any)[key]
		delete(object["models"].([]any)[0].(map[string]any), key)
		b, _ := json.Marshal(object)
		if _, e := decodeProviderBudgetConfig(b); e == nil {
			t.Fatal("missing policy field accepted")
		}
		object["models"].([]any)[0].(map[string]any)[key] = saved
	}
	if budgetJSONKeys([]byte(`{"a":1,"b":2,"c":3}`), "a b") || budgetJSONKeys([]byte(`{"a":1,"a":2,"b":3}`), "a b") || budgetJSONKeys([]byte(`{"a":1,"c":2}`), "a b") || budgetJSONKeys([]byte(`{"a":1}`), "a b") || !budgetJSONKeys([]byte(`{"a":null,"b":false}`), "a b") {
		t.Fatal("closed key inventory failed")
	}
}

func TestProviderBudgetQuantities(t *testing.T) {
	for _, v := range []string{"-1", "1.0", "01", "1e2", ".1", "0.0", "1000000000", "0.0000000001", "0.100000000", "private"} {
		if _, ok := budgetQuantity(&v); ok {
			t.Fatal("invalid quantity accepted")
		}
	}
	if _, ok := budgetQuantity(nil); ok {
		t.Fatal("unknown quantity became zero")
	}
	for v, want := range map[string]int64{"0": 0, "1": 1000000000, "0.000000001": 1, "999999999.999999999": 999999999999999999} {
		n, ok := budgetQuantity(&v)
		if !ok || n != want {
			t.Fatal("fixed point lost precision")
		}
	}
}

func TestProviderBudgetSourceAndDecision(t *testing.T) {
	cases := []string{"reached", "below", "zero-policy", "overage", "unknown", "cheap-flag", "aggregate", "provider", "policy-provider-mismatch", "model", "case", "account", "unit", "period", "day", "month", "duplicate", "source-hash", "schema", "provider-schema", "no-meters", "meter-cap", "missing-account", "missing-prices", "missing-history", "missing-coverage", "unknown-key", "duplicate-key", "partial-write", "missing-source", "source-owner", "source-mode", "source-special-mode", "source-directory", "source-symlink", "parent-symlink", "oversize", "snapshot-future", "snapshot-clock", "snapshot-comma", "provider-comma", "provider-clock", "provider-future", "provider-stale", "observed-future", "observed-stale", "source", "billing-provider", "state", "reason", "missing-observed", "observed-comma", "bad-clock", "span", "gross", "gross-usd", "quantity-split", "usd-split", "watermark", "watermark-comma", "price", "price-type", "missing-price", "usd-unit", "credits", "unknown-unit"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, now, s, write := budgetFixture(t)
			r := budgetRow(s)
			switch name {
			case "below", "cheap-flag":
				r["grossQuantity"] = "1"
				r["includedQuantity"] = "1"
				if name == "cheap-flag" {
					cfg.Models[0].Expensive = budgetFlag(false)
				}
			case "zero-policy":
				cfg.Models[0].Limit = budgetString("0")
				r["state"] = "unknown"
			case "overage":
				cfg.Models[0].Limit = budgetString("3")
				r["includedQuantity"] = "1"
				r["netQuantity"] = "1"
			case "unknown":
				r["state"] = "unknown"
				r["reason"] = "no-plan-read-credential"
				r["observedAt"] = nil
				for _, key := range []string{"grossQuantity", "includedQuantity", "netQuantity", "grossUsd", "includedUsd", "netUsd"} {
					r[key] = nil
				}
			case "aggregate":
				r["model"] = nil
			case "provider":
				r["provider"] = "foreign"
			case "policy-provider-mismatch":
				cfg.Models[0].Provider = "fixture-provider"
			case "model":
				r["model"] = "alias"
			case "case":
				r["model"] = "Vendor/native-model"
			case "account":
				r["accountRef"] = "paccount_" + strings.Repeat("c", 64)
			case "unit":
				r["unit"] = "ai_credits"
			case "period":
				r["period"] = "month"
			case "day":
				r["from"] = "2026-10-01T00:00:00.000Z"
			case "month":
				cfg.Models[0].Period = "month"
				r["period"] = "month"
				r["from"] = "2026-10-01T00:00:00.000Z"
				r["through"] = "2026-11-01T00:00:00.000Z"
			case "duplicate":
				budgetProviders(s)["meters"] = []any{r, r}
			case "source-hash":
				s["sourceHash"] = strings.Repeat("d", 64)
			case "schema":
				budgetSnapshot(s)["schemaVersion"] = 1
			case "provider-schema":
				budgetProviders(s)["schemaVersion"] = 2
			case "no-meters":
				budgetProviders(s)["meters"] = nil
			case "meter-cap":
				budgetProviders(s)["meters"] = make([]any, 1025)
			case "missing-account":
				delete(budgetSnapshot(s), "account")
			case "missing-prices", "missing-history", "missing-coverage":
				delete(budgetProviders(s), strings.TrimPrefix(name, "missing-"))
			case "unknown-key":
				s["PRIVATE-CANARY"] = "private-value"
			case "snapshot-future":
				budgetSnapshot(s)["generatedAt"] = now.Add(time.Second).Format(transportAvailabilityTime)
			case "snapshot-clock":
				budgetSnapshot(s)["generatedAt"] = "2026-10-02T12:00:00Z"
			case "provider-clock":
				budgetProviders(s)["generatedAt"] = "2026-10-02T12:00:00Z"
			case "snapshot-comma":
				budgetSnapshot(s)["generatedAt"] = "2026-10-02T12:00:00,000Z"
			case "provider-comma":
				budgetProviders(s)["generatedAt"] = "2026-10-02T12:00:00,000Z"
			case "observed-comma":
				r["observedAt"] = "2026-10-02T12:00:00,000Z"
			case "provider-future":
				budgetProviders(s)["generatedAt"] = now.Add(time.Second).Format(transportAvailabilityTime)
			case "provider-stale":
				budgetProviders(s)["generatedAt"] = now.Add(-time.Minute).Format(transportAvailabilityTime)
			case "observed-future":
				r["observedAt"] = now.Add(time.Second).Format(transportAvailabilityTime)
			case "observed-stale":
				r["observedAt"] = now.Add(-time.Minute).Format(transportAvailabilityTime)
			case "source":
				r["source"] = "opencode-history"
			case "billing-provider":
				cfg.Models[0].Provider = "fixture-provider"
				r["provider"] = "fixture-provider"
			case "state":
				r["state"] = "partial"
			case "reason":
				r["reason"] = "shape-mismatch"
			case "missing-observed":
				r["observedAt"] = nil
			case "bad-clock":
				r["observedAt"] = "2026-10-02T24:00:00.000Z"
			case "span":
				r["through"] = "2026-10-04T00:00:00.000Z"
			case "gross":
				r["grossQuantity"] = "-1"
			case "gross-usd":
				r["grossUsd"] = "-1"
			case "quantity-split":
				r["includedQuantity"] = "0"
			case "usd-split":
				r["grossUsd"] = "1"
			case "watermark":
				r["reportedThrough"] = now.Add(time.Second).Format(transportAvailabilityTime)
			case "watermark-comma":
				r["reportedThrough"] = "2026-10-02T12:00:00,000Z"
			case "credits", "usd-unit":
				cfg.Models[0].Unit = "usd"
				if name == "credits" {
					cfg.Models[0].Unit = "ai_credits"
				}
				r["unit"] = cfg.Models[0].Unit
			case "price":
				r["pricePerUnitUsd"] = "-1"
			case "price-type":
				r["pricePerUnitUsd"] = true
			case "missing-price":
				delete(r, "pricePerUnitUsd")
			case "unknown-unit":
				cfg.Models[0].Unit = "unknown"
				cfg.Models[0].Limit = nil
				cfg.Models[0].IncludedEntitlement = nil
				cfg.Models[0].MaxOverage = nil
			}
			write()
			switch name {
			case "duplicate-key":
				b, _ := os.ReadFile(cfg.SourceFile)
				writeFixture(t, cfg.SourceFile, strings.Replace(string(b), `"sourceHash":`, `"sourceHash":"duplicated","sourceHash":`, 1))
			case "partial-write":
				writeFixture(t, cfg.SourceFile, `{"sourceHash":`)
			case "missing-source":
				_ = os.Remove(cfg.SourceFile)
			case "source-owner":
				uid := os.Geteuid() + 1
				cfg.SourceOwnerUID = &uid
			case "source-mode":
				_ = os.Chmod(cfg.SourceFile, 0644)
			case "source-special-mode":
				_ = os.Chmod(cfg.SourceFile, os.ModeSetuid|0600)
			case "source-directory":
				_ = os.Remove(cfg.SourceFile)
				_ = os.Mkdir(cfg.SourceFile, 0600)
			case "source-symlink":
				target := cfg.SourceFile + ".actual"
				_ = os.Rename(cfg.SourceFile, target)
				_ = os.Symlink(target, cfg.SourceFile)
			case "parent-symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				_ = os.Symlink(filepath.Dir(cfg.SourceFile), alias)
				cfg.SourceFile = filepath.Join(alias, filepath.Base(cfg.SourceFile))
			case "oversize":
				writeFixture(t, cfg.SourceFile, strings.Repeat(" ", 4*1024*1024+1))
			}
			want := "budget-unavailable"
			if name == "reached" || name == "zero-policy" || name == "overage" || name == "month" || name == "credits" {
				want = "budget-reached"
			}
			if got := budgetDecisionReason(t, cfg, now); got != want {
				t.Fatalf("decision %s, want %s", got, want)
			}
			c := &Coord{cfg: &Config{ProviderBudgets: cfg}}
			o := Overrides{Model: cfg.Models[0].Provider + "/" + cfg.Models[0].Model}
			if c.providerBudgetLaunchReason("opencode", o, now) != "" {
				t.Fatal("legacy budget evidence vetoed launch")
			}
		})
	}
}

func TestProviderBudgetScopeAndPublicRows(t *testing.T) {
	cfg, now, _, _ := budgetFixture(t)
	c := &Coord{cfg: &Config{ProviderBudgets: cfg}}
	for _, agent := range []string{"claude", "codex", "agy", "codex-run"} {
		if c.providerBudgetLaunchReason(agent, Overrides{Model: "github-copilot/vendor/native-model"}, now) != "" {
			t.Fatal("native/legacy quota changed")
		}
	}
	if c.providerBudgetLaunchReason("opencode-run", Overrides{Model: "github-copilot/vendor/native-model"}, now) != "" {
		t.Fatal("legacy OpenCode alias bypassed paid policy")
	}
	if c.providerBudgetLaunchReason("opencode", Overrides{Model: "fixture-free/cheap-model"}, now) != "" {
		t.Fatal("unrelated cheap route inherited expensive policy")
	}
	if c.providerBudgetLaunchReason("opencode", Overrides{Model: "github-copilot/unlisted"}, now) != "" {
		t.Fatal("covered provider bypassed missing model policy")
	}
	if c.providerBudgetLaunchReason("opencode", Overrides{Model: "invalid"}, now) != "" {
		t.Fatal("unresolved identity bypassed policy")
	}
	cfg.Models[0].Limit = budgetString("3")
	for _, model := range []string{"~vendor/native-model", "github-copilotish/native-model", "Vendor/native-model"} {
		p := cfg.Models[0]
		p.Model = model
		cfg.Models = append(cfg.Models, p)
	}
	p := cfg.Models[0]
	p.Provider = "fixture-provider"
	cfg.Models = append(cfg.Models, p)
	rows, err := buildProviderBudgetDecisions(cfg, now, now.Add(time.Minute).Format(transportAvailabilityTime))
	if err != nil || len(rows) != 5 {
		t.Fatal("native identities lost")
	}
	for i, row := range rows {
		var obj map[string]any
		b, _ := json.Marshal(row)
		_ = json.Unmarshal(b, &obj)
		if len(obj) != 6 || row.Available || row.Reason == nil || *row.Reason != "budget-unavailable" || row.ObservedAt != now.Format(transportAvailabilityTime) {
			t.Fatal("public shape or identity changed")
		}
		for _, key := range []string{"provider", "model", "observedAt", "validUntil", "available", "reason"} {
			if _, ok := obj[key]; !ok {
				t.Fatal("public field inventory changed")
			}
		}
		if i > 0 && (rows[i-1].Provider > row.Provider || (rows[i-1].Provider == row.Provider && rows[i-1].Model > row.Model)) {
			t.Fatal("ordering changed")
		}
		if strings.Contains(string(b), "paccount_") || strings.Contains(string(b), "source_file") {
			t.Fatal("private policy leaked")
		}
	}
	if output := os.Getenv("ORCHID_PROVIDER_BUDGET_PROOF"); output != "" {
		s := buildTransportAvailability(map[string]int{"opencode": 1}, nil, now, time.Minute, 92, time.Minute, nil, []openCodeProviderPool{{"github-copilot", 1, 0}})
		s.ProviderBudgets = rows
		b, _ := json.Marshal(s)
		if err := os.WriteFile(output, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Models[0].Model = "github-copilot/duplicate"
	if rows, err := buildProviderBudgetDecisions(cfg, now, now.Add(time.Minute).Format(transportAvailabilityTime)); err == nil || rows != nil {
		t.Fatal("invalid policy published")
	}
	if c.providerBudgetLaunchReason("opencode", Overrides{Model: "fixture-free/cheap-model"}, now) != "" {
		t.Fatal("invalid policy opened admission")
	}
	if rows, err := buildProviderBudgetDecisions(nil, now, ""); err != nil || rows != nil {
		t.Fatal("legacy snapshot gained a new field")
	}
	c.cfg.ProviderBudgets = nil
	if c.providerBudgetLaunchReason("opencode", Overrides{}, now) != "" {
		t.Fatal("disabled budget feature changed legacy")
	}
}

func TestProviderBudgetTickAndRefusals(t *testing.T) {
	cfg, now, _, _ := budgetFixture(t)
	root := privateTestRoot(t)
	c := &Coord{cfg: &Config{ProviderBudgets: cfg, Matrix: MatrixConfig{ReceiptRoot: root}, OpenCode: OpenCodeConfig{Providers: map[string]OpenCodeProvider{"github-copilot": {0}}}}, st: &State{Jobs: map[int]*Job{}}}
	c.gov.q = map[string]quota{}
	pools := openCodeProviderPools(c.cfg.OpenCode, c.st.Jobs)
	c.publishTransportAvailability(map[string]int{"codex": 1, "opencode": 0}, now, pools)
	var s transportAvailabilitySnapshot
	if err := readPrivateActionJSON(transportAvailabilityPath(root), &s); err != nil || len(s.ProviderBudgets) != 0 {
		t.Fatal("tick emitted obsolete budget availability decisions")
	}
	if !reflect.DeepEqual(s.OpenCodeProviderPools, pools) || s.Transports[3].Available {
		t.Fatal("clocks/capacity/copilot disable changed")
	}
	for _, reason := range []string{"budget-reached", "budget-unavailable"} {
		r := refusalFor(matrixReason(reason))
		if r.Status != "refused" || r.ReasonCode != reason || !validMatrixRefusal(r) {
			t.Fatal("closed diagnostics lost budget reason")
		}
		is := Issue{ID: "fixture-issue", Number: 7}
		if err := publishLaunchState(root, nil, "example/inbox", 7, is, "refused", reason); err != nil {
			t.Fatal("budget refusal not reader compatible")
		}
	}
	cfg.Models[0].Model = "github-copilot/duplicate"
	c.publishTransportAvailability(nil, now, pools)
	if _, e := os.Stat(transportAvailabilityPath(root)); e != nil {
		t.Fatal("invalid unused budget evidence vetoed physical capacity snapshot")
	}
}

func TestProviderBudgetBeforeSpawnEffects(t *testing.T) {
	cfg, now, _, _ := budgetFixture(t)
	_ = now
	c := &Coord{cfg: &Config{ProviderBudgets: cfg}}
	err := c.spawn(context.Background(), 7, Issue{}, Host{}, "opencode", Overrides{Model: "github-copilot/vendor/native-model"}, nil)
	if err == nil || refusalFor(err).ReasonCode == "budget-unavailable" || refusalFor(err).ReasonCode == "budget-reached" {
		t.Fatal("quota budget veto remains before physical launch validation")
	}
}

func TestProviderBudgetMatrixBeforeEffects(t *testing.T) {
	for _, mode := range []string{"normal", "retry", "preflight", "reached", "unlisted"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _, _, _ := budgetFixture(t)
			if mode == "reached" {
				cfg.Models[0].Limit = budgetString("0")
			}
			root := privateTestRoot(t)
			c := &Coord{cfg: &Config{Inbox: "example/inbox", ProviderBudgets: cfg, Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: strings.Repeat("b", 40), TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)}}, OpenCode: OpenCodeConfig{Providers: map[string]OpenCodeProvider{"github-copilot": {1}}}}}
			c.gov.q = map[string]quota{}
			is := Issue{ID: "fixture-node", Number: 7, Title: "Synthetic", Body: "tier: feature\nrole: implementation"}
			route := syntheticRoute()
			route.Transport = "opencode"
			route.Model = "github-copilot/vendor/native-model"
			route.Effort = "provider_default"
			if mode == "unlisted" {
				route.Model = "github-copilot/other-model"
			}
			effects := 0
			refused := matrixRefusal{}
			d := matrixAttemptDeps{report: func(r matrixRefusal) { refused = r }, read: func(context.Context, string, string, string) (string, error) {
				return "| `routing` | matrix `implementation` row |", nil
			}, resolve: func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) { return route, nil }, host: func(Target, string) (Host, bool) { effects++; return Host{Name: "fixture-node"}, true }, persist: func(string, string, string, matrixReceipt, any, ...*receiptOwner) (*durableMatrixReceipt, error) {
				effects++
				return nil, errMatrix
			}, launch: func(context.Context, int, Issue, Host, string, Overrides, *durableMatrixReceipt) error {
				effects++
				return nil
			}}
			if mode == "retry" {
				d.retry = &retryExpectation{}
			}
			if mode == "preflight" {
				d.preflight = true
			}
			_, launched := c.matrixAttempt(context.Background(), 7, is, Target{Repo: "example/project"}, map[string]int{"opencode": 1, "opencode:github-copilot": 1}, d)
			if effects < 1 || refused.ReasonCode == "budget-unavailable" || refused.ReasonCode == "budget-reached" {
				t.Fatalf("budget veto retained: launched=%v effects=%d reason=%s", launched, effects, refused.ReasonCode)
			}

		})
	}
}

func TestProviderBudgetReadRaces(t *testing.T) {
	for _, mode := range []string{"symlink-before-open", "replace-before-open", "replace-after-read", "size-after-read", "mtime-after-read", "mode-after-read", "grow-during-read", "bounded-output", "size-before-read"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _, _, _ := budgetFixture(t)
			original, _ := os.ReadFile(cfg.SourceFile)
			info, _ := os.Stat(cfg.SourceFile)
			unsafeOpen := false
			reads := 0
			readBytes := 0
			open := func(path string, flags int, nativeMode uint32) (int, error) {
				switch mode {
				case "symlink-before-open":
					target := path + ".actual"
					_ = os.Rename(path, target)
					_ = os.Symlink(target, path)
				case "replace-before-open":
					_ = os.Rename(path, path+".old")
					writeFixture(t, path, string(original))
				}
				fd, e := syscall.Open(path, flags, nativeMode)
				if mode == "symlink-before-open" && fd >= 0 {
					unsafeOpen = true
				}
				return fd, e
			}
			read := func(reader io.Reader) ([]byte, error) {
				reads++
				if mode == "grow-during-read" {
					writeFixture(t, cfg.SourceFile, string(original)+strings.Repeat(" ", 4*1024*1024+1))
				}
				raw, e := io.ReadAll(reader)
				readBytes = len(raw)
				switch mode {
				case "replace-after-read":
					_ = os.Rename(cfg.SourceFile, cfg.SourceFile+".old")
					writeFixture(t, cfg.SourceFile, string(original))
				case "size-after-read":
					writeFixture(t, cfg.SourceFile, string(original)+" ")
					_ = os.Chtimes(cfg.SourceFile, info.ModTime(), info.ModTime())
				case "mtime-after-read":
					_ = os.Chtimes(cfg.SourceFile, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second))
				case "mode-after-read":
					_ = os.Chmod(cfg.SourceFile, 0644)
				case "bounded-output":
					raw = append([]byte(strings.Repeat(" ", 4*1024*1024)), original...)
				case "size-before-read":
					raw = original
				}
				return raw, e
			}
			if mode == "size-before-read" {
				writeFixture(t, cfg.SourceFile, strings.Repeat(" ", 4*1024*1024+1))
			}
			_, err := readBudgetCheckpointIO(cfg, open, read)
			if mode == "replace-before-open" { // A valid replacement before open is a fresh bound read, not a cached pathname identity.
				if err != nil {
					t.Fatal("valid replacement refused")
				}
			} else if err == nil {
				t.Fatal("unsafe source read accepted")
			}
			if readBytes > 4*1024*1024+1 {
				t.Fatal("private source read exceeded bound")
			}
			if unsafeOpen {
				t.Fatal("symlink race opened its target")
			}
			if mode == "size-before-read" && reads != 0 {
				t.Fatal("oversized source was read")
			}
		})
	}
	cfg, _, _, _ := budgetFixture(t)
	if _, err := readBudgetCheckpointIO(cfg, syscall.Open, func(io.Reader) ([]byte, error) {
		b, _ := os.ReadFile(cfg.SourceFile)
		return b, errors.New("PRIVATE-CANARY")
	}); err == nil || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("read error escaped")
	}
}

func TestProviderBudgetFIFOReadIsBounded(t *testing.T) {
	cfg, _, _, _ := budgetFixture(t)
	_ = os.Remove(cfg.SourceFile)
	if err := syscall.Mkfifo(cfg.SourceFile, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readBudgetCheckpoint(cfg); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted as checkpoint")
		}
	case <-time.After(time.Second):
		// Release a mutant's blocking reader before asserting; never count a panic or stuck test as RED.
		fd, err := syscall.Open(cfg.SourceFile, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = syscall.Close(fd)
		}
		<-done
		t.Fatal("private source open blocked on FIFO")
	}
}

// Loader/source envelope controls exercise their guards independently of later
// per-meter refusals, so redundant protection does not hide a surviving mutant.
func TestProviderBudgetClosedEnvelopes(t *testing.T) {
	cfg, _, _, _ := budgetFixture(t)
	block, _ := json.Marshal(cfg)
	for _, raw := range []string{
		`{"provider_budgets":` + string(block) + `,"provider_budgets":null}`,
		`{"provider_budgets":null}`,
		`{"provider_budgets":null,"provider_budgets":` + string(block) + `}`,
	} {
		file := filepath.Join(t.TempDir(), "config.json")
		writeFixture(t, file, raw)
		if _, err := loadConfig(file); err == nil {
			t.Fatal("ambiguous budget config loaded")
		}
		if _, _, problems := readMatrixConfigFile(file); len(problems) == 0 {
			t.Fatal("ambiguous config diagnostics accepted")
		}
	}
	for _, mode := range []string{"too-many-meters", "no-meters", "missing-account", "missing-prices", "missing-history", "missing-coverage"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _, state, write := budgetFixture(t)
			switch mode {
			case "too-many-meters":
				rows := make([]any, 1025)
				for i := range rows {
					rows[i] = budgetRow(state)
				}
				budgetProviders(state)["meters"] = rows
			case "no-meters":
				budgetProviders(state)["meters"] = nil
			case "missing-account":
				delete(budgetSnapshot(state), "account")
			default:
				delete(budgetProviders(state), strings.TrimPrefix(mode, "missing-"))
			}
			write()
			if _, err := readBudgetCheckpoint(cfg); err == nil {
				t.Fatal("incomplete source envelope accepted")
			}
		})
	}
}

func TestProviderBudgetInstant(t *testing.T) {
	if _, ok := budgetInstant("2026-10-02T12:00:00.000Z"); !ok {
		t.Fatal("canonical clock rejected")
	}
	for _, clock := range []string{"2026-10-02T12:00:00,000Z", "2026-10-02T24:00:00.000Z", "2026-02-31T12:00:00.000Z"} {
		if _, ok := budgetInstant(clock); ok {
			t.Fatal("noncanonical clock accepted")
		}
	}
}

func TestProviderBudgetPrivateFileBeforeRead(t *testing.T) {
	for _, mode := range []string{"owner", "permissions", "special", "directory"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _, _, _ := budgetFixture(t)
			switch mode {
			case "owner":
				v := os.Geteuid() + 1
				cfg.SourceOwnerUID = &v
			case "permissions":
				_ = os.Chmod(cfg.SourceFile, 0644)
			case "special":
				_ = os.Chmod(cfg.SourceFile, os.ModeSetuid|0600)
			case "directory":
				_ = os.Remove(cfg.SourceFile)
				_ = os.Mkdir(cfg.SourceFile, 0600)
			}
			reads := 0
			_, err := readBudgetCheckpointIO(cfg, syscall.Open, func(r io.Reader) ([]byte, error) { reads++; return io.ReadAll(r) })
			if err == nil || reads != 0 {
				t.Fatal("source failed private-file check before read")
			}
		})
	}
}

func TestProviderBudgetPolicyClosedJSON(t *testing.T) {
	cfg, _, _, _ := budgetFixture(t)
	b, _ := json.Marshal(cfg)
	for _, raw := range []string{
		strings.TrimSuffix(string(b), "}") + `,"PRIVATE":true}`,
		strings.Replace(string(b), `"source_hash":`, `"source_hash":"duplicate","source_hash":`, 1),
	} {
		if _, err := decodeProviderBudgetConfig([]byte(raw)); err == nil {
			t.Fatal("unclosed policy JSON accepted")
		}
	}
}
