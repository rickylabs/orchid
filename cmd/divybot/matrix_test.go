package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// All identities in this suite are invented; no fixture is an operational receipt.
func syntheticRoute() matrixRoute {
	return matrixRoute{Provider: "synthetic-router", Model: "synthetic-physical", LogicalModel: "synthetic-logical", Effort: "medium", RequestedEffort: "medium", Transport: "claude", Family: "synthetic-family", Tier: "feature", Role: "implementation", Digest: strings.Repeat("a", 64)}
}
func privateTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("private fixture setup failed")
	}
	return root
}
func writeFixture(t *testing.T, name, data string) {
	t.Helper()
	if os.MkdirAll(filepath.Dir(name), 0700) != nil || os.WriteFile(name, []byte(data), 0600) != nil {
		t.Fatal("fixture write failed")
	}
}
func testCommand(t *testing.T, cwd, command string, args ...string) string {
	t.Helper()
	b, e := matrixCommand(context.Background(), cwd, command, nil, args...)
	if e != nil {
		t.Fatal("fixture command failed")
	}
	return strings.TrimSpace(string(b))
}

func TestCommonMatrixAttempt(t *testing.T) {
	cases := []string{"success", "wrong-issue", "invalid-inbox", "missing-config", "missing-identity", "invalid-profile", "duplicate-routing", "unknown-pin", "mixed-pin", "no-quota", "stale-quota", "expired-bucket", "exhausted-account", "no-capacity", "resolver-failure", "profile-failure", "persist-failure", "nil-receipt", "evaluator-refused", "evaluator-inconclusive", "wrong-harness", "router-substitution", "dry-run"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			root := privateTestRoot(t)
			cfg := &Config{Inbox: "example/inbox", Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: strings.Repeat("b", 40), TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)}}, Governor: Gov{WeeklyCeiling: 92}}
			c := &Coord{cfg: cfg}
			now := time.Now()
			q := quota{ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()}}
			c.gov.q = map[string]quota{"claude": q}
			is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic", Body: "/swarm\ntier: feature\nrole: implementation\n\nSynthetic task"}
			route := syntheticRoute()
			budget := map[string]int{"claude": 1}
			events := []string{}
			refusals := []matrixRefusal{}
			deps := matrixAttemptDeps{
				report: func(refusal matrixRefusal) { refusals = append(refusals, refusal) },
				read: func(context.Context, string, string, string) (string, error) {
					events = append(events, "profile")
					return "| `routing` | matrix `implementation` row |", nil
				},
				resolve: func(_ context.Context, _ MatrixConfig, req matrixRequest) (matrixRoute, error) {
					events = append(events, "resolve")
					if !containsString(req.Available, "claude") {
						return matrixRoute{}, errMatrix
					}
					return route, nil
				},
				host: func(Target, string) (Host, bool) { events = append(events, "host"); return Host{}, true },
				persist: func(root, key, command string, r matrixReceipt, binding any) (*durableMatrixReceipt, error) {
					events = append(events, "persist")
					return persistMatrixReceipt(root, key, command, r, binding)
				},
				launch: func(_ context.Context, _ int, _ Issue, _ Host, agent string, o Overrides, r *durableMatrixReceipt) bool {
					events = append(events, "launch")
					if agent != "claude" || o.Model != route.Model || o.Effort != route.Effort || !r.claim(buildAgentCmd(agent, o)) {
						t.Fatal("launch did not consume the selected durable receipt")
					}
					data, err := os.ReadFile(filepath.Join(filepath.Dir(r.file), "dispatch.json"))
					var binding dispatchBinding
					if err != nil || json.Unmarshal(data, &binding) != nil || binding.Issue.Repo != "example/inbox" || binding.Issue.Number != 1 || binding.ParentRunID != nil || binding.Profile != "leaf" || binding.Provider != "synthetic-router" || binding.State != "reserved" {
						t.Fatal("authoritative inbox issue was not bound before launch")
					}
					return true
				},
			}
			switch name {
			case "wrong-issue":
				is.Number = 2
			case "invalid-inbox":
				cfg.Inbox = ""
			case "missing-config":
				cfg.Matrix.Revision = ""
			case "missing-identity":
				is.ID = ""
			case "invalid-profile":
				is.Body = "/swarm\nprofile: ../escape\ntier: feature"
			case "duplicate-routing":
				is.Body = "/swarm\nmodel: first\nmodel: second"
			case "unknown-pin":
				is.Body = "/swarm\npin: absent"
			case "mixed-pin":
				cfg.Matrix.Pins = map[string]MatrixPin{"choice": {"configured", "medium"}}
				is.Body = "/swarm\npin: choice\nmodel: other"
			case "no-quota":
				delete(c.gov.q, "claude")
			case "stale-quota":
				q.at = now.Add(-time.Hour)
				c.gov.q["claude"] = q
			case "expired-bucket":
				q.five.ResetsAt = now.Add(-time.Second).Unix()
				c.gov.q["claude"] = q
			case "exhausted-account":
				budget["claude"] = 0
			case "no-capacity":
				deps.host = func(Target, string) (Host, bool) { events = append(events, "host"); return Host{}, false }
			case "resolver-failure":
				deps.resolve = func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) {
					events = append(events, "resolve")
					return matrixRoute{}, errors.New("synthetic-private-error")
				}
			case "profile-failure":
				deps.read = func(context.Context, string, string, string) (string, error) { return "", errMatrix }
			case "persist-failure":
				deps.persist = func(string, string, string, matrixReceipt, any) (*durableMatrixReceipt, error) { return nil, errMatrix }
			case "nil-receipt":
				deps.persist = func(string, string, string, matrixReceipt, any) (*durableMatrixReceipt, error) { return nil, nil }
			case "evaluator-inconclusive":
				deps.resolve = func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) {
					return matrixRoute{}, errEvaluatorEvidence
				}
			case "evaluator-refused":
				route.Role = "implementation_evaluation"
			case "wrong-harness":
				is.Body = "/swarm\nharness: codex\ntier: feature"
			case "router-substitution":
				is.Body = "/swarm\nrouter: arbitrary\ntier: feature"
			case "dry-run":
				c.dry = true
			}
			_, ok := c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, budget, deps)
			success := name == "success" || name == "dry-run"
			if strings.HasPrefix(name, "evaluator-") {
				if !reflect.DeepEqual(refusals, []matrixRefusal{evaluatorRefusal()}) {
					t.Fatal("evaluator refusal became a silent skip")
				}
				if containsString(events, "persist") || containsString(events, "host") {
					t.Fatal("evaluator refusal reached launch preparation")
				}
			} else if len(refusals) != 0 {
				t.Fatal("unrelated failure mislabeled as missing observer")
			}
			if ok != success {
				t.Fatalf("unexpected admission for %s", name)
			}
			if name == "success" && !reflect.DeepEqual(events, []string{"profile", "resolve", "host", "persist", "launch"}) {
				t.Fatal("resolution, host, persistence and effect order changed")
			}
			if name != "success" && containsString(events, "launch") {
				t.Fatal("refused attempt reached effect")
			}
		})
	}
}

