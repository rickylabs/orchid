package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeRouteAndCatalogFailClosed(t *testing.T) {
	route := openCodeRoute{"fixture-provider", "fixture-model", "high"}
	good := []byte("fixture-provider/fixture-model\n" + `{"id":"fixture-model","providerID":"fixture-provider","variants":{"high":{}}}` + "\n")
	if err := validateOpenCodeCatalog(good, route); err != nil {
		t.Fatal("independent native route should be available", err)
	}
	for _, tc := range []struct{ name, raw, reason string }{
		{"wrong provider", strings.ReplaceAll(string(good), `"providerID":"fixture-provider"`, `"providerID":"foreign"`), "opencode-catalog-unavailable"},
		{"wrong model", strings.ReplaceAll(string(good), `"id":"fixture-model"`, `"id":"foreign"`), "opencode-catalog-unavailable"},
		{"missing model", strings.ReplaceAll(string(good), "fixture-model", "other-model"), "opencode-model-unavailable"},
		{"missing variant", strings.ReplaceAll(string(good), `"high":{}`, `"low":{}`), "opencode-variant-unavailable"},
		{"bad variant", strings.ReplaceAll(string(good), `"high":{}`, `"high":null`), "opencode-variant-unavailable"},
		{"duplicate model", string(good) + string(good), "opencode-catalog-unavailable"},
		{"duplicate field", strings.ReplaceAll(string(good), `"id":"fixture-model"`, `"id":"fixture-model","id":"fixture-model"`), "opencode-catalog-unavailable"},
		{"oversized", string(good) + strings.Repeat(" ", 1024*1024), "opencode-catalog-unavailable"},
		{"native diagnostics", "PRIVATE-OPENCODE-CANARY\n" + string(good), "opencode-catalog-unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateOpenCodeCatalog([]byte(tc.raw), route); err != matrixReason(tc.reason) {
				t.Fatalf("catalog wrongly admitted or misclassified: %v", err)
			}
		})
	}
	for _, o := range []Overrides{
		{Model: "fixture-provider/fixture-model", Router: "foreign", Effort: "high"},
		{Model: "fixture-model", Effort: "high"}, {Model: "fixture-provider/", Effort: "high"},
		{Model: "fixture-provider/fixture-model", Effort: "high\nPRIVATE-OPENCODE-CANARY"},
	} {
		if _, err := resolveOpenCodeRoute(o); err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("invalid routing accepted or reflected")
		}
	}
	for _, effort := range []string{"", "none", "provider_default"} {
		r, err := resolveOpenCodeRoute(Overrides{Model: route.qualifiedModel(), Effort: effort})
		if err != nil || r.Variant != "" || validateOpenCodeCatalog(good, r) != nil {
			t.Fatal("explicit native default was not preserved")
		}
	}
	if _, err := resolveOpenCodeRoute(Overrides{Router: route.Provider, Model: route.Model, Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	policy := syntheticRoute()
	policy.Transport, policy.Model = "opencode", route.qualifiedModel()
	if routeRouterError(policy, Overrides{Router: "foreign"}) == nil || routeRouterError(policy, Overrides{Router: route.Provider}) != nil {
		t.Fatal("matrix provider substitution was allowed")
	}
}

// Native-shaped synthetic data only. No API request or real agent is executed.
func openCodeExportFixture(run *openCodeRun, answer string) []byte {
	created := int64(2000)
	user := map[string]any{"id": "fixture-user", "sessionID": run.SessionID, "role": "user", "time": map[string]any{"created": created},
		"model": map[string]any{"providerID": run.Route.Provider, "modelID": run.Route.Model, "variant": run.Route.Variant}}
	assistant := map[string]any{"id": "fixture-assistant", "sessionID": run.SessionID, "parentID": "fixture-user", "role": "assistant",
		"providerID": run.Route.Provider, "modelID": run.Route.Model, "variant": run.Route.Variant, "time": map[string]any{"created": created + 1, "completed": created + 2}, "finish": "stop"}
	part := func(id, text string) []any {
		return []any{map[string]any{"type": "text", "sessionID": run.SessionID, "messageID": id, "text": text}}
	}
	data, _ := json.Marshal(map[string]any{"info": map[string]any{"id": run.SessionID, "directory": run.Cwd, "time": map[string]any{"created": created}},
		"messages": []any{map[string]any{"info": user, "parts": part("fixture-user", runPointer)}, map[string]any{"info": assistant, "parts": part("fixture-assistant", answer)}}})
	return data
}

func TestOpenCodeNativeExportRequiresRoutePromptAndNonemptyAnswer(t *testing.T) {
	run := &openCodeRun{Route: openCodeRoute{"fixture-provider", "fixture-model", "high"}, Cwd: "/fixture/checkout", SessionID: "ses_fixture", NotBefore: 1000}
	good := string(openCodeExportFixture(run, "OK"))
	if confirmed, complete, err := inspectOpenCodeExport([]byte(good), run); !confirmed || !complete || err != nil {
		t.Fatal("native answer was not confirmed", err)
	}
	for _, tc := range []struct{ name, raw, reason string }{
		{"empty", string(openCodeExportFixture(run, "")), "opencode-empty-answer"},
		{"whitespace", string(openCodeExportFixture(run, " \n\t")), "opencode-empty-answer"},
		{"reasoning only", strings.Replace(good, `"text":"OK","type":"text"`, `"text":"OK","type":"reasoning"`, 1), "opencode-empty-answer"},
		{"synthetic text", strings.Replace(good, `"text":"OK"`, `"synthetic":true,"text":"OK"`, 1), "opencode-empty-answer"},
		{"wrong session", strings.Replace(good, `"id":"ses_fixture"`, `"id":"ses_foreign"`, 1), "opencode-output-unconfirmed"},
		{"wrong cwd", strings.Replace(good, "/fixture/checkout", "/fixture/foreign", 1), "opencode-output-unconfirmed"},
		{"wrong provider", strings.Replace(good, `"providerID":"fixture-provider"`, `"providerID":"foreign"`, 1), "opencode-route-mismatch"},
		{"wrong model", strings.Replace(good, `"modelID":"fixture-model"`, `"modelID":"foreign"`, 1), "opencode-route-mismatch"},
		{"wrong variant", strings.Replace(good, `"variant":"high"`, `"variant":"low"`, 1), "opencode-route-mismatch"},
		{"wrong assistant variant", strings.Replace(good, `"created":2001},"variant":"high"`, `"created":2001},"variant":"low"`, 1), "opencode-route-mismatch"},
		{"wrong assistant provider", strings.Replace(good, `"parentID":"fixture-user","providerID":"fixture-provider"`, `"parentID":"fixture-user","providerID":"foreign"`, 1), "opencode-route-mismatch"},
		{"wrong assistant model", strings.Replace(good, `"finish":"stop","id":"fixture-assistant","modelID":"fixture-model"`, `"finish":"stop","id":"fixture-assistant","modelID":"foreign"`, 1), "opencode-route-mismatch"},
		{"wrong parent", strings.Replace(good, `"parentID":"fixture-user"`, `"parentID":"foreign"`, 1), "opencode-route-mismatch"},
		{"wrong prompt", strings.Replace(good, runPointer, "different prompt", 1), "opencode-output-unconfirmed"},
		{"provider error", strings.Replace(good, `"finish":"stop"`, `"error":{"name":"PRIVATE-OPENCODE-CANARY"},"finish":"stop"`, 1), "opencode-provider-error"},
		{"duplicate field", strings.Replace(good, `"finish":"stop"`, `"finish":"stop","finish":"stop"`, 1), "opencode-output-unconfirmed"},
		{"oversized", good + strings.Repeat(" ", 1024*1024), "opencode-output-unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			confirmed, complete, err := inspectOpenCodeExport([]byte(tc.raw), run)
			if confirmed || complete || err != matrixReason(tc.reason) {
				t.Fatalf("bad native evidence passed: confirmed=%v completed=%v err=%v", confirmed, complete, err)
			}
		})
	}
	stale := *run
	stale.NotBefore = 3000
	if _, _, err := inspectOpenCodeExport([]byte(good), &stale); err == nil {
		t.Fatal("old export confirmed a new launch")
	}
	stale.NotBefore = 0
	if _, _, err := inspectOpenCodeExport([]byte(good), &stale); err == nil {
		t.Fatal("missing native freshness baseline was accepted")
	}
	stale.NotBefore, stale.CreatedAt = run.NotBefore, 9999
	if _, _, err := inspectOpenCodeExport([]byte(good), &stale); err == nil {
		t.Fatal("export replaced the bound session creation time")
	}
	// A currently streaming answer can confirm submission, never completion.
	streaming := strings.Replace(good, `"completed":2002`, `"completed":0`, 1)
	if confirmed, complete, err := inspectOpenCodeExport([]byte(streaming), run); !confirmed || complete || err != nil {
		t.Fatal("streaming turn got completion or lost acceptance")
	}
	var multiple map[string]any
	if json.Unmarshal([]byte(good), &multiple) != nil {
		t.Fatal("invalid synthetic export")
	}
	var next map[string]any
	if json.Unmarshal([]byte(streaming), &next) != nil {
		t.Fatal("invalid streaming export")
	}
	messages := next["messages"].([]any)
	assistant := messages[1].(map[string]any)
	assistant["info"].(map[string]any)["id"] = "fixture-later-assistant"
	assistant["parts"].([]any)[0].(map[string]any)["messageID"] = "fixture-later-assistant"
	multiple["messages"] = append(multiple["messages"].([]any), assistant)
	later, _ := json.Marshal(multiple)
	if confirmed, complete, err := inspectOpenCodeExport(later, run); !confirmed || complete || err != nil {
		t.Fatal("earlier answer completed a later streaming response")
	}
	// A real older parent still cannot complete a newly submitted user turn.
	var oldParent map[string]any
	_ = json.Unmarshal([]byte(good), &oldParent)
	previous := oldParent["messages"].([]any)
	var followup map[string]any
	_ = json.Unmarshal([]byte(good), &followup)
	newUser := followup["messages"].([]any)[0].(map[string]any)
	newUser["info"].(map[string]any)["id"] = "fixture-new-user"
	newUser["parts"].([]any)[0].(map[string]any)["messageID"] = "fixture-new-user"
	oldParent["messages"] = []any{previous[0], newUser, previous[1]}
	wrongTurn, _ := json.Marshal(oldParent)
	if confirmed, complete, err := inspectOpenCodeExport(wrongTurn, run); confirmed || complete || err != matrixReason("opencode-route-mismatch") {
		t.Fatal("an earlier parent completed a different current user turn")
	}
}

