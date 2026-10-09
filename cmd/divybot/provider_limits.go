package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const providerLimitMaxRows = 1024

// Each scope reserves one blocking/success row and one informational rate row.
const providerLimitMaxScopes = providerLimitMaxRows / 2
const providerLimitMaxBytes = 4 * 1024 * 1024
const providerLimitWarningPercent = 90
const providerLimitKeyEndpoint = "https://openrouter.ai/api/v1/key"

var limitAccountPattern = regexp.MustCompile(`^(?:aref:v1:(?:claude|codex):[A-Za-z0-9_-]{43}|paccount_[a-f0-9]{64})$`)
var limitAliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var limitEnvPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
var limitUnsafePattern = regexp.MustCompile(`(?i)(?:github_pat_|gh[pousr]_|sk-)[A-Za-z0-9_-]{12,}|\bBearer\s+\S+|\.ts\.net\b|\b(?:\d{1,3}\.){3}\d{1,3}\b|(?:/home/|/Users/|/root/|/tmp/|~/)`)
var limitProviders = []string{"claude", "codex", "opencode-go", "ollama-cloud", "github-copilot", "agy"}

// Operator aliases and explicitly proven credential/account bindings; never derive identity from a secret.
type ProviderLimitsConfig struct {
	PrivateRoot    string                 `json:"private_root"`
	SnapshotFile   string                 `json:"snapshot_file"`
	Accounts       []providerLimitAccount `json:"accounts"`
	OpenRouterKeys []providerLimitKey     `json:"openrouter_keys"`
	// ObservedRoutes reserves finite ledger scopes without claiming a credential binding.
	ObservedRoutes []string `json:"observed_routes"`
}
type providerLimitAccount struct {
	Provider     string   `json:"provider"`
	AccountRef   string   `json:"account_ref"`
	SourceHost   string   `json:"source_host"`
	LaunchModels []string `json:"launch_models"`
}
type providerLimitKey struct {
	Name          string   `json:"name"`
	CredentialEnv string   `json:"credential_env"`
	LaunchModels  []string `json:"launch_models"`
}
type providerLimitMeter struct {
	Provider     string   `json:"provider"`
	Scope        string   `json:"scope"`
	KeyName      *string  `json:"keyName"`
	AccountRef   *string  `json:"accountRef"`
	LimitID      *string  `json:"limitId"`
	Model        *string  `json:"model"`
	LaunchModels []string `json:"launchModels"`
	Window       string   `json:"window"`
	Unit         *string  `json:"unit"`
	Used         *float64 `json:"used"`
	Limit        *float64 `json:"limit"`
	Remaining    *float64 `json:"remaining"`
	UsedPercent  *float64 `json:"usedPercent"`
	State        string   `json:"state"`
	Source       string   `json:"source"`
	Reason       *string  `json:"reason"`
	ObservedAt   *string  `json:"observedAt"`
	ResetsAt     *string  `json:"resetsAt"`
}
type providerLimitOutcome struct {
	Provider   string  `json:"provider"`
	KeyName    *string `json:"keyName"`
	AccountRef *string `json:"accountRef"`
	Model      *string `json:"model"`
	Outcome    string  `json:"outcome"`
	Reason     *string `json:"reason"`
	Source     string  `json:"source"`
	ObservedAt string  `json:"observedAt"`
	ResetsAt   *string `json:"resetsAt"`
}
type providerLimitSnapshot struct {
	SchemaVersion int                    `json:"schemaVersion"`
	GeneratedAt   string                 `json:"generatedAt"`
	Meters        []providerLimitMeter   `json:"meters"`
	Outcomes      []providerLimitOutcome `json:"outcomes"`
}

// Private provenance and ingestion sequence never enter the public wire document.
type providerLimitEntry struct {
	Outcome     providerLimitOutcome `json:"outcome"`
	Clock       string               `json:"clock"`
	Event       string               `json:"event"`
	Sequence    uint64               `json:"sequence"`
	NativeAt    string               `json:"nativeAt"`
	SeenThrough string               `json:"seenThrough"`
}
type providerLimitLedger struct {
	Meters        map[string]providerLimitMeter `json:"meters"`
	Reserved      map[string]bool               `json:"reserved"`
	SchemaVersion int                           `json:"schemaVersion"`
	Sequence      uint64                        `json:"sequence"`
	Entries       map[string]providerLimitEntry `json:"entries"`
	// Retired scopes are permanent fences: automatic collection cannot resurrect an archived outcome.
	Retired map[string]providerLimitEntry `json:"retired"`
}
type providerLimitRuntime struct {
	mu          sync.Mutex
	ledger      providerLimitLedger
	initialized bool
	failure     bool
	meters      []providerLimitMeter
	lock        *os.File
	compacting  bool
}

func limitString(s string) *string    { return &s }
func limitNumber(v float64) *float64  { return &v }
func limitInstant(t time.Time) string { return t.UTC().Format(transportAvailabilityTime) }
func limitScope(o providerLimitOutcome) string {
	b, _ := json.Marshal([]any{o.Provider, o.KeyName, o.AccountRef, o.Model})
	return string(b)
}
func limitPublic(v any) bool {
	b, e := json.Marshal(v)
	return e == nil && !limitUnsafePattern.Match(b)
}
func finiteLimit(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 9007199254740991
}