func TestPinsAndAuthorityAreBoundConfiguration(t *testing.T) {
	is := Issue{ID: "synthetic-node", Title: "Synthetic", Body: "/swarm\ntier: architecture\npin: chosen"}
	cfg := MatrixConfig{Pins: map[string]MatrixPin{"chosen": {"replaceable-logical", "medium"}}, Grants: []MatrixGrant{{IssueID: is.ID, Repo: "example/project", BriefDigest: briefDigest(is), Authorization: &MatrixAuthority{"owner", "Synthetic rationale"}}}}
	req, e := prepareMatrixRequest(cfg, is, "example/project", parseOverrides(is.Body))
	if e != nil || req.PinName != "chosen" || req.Pin.Model != "replaceable-logical" || req.Authorization == nil {
		t.Fatal("configured pin or authority was lost")
	}
	cfg.Pins["chosen"] = MatrixPin{"different-logical", "high"}
	req, e = prepareMatrixRequest(cfg, is, "example/project", parseOverrides(is.Body))
	if e != nil || req.Pin.Model != "different-logical" {
		t.Fatal("pin values were hard-coded")
	}
	for _, change := range []string{"body", "title", "node", "repo"} {
		other := is
		repo := "example/project"
		switch change {
		case "body":
			other.Body += "\nChanged"
		case "title":
			other.Title = "Changed"
		case "node":
			other.ID = "different-node"
		case "repo":
			repo = "example/other"
		}
		req, _ := prepareMatrixRequest(cfg, other, repo, parseOverrides(other.Body))
		if req.Authorization != nil {
			t.Fatal("authority escaped its binding")
		}
	}
	cfg.Grants = append(cfg.Grants, cfg.Grants[0])
	if _, e := prepareMatrixRequest(cfg, is, "example/project", parseOverrides(is.Body)); e == nil {
		t.Fatal("duplicate authority accepted")
	}
}