func TestOpenCodeSessionFreshnessAndCapacity(t *testing.T) {
	if empty, err := decodeOpenCodeSessions(nil); err != nil || len(empty) != 0 {
		t.Fatal("native empty index was not recognized without session evidence")
	}
	run := &openCodeRun{Cwd: "/fixture/checkout", NotBefore: 2000, ExcludedIDs: []string{"ses_excluded"}}
	sessions := []openCodeSession{{"ses_old", run.Cwd, 1000}, {"ses_foreign", "/fixture/foreign", 3000}, {"ses_excluded", run.Cwd, 3000}, {"ses_new", run.Cwd, 3000}}
	if id, err := selectOpenCodeSession(sessions, run); err != nil || id != "ses_new" {
		t.Fatal("exact fresh session not selected")
	}
	if _, err := selectOpenCodeSession(append(sessions, openCodeSession{"ses_ambiguous", run.Cwd, 3000}), run); err == nil {
		t.Fatal("ambiguous sessions were guessed")
	}
	run.SessionID = "ses_other"
	if _, err := selectOpenCodeSession(sessions, run); err == nil {
		t.Fatal("a bound native session was replaced")
	}
	config := OpenCodeConfig{Providers: map[string]OpenCodeProvider{"first": {2}, "second": {1}}}
	jobs := map[int]*Job{1: {Agent: "opencode", OpenCode: &openCodeRun{Route: openCodeRoute{Provider: "first"}}}, 2: {Agent: "codex"}}
	budget := openCodeCapacityBudget(config, jobs, nil)
	if budget["opencode:first"] != 1 || budget["opencode:second"] != 1 || budget["opencode"] != 2 || accountKey("opencode") == "codex" {
		t.Fatal("provider or subscription capacity was shared")
	}
	jobs[3] = &Job{Agent: "opencode"}
	budget = openCodeCapacityBudget(config, jobs, nil)
	if budget["opencode:first"] != 0 || budget["opencode:second"] != 0 || budget["opencode"] != 0 {
		t.Fatal("unbound seat freed a guessed provider pool")
	}
	config.Providers["first"] = OpenCodeProvider{257}
	if config.valid() || openCodeCapacityBudget(config, nil, nil)["opencode"] != 0 || openCodeCapacityBudget(OpenCodeConfig{}, nil, nil)["opencode"] != 0 {
		t.Fatal("invalid or unconfigured pool was admitted")
	}
}

