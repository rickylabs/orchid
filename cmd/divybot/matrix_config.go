package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Diagnostics contain only known schema keys and fixed codes; never input values.
type matrixConfigProblem struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func configProblem(field, reason string) []matrixConfigProblem {
	return []matrixConfigProblem{{field, reason}}
}

func readMatrixConfigFile(name string) (*Config, map[string]json.RawMessage, []matrixConfigProblem) {
	data, err := os.ReadFile(name)
	if err != nil || len(data) > 1024*1024 {
		return nil, nil, configProblem("config", "unreadable-or-oversized")
	}
	var raw map[string]json.RawMessage
	if strictJSON(data, &raw) != nil || raw == nil {
		return nil, nil, configProblem("config", "invalid-or-duplicate-json")
	}
	var cfg Config
	if block, ok := raw["matrix"]; ok {
		var fields map[string]json.RawMessage
		if strictJSON(block, &fields) != nil || fields == nil {
			return nil, nil, configProblem("matrix", "object-required")
		}
		checks := []struct {
			name  string
			value any
		}{{"source", &cfg.Matrix.Source}, {"revision", &cfg.Matrix.Revision}, {"receipt_root", &cfg.Matrix.ReceiptRoot}, {"target_revisions", &cfg.Matrix.TargetRevisions}, {"pins", &cfg.Matrix.Pins}, {"grants", &cfg.Matrix.Grants}}
		for _, check := range checks {
			if value, ok := fields[check.name]; ok && json.Unmarshal(value, check.value) != nil {
				return nil, nil, configProblem("matrix."+check.name, "invalid-field-type")
			}
		}
	}
	if json.Unmarshal(data, &cfg) != nil {
		return nil, nil, configProblem("config", "invalid-field-type")
	}
	if block, ok := raw["matrix"]; ok {
		if string(block) == "null" || strictJSON(block, &cfg.Matrix) != nil {
			return nil, nil, configProblem("matrix", "invalid-or-unknown-field")
		}
	}
	cfg.withDefaults()
	return &cfg, raw, nil
}