func TestReceiptPersistenceAndEffectBoundary(t *testing.T) {
	root := privateTestRoot(t)
	r := receiptFor(MatrixConfig{Revision: strings.Repeat("b", 40)}, syntheticRoute())
	key := strings.Repeat("c", 64)
	handle, e := persistMatrixReceipt(root, key, "synthetic-command", r, struct{}{})
	if e != nil {
		t.Fatal("durable receipt failed")
	}
	for _, name := range []string{"receipt.json", "binding.json"} {
		st, e := os.Stat(filepath.Join(root, key, "record", name))
		if e != nil || st.Mode().Perm() != 0600 {
			t.Fatal("private permissions lost")
		}
	}
	if handle.claim("different-command") || !handle.claim("synthetic-command") || handle.claim("synthetic-command") {
		t.Fatal("single-use command binding failed")
	}
	if _, e := persistMatrixReceipt(root, key, "synthetic-command", r, nil); e == nil {
		t.Fatal("restart replay was admitted")
	}
	if _, _, e := (Host{}).spawnAgent(context.Background(), "", "", nil, "", nil); e == nil {
		t.Fatal("bare effect bypass accepted")
	}
	forged := &durableMatrixReceipt{}
	if _, _, e := (Host{}).spawnAgent(context.Background(), "", "", nil, "", forged); e == nil {
		t.Fatal("unpersisted effect bypass accepted")
	}
	writeFixture(t, filepath.Join(root, ".git"), "synthetic marker")
	if privateReceiptRoot(root) {
		t.Fatal("receipt directory in checkout accepted")
	}
}

func TestStrictBridgeEnvelope(t *testing.T) {
	for _, bad := range []string{`{"model":"one","mo\u0064el":"two"}`, `{"model":"one"} {}`, `{"unexpected":true}`, `[]`, ``} {
		var r matrixRoute
		if strictJSON([]byte(bad), &r) == nil {
			t.Fatal("malformed or ambiguous envelope accepted")
		}
	}
}

