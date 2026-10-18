package main

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// The cockpit owns which repositories agents may work in. It publishes the
// enabled list as one atomic snapshot in the private action spool, which the
// coordinator already reads every tick. The static targets block is only a
// bootstrap fallback until the first valid snapshot; after that the cockpit
// list alone decides which repositories agents may work in.
const (
	cockpitTargetsFile  = "targets.json"
	cockpitTargetsLimit = 64 * 1024
	cockpitTargetsMax   = 256
	// cockpitSinceSkew bounds how far in the future a snapshot's since may be.
	cockpitSinceSkew = 5 * time.Minute
)

// cockpitTargetAgents are the transports a cockpit target may allow.
var cockpitTargetAgents = []string{"claude", "codex", "agy", "opencode"}

// cockpitTargetSource holds the last good cockpit list. Config copies share
// one source, so the coordinator and the owner-grant store read the same list.
type cockpitTargetSource struct {
	list atomic.Pointer[[]Target]
	mu   sync.Mutex // one refresh at a time; guards the fields below
	seen string     // digest of the last snapshot bytes handled, good or bad
	told string     // the last problem logged, so each distinct reason logs once
	off  string     // the static repositories last logged as not enabled
}

type cockpitTargetsSnapshot struct {
	SchemaVersion int `json:"schema_version"`
	Targets       []struct {
		Repo   string   `json:"repo"`
		Agents []string `json:"agents"`
		Since  string   `json:"since"`
	} `json:"targets"`
}

// effectiveTargets is the static targets until a valid cockpit snapshot has
// loaded. From then on it is exactly the cockpit list (an empty list means no
// targets): a static entry for the same repository (case-insensitive) only
// lends its metadata, never authorization.
func (c *Config) effectiveTargets() []Target {
	if c.cockpit == nil {
		return c.Targets
	}
	loaded := c.cockpit.list.Load()
	if loaded == nil {
		return c.Targets
	}
	out := make([]Target, 0, len(*loaded))
	for _, t := range *loaded { // a loaded list is never modified in place
		out = append(out, withStaticMetadata(t, c.Targets))
	}
	return out
}

// withStaticMetadata copies the operator metadata of the static entry for the
// same repository onto a cockpit target. Repo, agents and the cockpit mark stay.
func withStaticMetadata(t Target, static []Target) Target {
	for _, s := range static {
		if strings.EqualFold(s.Repo, t.Repo) {
			t.Label, t.NeedCap, t.AutoMerge, t.Priority = s.Label, s.NeedCap, s.AutoMerge, s.Priority
			t.PromptHint, t.MirrorAssignments, t.Disabled = s.PromptHint, s.MirrorAssignments, s.Disabled
			break
		}
	}
	return t
}

// sourceSince is when agents were last turned on for a cockpit repository; the
// source scanner never starts that repository before it.
func (c *Config) sourceSince(repo string) (time.Time, bool) {
	for _, t := range c.effectiveTargets() {
		if t.cockpit && !t.since.IsZero() && strings.EqualFold(t.Repo, repo) {
			return t.since, true
		}
	}
	return time.Time{}, false
}

// allowsTransport: a cockpit target's agents are an allow-list; a static
// target allows every transport its route resolves to.
func (t Target) allowsTransport(transport string) bool {
	if !t.cockpit {
		return true
	}
	for _, a := range t.Agents {
		if accountKey(a) == accountKey(transport) {
			return true
		}
	}
	return false
}

// refreshCockpitTargets reads the cockpit snapshot once per tick. A missing
// file before any load means no cockpit list yet. A problem keeps the last good
// list and is logged once per distinct reason, never with the file contents.
func (c *Coord) refreshCockpitTargets() {
	if c.cfg.ActionRequestRoot == "" || c.cfg.cockpit == nil {
		return
	}
	c.cfg.cockpit.refresh(c.cfg.ActionRequestRoot, c.cfg.Targets, time.Now())
}