func openCodeHostFixture(t *testing.T, mode string) (Host, *Job, func() string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "checkout")
	writeFixture(t, filepath.Join(cwd, ".git", "info", "exclude"), "")
	log := filepath.Join(root, "calls.jsonl")
	t.Setenv("OC_FIXTURE_ROOT", root)
	t.Setenv("OC_FIXTURE_CWD", cwd)
	t.Setenv("OC_FIXTURE_MODE", mode)
	t.Setenv("OC_FIXTURE_POINTER", runPointer)
	script := `#!/usr/bin/env python3
import json,os,sys,time,pathlib
root=pathlib.Path(os.environ['OC_FIXTURE_ROOT']);cwd=os.environ['OC_FIXTURE_CWD'];mode=os.environ['OC_FIXTURE_MODE'];a=sys.argv[1:]
with (root/'calls.jsonl').open('a') as f:f.write(json.dumps({'bin':pathlib.Path(sys.argv[0]).name,'args':a})+'\n')
def emit(x):print(json.dumps(x))
prompted=(root/'prompted').exists()
if pathlib.Path(sys.argv[0]).name=='herdr':
 if a[:2]==['workspace','create']:emit({'result':{'workspace':{'workspace_id':'w1'},'root_pane':{'pane_id':'w1:p1'}}})
 elif a[:2]==['agent','get']:
  info={'agent':'opencode','name':'fixture-agent','pane_id':'w1:p1','workspace_id':'w1','cwd':cwd,'interactive_ready':True,'agent_status':'working' if prompted else 'idle','state_change_seq':2 if prompted else 1}
  if mode=='foreign':info['name']='foreign'
  if mode=='changing' and prompted:info['state_change_seq']=sum('get' in x for x in (root/'calls.jsonl').read_text().splitlines())
  emit({'result':{'agent':info}})
 elif a[:2]==['agent','prompt']:(root/'prompted').write_text(str(int(time.time()*1000)));emit({'result':{'accepted':True}})
 else:emit({'result':{}})
elif pathlib.Path(sys.argv[0]).name=='opencode':
 a=[x for x in a if x!='--pure']
 if a[:1]==['models']:
  model='other-model' if mode=='missing-model' else 'fixture-model'
  print('fixture-provider/'+model);emit({'id':model,'providerID':'fixture-provider','variants':{'low' if mode=='missing-variant' else 'high':{}}})
 elif a[:2]==['debug','agent']:
  emit({'name':'build','mode':'primary','model':{'providerID':'fixture-provider','modelID':'foreign' if mode=='fallback-agent' else 'fixture-model'}})
 elif a[:2]==['session','list']:
   if prompted and mode!='no-session':emit([{'id':'ses_fixture','directory':cwd,'created':int((root/'prompted').read_text())}])
 elif a[:1]==['export']:
  now=int((root/'prompted').read_text());sid='ses_fixture'
  part=lambda mid,text:{'type':'text','text':text,'sessionID':sid,'messageID':mid}
  emit({'info':{'id':sid,'directory':cwd,'time':{'created':now}},'messages':[
   {'info':{'id':'fixture-user','sessionID':sid,'role':'user','time':{'created':now},'model':{'providerID':'fixture-provider','modelID':'fixture-model','variant':'high'}},'parts':[part('fixture-user',os.environ['OC_FIXTURE_POINTER'])]},
    {'info':{'id':'fixture-assistant','sessionID':sid,'role':'assistant','parentID':'fixture-user','providerID':'foreign' if mode=='wrong-provider' else 'fixture-provider','modelID':'fixture-model','variant':'high','time':{'created':now,'completed':0 if mode=='streaming' else now},'finish':'stop'},'parts':[part('fixture-assistant','' if mode=='empty' else 'OK')]}]})
elif pathlib.Path(sys.argv[0]).name=='gh':
 if '--body-file' in a:
  with (root/'calls.jsonl').open('a') as f:f.write(json.dumps({'body':pathlib.Path(a[a.index('--body-file')+1]).read_text()})+'\n')
else:sys.exit(1)
`
	for _, name := range []string{"herdr", "opencode", "gh"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	job := &Job{Issue: 7, Agent: "opencode", Label: "fixture-agent", Pane: "w1:p1", Workspace: "w1", Host: "fixture-host",
		OpenCode: &openCodeRun{Route: openCodeRoute{"fixture-provider", "fixture-model", "high"}, Cwd: cwd}}
	return Host{Home: root, Name: job.Host}, job, func() string {
		b, _ := os.ReadFile(log)
		return string(b)
	}
}