func syntheticSource(t *testing.T) MatrixConfig {
	t.Helper()
	root := privateTestRoot(t)
	runtime := filepath.Join(root, ".llm", "tools", "agentic", "runtime")
	writeFixture(t, filepath.Join(runtime, "contract.ts"), `export const EFFORTS=["medium","high"]; export const PROVIDER_KINDS=["invented-router"];`)
	writeFixture(t, filepath.Join(runtime, "delegation-matrix.ts"), `
export const WORKLOAD_TIERS=["feature","architecture"];
export const DELEGATION_ROLES=["implementation","deep_research","plan_evaluation","implementation_evaluation","vision_evaluation"];
export const COORDINATOR_TIERS=["milestone"];
export const MODEL_TRANSPORTS=["claude","codex","unsupported"];
export const MODEL_CATALOG={"invented-primary":{},"invented-alternate":{}};
export const COORDINATOR_MATRIX={milestone:[{model:"invented-primary",effort:"medium"}]};
export function assertPrivilegedTierAuthorization(t,a){if(t==="architecture"&&!a?.rationale)throw Error();}
export function assertOwnerMatrixOverride(t,r,a){if(a.authorizer!=="owner"||!a.rationale||a.worklogPath!==".llm/runs/synthetic/worklog.md")throw Error();}
export function ownerMatrixOverrideWorklogEntry(){return "Synthetic exact owner grant";}
export function isTransportAllowedForRole(r,t){return r!=="deep_research"||t==="codex";}
`)
	writeFixture(t, filepath.Join(runtime, "routing-policy.ts"), `
export function resolveWorkloadRoute(r){
 const transport=r.role==="deep_research"?"codex":"claude";
 if(r.unavailableTransports.includes(transport))throw Error();
 const route=r.ownerMatrixOverride?.route||{model:"invented-primary",effort:"medium"};
 return {provider:"invented-router",model:route.model+"-physical",logicalModel:route.model,effort:route.effort,requestedEffort:route.effort,transport,family:"invented-family",...(r.ownerMatrixOverride?{ownerMatrixOverride:r.ownerMatrixOverride}:{})};
}
export const resolveCoordinatorRoute=resolveWorkloadRoute;
`)
	writeFixture(t, filepath.Join(runtime, "cli", "delegation-matrix-table.ts"), `
const args=Deno.args;const tier=args[args.indexOf("--tier")+1];
const routes=[{model:"invented-primary",effort:"medium"}];
console.log(JSON.stringify({schemaVersion:1,tiers:[{tier,routes}],coordinators:{milestone:routes}}));
`)
	testCommand(t, root, "git", "init", "-q")
	testCommand(t, root, "git", "add", ".")
	msg := filepath.Join(t.TempDir(), "commit-message")
	writeFixture(t, msg, "Synthetic matrix fixture\n")
	testCommand(t, root, "git", "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-q", "-F", msg)
	return MatrixConfig{Source: root, Revision: testCommand(t, root, "git", "rev-parse", "HEAD")}
}

func TestFirstPartyBridgeBoundary(t *testing.T) {
	if _, e := exec.LookPath("deno"); e != nil {
		t.Fatal("Deno is required for the bridge gate")
	}
	cfg := syntheticSource(t)
	base := matrixRequest{Tier: "feature", Role: "implementation", Available: []string{"claude", "codex"}, ProfileText: "| `routing` | matrix `implementation` row; evaluator `implementation_evaluation` |"}
	for _, name := range []string{"matrix", "profile-default", "configured-override", "named-pin", "wrong-pin-name", "missing-pin-name", "changed-pin-value", "missing-grant", "missing-worklog", "invalid-authorizer", "privileged-without-authority", "override-does-not-waive-privilege", "authorized-privilege", "unknown-effort", "unknown-model", "evaluator", "profile-restriction", "unavailable", "coordinator"} {
		t.Run(name, func(t *testing.T) {
			req := base
			pass := false
			grant := &MatrixOverride{Authorizer: "owner", Rationale: "Synthetic rationale", WorklogPath: ".llm/runs/synthetic/worklog.md", Route: MatrixPin{"invented-alternate", "high"}}
			switch name {
			case "matrix":
				pass = true
			case "profile-default":
				req.Role = ""
				pass = true
			case "configured-override":
				req.Pin = &grant.Route
				req.Override = grant
				req.WorklogText = "Synthetic exact owner grant"
				pass = true
			case "named-pin", "wrong-pin-name", "missing-pin-name", "changed-pin-value":
				req.PinName = "chosen"
				requested := grant.Route
				req.Pin, req.Override, req.WorklogText = &requested, grant, "Synthetic exact owner grant"
				grant.Pin = "chosen"
				switch name {
				case "named-pin":
					pass = true
				case "wrong-pin-name":
					grant.Pin = "other"
				case "missing-pin-name":
					grant.Pin = ""
				case "changed-pin-value":
					requested.Model = "not-the-authorized-model"
				}
			case "missing-grant":
				req.Pin = &grant.Route
			case "missing-worklog":
				req.Pin = &grant.Route
				req.Override = grant
			case "invalid-authorizer":
				req.Tier = "architecture"
				req.Authorization = &MatrixAuthority{"untrusted", "Synthetic rationale"}
			case "privileged-without-authority":
				req.Tier = "architecture"
			case "override-does-not-waive-privilege":
				req.Tier = "architecture"
				req.Pin = &grant.Route
				req.Override = grant
				req.WorklogText = "Synthetic exact owner grant"
			case "authorized-privilege":
				req.Tier = "architecture"
				req.Authorization = &MatrixAuthority{"owner", "Synthetic rationale"}
				pass = true
			case "unknown-effort":
				grant.Route.Effort = "invented-invalid"
				req.Pin = &grant.Route
				req.Override = grant
				req.WorklogText = "Synthetic exact owner grant"
			case "unknown-model":
				grant.Route.Model = "not-in-catalog"
				req.Pin = &grant.Route
				req.Override = grant
				req.WorklogText = "Synthetic exact owner grant"
			case "evaluator":
				req.Role = "implementation_evaluation"
			case "profile-restriction":
				req.Role = "deep_research"
			case "unavailable":
				req.Available = nil
			case "coordinator":
				req.Tier = ""
				req.Role = ""
				req.ProfileText = "| `routing` | coordinator matrix at `milestone` scope |"
				pass = true
			}
			route, e := resolveMatrix(context.Background(), cfg, req)
			if name == "evaluator" && !errors.Is(e, errEvaluatorEvidence) {
				t.Fatal("bridge dropped inconclusive observer reason")
			}
			if (e == nil) != pass {
				t.Fatalf("unexpected bridge verdict for %s", name)
			}
			if pass && (route.Provider != "invented-router" || !strings.HasPrefix(route.Model, "invented-") || !digestPattern.MatchString(route.Digest)) {
				t.Fatal("bridge lost configured identity or CLI evidence")
			}
		})
	}
	t.Run("changed-source", func(t *testing.T) {
		writeFixture(t, filepath.Join(cfg.Source, "untracked"), "changed")
		if _, e := resolveMatrix(context.Background(), cfg, base); e == nil {
			t.Fatal("dirty source accepted")
		}
	})
}