func validateMatrixConfig(ctx context.Context, cfg *Config) []matrixConfigProblem {
	var out []matrixConfigProblem
	bad := func(field, reason string) { out = append(out, matrixConfigProblem{field, reason}) }
	m := cfg.Matrix
	if !filepath.IsAbs(m.Source) {
		bad("matrix.source", "absolute-checkout-required")
	}
	if !sourceRevision.MatchString(m.Revision) {
		bad("matrix.revision", "full-lowercase-commit-required")
	}
	if filepath.IsAbs(m.Source) && sourceRevision.MatchString(m.Revision) && !sourceClean(ctx, m) {
		bad("matrix.source", "dirty-unreadable-or-revision-mismatch")
	}
	if filepath.IsAbs(m.Source) {
		for _, name := range []string{"delegation-matrix.ts", "routing-policy.ts", "contract.ts", "cli/delegation-matrix-table.ts"} {
			st, err := os.Stat(filepath.Join(m.Source, ".llm", "tools", "agentic", "runtime", name))
			if err != nil || !st.Mode().IsRegular() {
				bad("matrix.source", "source-contract-files-unavailable")
				break
			}
		}
	}
	if !privateReceiptRoot(m.ReceiptRoot) {
		bad("matrix.receipt_root", "existing-private-nonsymlink-directory-outside-git-required")
	}
	if !repositoryName.MatchString(cfg.Inbox) {
		bad("inbox", "repository-required")
	}
	if len(cfg.Targets) == 0 {
		bad("targets", "at-least-one-target-required")
	}
	for i, t := range cfg.Targets {
		field := fmt.Sprintf("targets[%d].repo", i)
		if !repositoryName.MatchString(t.Repo) {
			bad(field, "repository-required")
			continue
		}
		if !sourceRevision.MatchString(m.TargetRevisions[t.Repo]) {
			bad(fmt.Sprintf("matrix.target_revisions (target %d)", i), "full-lowercase-commit-required")
		}
	}
	for repo, revision := range m.TargetRevisions {
		if !repositoryName.MatchString(repo) || !sourceRevision.MatchString(revision) {
			bad("matrix.target_revisions", "invalid-repository-or-commit")
			break
		}
	}
	for name, p := range m.Pins {
		if !cleanText(name) || !cleanText(p.Model) || !cleanText(p.Effort) {
			bad("matrix.pins", "nonblank-name-model-effort-required")
			break
		}
	}
	seen := map[string]bool{}
	for i, g := range m.Grants {
		prefix := fmt.Sprintf("matrix.grants[%d]", i)
		if !cleanText(g.IssueID) {
			bad(prefix+".issue_id", "issue-node-id-required")
		}
		if !repositoryName.MatchString(g.Repo) {
			bad(prefix+".repo", "target-repository-required")
		}
		if !digestPattern.MatchString(g.BriefDigest) {
			bad(prefix+".brief_digest", "sha256-required-use-matrix-build")
		}
		key := g.IssueID + "\x00" + g.Repo + "\x00" + g.BriefDigest
		if seen[key] {
			bad(prefix, "duplicate-brief-grant")
		}
		seen[key] = true
		if g.Authorization != nil && ((g.Authorization.Authorizer != "owner" && g.Authorization.Authorizer != "milestone_coordinator") || !cleanText(g.Authorization.Rationale)) {
			bad(prefix+".authorization", "named-authorizer-and-rationale-required")
		}
		if g.Override != nil {
			o := g.Override
			if o.Authorizer != "owner" || !cleanText(o.Rationale) || !cleanText(o.WorklogPath) || filepath.IsAbs(o.WorklogPath) || strings.Contains(o.WorklogPath, "\\") || filepath.ToSlash(filepath.Clean(o.WorklogPath)) != o.WorklogPath || strings.HasPrefix(o.WorklogPath, "../") || !cleanText(o.Route.Model) || !cleanText(o.Route.Effort) {
				bad(prefix+".ownerMatrixOverride", "owner-route-and-relative-worklog-required")
			}
			if o.Pin != "" {
				if p, ok := m.Pins[o.Pin]; !ok || p != o.Route {
					bad(prefix+".ownerMatrixOverride.pin", "configured-pin-must-match-authorized-route")
				}
			}
		}
	}
	if _, err := exec.LookPath("deno"); err != nil {
		bad("matrix.source", "deno-unavailable")
	}
	return out
}

type matrixConfigDeps struct {
	command func(context.Context, string, string, []byte, ...string) ([]byte, error)
	read    func(context.Context, string, string, string) (string, error)
	resolve func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error)
}

func configDeps() matrixConfigDeps {
	return matrixConfigDeps{matrixCommand, readRoutingFile, resolveMatrix}
}

func fetchMatrixIssue(ctx context.Context, cfg *Config, n int, d matrixConfigDeps) (Issue, []matrixConfigProblem) {
	data, err := d.command(ctx, "", "gh", nil, "issue", "view", strconv.Itoa(n), "--repo", cfg.Inbox, "--json", "id,number,title,body")
	var is Issue
	var fields map[string]json.RawMessage
	if strictJSON(data, &fields) != nil {
		return is, configProblem("issue", "complete-issue-read-failed")
	}
	for _, key := range []string{"id", "number", "title", "body"} {
		if value, ok := fields[key]; !ok || string(value) == "null" {
			return is, configProblem("issue", "complete-issue-read-failed")
		}
	}
	if err != nil || strictJSON(data, &is) != nil || is.ID == "" || is.Number != n {
		return is, configProblem("issue", "complete-issue-read-failed")
	}
	return is, nil
}

