package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Policies are operator data. This first producer has no verified full-run
// enforcement/reservation adapter: neither a meter nor an estimate grants a run.
type ProviderBudgetConfig struct {
	SourceFile     string                `json:"source_file"`
	SourceHash     string                `json:"source_hash"`
	SourceOwnerUID *int                  `json:"source_owner_uid"`
	MaxAge         string                `json:"max_age"`
	Models         []ProviderModelBudget `json:"models"`
}

type ProviderModelBudget struct {
	Provider            string  `json:"provider"`
	Model               string  `json:"model"`
	AccountRef          string  `json:"account_ref"`
	Unit                string  `json:"unit"`
	Period              string  `json:"period"`
	Limit               *string `json:"limit"`
	IncludedEntitlement *string `json:"included_entitlement"`
	MaxOverage          *string `json:"max_overage"`
	Expensive           *bool   `json:"expensive"`
}

// This is the entire public shape. No policy, amount, account or path is copied.
type providerBudgetDecision struct {
	Provider   string  `json:"provider"`
	Model      string  `json:"model"`
	ObservedAt string  `json:"observedAt"`
	ValidUntil string  `json:"validUntil"`
	Available  bool    `json:"available"`
	Reason     *string `json:"reason"`
}

var budgetModelSyntax = regexp.MustCompile(`^~?[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
var budgetAccountSyntax = regexp.MustCompile(`^paccount_[a-f0-9]{64}$`)
var budgetDecimalSyntax = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})(\.[0-9]{0,8}[1-9])?$`)

func budgetQuantity(value *string) (int64, bool) {
	if value == nil || !budgetDecimalSyntax.MatchString(*value) {
		return 0, false
	}
	parts := strings.Split(*value, ".")
	whole, _ := strconv.ParseInt(parts[0], 10, 64)
	fraction := int64(0)
	if len(parts) == 2 {
		fraction, _ = strconv.ParseInt(parts[1]+strings.Repeat("0", 9-len(parts[1])), 10, 64)
	}
	return whole*1_000_000_000 + fraction, true
}

func budgetPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && len(path) <= 4096 && cleanText(path)
}

func (cfg *ProviderBudgetConfig) valid() bool {
	if cfg == nil {
		return true
	}
	age, err := time.ParseDuration(cfg.MaxAge)
	if !budgetPath(cfg.SourceFile) || !digestPattern.MatchString(cfg.SourceHash) || cfg.SourceOwnerUID == nil ||
		uint64(*cfg.SourceOwnerUID) >= 4294967295 || err != nil || age <= 0 ||
		len(cfg.Models) == 0 || len(cfg.Models) > 1024 {
		return false
	}
	seen := map[string]bool{}
	for _, p := range cfg.Models {
		if !openCodeProviderID.MatchString(p.Provider) || len(p.Model) > 256 || !budgetModelSyntax.MatchString(p.Model) ||
			strings.HasPrefix(p.Model, p.Provider+"/") || !budgetAccountSyntax.MatchString(p.AccountRef) || p.Expensive == nil ||
			(p.Period != "day" && p.Period != "month") {
			return false
		}
		if p.Unit != "premium_requests" && p.Unit != "ai_credits" && p.Unit != "usd" && p.Unit != "unknown" {
			return false
		}
		key := p.Provider + "\x00" + p.Model
		if seen[key] {
			return false
		}
		seen[key] = true
		if p.Unit == "unknown" {
			if p.Limit != nil || p.IncludedEntitlement != nil || p.MaxOverage != nil {
				return false
			}
		} else {
			if _, ok := budgetQuantity(p.Limit); !ok {
				return false
			}
			for _, value := range []*string{p.IncludedEntitlement, p.MaxOverage} {
				if value != nil {
					if _, ok := budgetQuantity(value); !ok {
						return false
					}
				}
			}
		}
	}
	return true
}