func TestRealNetScriptReadOnlyProbe(t *testing.T) {
	source := os.Getenv("MATRIX_PROBE_SOURCE")
	revision := os.Getenv("MATRIX_PROBE_REVISION")
	if source == "" {
		t.Skip("optional real-source acceptance; synthetic bridge gate still runs")
	}
	route, e := resolveMatrix(context.Background(), MatrixConfig{Source: source, Revision: revision}, matrixRequest{Tier: "feature", Role: "implementation", Available: []string{"claude", "codex"}})
	if e != nil || route.Model == "" {
		t.Fatal("real first-party resolution failed")
	}
	if output := os.Getenv("MATRIX_SYNTHETIC_RECEIPT"); output != "" {
		b, e := json.Marshal(receiptFor(MatrixConfig{Revision: strings.Repeat("a", 40)}, syntheticRoute()))
		if e != nil || os.WriteFile(output, b, 0600) != nil {
			t.Fatal("synthetic compatibility fixture failed")
		}
	}
}

func TestBridgeProcessFailuresRefuse(t *testing.T) {
	for _, name := range []string{"empty", "malformed", "nonzero", "oversized", "timeout"} {
		t.Run(name, func(t *testing.T) {
			cfg := syntheticSource(t)
			source := map[string]string{"empty": "", "malformed": `console.log("invalid")`, "nonzero": "Deno.exit(1)", "oversized": `console.log("x".repeat(2*1024*1024))`, "timeout": "while(true){}"}[name]
			writeFixture(t, filepath.Join(cfg.Source, ".llm", "tools", "agentic", "runtime", "cli", "delegation-matrix-table.ts"), source)
			testCommand(t, cfg.Source, "git", "add", ".")
			msg := filepath.Join(t.TempDir(), "message")
			writeFixture(t, msg, "Synthetic failure fixture\n")
			testCommand(t, cfg.Source, "git", "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-q", "-F", msg)
			cfg.Revision = testCommand(t, cfg.Source, "git", "rev-parse", "HEAD")
			ctx := context.Background()
			if name == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			}
			if _, e := resolveMatrix(ctx, cfg, matrixRequest{Tier: "feature", Role: "implementation", Available: []string{"claude"}}); e == nil {
				t.Fatal("failed CLI accepted")
			}
		})
	}
}

