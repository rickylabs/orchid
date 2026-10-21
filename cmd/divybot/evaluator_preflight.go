package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// This command ends before coordinator construction. It never admits, reserves or launches.
// INTERIM source bridge: replaceable model/config data is owned by Harness #270, not this daemon.
//
//go:embed evaluator-preflight-bridge.ts
var evaluatorPreflightBridge string

type evaluatorCatalogRequest struct {
	Tier                  string           `json:"tier"`
	Role                  string           `json:"role"`
	GeneratorModel        string           `json:"generatorModel"`
	Authorization         *MatrixAuthority `json:"authorization,omitempty"`
	UnavailableTransports []string         `json:"unavailableTransports,omitempty"`
}
type evaluatorCatalogResult struct {
	Status            string `json:"status"`
	ReasonCode        string `json:"reasonCode,omitempty"`
	Launcher          string `json:"launcher,omitempty"`
	Model             string `json:"model,omitempty"`
	LogicalModel      string `json:"logicalModel,omitempty"`
	Family            string `json:"family,omitempty"`
	Effort            string `json:"effort,omitempty"`
	CatalogObservedAt string `json:"catalogObservedAt,omitempty"`
}

var catalogIdentity = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:/-]{0,255}$`)
var catalogName = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

func validCatalogRequest(r evaluatorCatalogRequest) bool {
	if !catalogName.MatchString(r.Tier) || !catalogName.MatchString(r.GeneratorModel) {
		return false
	}
	if !catalogName.MatchString(r.Role) {
		return false
	}
	if r.Authorization != nil && (len(r.Authorization.Rationale) > 4096 || !cleanText(r.Authorization.Rationale) ||
		(r.Authorization.Authorizer != "owner" && r.Authorization.Authorizer != "milestone_coordinator")) {
		return false
	}
	if len(r.UnavailableTransports) > 64 {
		return false
	}
	for _, name := range r.UnavailableTransports {
		if !catalogName.MatchString(name) {
			return false
		}
	}
	return true
}
func validCatalogResult(r evaluatorCatalogResult) bool {
	if r.Status == "launchable" {
		_, err := time.Parse(time.RFC3339Nano, r.CatalogObservedAt)
		return err == nil && r.ReasonCode == "" && r.Launcher == "opencode" && catalogIdentity.MatchString(r.Model) &&
			catalogName.MatchString(r.LogicalModel) && catalogName.MatchString(r.Family) && catalogName.MatchString(r.Effort)
	}
	if r.Status != "refused" || r.LogicalModel != "" || r.Family != "" || r.Effort != "" || r.CatalogObservedAt != "" {
		return false
	}
	switch r.ReasonCode {
	case "launcher-model-unconfigured", "launcher-catalog-unavailable", "launcher-model-absent":
		return catalogIdentity.MatchString(r.Model) && catalogName.MatchString(r.Launcher)
	case "routing-invalid", "source-contract-unavailable":
		return r.Model == "" && r.Launcher == ""
	}
	return false
}

// evaluatorOpenCodeBinary resolves the configured OpenCode CLI on this host: an
// absolute path, or `$HOME/`-relative as in `opencode.binary`. Empty when unusable.
func evaluatorOpenCodeBinary(configured string) string {
	if !openCodeBinaryPath.MatchString(configured) || strings.Contains(configured, "/../") || strings.Contains(configured, "/./") {
		return ""
	}
	if rest, ok := strings.CutPrefix(configured, "$HOME/"); ok {
		home := os.Getenv("HOME")
		if !filepath.IsAbs(home) {
			return ""
		}
		return filepath.Join(home, rest)
	}
	return configured
}
func evaluatorCatalogPreflight(ctx context.Context, cfg MatrixConfig, request evaluatorCatalogRequest, binary string) (evaluatorCatalogResult, error) {
	var result evaluatorCatalogResult
	if !validCatalogRequest(request) || !filepath.IsAbs(binary) {
		return result, errMatrix
	}
	ctx, cancel := context.WithTimeout(ctx, matrixResolveTimeout)
	defer cancel()
	if err := sourceCheck(ctx, cfg); err != nil {
		return result, err
	}
	script, err := os.CreateTemp("", "evaluator-preflight-*.ts")
	if err != nil {
		return result, errMatrix
	}
	defer os.Remove(script.Name())
	_, writeErr := script.WriteString(evaluatorPreflightBridge)
	closeErr := script.Close()
	if writeErr != nil || closeErr != nil {
		return result, errMatrix
	}
	// The bridge passes this exact executable to Harness's preflight and may run nothing else.
	input, err := json.Marshal(struct {
		evaluatorCatalogRequest
		Binary string `json:"binary"`
	}{request, binary})
	if err != nil {
		return result, errMatrix
	}
	out, err := matrixCommand(ctx, cfg.Source, "deno", input, "run", "--no-config", "--no-lock", "--no-prompt", "--allow-read="+cfg.Source, "--allow-run="+binary, script.Name())
	if err != nil {
		return result, err
	}
	if err = sourceCheck(ctx, cfg); err != nil {
		return result, err
	}
	if strictJSON(out, &result) != nil || !validCatalogResult(result) {
		return evaluatorCatalogResult{}, errMatrix
	}
	return result, nil
}
func evaluatorCatalogCLI(args []string, stdout io.Writer) int {
	refuse := func(code string) int {
		_ = json.NewEncoder(stdout).Encode(evaluatorCatalogResult{Status: "refused", ReasonCode: code})
		return 2
	}
	fs := flag.NewFlagSet("evaluator-preflight", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "clean pinned Harness checkout")
	revision := fs.String("revision", "", "exact Harness head")
	requestPath := fs.String("request", "", "JSON evaluator request")
	opencode := fs.String("opencode", "", "the host's OpenCode CLI, as configured in opencode.binary")
	if fs.Parse(args) != nil || fs.NArg() != 0 || !filepath.IsAbs(*source) || !sourceRevision.MatchString(*revision) || *requestPath == "" {
		return refuse("arguments-invalid")
	}
	binary := evaluatorOpenCodeBinary(*opencode)
	if binary == "" {
		return refuse("opencode-binary-unconfigured")
	}
	file, err := os.OpenFile(*requestPath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return refuse("request-invalid")
	}
	stat, statErr := file.Stat()
	if statErr != nil || !stat.Mode().IsRegular() {
		file.Close()
		return refuse("request-invalid")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 64*1024+1))
	closeErr := file.Close()
	var request evaluatorCatalogRequest
	if readErr != nil || closeErr != nil || len(raw) > 64*1024 || strictJSON(raw, &request) != nil || !validCatalogRequest(request) {
		return refuse("request-invalid")
	}
	result, err := evaluatorCatalogPreflight(context.Background(), MatrixConfig{Source: *source, Revision: *revision}, request, binary)
	if err != nil {
		return refuse("preflight-unavailable")
	}
	// A positive catalog result never claims I2 or live dispatch admission.
	output := struct {
		evaluatorCatalogResult
		DispatchStatus     string `json:"dispatchStatus"`
		DispatchReasonCode string `json:"dispatchReasonCode"`
	}{result, "inconclusive", "observer-unavailable"}
	if json.NewEncoder(stdout).Encode(output) != nil {
		return 2
	}
	if result.Status != "launchable" {
		return 2
	}
	return 0
}