// This is policy preflight with hypothetical native eligibility, NOT quota or
// placement admission. It never constructs a coordinator, reserves or launches.
func validateMatrixIssue(ctx context.Context, cfg *Config, is Issue, repo string, d matrixConfigDeps) []matrixConfigProblem {
	found := false
	for _, t := range cfg.Targets {
		if t.Repo == repo {
			found = true
		}
	}
	if !found {
		return configProblem("target", "repository-not-configured")
	}
	o := parseOverrides(is.Body)
	req, err := prepareMatrixRequest(cfg.Matrix, is, repo, o)
	if err != nil {
		r := refusalFor(err)
		return configProblem(matrixReasons[r.ReasonCode].field, r.ReasonCode)
	}
	profile := o.Profile
	if profile == "" {
		profile = "leaf"
	}
	if !profileStem.MatchString(profile) {
		return configProblem("issue.profile", "profile-invalid")
	}
	req.ProfileText, err = d.read(ctx, repo, cfg.Matrix.TargetRevisions[repo], "profiles/"+profile+".md")
	if err != nil || req.ProfileText == "" {
		return configProblem("matrix.target_revisions", "profile-unavailable")
	}
	if req.Override != nil {
		req.WorklogText, err = d.read(ctx, repo, cfg.Matrix.TargetRevisions[repo], req.Override.WorklogPath)
		if err != nil {
			return configProblem("matrix.grants.ownerMatrixOverride.worklogPath", "override-worklog-unavailable")
		}
	}
	req.Available = []string{"claude", "codex", "agy"}
	route, err := d.resolve(ctx, cfg.Matrix, req)
	if err != nil {
		r := refusalFor(err)
		return configProblem(matrixReasons[r.ReasonCode].field, r.ReasonCode)
	}
	if strings.HasSuffix(route.Role, "_evaluation") {
		return configProblem("route.observed", "observer-unavailable")
	}
	if o.Router != "" {
		return configProblem("issue.router", "router-unsupported")
	}
	if o.Harness != "" && o.Harness != route.Transport && !(o.Harness == "codex-run" && route.Transport == "codex") {
		return configProblem("issue.harness", "harness-conflict")
	}
	return nil
}

func buildMatrixConfig(ctx context.Context, cfg *Config, source, receipt string, d matrixConfigDeps) []matrixConfigProblem {
	if source != "" {
		cfg.Matrix.Source = source
	}
	if receipt != "" {
		cfg.Matrix.ReceiptRoot = receipt
	}
	absolute, err := filepath.Abs(cfg.Matrix.Source)
	if cfg.Matrix.Source == "" || err != nil {
		return configProblem("matrix.source", "checkout-required")
	}
	source, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return configProblem("matrix.source", "checkout-unreadable")
	}
	cfg.Matrix.Source = source
	head, err := d.command(ctx, source, "git", nil, "rev-parse", "HEAD")
	if err != nil {
		return configProblem("matrix.source", "checkout-unreadable")
	}
	revision := strings.TrimSpace(string(head))
	if cfg.Matrix.Revision != "" && cfg.Matrix.Revision != revision {
		return configProblem("matrix.revision", "existing-pin-mismatch-not-overwritten")
	}
	cfg.Matrix.Revision = revision
	if cfg.Matrix.TargetRevisions == nil {
		cfg.Matrix.TargetRevisions = map[string]string{}
	}
	for _, t := range cfg.Targets {
		if !repositoryName.MatchString(t.Repo) {
			return configProblem("targets.repo", "repository-required")
		}
		if cfg.Matrix.TargetRevisions[t.Repo] != "" {
			continue
		}
		b, err := d.command(ctx, "", "gh", nil, "api", "repos/"+t.Repo+"/commits/HEAD", "--jq", ".sha")
		if err != nil {
			return configProblem("matrix.target_revisions", "target-head-read-failed")
		}
		cfg.Matrix.TargetRevisions[t.Repo] = strings.TrimSpace(string(b))
	}
	if cfg.Matrix.Pins == nil {
		cfg.Matrix.Pins = map[string]MatrixPin{}
	}
	if cfg.Matrix.Grants == nil {
		cfg.Matrix.Grants = []MatrixGrant{}
	}
	return validateMatrixConfig(ctx, cfg)
}

// The input is an existing MatrixGrant with binding fields omitted. Authority is
// supplied by the operator, never synthesized by the builder or issue body.
func addMatrixGrant(cfg *Config, is Issue, repo string, data []byte) []matrixConfigProblem {
	var grant MatrixGrant
	if strictJSON(data, &grant) != nil || grant.IssueID != "" || grant.Repo != "" || grant.BriefDigest != "" {
		return configProblem("grant-input", "omit-generated-issue-id-repo-and-brief-digest")
	}
	grant.IssueID, grant.Repo, grant.BriefDigest = is.ID, repo, briefDigest(is)
	for _, old := range cfg.Matrix.Grants {
		if old.IssueID == grant.IssueID && old.Repo == repo && old.BriefDigest == grant.BriefDigest {
			return configProblem("matrix.grants", "matching-grant-already-exists")
		}
	}
	cfg.Matrix.Grants = append(cfg.Matrix.Grants, grant)
	return nil
}