func decodeProviderBudgetConfig(raw []byte) (*ProviderBudgetConfig, error) {
	var cfg ProviderBudgetConfig
	if strictJSON(raw, &cfg) != nil || !cfg.valid() {
		return nil, errMatrix
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	var models []json.RawMessage
	_ = json.Unmarshal(fields["models"], &models)
	for _, model := range models {
		if !budgetJSONKeys(model, "provider model account_ref unit period limit included_entitlement max_overage expensive") {
			return nil, errMatrix
		}
	}
	return &cfg, nil
}

func budgetJSONKeys(raw []byte, names string) bool {
	var fields map[string]json.RawMessage
	keys := strings.Fields(names)
	if strictJSON(raw, &fields) != nil || len(fields) != len(keys) {
		return false
	}
	for _, name := range keys {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return true
}

type budgetMeter struct {
	Provider         string  `json:"provider"`
	Model            *string `json:"model"`
	AccountRef       string  `json:"accountRef"`
	Unit             string  `json:"unit"`
	Period           string  `json:"period"`
	From             string  `json:"from"`
	Through          string  `json:"through"`
	ObservedAt       *string `json:"observedAt"`
	ReportedThrough  *string `json:"reportedThrough"`
	Source           string  `json:"source"`
	State            string  `json:"state"`
	Reason           *string `json:"reason"`
	GrossQuantity    *string `json:"grossQuantity"`
	IncludedQuantity *string `json:"includedQuantity"`
	NetQuantity      *string `json:"netQuantity"`
	GrossUSD         *string `json:"grossUsd"`
	IncludedUSD      *string `json:"includedUsd"`
	NetUSD           *string `json:"netUsd"`
	PricePerUnitUSD  *string `json:"pricePerUnitUsd"`
}

type budgetCheckpoint struct {
	SourceHash string `json:"sourceHash"`
	Snapshot   struct {
		SchemaVersion int             `json:"schemaVersion"`
		GeneratedAt   string          `json:"generatedAt"`
		Account       json.RawMessage `json:"account"`
		Providers     struct {
			SchemaVersion int               `json:"schemaVersion"`
			GeneratedAt   string            `json:"generatedAt"`
			Meters        []json.RawMessage `json:"meters"`
			Prices        json.RawMessage   `json:"prices"`
			History       json.RawMessage   `json:"history"`
			Coverage      json.RawMessage   `json:"coverage"`
		} `json:"providers"`
	} `json:"snapshot"`
}

// Read the existing collector checkpoint, never credentials or a provider API.
// The unused native account/pricing/history payloads cannot authorize paid work.
func readBudgetCheckpoint(cfg *ProviderBudgetConfig) (budgetCheckpoint, error) {
	return readBudgetCheckpointIO(cfg, syscall.Open, io.ReadAll)
}

// IO dependencies exist only for deterministic read/race controls, never config.
func readBudgetCheckpointIO(cfg *ProviderBudgetConfig, open func(string, int, uint32) (int, error), read func(io.Reader) ([]byte, error)) (budgetCheckpoint, error) {
	var result budgetCheckpoint
	resolved, err := filepath.EvalSymlinks(cfg.SourceFile)
	if err != nil || resolved != cfg.SourceFile {
		return result, errMatrix
	}
	fd, err := open(cfg.SourceFile, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return result, errMatrix
	}
	file := os.NewFile(uintptr(fd), "budget-source")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result, errMatrix
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != *cfg.SourceOwnerUID || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() > 4*1024*1024 {
		return result, errMatrix
	}
	raw, err := read(io.LimitReader(file, 4*1024*1024+1))
	after, statErr := file.Stat()
	pathInfo, pathErr := os.Lstat(cfg.SourceFile)
	if err != nil || len(raw) > 4*1024*1024 || statErr != nil || pathErr != nil || !os.SameFile(info, pathInfo) ||
		info.Size() != after.Size() || info.Mode() != after.Mode() || !info.ModTime().Equal(after.ModTime()) || strictJSON(raw, &result) != nil {
		return budgetCheckpoint{}, errMatrix
	}
	p := result.Snapshot.Providers
	if result.SourceHash != cfg.SourceHash || result.Snapshot.SchemaVersion != 2 || p.SchemaVersion != 1 ||
		len(p.Meters) > 1024 || p.Meters == nil || len(result.Snapshot.Account) == 0 || len(p.Prices) == 0 || len(p.History) == 0 || len(p.Coverage) == 0 {
		return budgetCheckpoint{}, errMatrix
	}
	return result, nil
}

func budgetInstant(text string) (time.Time, bool) {
	at, err := time.Parse(transportAvailabilityTime, text)
	return at, err == nil && at.UTC().Format(transportAvailabilityTime) == text
}

// Only a fresh exact billing row can prove reported exhaustion. Included
// discount is consumption, not remaining entitlement; fetch time is not settled.
func reportedBudgetReached(p ProviderModelBudget, cfg *ProviderBudgetConfig, doc budgetCheckpoint, now time.Time) bool {
	generated, ok := budgetInstant(doc.Snapshot.GeneratedAt)
	providerAt, providerOK := budgetInstant(doc.Snapshot.Providers.GeneratedAt)
	age, _ := time.ParseDuration(cfg.MaxAge)
	if !ok || !providerOK || generated.After(now) || providerAt.After(generated) {
		return false
	}
	from := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	through := from.AddDate(0, 1, 0)
	if p.Period == "day" {
		from = time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
		through = from.AddDate(0, 0, 1)
	}
	matches := 0
	reached := false
	for _, raw := range doc.Snapshot.Providers.Meters {
		var r budgetMeter
		if !budgetJSONKeys(raw, "provider model accountRef unit period from through observedAt reportedThrough source state reason grossQuantity includedQuantity netQuantity grossUsd includedUsd netUsd pricePerUnitUsd") || strictJSON(raw, &r) != nil {
			return false
		}
		if r.Provider != p.Provider || r.Model == nil || *r.Model != p.Model || r.AccountRef != p.AccountRef || r.Unit != p.Unit || r.Period != p.Period {
			continue
		}
		matches++
		observed := time.Time{}
		if r.ObservedAt != nil {
			observed, ok = budgetInstant(*r.ObservedAt)
		} else {
			ok = false
		}
		if r.Source != "github-billing" || r.Provider != "github-copilot" || (r.Unit != "premium_requests" && r.Unit != "ai_credits") ||
			r.State != "known" || r.Reason != nil || !ok || observed.After(providerAt) || now.Sub(observed) >= age ||
			r.From != from.Format(transportAvailabilityTime) || r.Through != through.Format(transportAvailabilityTime) {
			return false
		}
		var quantities []int64
		for _, v := range []*string{r.GrossQuantity, r.IncludedQuantity, r.NetQuantity, r.GrossUSD, r.IncludedUSD, r.NetUSD} {
			n, valid := budgetQuantity(v)
			if !valid {
				return false
			}
			quantities = append(quantities, n)
		}
		if quantities[0] != quantities[1]+quantities[2] || quantities[3] != quantities[4]+quantities[5] {
			return false
		}
		if r.ReportedThrough != nil {
			watermark, valid := budgetInstant(*r.ReportedThrough)
			if !valid || watermark.After(observed) {
				return false
			}
		}
		if r.PricePerUnitUSD != nil {
			if _, valid := budgetQuantity(r.PricePerUnitUSD); !valid {
				return false
			}
		}
		limit, _ := budgetQuantity(p.Limit)
		maxOverage, hasOverage := budgetQuantity(p.MaxOverage)
		reached = quantities[0] >= limit || (hasOverage && quantities[2] > maxOverage)
	}
	return matches == 1 && reached
}

func evaluateProviderBudget(p ProviderModelBudget, cfg *ProviderBudgetConfig, doc budgetCheckpoint, sourceOK bool, now time.Time) string {
	limit, known := budgetQuantity(p.Limit)
	if known && limit == 0 {
		return "budget-reached"
	}
	if sourceOK && reportedBudgetReached(p, cfg, doc, now) {
		return "budget-reached"
	}
	// There is intentionally no paid allow branch. Measurement alone is not an
	// enforceable full-run maximum, durable reservation or settlement adapter.
	return "budget-unavailable"
}

func buildProviderBudgetDecisions(cfg *ProviderBudgetConfig, now time.Time, validUntil string) ([]providerBudgetDecision, error) {
	if cfg == nil {
		return nil, nil
	}
	if !cfg.valid() {
		return nil, errMatrix
	}
	doc, err := readBudgetCheckpoint(cfg)
	rows := make([]providerBudgetDecision, 0, len(cfg.Models))
	for _, p := range cfg.Models {
		reason := evaluateProviderBudget(p, cfg, doc, err == nil, now)
		rows = append(rows, providerBudgetDecision{p.Provider, p.Model, now.UTC().Format(transportAvailabilityTime), validUntil, false, &reason})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Provider < rows[j].Provider || (rows[i].Provider == rows[j].Provider && rows[i].Model < rows[j].Model)
	})
	return rows, nil
}

// Legacy billing policy remains parseable, but quota/budget observations cannot veto a route.
func (c *Coord) providerBudgetLaunchReason(agent string, o Overrides, now time.Time) string {
	return ""
}