func TestOpenCodeRegisteredLaunchCatalogBeforeSeatAndIsolatedVariant(t *testing.T) {
	h, j, calls := openCodeHostFixture(t, "")
	h.ClaudeChildEventRoot = filepath.Join(h.Home, "runs", "child-events")
	o := Overrides{Model: j.OpenCode.Route.qualifiedModel(), Effort: "high", Router: "fixture-provider"}
	pane, ws, err := h.spawnAgent(context.Background(), j.Label, j.OpenCode.Cwd, nil, "opencode", o, registrationReceipt(t, "opencode", o))
	if err != nil || pane != j.Pane || ws != j.Workspace {
		t.Fatal("native registration failed", err)
	}
	log := calls()
	if strings.Contains(log, "export HARNESS_CLAUDE_CHILD_EVENT_ROOT=") {
		t.Fatal("Claude child event root escaped into the OpenCode pane")
	}
	if !strings.HasPrefix(log, `{"bin": "opencode"`) || !strings.Contains(log, `"--agent", "build", "--model", "fixture-provider/fixture-model"`) || strings.Contains(log, `"prompt"`) {
		t.Fatal("catalog did not precede registration or startup sent a goal")
	}
	statePath := filepath.Join(j.OpenCode.Cwd, ".divybot-opencode", "state", "opencode", "model.json")
	data, err := os.ReadFile(statePath)
	var state struct{ Variant map[string]string }
	if err != nil || json.Unmarshal(data, &state) != nil || state.Variant[j.OpenCode.Route.qualifiedModel()] != "high" {
		t.Fatal("explicit variant was dropped")
	}
	stat, _ := os.Stat(statePath)
	if stat.Mode().Perm() != 0600 {
		t.Fatal("launch state is not private")
	}
	env, err := openCodeEnvironment(j.OpenCode.Cwd, j.OpenCode.Route)
	if err != nil || strings.Contains(env["OPENCODE_CONFIG_CONTENT"], "permission") || env["XDG_STATE_HOME"] == "" {
		t.Fatal("route changed permission rules or inherited shared state")
	}
	// Never overwrite a prior state tree (including repository-supplied symlinks).
	if err := h.prepareOpenCodeLaunch(context.Background(), j.OpenCode.Cwd, j.OpenCode.Route, env); err != matrixReason("opencode-state-unavailable") {
		t.Fatal("existing state tree was reused")
	}
}