func TestEvaluatorRefusalVocabularyIsClosed(t *testing.T) {
	for _, input := range []string{
		`{"status":"inconclusive","reasonCode":"observer-unavailable"}`,
		`{"status":"inconclusive","reasonCode":"invented"}`,
		`{"status":"pass","reasonCode":"observer-unavailable"}`,
		`{"status":"inconclusive","reasonCode":"observer-unavailable","detail":"private"}`,
		`{"status":"inconclusive","status":"inconclusive","reasonCode":"observer-unavailable"}`,
	} {
		_, err := decodeMatrixResult([]byte(input))
		if err == nil {
			t.Fatal("refusal became successful resolution")
		}
		valid := input == `{"status":"inconclusive","reasonCode":"observer-unavailable"}`
		if errors.Is(err, errEvaluatorEvidence) != valid {
			t.Fatal("refusal vocabulary accepted malformed evidence")
		}
	}
}

func TestRefusalLogContainsOnlyClosedEvidence(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	reportMatrixRefusal(matrixRefusal{"inconclusive", "synthetic-private-detail"})
	if output.Len() != 0 {
		t.Fatal("untrusted refusal data reached the log")
	}
	reportMatrixRefusal(evaluatorRefusal())
	if !strings.Contains(output.String(), `{"status":"inconclusive","reasonCode":"observer-unavailable"}`) {
		t.Fatal("refusal diagnostic was not recorded")
	}
}

// Exercises Host.spawnAgent itself with a synthetic herdr executable. No native agent runs.
func TestSpawnDispatchBindingAtEffect(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "acknowledged", true: "ambiguous"}[fail], func(t *testing.T) {
			root := privateTestRoot(t)
			key := strings.Repeat("a", 64)
			r, err := persistMatrixReceipt(root, key, "synthetic-command", receiptFor(MatrixConfig{}, syntheticRoute()), nil)
			if err != nil {
				t.Fatal("reservation failed")
			}
			r.dispatch = &dispatchBinding{SchemaVersion: 1, RunID: "orchid-" + key,
				Issue: dispatchIssue{Repo: "example/inbox", Number: 42}, Source: "claude", Model: "synthetic-model", Effort: "medium", Profile: "leaf"}
			if r.writeDispatch("reserved", nil) != nil {
				t.Fatal("binding failed")
			}
			file := filepath.Join(filepath.Dir(r.file), "dispatch.json")
			bin := filepath.Join(root, "bin")
			if os.Mkdir(bin, 0700) != nil {
				t.Fatal("fixture setup failed")
			}
			script := `#!/bin/sh
if [ "$1" = workspace ]; then
 printf '%s\n' '{"result":{"workspace":{"workspace_id":"fixture-workspace"},"root_pane":{"pane_id":"fixture-pane"}}}'
 exit 0
fi
if [ "$1" = pane ]; then
 grep -q '"state":"launching"' "$FIXTURE_DISPATCH_FILE" || exit 8
 grep -q '"paneId":"fixture-pane"' "$FIXTURE_DISPATCH_FILE" || exit 9
 [ "$FIXTURE_FAIL" != yes ] || exit 7
 printf '%s\n' '{"result":{}}'
 exit 0
fi
exit 6
`
			if os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0700) != nil {
				t.Fatal("fixture setup failed")
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FIXTURE_DISPATCH_FILE", file)
			t.Setenv("FIXTURE_FAIL", map[bool]string{false: "no", true: "yes"}[fail])
			pane, workspace, err := (Host{Home: root}).spawnAgent(context.Background(), "fixture", root, nil, "synthetic-command", r)
			if (err != nil) != fail {
				t.Fatal("effect result changed")
			}
			if !fail && (pane != "fixture-pane" || workspace != "fixture-workspace") {
				t.Fatal("exact location lost")
			}
			data, err := os.ReadFile(file)
			var binding dispatchBinding
			if err != nil || json.Unmarshal(data, &binding) != nil || binding.Location == nil || binding.Location.PaneID != "fixture-pane" || binding.State != map[bool]string{false: "dispatched", true: "uncertain"}[fail] {
				t.Fatal("effect state not durable")
			}
			st, err := os.Stat(file)
			if err != nil || st.Mode().Perm() != 0600 {
				t.Fatal("binding is not private")
			}
			if r.claim("synthetic-command") {
				t.Fatal("effect was replayable")
			}
		})
	}
}