func (cfg *ProviderLimitsConfig) valid() bool {
	if cfg == nil {
		return true
	}
	if !budgetPath(cfg.PrivateRoot) || !budgetPath(cfg.SnapshotFile) || filepath.Dir(cfg.SnapshotFile) == cfg.PrivateRoot || len(cfg.Accounts) > 6 || len(cfg.OpenRouterKeys) > 32 || len(cfg.ObservedRoutes) > 512 {
		return false
	}
	bindings := map[string]string{}
	accounts := map[string]bool{}
	aliases := map[string]bool{}
	credentials := map[string]bool{}
	bind := func(routes []string, prefix, identity string) bool {
		if routes == nil || len(routes) > 256 {
			return false
		}
		seen := map[string]bool{}
		for _, route := range routes {
			if !openCodeModelID.MatchString(route) || !strings.HasPrefix(route, prefix+"/") || seen[route] || bindings[route] != "" && bindings[route] != identity || !limitPublic(route) {
				return false
			}
			seen[route] = true
			bindings[route] = identity
		}
		return true
	}
	for _, a := range cfg.Accounts {
		if !containsString(limitProviders, a.Provider) || !limitAccountPattern.MatchString(a.AccountRef) || a.SourceHost == "" || len(a.SourceHost) > 256 || strings.ContainsAny(a.SourceHost, "\r\n") || strings.HasPrefix(a.AccountRef, "aref:v1:") && !strings.HasPrefix(a.AccountRef, "aref:v1:"+a.Provider+":") || accounts[a.Provider] || !bind(a.LaunchModels, a.Provider, a.AccountRef) {
			return false
		}
		accounts[a.Provider] = true
	}
	for _, k := range cfg.OpenRouterKeys {
		if !limitAliasPattern.MatchString(k.Name) || !limitPublic(k.Name) || !limitEnvPattern.MatchString(k.CredentialEnv) || aliases[k.Name] || credentials[k.CredentialEnv] || !bind(k.LaunchModels, "openrouter", k.Name) {
			return false
		}
		aliases[k.Name] = true
		credentials[k.CredentialEnv] = true
	}
	seen := map[string]bool{}
	for _, route := range cfg.ObservedRoutes {
		p, m, ok := strings.Cut(route, "/")
		if !ok || !openCodeProviderID.MatchString(p) || !openCodeModelID.MatchString(m) || seen[route] || !limitPublic(route) {
			return false
		}
		seen[route] = true
	}
	return cfg.Accounts != nil && cfg.OpenRouterKeys != nil && cfg.ObservedRoutes != nil
}
func decodeProviderLimitsConfig(raw []byte) (*ProviderLimitsConfig, error) {
	var c ProviderLimitsConfig
	if strictJSON(raw, &c) != nil || !c.valid() || !budgetJSONKeys(raw, "private_root snapshot_file accounts openrouter_keys observed_routes") {
		return nil, errMatrix
	}
	return &c, nil
}
func unknownLimit(provider, window, reason string) providerLimitMeter {
	scope := "subscription"
	if provider == "openrouter" {
		scope = "key"
	}
	return providerLimitMeter{Provider: provider, Scope: scope, LaunchModels: []string{}, Window: window, State: "unknown", Source: "unavailable", Reason: limitString(reason)}
}
func (cfg *ProviderLimitsConfig) bindMeter(m *providerLimitMeter) {
	for _, a := range cfg.Accounts {
		if a.Provider == m.Provider {
			m.AccountRef = limitString(a.AccountRef)
			m.LaunchModels = append([]string{}, a.LaunchModels...)
			return
		}
	}
}

// Native readings are reused, and their source time is never refreshed by publication.
func nativeLimitMeters(cfg *ProviderLimitsConfig, quotas map[string]quota) []providerLimitMeter {
	rows := []providerLimitMeter{}
	for _, p := range limitProviders {
		if p != "claude" && p != "codex" {
			m := unknownLimit(p, "unknown", "unsupported")
			cfg.bindMeter(&m)
			rows = append(rows, m)
			continue
		}
		q := quotas[p]
		for _, a := range cfg.Accounts {
			if a.Provider == p && q.sourceHost != a.SourceHost {
				q = quota{}
			}
		}
		for _, w := range []struct {
			name    string
			reading RateLimit
		}{{"5h", q.five}, {"weekly", q.seven}} {
			m := unknownLimit(p, w.name, "measurement_missing")
			m.Unit = limitString("percent")
			m.LimitID = limitString("native")
			cfg.bindMeter(&m)
			if q.ok && !q.at.IsZero() && w.reading.ResetsAt > 0 && finiteLimit(w.reading.UsedPct) && w.reading.UsedPct <= 100 {
				m.State = "known"
				m.Source = "orchid-governor"
				m.Reason = nil
				m.UsedPercent = limitNumber(w.reading.UsedPct)
				m.ObservedAt = limitString(limitInstant(q.at))
				m.ResetsAt = limitString(limitInstant(time.Unix(w.reading.ResetsAt, 0)))
			}
			rows = append(rows, m)
		}
	}
	return rows
}