func matrixConfigCLI(args []string, stdout, stderr io.Writer) int {
	problems := func(p []matrixConfigProblem) int {
		for _, x := range p {
			fmt.Fprintf(stderr, "%s: %s\n", x.Field, x.Reason)
		}
		return 2
	}
	if len(args) == 0 || (args[0] != "build" && args[0] != "validate") {
		return problems(configProblem("command", "use-matrix-build-or-matrix-validate"))
	}
	fs := flag.NewFlagSet("matrix", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", "", "private dispatcher config")
	source := fs.String("source", "", "NetScript checkout (build only)")
	receipt := fs.String("receipt-root", "", "existing private receipt directory (build only)")
	output := fs.String("out", "", "new private candidate config (build only)")
	grant := fs.String("grant-input", "", "private MatrixGrant without generated binding fields (build only)")
	issue := fs.Int("issue", 0, "inbox issue number for complete-brief preflight")
	target := fs.String("target", "", "configured target repository")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *config == "" || *issue < 0 || (*issue > 0) != (*target != "") {
		return problems(configProblem("arguments", "config-required-issue-and-target-must-be-paired"))
	}
	build := args[0] == "build"
	if !build && (*source != "" || *receipt != "" || *output != "" || *grant != "") {
		return problems(configProblem("arguments", "build-only-option-on-validate"))
	}
	if build && (*output == "" || *issue < 1) {
		return problems(configProblem("arguments", "build-requires-out-issue-and-target"))
	}
	if build && (!filepath.IsAbs(*output) || !privateReceiptRoot(filepath.Dir(*output))) {
		return problems(configProblem("out", "new-file-in-private-directory-outside-git-required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg, raw, p := readMatrixConfigFile(*config)
	if len(p) > 0 {
		return problems(p)
	}
	deps := configDeps()
	if build {
		p = buildMatrixConfig(ctx, cfg, *source, *receipt, deps)
	} else {
		p = validateMatrixConfig(ctx, cfg)
	}
	if len(p) > 0 {
		return problems(p)
	}
	if *issue > 0 {
		is, p := fetchMatrixIssue(ctx, cfg, *issue, deps)
		if len(p) > 0 {
			return problems(p)
		}
		if *grant != "" {
			b, err := os.ReadFile(*grant)
			if err != nil || len(b) > 1024*1024 {
				return problems(configProblem("grant-input", "unreadable-or-oversized"))
			}
			if p = addMatrixGrant(cfg, is, *target, b); len(p) > 0 {
				return problems(p)
			}
			if p = validateMatrixConfig(ctx, cfg); len(p) > 0 {
				return problems(p)
			}
		}
		if p = validateMatrixIssue(ctx, cfg, is, *target, deps); len(p) > 0 {
			return problems(p)
		}
	}
	if build {
		block, err := json.Marshal(cfg.Matrix)
		if err != nil {
			return problems(configProblem("matrix", "encoding-failed"))
		}
		raw["matrix"] = block
		data, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return problems(configProblem("config", "encoding-failed"))
		}
		f, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return problems(configProblem("out", "create-new-file-failed"))
		}
		_, err = f.Write(append(data, '\n'))
		synced := f.Sync()
		closed := f.Close()
		if err != nil || synced != nil || closed != nil {
			os.Remove(*output)
			return problems(configProblem("out", "write-failed"))
		}
		fmt.Fprintln(stdout, "matrix candidate written; existing configuration and authority preserved")
	}
	fmt.Fprintln(stdout, "matrix configuration valid; live quota, host placement and launch are UNCHECKED")
	if *issue == 0 {
		fmt.Fprintln(stdout, "issue authorization and profile policy are UNCHECKED; pass -issue and -target")
	} else {
		fmt.Fprintln(stdout, "complete issue policy preflight passed with hypothetical native transport eligibility")
	}
	return 0
}
