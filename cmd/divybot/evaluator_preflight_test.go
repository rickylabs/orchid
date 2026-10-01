package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func catalogFixture(t *testing.T) (MatrixConfig, string) {
	t.Helper()
	cfg := syntheticSource(t)
	runtime := filepath.Join(cfg.Source, "packages", "routing", "matrix")
	f, err := os.OpenFile(filepath.Join(runtime, "delegation-matrix.ts"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("fixture setup failed")
	}
	f.WriteString(`
export const MATRIX_AUTHORITY={configuration:{roles:{implementation_evaluation:{certifies:"any"}}}};`)
	f.Close()
	writeFixture(t, filepath.Join(runtime, "launchability.ts"), `export class RouteLaunchError extends Error {constructor(code,model,launcher='opencode'){super('fixed');this.code=code;this.model=model;this.launcher=launcher;}}`)
	writeFixture(t, filepath.Join(runtime, "opencode-preflight.ts"), `
import {RouteLaunchError} from './launchability.ts';
export async function preflightWorkloadRoute(r){
 if(r.tier!=='feature'||r.role!=='implementation_evaluation'||r.generatorModel!=='invented_writer')throw Error('private-native-canary');
 const output=await new Deno.Command('opencode',{args:['models','synthetic'],stdout:'piped',stderr:'null'}).output();
 if(!output.success)throw new RouteLaunchError('launcher-catalog-unavailable','synthetic/critic');
 if(!new TextDecoder().decode(output.stdout).split(/\r?\n/).includes('synthetic/critic'))throw new RouteLaunchError('launcher-model-absent','synthetic/critic');
 return {model:'synthetic/critic',logicalModel:'invented_critic',family:'invented_family',effort:'high',catalogObservedAt:'2026-09-30T00:00:00Z',launchability:{launcher:'opencode'}};
}`)
	testCommand(t, cfg.Source, "git", "add", ".")
	testCommand(t, cfg.Source, "git", "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic catalog contract")
	cfg.Revision = testCommand(t, cfg.Source, "git", "rev-parse", "HEAD")
	bin := t.TempDir()
	launcher := filepath.Join(bin, "opencode")
	writeFixture(t, launcher, "#!/bin/sh\n[ \"$1\" = models ] && [ \"$2\" = synthetic ] || exit 77\nprintf 'synthetic/critic\\n'\n")
	if os.Chmod(launcher, 0700) != nil {
		t.Fatal("launcher setup failed")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return cfg, launcher
}
func catalogRequest() evaluatorCatalogRequest {
	return evaluatorCatalogRequest{Tier: "feature", Role: "implementation_evaluation", GeneratorModel: "invented_writer"}
}
func TestEvaluatorCatalogPreflight(t *testing.T) {
	cfg, launcher := catalogFixture(t)
	good, err := evaluatorCatalogPreflight(context.Background(), cfg, catalogRequest())
	if err != nil || good.Status != "launchable" || good.Model != "synthetic/critic" {
		t.Fatal("catalog positive control did not pass")
	}
	writeFixture(t, launcher, "#!/bin/sh\n[ \"$1\" = models ] && [ \"$2\" = synthetic ] || exit 77\nexit 0\n")
	os.Chmod(launcher, 0700)
	bad, err := evaluatorCatalogPreflight(context.Background(), cfg, catalogRequest())
	if err != nil || bad.Status != "refused" || bad.ReasonCode != "launcher-model-absent" || bad.Model != "synthetic/critic" {
		t.Fatal("missing launcher ID was not named")
	}
	if evaluatorRefusal().ReasonCode != "observer-unavailable" {
		t.Fatal("catalog probe waived dispatch evidence")
	}
	wrong := cfg
	wrong.Revision = strings.Repeat("f", 40)
	if _, err = evaluatorCatalogPreflight(context.Background(), wrong, catalogRequest()); err == nil {
		t.Fatal("wrong source pin accepted")
	}
	writeFixture(t, filepath.Join(cfg.Source, "untracked"), "synthetic")
	if _, err = evaluatorCatalogPreflight(context.Background(), cfg, catalogRequest()); err == nil {
		t.Fatal("dirty source accepted")
	}
}
func TestEvaluatorCatalogCLI(t *testing.T) {
	cfg, _ := catalogFixture(t)
	request := filepath.Join(t.TempDir(), "request.json")
	raw, _ := json.Marshal(catalogRequest())
	writeFixture(t, request, string(raw))
	args := []string{"evaluator-preflight", "-source", cfg.Source, "-revision", cfg.Revision, "-request", request}
	var out, stderr bytes.Buffer
	if matrixConfigCLI(args, &out, &stderr) != 0 {
		t.Fatal("read-only CLI did not pass catalog control")
	}
	var receipt map[string]any
	if json.Unmarshal(out.Bytes(), &receipt) != nil {
		t.Fatal("CLI output not JSON")
	}
	if receipt["dispatchStatus"] != "inconclusive" || receipt["dispatchReasonCode"] != "observer-unavailable" {
		t.Fatal("CLI certified dispatch from catalog membership")
	}
	for _, key := range []string{"worktree", "sessionId", "source", "authorization"} {
		if _, ok := receipt[key]; ok {
			t.Fatal("private input projected")
		}
	}
	writeFixture(t, request, `{"tier":"feature","role":"implementation_evaluation","generatorModel":"invented_writer","unexpected":"private-canary"}`)
	out.Reset()
	if matrixConfigCLI(args, &out, &stderr) != 2 || strings.Contains(out.String(), "private-canary") {
		t.Fatal("unknown request field was accepted or reflected")
	}
}
func TestEvaluatorCatalogClosedEnvelope(t *testing.T) {
	for _, value := range []evaluatorCatalogResult{
		{Status: "launchable", Launcher: "opencode", Model: "/private/canary", LogicalModel: "critic", Family: "family", Effort: "high", CatalogObservedAt: "2026-09-30T00:00:00Z"},
		{Status: "launchable", Launcher: "opencode", Model: "synthetic/critic", LogicalModel: "critic", Family: "family", Effort: "high", CatalogObservedAt: "bad-time"},
		{Status: "refused", ReasonCode: "private-canary"},
		{Status: "refused", ReasonCode: "routing-invalid", Model: "synthetic/critic"},
	} {
		if validCatalogResult(value) {
			t.Fatal("invalid catalog result accepted")
		}
	}
}