func (s *cockpitTargetSource) refresh(root string, static []Target, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, problem := readCockpitTargets(root)
	if problem == "missing" && s.list.Load() == nil {
		return
	}
	if problem != "" {
		s.seen = "" // the next readable snapshot is parsed again
		s.problem(problem)
		return
	}
	digest := shaText(raw)
	if digest == s.seen {
		return
	}
	s.seen = digest
	list, problem := parseCockpitTargets(raw, now)
	if problem != "" {
		s.problem(problem)
		return
	}
	s.told = ""
	if current := s.list.Load(); current != nil && reflect.DeepEqual(*current, list) {
		return
	}
	s.list.Store(&list)
	repos := make([]string, len(list))
	for i, t := range list {
		repos[i] = t.Repo
	}
	log.Printf("dispatch targets from cockpit: %d (%s)", len(list), strings.Join(repos, ", "))
	var off []string
	for _, t := range static {
		enabled := false
		for _, e := range list {
			enabled = enabled || strings.EqualFold(e.Repo, t.Repo)
		}
		if !enabled && !containsString(off, t.Repo) {
			off = append(off, t.Repo)
		}
	}
	if key := strings.Join(off, ", "); key != s.off {
		s.off = key
		log.Printf("static targets not enabled in the cockpit: %d (%s)", len(off), key)
	}
}

func (s *cockpitTargetSource) problem(reason string) {
	if s.told == reason {
		return
	}
	s.told = reason
	log.Printf("dispatch targets from cockpit unusable (%s); keeping the last good list", reason)
}

// readCockpitTargets returns the snapshot bytes, or a fixed problem reason.
func readCockpitTargets(root string) ([]byte, string) {
	if !privateActionSpoolDir(root) {
		return nil, "spool-invalid"
	}
	path := filepath.Join(root, cockpitTargetsFile)
	before, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, "missing"
	}
	if err != nil {
		return nil, "unreadable"
	}
	if !before.Mode().IsRegular() {
		return nil, "not-a-regular-file"
	}
	if perm := before.Mode().Perm(); perm != 0600 && perm != 0660 {
		return nil, "mode-not-private"
	}
	if before.Size() > cockpitTargetsLimit {
		return nil, "oversized"
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "unreadable"
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, "unreadable"
	}
	raw, err := io.ReadAll(io.LimitReader(f, cockpitTargetsLimit+1))
	if err != nil {
		return nil, "unreadable"
	}
	if len(raw) > cockpitTargetsLimit {
		return nil, "oversized"
	}
	return raw, ""
}

// parseCockpitTargets validates the whole snapshot; one bad entry refuses it.
func parseCockpitTargets(raw []byte, now time.Time) ([]Target, string) {
	var snap cockpitTargetsSnapshot
	if len(bytes.TrimSpace(raw)) == 0 || strictJSON(raw, &snap) != nil {
		return nil, "invalid-json"
	}
	if snap.SchemaVersion != 1 || snap.Targets == nil {
		return nil, "schema-version-or-targets"
	}
	if len(snap.Targets) > cockpitTargetsMax {
		return nil, "too-many-targets"
	}
	list := make([]Target, 0, len(snap.Targets))
	seen := map[string]bool{}
	for _, entry := range snap.Targets {
		key := strings.ToLower(entry.Repo)
		if !repositoryName.MatchString(entry.Repo) {
			return nil, "invalid-repository"
		}
		if seen[key] {
			return nil, "duplicate-repository"
		}
		seen[key] = true
		if len(entry.Agents) == 0 {
			return nil, "invalid-agents"
		}
		agents := map[string]bool{}
		for _, a := range entry.Agents {
			if !containsString(cockpitTargetAgents, a) || agents[a] {
				return nil, "invalid-agents"
			}
			agents[a] = true
		}
		since, err := time.Parse(time.RFC3339Nano, entry.Since)
		if err != nil || since.IsZero() || since.After(now.Add(cockpitSinceSkew)) {
			return nil, "invalid-since"
		}
		list = append(list, Target{Repo: entry.Repo, Agents: append([]string(nil), entry.Agents...), cockpit: true, since: since.UTC()})
	}
	return list, ""
}