func TestOpenCodeCatalogFailureNeverCreatesSeatOrDeliversGoal(t *testing.T) {
	for _, mode := range []string{"missing-model", "missing-variant", "fallback-agent"} {
		t.Run(mode, func(t *testing.T) {
			h, j, calls := openCodeHostFixture(t, mode)
			o := Overrides{Model: j.OpenCode.Route.qualifiedModel(), Effort: "high"}
			if _, _, err := h.spawnAgent(context.Background(), j.Label, j.OpenCode.Cwd, nil, "opencode", o, registrationReceipt(t, "opencode", o)); err == nil {
				t.Fatal("missing native capability was launched")
			}
			if strings.Contains(calls(), `"herdr"`) || strings.Contains(calls(), `"prompt"`) {
				t.Fatal("preflight failure caused a native effect")
			}
		})
	}
}

func TestOpenCodeGoalSingleEffectAndLoudEmptyFailure(t *testing.T) {
	for _, mode := range []string{"", "empty", "wrong-provider", "foreign", "changing", "save-failure"} {
		t.Run(mode, func(t *testing.T) {
			h, j, calls := openCodeHostFixture(t, mode)
			budget := 3 * time.Second // Allow the bounded synthetic CLI subprocesses under CI load.
			if mode == "changing" {
				budget = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			err := h.injectOpenCodeGoal(ctx, j, func() error {
				if mode == "save-failure" {
					return errors.New("PRIVATE-OPENCODE-CANARY")
				}
				return nil
			})
			if (err == nil) != (mode == "") {
				t.Fatal("goal route/answer/occupant proof disagreed", err)
			}
			if mode == "empty" && err != matrixReason("opencode-empty-answer") {
				t.Fatal("empty rc-zero response did not fail loudly", err)
			}
			log := calls()
			want := 1
			if mode == "foreign" || mode == "save-failure" {
				want = 0
			}
			if strings.Count(log, `"agent", "prompt"`) != want || strings.Contains(log, "send-keys") {
				t.Fatal("native prompt was repeated, nudged or sent without its fence")
			}
		})
	}
}

func TestOpenCodeEmptyAnswerBlockSurvivesRestartBeforeCompletionActions(t *testing.T) {
	h, j, calls := openCodeHostFixture(t, "empty")
	j.GoalDelivery = "confirmed"
	j.OpenCode.NotBefore, j.OpenCode.SessionID = 1000, "ses_fixture"
	j.Deadline = time.Now().Add(-time.Hour)
	statePath := filepath.Join(t.TempDir(), "state.json")
	c := &Coord{cfg: &Config{Inbox: "fixture/inbox"}, st: loadState(statePath), hosts: map[string]Host{h.Name: h}}
	c.st.Jobs[j.Issue] = j
	// Native export is available only after this synthetic prompt marker.
	if err := os.WriteFile(filepath.Join(h.Home, "prompted"), []byte("2000"), 0600); err != nil {
		t.Fatal(err)
	}
	status := map[int]agentRef{j.Issue: {Host: h.Name, Agent: "opencode", Pane: j.Pane, Workspace: j.Workspace, Status: "done"}}
	c.supervise(context.Background(), j.Issue, j, status, Issue{})
	reloaded := loadState(statePath)
	if got := reloaded.Jobs[j.Issue]; got == nil || got.OpenCode.Failure != "opencode-empty-answer" || reloaded.LaunchBlocks[j.Issue].Reason != "opencode-empty-answer" {
		t.Fatal("empty answer was not durably blocked")
	}
	c.st = reloaded
	before := calls()
	c.supervise(context.Background(), j.Issue, reloaded.Jobs[j.Issue], status, Issue{})
	if calls() != before || strings.Contains(calls(), `"close"`) || strings.Contains(calls(), `"merge"`) || strings.Contains(calls(), `"send-keys"`) {
		t.Fatal("failed run was closed, merged, poked or replayed after restart")
	}
	if !strings.Contains(calls(), "opencode-empty-answer") {
		t.Fatal("empty-answer failure was not reported loudly")
	}
}

func TestOpenCodeUnfinishedOutputNeverCompletesAtDeadline(t *testing.T) {
	for _, mode := range []string{"no-session", "streaming"} {
		for _, expired := range []bool{false, true} {
			t.Run(mode+"/"+map[bool]string{false: "working", true: "expired"}[expired], func(t *testing.T) {
				h, j, calls := openCodeHostFixture(t, mode)
				j.GoalDelivery = "confirmed"
				j.OpenCode.NotBefore = 1000
				if expired {
					j.Deadline = time.Now().Add(-time.Hour)
				}
				c := &Coord{cfg: &Config{Inbox: "fixture/inbox"}, st: loadState(filepath.Join(t.TempDir(), "state.json")), hosts: map[string]Host{h.Name: h}}
				c.st.Jobs[j.Issue] = j
				if err := os.WriteFile(filepath.Join(h.Home, "prompted"), []byte("2000"), 0600); err != nil {
					t.Fatal(err)
				}
				status := map[int]agentRef{j.Issue: {Host: h.Name, Agent: "opencode", Pane: j.Pane, Workspace: j.Workspace, Status: "working"}}
				c.supervise(context.Background(), j.Issue, j, status, Issue{})
				if (j.OpenCode.Failure != "") != expired || (expired && j.OpenCode.Failure != "opencode-output-unconfirmed") {
					t.Fatal("unfinished native answer got the wrong deadline outcome")
				}
				for _, forbidden := range []string{`"close"`, `"merge"`, `"send-keys"`, `"pr"`, `"prompt"`} {
					if strings.Contains(calls(), forbidden) {
						t.Fatal("unfinished answer reached a completion or input action")
					}
				}
			})
		}
	}
}

func TestOpenCodeAvailabilityUsesOnlyExplicitProviderCapacity(t *testing.T) {
	now := time.Now()
	for _, capacity := range []int{-1, 0, 1} {
		for _, nativeQuota := range []quota{{}, {ok: true, at: now, seven: RateLimit{UsedPct: 100, ResetsAt: now.Add(time.Hour).Unix()}}} {
			s := buildTransportAvailability(map[string]int{"opencode": capacity}, map[string]quota{"opencode": nativeQuota}, now, time.Minute, 92, time.Minute)
			if len(s.Transports) != 4 || s.Transports[3].Transport != "opencode" {
				t.Fatal("OpenCode capacity row is missing or out of order")
			}
			row := s.Transports[3]
			if row.Available != (capacity > 0) || (capacity > 0 && row.Reason != nil) || (capacity <= 0 && (row.Reason == nil || *row.Reason != availabilityNoCapacity)) {
				t.Fatal("OpenCode capacity was treated as subscription quota or fabricated")
			}
		}
	}
}

func TestOpenCodeMatrixProviderAdmissionAndFence(t *testing.T) {
	for _, mode := range []string{"success", "disabled", "wrong-provider-capacity", "conflicting-router", "ambiguous-launch"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTestRoot(t)
			cfg := &Config{Inbox: "fixture/inbox", OpenCode: OpenCodeConfig{Providers: map[string]OpenCodeProvider{"fixture-provider": {1}}},
				Matrix: MatrixConfig{Source: root, ReceiptRoot: root, Revision: strings.Repeat("b", 40), TargetRevisions: map[string]string{"fixture/project": strings.Repeat("c", 40)}}}
			c := &Coord{cfg: cfg}
			budget := map[string]int{"opencode": 1, "opencode:fixture-provider": 1}
			body := "/swarm\ntier: feature\nrole: implementation\nrouter: fixture-provider\n"
			if mode == "disabled" {
				cfg.OpenCode = OpenCodeConfig{}
			}
			if mode == "wrong-provider-capacity" {
				budget["opencode:fixture-provider"] = 0
				budget["opencode:foreign"] = 1
			}
			if mode == "conflicting-router" {
				body = strings.Replace(body, "router: fixture-provider", "router: foreign", 1)
			}
			route := syntheticRoute()
			route.Transport, route.Provider, route.Model, route.Effort = "opencode", "synthetic-provider-kind", "fixture-provider/fixture-model", "high"
			launched := false
			_, ok := c.matrixAttempt(context.Background(), 7, Issue{Number: 7, ID: "fixture-node", Title: "Synthetic task", Body: body}, Target{Repo: "fixture/project"}, budget, matrixAttemptDeps{
				report: func(r matrixRefusal) {
					if !validMatrixRefusal(r) {
						t.Fatal("unsafe refusal")
					}
				},
				read: func(context.Context, string, string, string) (string, error) {
					return "| `routing` | matrix `implementation` row |", nil
				},
				resolve: func(_ context.Context, _ MatrixConfig, req matrixRequest) (matrixRoute, error) {
					if !containsString(req.Available, "opencode") {
						return matrixRoute{}, matrixReason("route-unavailable")
					}
					return route, nil
				},
				host: func(Target, string) (Host, bool) { return Host{Name: "fixture-host"}, true }, persist: persistMatrixReceipt,
				launch: func(_ context.Context, _ int, _ Issue, _ Host, agent string, o Overrides, r *durableMatrixReceipt) error {
					launched = true
					if agent != "opencode" || o.Router != "fixture-provider" || !r.claim(mustBuildAgentCmd(t, agent, o)) {
						t.Fatal("receipt/provider route not bound")
					}
					if mode == "ambiguous-launch" {
						return errMatrix
					}
					return nil
				},
			})
			if ok != (mode == "success") || launched != (mode == "success" || mode == "ambiguous-launch") {
				t.Fatal("provider admission disagreed")
			}
			if launched && budget["opencode:fixture-provider"] != 0 {
				t.Fatal("launch ambiguity refunded provider capacity")
			}
		})
	}
}