// Fixed own-key endpoint only. Redirects, metadata failures and raw errors never become outcomes.
func collectOpenRouterLimit(ctx context.Context, k providerLimitKey, credential string, client *http.Client, now time.Time) providerLimitMeter {
	m := unknownLimit("openrouter", "total", "source_unavailable")
	m.KeyName = limitString(k.Name)
	m.LaunchModels = append([]string{}, k.LaunchModels...)
	m.Unit = limitString("usd")
	if credential == "" || strings.ContainsAny(credential, "\r\n") {
		return m
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, providerLimitKeyEndpoint, nil)
	if e != nil {
		return m
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	copy := *client
	copy.Timeout = 15 * time.Second
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := copy.Do(req)
	if e != nil {
		return m
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if e != nil || len(raw) > 64*1024 {
		return m
	}
	var root map[string]json.RawMessage
	var data map[string]json.RawMessage
	if strictJSON(raw, &root) != nil || strictJSON(root["data"], &data) != nil {
		return m
	}
	var cap, remaining, usage *float64
	var reset *string
	if !hasJSONField(data, "limit") || !hasJSONField(data, "limit_remaining") || json.Unmarshal(data["limit"], &cap) != nil || json.Unmarshal(data["limit_remaining"], &remaining) != nil {
		return m
	}
	if cap == nil {
		if remaining != nil || json.Unmarshal(data["usage"], &usage) != nil || usage == nil || !finiteLimit(*usage) {
			return m
		}
		m.Used = usage
	} else {
		if !finiteLimit(*cap) || remaining == nil || math.IsNaN(*remaining) || math.IsInf(*remaining, 0) || *remaining > *cap || !finiteLimit(*cap-*remaining) {
			return m
		}
		m.Limit = cap
		m.Remaining = remaining
		m.Used = limitNumber(*cap - *remaining)
	}
	if value, ok := data["limit_reset"]; ok {
		if json.Unmarshal(value, &reset) != nil {
			return m
		}
	}
	if reset != nil {
		switch *reset {
		case "daily":
			m.Window = "daily"
		case "weekly":
			m.Window = "weekly"
		case "monthly":
			m.Window = "monthly"
		default:
			return m
		}
	}
	// The API provides a reset policy, not an absolute reset instant; do not fabricate one.
	m.State = "known"
	m.Source = "openrouter-key"
	m.Reason = nil
	m.ObservedAt = limitString(limitInstant(now))
	return m
}
func hasJSONField(m map[string]json.RawMessage, k string) bool { _, ok := m[k]; return ok }

// Own private files only, bounded before allocation and fenced against replacement or in-place change.
func readLimitPrivate(path string) ([]byte, error) {
	resolved, e := filepath.EvalSymlinks(path)
	if e != nil {
		return nil, e
	}
	if resolved != path {
		return nil, errMatrix
	}
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), "limit-state")
	defer f.Close()
	before, e := f.Stat()
	if e != nil {
		return nil, errMatrix
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Getuid() || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || before.Size() > providerLimitMaxBytes {
		return nil, errMatrix
	}
	raw, e := io.ReadAll(io.LimitReader(f, providerLimitMaxBytes+1))
	after, se := f.Stat()
	bound, pe := os.Lstat(path)
	if e != nil || se != nil || pe != nil || !os.SameFile(before, bound) || before.Size() != after.Size() || !os.SameFile(before, after) || owner.Uid != after.Sys().(*syscall.Stat_t).Uid || owner.Ctim != after.Sys().(*syscall.Stat_t).Ctim || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || len(raw) > providerLimitMaxBytes {
		return nil, errMatrix
	}
	return raw, nil
}
func (c *Coord) limitRuntime() *providerLimitRuntime { return &c.limits }
func limitLedgerPath(cfg *ProviderLimitsConfig) string {
	return filepath.Join(cfg.PrivateRoot, "provider-limits-ledger.json")
}
func validLimitOutcome(o providerLimitOutcome, now time.Time) bool {
	at, ok := budgetInstant(o.ObservedAt)
	return ok && !at.After(now) && openCodeProviderID.MatchString(o.Provider) && (o.KeyName == nil || o.Provider == "openrouter" && limitAliasPattern.MatchString(*o.KeyName)) && (o.AccountRef == nil || limitAccountPattern.MatchString(*o.AccountRef)) && (o.Model == nil || openCodeModelID.MatchString(*o.Model)) &&
		(o.Source == "provider-run" || o.Source == "inference-probe") && (o.Outcome == "succeeded" && o.Reason == nil || o.Outcome == "refused" && o.Reason != nil && containsString([]string{"quota_exhausted", "payment_required", "rate_limited"}, *o.Reason)) && (o.ResetsAt == nil || func() bool { _, ok := budgetInstant(*o.ResetsAt); return ok }()) && limitPublic(o)
}
func limitPrivateRoot(path string) bool {
	if !privateReceiptRoot(path) {
		return false
	}
	st, e := os.Stat(path)
	if e != nil {
		return false
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(owner.Uid) == os.Getuid()
}
func (c *Coord) loadLimitsLocked(now time.Time) error {
	cfg := c.cfg.ProviderLimits
	r := c.limitRuntime()
	if cfg == nil {
		return nil
	}
	if r.failure {
		return errMatrix
	}
	if r.initialized {
		return nil
	}
	if !cfg.valid() || !limitPrivateRoot(cfg.PrivateRoot) || !distinctLimitCredentials(cfg) {
		r.failure = true
		return errMatrix
	}
	lockPath := filepath.Join(cfg.PrivateRoot, "provider-limits.lock")
	fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		r.failure = true
		return errMatrix
	}
	lock := os.NewFile(uintptr(fd), "provider-limits-lock")
	st, err := lock.Stat()
	if err != nil {
		lock.Close()
		r.failure = true
		return errMatrix
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Getuid() || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lock.Close()
		r.failure = true
		return errMatrix
	}
	r.lock = lock
	r.ledger = providerLimitLedger{SchemaVersion: 1, Entries: map[string]providerLimitEntry{}, Retired: map[string]providerLimitEntry{}, Reserved: map[string]bool{}, Meters: map[string]providerLimitMeter{}}
	raw, e := readLimitPrivate(limitLedgerPath(cfg))
	if e != nil && !os.IsNotExist(e) {
		r.failure = true
		return errMatrix
	}
	if e == nil {
		if strictJSON(raw, &r.ledger) != nil || r.ledger.SchemaVersion != 1 || r.ledger.Entries == nil || r.ledger.Retired == nil || r.ledger.Meters == nil || len(r.ledger.Meters) > providerLimitMaxRows || r.ledger.Reserved == nil || len(r.ledger.Reserved) > providerLimitMaxScopes || len(r.ledger.Entries) > providerLimitMaxRows || len(r.ledger.Retired) > providerLimitMaxRows*4 {
			r.failure = true
			return errMatrix
		}
		for key, entry := range r.ledger.Entries {
			if limitRateObservation(entry.Outcome) && key == limitScope(entry.Outcome) {
				newKey := limitEntryKey(entry.Outcome)
				if _, duplicate := r.ledger.Entries[newKey]; duplicate {
					r.failure = true
					return errMatrix
				}
				r.ledger.Entries[newKey] = entry
				delete(r.ledger.Entries, key)
			}
		}
		for identity, meter := range r.ledger.Meters {
			if identity != limitMeterIdentity(meter) || !validLimitMeterIdentity(meter) || len(meter.LaunchModels) != 0 || meter.State != "unknown" {
				r.failure = true
				return errMatrix
			}
		}
		for scope := range r.ledger.Entries {
			if _, overlap := r.ledger.Retired[scope]; overlap {
				r.failure = true
				return errMatrix
			}
		}
		for scope, v := range allLimitEntries(r.ledger) {
			if scope != limitEntryKey(v.Outcome) || !validLimitOutcome(v.Outcome, now) || v.Sequence == 0 || v.Sequence > r.ledger.Sequence || v.Clock == "" || len(v.Clock) > 256 || v.Event == "" || len(v.Event) > 1024 || v.Outcome.ObservedAt != v.NativeAt || !validLimitPrivateTime(v.NativeAt, now) || !validLimitPrivateTime(v.SeenThrough, now) || v.SeenThrough < v.NativeAt {
				r.failure = true
				return errMatrix
			}
		}
	}
	// Reserve every configured route and explicit provider-global scope before admitting any new work.
	reserved := map[string]bool{}
	for k, v := range r.ledger.Reserved {
		if !v || !validLimitScope(k) {
			r.failure = true
			return errMatrix
		}
		reserved[k] = true
	}
	for _, v := range r.ledger.Entries {
		reserved[limitScope(v.Outcome)] = true
	}
	for scope := range configuredLimitScopes(cfg) {
		if _, retired := r.ledger.Retired[scope]; retired && !r.compacting {
			r.failure = true
			return errMatrix
		}
		reserved[scope] = true
	}
	if !r.compacting && len(reserved) > providerLimitMaxScopes {
		r.failure = true
		return errMatrix
	}
	r.ledger.Reserved = reserved
	if writeLimitLedger(limitLedgerPath(cfg), ".provider-limits-ledger-", nil, r.ledger) != nil {
		r.failure = true
		return errMatrix
	}
	r.initialized = true
	return nil
}

// Exact proven bindings only: an unknown named key cannot emit a provider-wide paid refusal.
func (cfg *ProviderLimitsConfig) routeOutcome(provider, model string) (providerLimitOutcome, bool) {
	o := providerLimitOutcome{Provider: provider, Source: "provider-run"}
	if model != "" {
		o.Model = limitString(model)
	}
	if provider == "openrouter" {
		for _, k := range cfg.OpenRouterKeys {
			if containsString(k.LaunchModels, provider+"/"+model) {
				o.KeyName = limitString(k.Name)
				return o, true
			}
		}
		return o, false
	}
	for _, a := range cfg.Accounts {
		if a.Provider == provider {
			o.AccountRef = limitString(a.AccountRef)
			return o, true
		}
	}
	return o, true
}
func (c *Coord) recordLimitOutcome(o providerLimitOutcome, clock, event string, now time.Time) error {
	if c.cfg.ProviderLimits == nil || c.dry {
		return nil
	}
	r := c.limitRuntime()
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.loadLimitsLocked(now) != nil || !validLimitOutcome(o, now) || clock == "" || len(clock) > 256 || event == "" || len(event) > 1024 {
		r.failure = true
		return errMatrix
	}
	scope := limitScope(o)
	entryKey := limitEntryKey(o)
	if _, retired := r.ledger.Retired[scope]; retired {
		return errMatrix
	}
	if !r.ledger.Reserved[scope] {
		if len(r.ledger.Reserved) >= providerLimitMaxScopes {
			r.failure = true
			return errMatrix
		}
		r.ledger.Reserved[scope] = true
	}

	nativeAt := o.ObservedAt
	before, seen := r.ledger.Entries[entryKey]
	if seen && before.Event == event {
		return nil
	}
	if seen && (before.Clock == clock && (nativeAt < before.SeenThrough || nativeAt == before.SeenThrough && o.Outcome == "succeeded") || o.Outcome == "succeeded" && before.Clock != clock) {
		return nil
	}
	// Preserve the verified source instant; receipt time is not a new native observation.
	for _, prior := range r.ledger.Entries {
		if prior.Outcome.Outcome == "succeeded" && o.Outcome == "refused" && prior.Clock == clock && prior.Outcome.Provider == o.Provider && sameLimitString(prior.Outcome.KeyName, o.KeyName) && sameLimitString(prior.Outcome.AccountRef, o.AccountRef) && (o.Model == nil || sameLimitString(o.Model, prior.Outcome.Model)) && prior.Outcome.ObservedAt > o.ObservedAt {
			return nil
		}
		if !validLimitOutcome(prior.Outcome, now) {
			r.failure = true
			return errMatrix
		}
	}
	if seen && o.ObservedAt < before.Outcome.ObservedAt {
		if limitRateObservation(o) {
			return nil // Older informational observations cannot fence admission.
		}
		r.failure = true
		return errMatrix
	}
	if seen && o.ObservedAt == before.Outcome.ObservedAt && before.Outcome.Outcome == "refused" && o.Outcome == "succeeded" {
		return nil
	}
	if o.Outcome == "succeeded" {
		for _, e := range r.ledger.Entries {
			p := e.Outcome
			if p.Outcome == "refused" && !limitRateObservation(p) && p.Provider == o.Provider && sameLimitString(p.KeyName, o.KeyName) && sameLimitString(p.AccountRef, o.AccountRef) && (p.Model == nil || sameLimitString(p.Model, o.Model)) && (e.Clock != clock || nativeAt <= e.SeenThrough || o.ObservedAt <= p.ObservedAt) {
				return nil
			}
		}
	}
	through := nativeAt
	if seen && before.Clock == clock && before.SeenThrough > through {
		through = before.SeenThrough
	}
	if !seen && len(r.ledger.Entries) >= providerLimitMaxRows {
		r.failure = true
		return errMatrix
	}
	if r.ledger.Sequence == ^uint64(0) {
		r.failure = true
		return errMatrix
	}
	r.ledger.Sequence++
	r.ledger.Entries[entryKey] = providerLimitEntry{Outcome: o, Clock: clock, Event: event, Sequence: r.ledger.Sequence, NativeAt: nativeAt, SeenThrough: through}
	if writeLimitLedger(limitLedgerPath(c.cfg.ProviderLimits), ".provider-limits-ledger-", nil, r.ledger) != nil {
		r.failure = true
		return errMatrix
	}
	if r.meters != nil {
		return c.writeLimitSnapshotLocked(r.meters, now)
	}
	return nil
}
func sameLimitString(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func activeLimitRefusals(entries map[string]providerLimitEntry) []providerLimitOutcome {
	out := []providerLimitOutcome{}
	for _, entry := range entries {
		o := entry.Outcome
		if o.Outcome != "refused" {
			continue
		}
		active := true
		for _, success := range entries {
			s := success.Outcome
			if s.Outcome == "succeeded" && s.Provider == o.Provider && sameLimitString(s.KeyName, o.KeyName) && sameLimitString(s.AccountRef, o.AccountRef) && (o.Model == nil || sameLimitString(s.Model, o.Model)) && s.ObservedAt > o.ObservedAt && success.Clock == entry.Clock {
				active = false
			}
		}
		if active {
			out = append(out, o)
		}
	}
	return out
}
func (c *Coord) providerLimitLaunchReason(agent string, o Overrides, now time.Time) string {
	if c.cfg.ProviderLimits == nil {
		return ""
	}
	r := c.limitRuntime()
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.loadLimitsLocked(now) != nil {
		return "receipt-persistence-failed"
	}
	provider, model := accountKey(agent), o.Model
	if agent == "opencode" || agent == "opencode-run" {
		route, e := resolveOpenCodeRoute(o)
		if e != nil {
			return ""
		}
		provider, model = route.Provider, route.Model
	}
	bound, proven := c.cfg.ProviderLimits.routeOutcome(provider, model)
	if proven {
		scope := limitScope(bound)
		if _, retired := r.ledger.Retired[scope]; retired {
			return "receipt-persistence-failed"
		}
		if !r.ledger.Reserved[scope] {
			if len(r.ledger.Reserved) >= providerLimitMaxScopes {
				return "receipt-persistence-failed"
			}
			r.ledger.Reserved[scope] = true
			if writeLimitLedger(limitLedgerPath(c.cfg.ProviderLimits), ".provider-limits-reservation-", nil, r.ledger) != nil {
				r.failure = true
				return "receipt-persistence-failed"
			}
		}
	}
	for _, f := range activeLimitRefusals(r.ledger.Entries) {
		if limitRateObservation(f) {
			continue // Owner549: rate observations remain visible but never veto admission.
		}
		if f.Provider != provider || f.Model != nil && *f.Model != model {
			continue
		}
		if f.KeyName != nil && (!proven || !sameLimitString(f.KeyName, bound.KeyName)) {
			continue
		}
		if f.AccountRef != nil {
			if !sameLimitString(f.AccountRef, bound.AccountRef) {
				continue
			}
			routes := []string{}
			for _, a := range c.cfg.ProviderLimits.Accounts {
				if a.Provider == provider {
					routes = a.LaunchModels
				}
			}
			if !containsString(routes, provider+"/"+model) {
				continue
			}
		}
		return "quota-unavailable"
	}
	return ""
}

// Publish full durable history each sample, retaining ledger and prior source on failure.
func (c *Coord) publishProviderLimits(ctx context.Context, quotas map[string]quota, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cfg := c.cfg.ProviderLimits
	if cfg == nil || c.dry {
		return nil
	}
	r := c.limitRuntime()
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.loadLimitsLocked(now) != nil {
		return errMatrix
	}
	snapshot := providerLimitSnapshot{1, limitInstant(now), nativeLimitMeters(cfg, quotas), []providerLimitOutcome{}}
	for _, k := range cfg.OpenRouterKeys {
		snapshot.Meters = append(snapshot.Meters, collectOpenRouterLimit(ctx, k, os.Getenv(k.CredentialEnv), http.DefaultClient, now))
	}
	current := map[string]bool{}
	for _, m := range snapshot.Meters {
		identity := limitMeterIdentity(m)
		current[identity] = true
		r.ledger.Meters[identity] = unboundLimitMeter(m)
	}
	if len(r.ledger.Meters) > providerLimitMaxRows {
		r.failure = true
		return errMatrix
	}
	for identity, m := range r.ledger.Meters {
		if !current[identity] {
			snapshot.Meters = append(snapshot.Meters, m)
		}
	}
	sort.Slice(snapshot.Meters, func(i, j int) bool {
		return limitMeterIdentity(snapshot.Meters[i]) < limitMeterIdentity(snapshot.Meters[j])
	})
	if writeLimitLedger(limitLedgerPath(cfg), ".provider-limits-identities-", nil, r.ledger) != nil {
		r.failure = true
		return errMatrix
	}
	r.meters = snapshot.Meters
	return c.writeLimitSnapshotLocked(snapshot.Meters, now)
}
func (c *Coord) writeLimitSnapshotLocked(meters []providerLimitMeter, now time.Time) error {
	cfg := c.cfg.ProviderLimits
	r := c.limitRuntime()
	snapshot := providerLimitSnapshot{1, limitInstant(now), meters, []providerLimitOutcome{}}
	for _, v := range r.ledger.Entries {
		snapshot.Outcomes = append(snapshot.Outcomes, v.Outcome)
	}
	sort.Slice(snapshot.Outcomes, func(i, j int) bool { return limitEntryKey(snapshot.Outcomes[i]) < limitEntryKey(snapshot.Outcomes[j]) })
	if !limitSnapshotTimes(snapshot, now) || !limitPublic(snapshot) || len(snapshot.Meters) > providerLimitMaxRows || !privateReceiptRoot(filepath.Dir(cfg.SnapshotFile)) {
		return errMatrix
	}
	owner, e := configuredReceiptOwner(c.cfg.Matrix)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(snapshot)
	if e != nil || len(raw) > providerLimitMaxBytes {
		return errMatrix
	}
	return writePrivateJSON(cfg.SnapshotFile, ".provider-limits-", owner, snapshot)
}

// Compact inactive verified successes only, with permanent private replay fences. Refusals are never removed.
func compactProviderLimitLedger(cfg *ProviderLimitsConfig, now time.Time) error {
	c := &Coord{cfg: &Config{ProviderLimits: cfg}}
	defer c.closeProviderLimits()
	r := c.limitRuntime()
	r.compacting = true
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.loadLimitsLocked(now) != nil {
		return errMatrix
	}
	for scope, v := range r.ledger.Entries {
		o := v.Outcome
		if o.Outcome != "succeeded" || o.Model == nil || configuredLimitScopes(cfg)[scope] || limitSuccessClearsRetainedRefusal(v, r.ledger.Entries) {
			continue
		}
		if len(r.ledger.Retired) >= providerLimitMaxRows*4 {
			return errMatrix
		}
		r.ledger.Retired[scope] = v
		delete(r.ledger.Entries, scope)
		delete(r.ledger.Reserved, scope)
	}
	for scope := range r.ledger.Reserved {
		_, hardExists := r.ledger.Entries[scope]
		_, rateExists := r.ledger.Entries[scope+"/rate_limited"]
		if !hardExists && !rateExists && !configuredLimitScopes(cfg)[scope] {
			delete(r.ledger.Reserved, scope)
		}
	}
	return writeLimitLedger(limitLedgerPath(cfg), ".provider-limits-compact-", nil, r.ledger)
}

// Typed private errors expose only the existing closed OpenCode error to generic callers.
type providerLimitNativeError struct {
	reason   string
	at       time.Time
	event    string
	resetsAt *string
}

func (e *providerLimitNativeError) Error() string { return "opencode-provider-error" }
func (e *providerLimitNativeError) Unwrap() error { return matrixReason("opencode-provider-error") }
func nativeLimitError(raw json.RawMessage, at int64, event string) error {
	var e struct {
		Name string `json:"name"`
		Data struct {
			StatusCode      int               `json:"statusCode"`
			Message         string            `json:"message"`
			ResponseBody    string            `json:"responseBody"`
			ResponseHeaders map[string]string `json:"responseHeaders"`
		} `json:"data"`
	}
	if decodeNativeJSON(raw, &e) != nil {
		return matrixReason("opencode-provider-error")
	}
	reason := ""
	switch {
	case e.Name == "UsageLimitError" || e.Name == "APIError" && goUsageLimit(e.Data.ResponseBody):
		reason = "quota_exhausted"
	case e.Name == "APIError" && e.Data.StatusCode == 402:
		reason = "payment_required"
	case e.Name == "APIError" && e.Data.StatusCode == 429:
		reason = "rate_limited"
	case e.Name == "APIError" && strings.Contains(strings.ToLower(e.Data.Message), "usage limit reached"):
		reason = "quota_exhausted"
	}
	if reason == "" || at <= 0 {
		return matrixReason("opencode-provider-error")
	}
	failure := &providerLimitNativeError{reason: reason, at: time.UnixMilli(at), event: event}
	if value, ok := e.Data.ResponseHeaders["retry-after"]; ok {
		if seconds, err := time.ParseDuration(value + "s"); err == nil && seconds >= 0 && seconds <= 31*24*time.Hour {
			failure.resetsAt = limitString(limitInstant(failure.at.Add(seconds)))
		} else if reset, err := http.ParseTime(value); err == nil {
			failure.resetsAt = limitString(limitInstant(reset))
		}
	}
	return failure
}
func (c *Coord) recordOpenCodeLimit(j *Job, err error, success *openCodeRun, now time.Time) {
	if c.cfg.ProviderLimits == nil || j == nil || j.OpenCode == nil {
		return
	}
	run := j.OpenCode
	o, proven := c.cfg.ProviderLimits.routeOutcome(run.Route.Provider, run.Route.Model)
	if !proven {
		return
	}
	var refused *providerLimitNativeError
	if errors.As(err, &refused) {
		o.Outcome = "refused"
		o.Reason = limitString(refused.reason)
		o.ResetsAt = refused.resetsAt
		o.ObservedAt = limitInstant(refused.at)
		_ = c.recordLimitOutcome(o, j.Host, refused.event, now)
	} else if err == nil && success != nil && success.Route == run.Route && success.SessionID == run.SessionID && success.LimitSuccessAt > 0 {
		o.Outcome = "succeeded"
		o.ObservedAt = limitInstant(time.UnixMilli(success.LimitSuccessAt))
		_ = c.recordLimitOutcome(o, j.Host, success.LimitSuccessEvent, now)
	}
}

// Exact native API error marker; never publish its response body or provider metadata.
func goUsageLimit(body string) bool {
	if len(body) > 64*1024 {
		return false
	}
	var value struct {
		Name  string `json:"name"`
		Error struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"error"`
	}
	return decodeNativeJSON([]byte(body), &value) == nil && (value.Name == "GoUsageLimitError" || value.Error.Name == "GoUsageLimitError" || value.Error.Type == "GoUsageLimitError")
}

func allLimitEntries(ledger providerLimitLedger) map[string]providerLimitEntry {
	out := map[string]providerLimitEntry{}
	for k, v := range ledger.Retired {
		out[k] = v
	}
	for k, v := range ledger.Entries {
		out[k] = v
	}
	return out
}
func (c *Coord) closeProviderLimits() {
	r := c.limitRuntime()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failure = true // A released lease cannot be reused by a late shutdown callback.
	if r.lock != nil {
		r.lock.Close()
		r.lock = nil
	}
}

// Offline compaction refuses a live writer lease, unknown flags and private-path diagnostics.
func providerLimitsCLI(args []string, out, diagnostic io.Writer) int {
	if len(args) != 3 || args[0] != "compact" || args[1] != "--config" {
		fmt.Fprintln(diagnostic, "provider-limits compact requires --config")
		return 2
	}
	cfg, e := loadConfig(args[2])
	if e != nil || cfg.ProviderLimits == nil {
		fmt.Fprintln(diagnostic, "provider limits configuration invalid")
		return 3
	}
	if compactProviderLimitLedger(cfg.ProviderLimits, time.Now()) != nil {
		fmt.Fprintln(diagnostic, "provider limits compaction unavailable")
		return 3
	}
	fmt.Fprintln(out, "provider limits compaction complete")
	return 0
}

func validLimitScope(scope string) bool {
	var values []json.RawMessage
	if json.Unmarshal([]byte(scope), &values) != nil || len(values) != 4 {
		return false
	}
	var o providerLimitOutcome
	if json.Unmarshal(values[0], &o.Provider) != nil || json.Unmarshal(values[1], &o.KeyName) != nil || json.Unmarshal(values[2], &o.AccountRef) != nil || json.Unmarshal(values[3], &o.Model) != nil {
		return false
	}
	o.Outcome = "succeeded"
	o.Source = "provider-run"
	o.ObservedAt = "2020-01-01T00:00:00.000Z"
	return limitScope(o) == scope && validLimitOutcome(o, time.Now())
}

// Compare credentials transiently, without deriving or retaining public identifiers.
func distinctLimitCredentials(cfg *ProviderLimitsConfig) bool {
	for i, a := range cfg.OpenRouterKeys {
		value := os.Getenv(a.CredentialEnv)
		if value == "" {
			continue
		}
		for _, b := range cfg.OpenRouterKeys[:i] {
			if value == os.Getenv(b.CredentialEnv) {
				return false
			}
		}
	}
	return true
}
func validLimitPrivateTime(value string, now time.Time) bool {
	at, ok := budgetInstant(value)
	return ok && !at.After(now)
}
func limitSnapshotTimes(snapshot providerLimitSnapshot, now time.Time) bool {
	for _, o := range snapshot.Outcomes {
		if !validLimitOutcome(o, now) {
			return false
		}
	}
	for _, m := range snapshot.Meters {
		if m.ObservedAt != nil && !validLimitPrivateTime(*m.ObservedAt, now) {
			return false
		}
	}
	return true
}

func writeLimitLedger(path, prefix string, owner *receiptOwner, ledger providerLimitLedger) error {
	encoded, err := json.Marshal(ledger)
	if err != nil || len(encoded) > providerLimitMaxBytes {
		return errMatrix
	}
	return writePrivateJSON(path, prefix, owner, ledger)
}

// Reserve every attested route as well as the observation inventory before dispatch.
func configuredLimitScopes(cfg *ProviderLimitsConfig) map[string]bool {
	out := map[string]bool{}
	routes := append([]string{}, cfg.ObservedRoutes...)
	for _, a := range cfg.Accounts {
		routes = append(routes, a.LaunchModels...)
	}
	for _, k := range cfg.OpenRouterKeys {
		routes = append(routes, k.LaunchModels...)
		out[limitScope(providerLimitOutcome{Provider: "openrouter", KeyName: limitString(k.Name)})] = true
	}
	for _, route := range routes {
		p, m, _ := strings.Cut(route, "/")
		o, ok := cfg.routeOutcome(p, m)
		if ok {
			out[limitScope(o)] = true
		}
	}
	for _, p := range limitProviders {
		o, _ := cfg.routeOutcome(p, "")
		o.Model = nil
		out[limitScope(o)] = true
	}
	return out
}

// A tombstone needed to clear a retained global refusal is still active evidence.
func limitSuccessClearsRetainedRefusal(success providerLimitEntry, entries map[string]providerLimitEntry) bool {
	s := success.Outcome
	for _, entry := range entries {
		o := entry.Outcome
		if o.Outcome == "refused" && s.Provider == o.Provider && sameLimitString(s.KeyName, o.KeyName) && sameLimitString(s.AccountRef, o.AccountRef) && (o.Model == nil || sameLimitString(s.Model, o.Model)) && s.ObservedAt > o.ObservedAt && success.Clock == entry.Clock {
			return true
		}
	}
	return false
}

func limitMeterIdentity(m providerLimitMeter) string {
	b, _ := json.Marshal([]any{m.Provider, m.Scope, m.KeyName, m.AccountRef, m.LimitID, m.Model, m.Window})
	return string(b)
}

// Explicit old identity rows retire route bindings without inventing inference success.
func unboundLimitMeter(m providerLimitMeter) providerLimitMeter {
	m.LaunchModels = []string{}
	m.State = "unknown"
	m.Source = "unavailable"
	m.Reason = limitString("source_not_configured")
	m.Used = nil
	m.Limit = nil
	m.Remaining = nil
	m.UsedPercent = nil
	m.ObservedAt = nil
	m.ResetsAt = nil
	return m
}
func validLimitMeterIdentity(m providerLimitMeter) bool {
	return limitPublic(m) && containsString([]string{"5h", "weekly", "daily", "monthly", "total", "unknown"}, m.Window) && m.Model == nil && (m.AccountRef == nil || limitAccountPattern.MatchString(*m.AccountRef)) && (m.LimitID == nil || *m.LimitID == "native") && (m.Scope == "subscription" && containsString(limitProviders, m.Provider) && m.KeyName == nil || m.Scope == "key" && m.Provider == "openrouter" && m.KeyName != nil && limitAliasPattern.MatchString(*m.KeyName))
}

// Rate observations cannot overwrite a hard refusal or manufacture a successful inference.
func limitRateObservation(o providerLimitOutcome) bool {
	return o.Outcome == "refused" && o.Reason != nil && *o.Reason == "rate_limited"
}
func limitEntryKey(o providerLimitOutcome) string {
	scope := limitScope(o)
	if limitRateObservation(o) {
		return scope + "/rate_limited"
	}
	return scope
}
