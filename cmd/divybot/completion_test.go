package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func completionFixture(t *testing.T) (*Coord, *Job, *bool, *bool, *int) {
	t.Helper()
	c, _, j, seatGone, processGone := teardownFixture(t)
	j.Agent, j.Label = "codex", "codex-fixture"
	dispatchPath := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record", "dispatch.json")
	var dispatch dispatchBinding
	if readPrivateActionJSON(dispatchPath, &dispatch) != nil {
		t.Fatal("fixture dispatch")
	}
	dispatch.Source = "codex"
	body, _ := json.Marshal(dispatch)
	if os.WriteFile(dispatchPath, body, 0600) != nil {
		t.Fatal("fixture dispatch")
	}
	j.GoalDelivery = "confirmed"
	c.cfg.Governor.MaxActive = 1
	c.cfg.Targets = []Target{{Agent: "codex"}}
	c.cfg.Hosts = []Host{c.hosts[j.Host]}
	c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
		if *seatGone {
			return nil, nil
		}
		return []AgentInfo{completionAgent(j)}, nil
	}
	c.actions.completionPR = func(context.Context, *Job) (bool, error) { return false, nil }
	c.actions.completed = func(_ context.Context, _ Host, _ *Job, native string) (bool, error) {
		if native != "thread-fixture" {
			t.Fatal("wrong native source")
		}
		return true, nil
	}
	capture := c.actions.stopProcess
	c.actions.stopProcess = func(ctx context.Context, h Host, pane, agent string) (*actionStopProcess, error) {
		if loadState(c.st.path).CompletedRuns[7].Phase != "retiring" {
			t.Fatal("process capture preceded completion fence persistence")
		}
		return capture(ctx, h, pane, agent)
	}
	closes := 0
	c.actions.close = func(_ context.Context, _ Host, ws string) error {
		if ws != j.Workspace {
			t.Fatal("closed another workspace")
		}
		persisted := loadState(c.st.path)
		if persisted.CompletedRuns[7].DispatchKey != j.DispatchKey || persisted.CompletedRuns[7].CloseAttempts != closes+1 {
			t.Fatal("close preceded its durable fence")
		}
		record := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
		if lstatRegular(filepath.Join(record, "teardown-anchor.json")) != nil || lstatRegular(filepath.Join(record, "teardown-intent.json")) != nil {
			t.Fatal("close preceded its process anchor")
		}
		closes++
		return nil
	}
	if err := c.st.save(); err != nil {
		t.Fatal(err)
	}
	return c, j, seatGone, processGone, &closes
}

func completionAgent(j *Job) AgentInfo {
	return AgentInfo{Agent: j.Agent, AgentStatus: "done", InteractiveReady: true, Name: j.Label,
		PaneID: j.Pane, WorkspaceID: j.Workspace, StateChangeSeq: 42,
		Cwd: "/fixture/issue-7", AgentSession: json.RawMessage(`{"agent":"codex","kind":"id","source":"herdr:codex","value":"thread-fixture"}`)}
}
func completionRef(j *Job) agentRef {
	return agentRef{Host: j.Host, Agent: j.Agent, Status: "done", Pane: j.Pane, Workspace: j.Workspace}
}

func TestCompletionHoldsSlotUntilSeatAndProcessAbsenceAndSurvivesRestart(t *testing.T) {
	c, j, seatGone, processGone, closes := completionFixture(t)
	ref := completionRef(j)
	if !c.retireCompleted(context.Background(), 7, j, ref, true) || *closes != 1 {
		t.Fatal("completion did not close once")
	}
	if c.st.CompletedRuns[7].Phase != "close-sent" || c.st.Jobs[7] == nil {
		t.Fatal("delivery asserted absence")
	}
	if got := c.admissionBudget(map[int]agentRef{7: ref})["codex"]; got != 0 {
		t.Fatalf("capacity released before cleanup: %d", got)
	}
	c.st = loadState(c.st.path) // close sent, absence not yet observed
	j = c.st.Jobs[7]
	c.retireCompleted(context.Background(), 7, j, ref, true)
	if *closes != 1 {
		t.Fatal("close-sent restart repeated a successful close while the seat remained")
	}
	*seatGone = true
	c.retireCompleted(context.Background(), 7, j, ref, true)
	if c.st.Jobs[7] == nil || *closes != 1 {
		t.Fatal("seat alone ended run or repeated close")
	}
	*processGone = true
	c.retireCompleted(context.Background(), 7, j, ref, true)
	if c.st.Jobs[7] != nil || c.st.CompletedRuns[7].Phase != "observed" || *closes != 1 {
		t.Fatal("paired absence did not release tracking")
	}
	if got := c.admissionBudget(map[int]agentRef{})["codex"]; got != 1 {
		t.Fatalf("same tick capacity not released: %d", got)
	}
	persisted := loadState(c.st.path)
	if persisted.Jobs[7] != nil || persisted.CompletedRuns[7].DispatchKey != j.DispatchKey {
		t.Fatal("restart lost open issue completion fence")
	}
	c.st = persisted
	c.cfg.Targets = []Target{{Label: "fixture", Agent: "codex", Repo: j.Repo}}
	if _, adopted := c.adopt(7, Issue{Number: 7, Labels: []string{"fixture"}}, map[int]agentRef{7: ref}); adopted || c.st.Jobs[7] != nil {
		t.Fatal("completed open issue was adopted after restart")
	}
	if c.st.reserveLaunch(7) {
		t.Fatal("completed open issue acquired a launch reservation")
	}
}

func TestCompletionOccupantMustBeUnique(t *testing.T) {
	_, j, _, _, _ := completionFixture(t)
	a := completionAgent(j)
	for _, agents := range [][]AgentInfo{nil, {a, a}} {
		if _, ok := completionOccupant(agents, j, "thread-fixture"); ok {
			t.Fatal("missing or duplicate occupant was accepted")
		}
	}
	if _, ok := completionOccupant([]AgentInfo{a}, j, "thread-fixture"); !ok {
		t.Fatal("unique exact occupant was unavailable")
	}
}

func TestCompletionLeavesOtherNativeAdaptersToTheirOwnSupervisor(t *testing.T) {
	c, j, _, _, closes := completionFixture(t)
	for _, agent := range []string{"opencode", "agy"} {
		c.actions.completionPR = func(context.Context, *Job) (bool, error) {
			t.Fatal("completion inspected another adapter PR")
			return false, nil
		}
		j.Agent = agent
		if c.retireCompleted(context.Background(), 7, j, completionRef(j), true) || *closes != 0 {
			t.Fatal("intercepted another native adapter")
		}
	}
}

func TestCompletionUnprovenOrChangedEvidenceNeverCloses(t *testing.T) {
	for _, name := range []string{"no-final", "source-error", "changed-sequence", "changed-after-capture", "other-native", "other-pane", "other-workspace", "other-agent", "not-ready", "extra-occupant", "working", "pending-goal", "receipt-location", "persistence-error", "anchor-error", "unknown-snapshot", "wrong-snapshot-host", "wrong-snapshot-pane", "wrong-snapshot-workspace", "wrong-snapshot-agent", "working-snapshot"} {
		t.Run(name, func(t *testing.T) {
			c, j, _, _, closes := completionFixture(t)
			reads := 0
			c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
				reads++
				a := completionAgent(j)
				switch name {
				case "changed-sequence":
					if reads >= 2 {
						a.StateChangeSeq++
					}
				case "changed-after-capture":
					if reads >= 5 {
						a.StateChangeSeq++
					}
				case "other-native":
					a.AgentSession = json.RawMessage(`{"agent":"codex","kind":"id","source":"herdr:codex","value":"other-thread"}`)
				case "other-pane":
					a.PaneID = "other-pane"
				case "other-workspace":
					a.WorkspaceID = "other-workspace"
				case "other-agent":
					a.Agent = "claude"
				case "not-ready":
					a.InteractiveReady = false
				case "working":
					a.AgentStatus = "working"
				case "extra-occupant":
					return []AgentInfo{a, a}, nil
				}
				return []AgentInfo{a}, nil
			}
			switch name {
			case "no-final":
				c.actions.completed = func(context.Context, Host, *Job, string) (bool, error) { return false, nil }
			case "source-error":
				c.actions.completed = func(context.Context, Host, *Job, string) (bool, error) { return true, errors.New("unavailable") }
			case "pending-goal":
				j.GoalDelivery = "pending"
			case "persistence-error":
				c.st.path = filepath.Join(t.TempDir(), "missing", "state.json")
			case "anchor-error":
				c.actions.stopProcess = func(context.Context, Host, string, string) (*actionStopProcess, error) {
					return nil, errors.New("unavailable")
				}
			case "receipt-location":
				p := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record", "dispatch.json")
				var d dispatchBinding
				if readPrivateActionJSON(p, &d) != nil {
					t.Fatal("fixture")
				}
				d.Location.PaneID = "other-pane"
				b, _ := json.Marshal(d)
				if os.WriteFile(p, b, 0600) != nil {
					t.Fatal("fixture")
				}
			}
			ref, known := completionRef(j), true
			switch name {
			case "unknown-snapshot":
				known = false
			case "wrong-snapshot-host":
				ref.Host = "other"
			case "wrong-snapshot-pane":
				ref.Pane = "other"
			case "wrong-snapshot-workspace":
				ref.Workspace = "other"
			case "wrong-snapshot-agent":
				ref.Agent = "other"
			case "working-snapshot":
				ref.Status = "working"
			}
			c.retireCompleted(context.Background(), 7, j, ref, known)
			if *closes != 0 || c.st.Jobs[7] == nil {
				t.Fatal("uncertain completion closed or released the job")
			}
		})
	}
}

func TestCompletionCloseFailuresAreBoundedAndKeepCapacity(t *testing.T) {
	c, j, _, _, _ := completionFixture(t)
	closes := 0
	c.actions.close = func(context.Context, Host, string) error { closes++; return errors.New("unconfirmed") }
	for i := 0; i < 5; i++ {
		c.retireCompleted(context.Background(), 7, j, completionRef(j), true)
	}
	if closes != 3 || c.st.CompletedRuns[7].Phase != "close-failed" || c.st.Jobs[7] == nil {
		t.Fatalf("unbounded or falsely completed cleanup: %d", closes)
	}
	if c.admissionBudget(map[int]agentRef{7: completionRef(j)})["codex"] != 0 {
		t.Fatal("failed close released capacity")
	}
}

func TestCompletionCrashBeforeCloseAndAfterAbsence(t *testing.T) {
	c, j, seatGone, processGone, closes := completionFixture(t)
	fence := completedRun{DispatchKey: j.DispatchKey, NativeSessionID: "thread-fixture", StateChangeSeq: 42, Phase: "retiring"}
	c.st.CompletedRuns = map[int]completedRun{7: fence}
	if c.st.save() != nil {
		t.Fatal("fixture")
	}
	c.st = loadState(c.st.path)
	c.retireCompleted(context.Background(), 7, c.st.Jobs[7], completionRef(j), true)
	if *closes != 1 {
		t.Fatal("fenced pre-close restart did not finish cleanup")
	}
	*seatGone, *processGone = true, true
	record := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
	var intent teardownIntent
	if readPrivateActionJSON(filepath.Join(record, "teardown-intent.json"), &intent) != nil || !c.teardownObserveOne(context.Background(), intent) {
		t.Fatal("fixture absence")
	}
	c.st = loadState(c.st.path) // observations survived, job removal not yet committed
	c.retireCompleted(context.Background(), 7, c.st.Jobs[7], completionRef(j), true)
	if c.st.Jobs[7] != nil || *closes != 1 {
		t.Fatal("observed restart repeated close or retained job")
	}
}

func TestNativeCompletionRequiresNewestFullCompletedFinalAnswer(t *testing.T) {
	for _, name := range []string{"valid", "commentary", "phase-less", "empty", "questions", "running", "failed", "summary", "no-time", "future", "multiple", "no-id", "other-type", "too-many"} {
		t.Run(name, func(t *testing.T) {
			item := map[string]any{"type": "agentMessage", "phase": "final_answer", "text": "Assignment finished."}
			turn := map[string]any{"id": "turn-fixture", "status": "completed", "itemsView": "full", "completedAt": time.Now().Unix() - 1, "items": []any{item}}
			switch name {
			case "commentary":
				item["phase"] = "commentary"
			case "phase-less":
				delete(item, "phase")
			case "empty":
				item["text"] = "   "
			case "questions":
				item["questions"] = []any{map[string]any{"id": "question"}}
			case "running":
				turn["status"] = "inProgress"
			case "failed":
				turn["status"] = "failed"
			case "summary":
				turn["itemsView"] = "summary"
			case "no-time":
				delete(turn, "completedAt")
			case "future":
				turn["completedAt"] = time.Now().Unix() + 100
			case "no-id":
				turn["id"] = ""
			case "other-type":
				item["type"] = "reasoning"
			case "too-many":
				items := make([]any, 513)
				for i := range items {
					items[i] = item
				}
				turn["items"] = items
			}
			data := []any{turn}
			if name == "multiple" {
				data = append(data, turn)
			}
			response, _ := json.Marshal(map[string]any{"id": 2, "result": map[string]any{"data": data}})
			var input bytes.Buffer
			p := newGoalRPC(&input, bytes.NewReader(append(response, '\n')), "thread-fixture")
			p.serial = 1
			got, err := p.lastTurnCompleted()
			if (err != nil && name != "multiple") || got != (name == "valid") {
				t.Fatalf("completion=%v error=%v", got, err)
			}
			if !strings.Contains(input.String(), `"sortDirection":"desc"`) || !strings.Contains(input.String(), `"itemsView":"full"`) || !strings.Contains(input.String(), `"limit":1`) {
				t.Fatal("completion did not request newest full native turn")
			}
		})
	}
}

func TestFinalCommentMarkerIsPublicOpaqueAndAuthorBound(t *testing.T) {
	key := strings.Repeat("d", 64)
	marker := finalCommentMarker(key)
	if marker != "<!-- orchid-run assignment_a1f5fcca4becf7d50bd0e31ad73fa92c02529e014327dc23b0aed46f3f5ef794 agent_01b2bf7785f015813e783817d39ed708678d7641631859805a0d30ddea71ce86 -->" {
		t.Fatal("marker differs from the Harness dispatch fixture")
	}
	if marker != "<!-- orchid-run "+actionOpaque("assignment", "orchid-"+key)+" "+actionOpaque("agent", "orchid-"+key)+" -->" {
		t.Fatal("marker derivation changed")
	}
	if strings.Contains(marker, key) || finalCommentMarker("bad") != "" || strings.Count(finalCommentInstruction(key), marker) != 1 {
		t.Fatal("private or invalid marker")
	}
	now := time.Now().UTC().Truncate(time.Second)
	if !markedCompletionComment("Final report.\n"+marker, "bot", "bot", now.Format(time.RFC3339), now.Add(-time.Minute), key) {
		t.Fatal("valid final marker rejected")
	}
	for _, body := range []string{"Final report.", marker + marker, strings.Replace(marker, "assignment_", "changed_", 1)} {
		if markedCompletionComment(body, "bot", "bot", now.Format(time.RFC3339), now.Add(-time.Minute), key) {
			t.Fatal("missing, duplicate or altered marker accepted")
		}
	}
	if markedCompletionComment(marker, "other", "bot", now.Format(time.RFC3339), now.Add(-time.Minute), key) || markedCompletionComment(marker, "bot", "bot", now.Add(-time.Hour).Format(time.RFC3339), now, key) {
		t.Fatal("forged author or stale comment accepted")
	}
}

func TestCompletionGoalMustBeCompleteAndMatchTheAssignment(t *testing.T) {
	j := &Job{NativeGoal: &dispatchGoal{Intent: goalIntent{Objective: "Assigned task"}}}
	for _, status := range []string{"active", "paused", "blocked", "usageLimited", "budgetLimited", "complete"} {
		if completionGoalAllowsRetirement(j, &nativeGoal{Objective: "Assigned task", Status: status}) != (status == "complete") {
			t.Fatal("native goal state misclassified")
		}
	}
	if completionGoalAllowsRetirement(j, nil) || completionGoalAllowsRetirement(j, &nativeGoal{Objective: "Other task", Status: "complete"}) {
		t.Fatal("unavailable or foreign goal retired")
	}
}

func TestCompletionUnprovenNoticeIsOncePerStateChangeAndPersists(t *testing.T) {
	c, j, _, _, _ := completionFixture(t)
	var output bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(prior)
	c.noteCompletionUnproven(j, 42)
	c.noteCompletionUnproven(j, 42)
	c.st = loadState(c.st.path)
	j = c.st.Jobs[7]
	c.noteCompletionUnproven(j, 42)
	c.noteCompletionUnproven(j, 43)
	if strings.Count(output.String(), "completion-unproven") != 2 {
		t.Fatal("unproven completion was silent or repeatedly notified")
	}
}

func TestCompletionFencePruningRequiresConfirmedClosureAndObservedCleanup(t *testing.T) {
	for _, name := range []string{"closed", "open", "listed", "poll-error", "state-unavailable", "retiring", "save-error"} {
		t.Run(name, func(t *testing.T) {
			c, j, _, _, _ := completionFixture(t)
			delete(c.st.Jobs, 7)
			c.st.CompletedRuns = map[int]completedRun{7: {DispatchKey: j.DispatchKey, Phase: "observed"}}
			allOpen, pollOK, state := map[int]bool{}, true, "CLOSED"
			switch name {
			case "open":
				state = "OPEN"
			case "listed":
				allOpen[7] = true
			case "poll-error":
				pollOK = false
			case "state-unavailable":
				state = ""
			case "retiring":
				f := c.st.CompletedRuns[7]
				f.Phase = "retiring"
				c.st.CompletedRuns[7] = f
			case "save-error":
				c.st.path = filepath.Join(t.TempDir(), "missing", "state.json")
			}
			c.pruneCompletedRuns(allOpen, pollOK, func(int) string { return state })
			_, fenced := c.st.CompletedRuns[7]
			if fenced != (name != "closed") {
				t.Fatal("pruning lost an open/unobserved fence or retained closed cleanup")
			}
		})
	}
}

func TestCompletionPruningWindowIsBounded(t *testing.T) {
	c, _, _, _, _ := completionFixture(t)
	c.st.Jobs = map[int]*Job{}
	c.st.CompletedRuns = map[int]completedRun{}
	for n := 1; n <= 12; n++ {
		c.st.CompletedRuns[n] = completedRun{Phase: "observed"}
	}
	reads := 0
	c.pruneCompletedRuns(map[int]bool{}, true, func(int) string { reads++; return "OPEN" })
	if reads != 8 {
		t.Fatalf("unbounded completion closure reads: %d", reads)
	}
}

func TestCompletionSuperviseChecksBeforePRRelay(t *testing.T) {
	c, j, seatGone, processGone, _ := completionFixture(t)
	dir := t.TempDir()
	callLog := filepath.Join(dir, "gh-called")
	gh := "#!/bin/sh\n: > \"$COMPLETION_ORDER_LOG\"\nprintf '[]\\n'\n"
	if os.WriteFile(filepath.Join(dir, "gh"), []byte(gh), 0700) != nil {
		t.Fatal("fixture")
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COMPLETION_ORDER_LOG", callLog)
	close := c.actions.close
	c.actions.close = func(ctx context.Context, h Host, ws string) error {
		err := close(ctx, h, ws)
		*seatGone, *processGone = true, true
		return err
	}
	c.supervise(context.Background(), 7, j, map[int]agentRef{7: completionRef(j)}, Issue{Number: 7})
	if _, err := os.Stat(callLog); !os.IsNotExist(err) {
		t.Fatal("PR lookup/relay preceded completion cleanup")
	}
}

func TestCompletionProcessProbeDistinguishesPIDReuseByStartTime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native process identity uses Linux procfs")
	}
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat)[strings.LastIndex(string(stat), ")")+2:])
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	a := actionStopProcess{SchemaVersion: 1, GroupID: 1 << 30, RootPID: os.Getpid(), Members: []actionProcessIdentity{{PID: os.Getpid(), Start: start}}}
	probe := func(anchor actionStopProcess) bool {
		body, _ := json.Marshal(anchor)
		out, err := run(context.Background(), "python3", "-c", actionProcGonePython, string(body))
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Gone bool `json:"gone"`
		}
		if json.Unmarshal([]byte(out), &result) != nil {
			t.Fatal("probe result")
		}
		return result.Gone
	}
	if probe(a) {
		t.Fatal("positive control missed the live captured process")
	}
	a.Members[0].Start++
	if !probe(a) {
		t.Fatal("reused PID with a different start time was treated as the captured process")
	}
}

func TestCompletionFencePrecedesAdmissionAdoptionAndSupervisionEffects(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	tick := src[strings.Index(src, "func (c *Coord) tick("):strings.Index(src, "func teardownEligible(")]
	if strings.Index(tick, "c.retireCompleted(") > strings.Index(tick, "budget := c.admissionBudget(") ||
		strings.Index(tick, "if completed {") > strings.Index(tick, "c.adopt(") ||
		!strings.Contains(tick, "c.superviseActive(ctx, n, j, status, is)") {
		t.Fatal("completion fence lost tick ordering")
	}
	supervise := src[strings.Index(src, "func (c *Coord) supervise("):strings.Index(src, "const strandedPokeInterval")]
	if strings.Index(supervise, "c.retireCompleted(") > strings.Index(supervise, "c.pollPR(") ||
		strings.Index(supervise, "c.retireCompleted(") > strings.Index(supervise, "c.retryBoundGoal(") {
		t.Fatal("completion check followed a continuation effect")
	}
}

// Executable doubles exercise the real PR and deadline paths without a host.
func completionSupervisionCommands(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	ghLog, sshLog := filepath.Join(dir, "gh-log"), filepath.Join(dir, "ssh-log")
	gh := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$COMPLETION_GH_LOG\"\nif [ \"$COMPLETION_GH_FAIL\" = yes ]; then exit 1; fi\ncase \"$1 $2\" in\n 'pr list') printf '%s\\n' \"$COMPLETION_PR_LIST\" ;;\n 'pr view') printf '%s\\n' \"$COMPLETION_PR_VIEW\" ;;\n *) printf '{}\\n' ;;\nesac\n"
	ssh := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$COMPLETION_SSH_LOG\"\nprintf '[]\\n'\n"
	for name, script := range map[string]string{"gh": gh, "ssh": ssh} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COMPLETION_GH_LOG", ghLog)
	t.Setenv("COMPLETION_SSH_LOG", sshLog)
	t.Setenv("COMPLETION_GH_FAIL", "no")
	t.Setenv("COMPLETION_PR_LIST", "[]")
	t.Setenv("COMPLETION_PR_VIEW", `{"number":8,"state":"OPEN","mergeable":"MERGEABLE","isDraft":false,"reviews":[],"comments":[{"id":"new","author":{"login":"reviewer"},"body":"Review this."}],"statusCheckRollup":[{"name":"check","status":"COMPLETED","conclusion":"SUCCESS"}]}`)
	return ghLog, sshLog
}

func TestCompletionPRScopeRequiresConfirmedAbsence(t *testing.T) {
	completionSupervisionCommands(t)
	for _, name := range []string{"unbound-open", "bound-open", "missing-scope", "read-error", "absent", "merged", "closed"} {
		t.Run(name, func(t *testing.T) {
			c, j, _, _, closes := completionFixture(t)
			c.actions.completionPR = nil
			j.Branch = "fixture-branch"
			t.Setenv("COMPLETION_GH_FAIL", "no")
			t.Setenv("COMPLETION_PR_LIST", "[]")
			t.Setenv("COMPLETION_PR_VIEW", `{"number":8,"state":"OPEN"}`)
			switch name {
			case "unbound-open":
				t.Setenv("COMPLETION_PR_LIST", `[{"number":8}]`)
			case "bound-open":
				j.PR = 8
			case "missing-scope":
				j.Branch = ""
			case "read-error":
				t.Setenv("COMPLETION_GH_FAIL", "yes")
			case "merged":
				j.PR = 8
				t.Setenv("COMPLETION_PR_VIEW", `{"number":8,"state":"MERGED"}`)
			case "closed":
				j.PR = 8
				t.Setenv("COMPLETION_PR_VIEW", `{"number":8,"state":"CLOSED"}`)
			}
			c.retireCompleted(context.Background(), 7, j, completionRef(j), true)
			wantClose := name == "absent" || name == "merged" || name == "closed"
			if (*closes == 1) != wantClose {
				t.Fatalf("PR scope %s close count %d", name, *closes)
			}
		})
	}
}

func TestCompletionUnprovenSupervisionPollsAndMergesWithoutInput(t *testing.T) {
	for _, name := range []string{"no-final", "receipt-missing", "open-pr", "merge-pr", "merged-pr", "stuck-draft"} {
		t.Run(name, func(t *testing.T) {
			ghLog, sshLog := completionSupervisionCommands(t)
			c, j, _, _, closes := completionFixture(t)
			h := c.hosts[j.Host]
			h.SSH = "fixture-host"
			c.hosts[j.Host] = h
			j.Branch = "fixture-branch"
			j.SpawnedAt = time.Now().Add(-time.Hour)
			c.actions.completed = func(context.Context, Host, *Job, string) (bool, error) { return false, nil }
			if name == "receipt-missing" {
				os.Remove(filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record", "binding.json"))
			}
			if name == "open-pr" || name == "merge-pr" || name == "merged-pr" || name == "stuck-draft" {
				j.PR = 8
				c.actions.completionPR = func(context.Context, *Job) (bool, error) { return true, nil }
			}
			if name == "merged-pr" {
				t.Setenv("COMPLETION_PR_VIEW", `{"number":8,"state":"MERGED"}`)
			}
			if name == "stuck-draft" {
				t.Setenv("COMPLETION_PR_VIEW", `{"number":8,"state":"OPEN","mergeable":"MERGEABLE","isDraft":true,"statusCheckRollup":[{"name":"check","conclusion":"SUCCESS"}]}`)
			}
			if name == "merge-pr" {
				c.cfg.Targets = []Target{{Repo: j.Repo, AutoMerge: true}}
			}
			c.supervise(context.Background(), 7, j, map[int]agentRef{7: completionRef(j)}, Issue{Number: 7})
			calls, _ := os.ReadFile(ghLog)
			if !strings.Contains(string(calls), "pr list") && !strings.Contains(string(calls), "pr view") {
				t.Fatal("unproven completion stopped PR polling")
			}
			if name == "merge-pr" && !strings.Contains(string(calls), "pr merge") {
				t.Fatal("done unproven worker stopped eligible PR merge")
			}
			if sent, _ := os.ReadFile(sshLog); len(sent) != 0 {
				t.Fatalf("unproven done seat received input: %s", sent)
			}
			if *closes != 0 || len(c.st.CompletedRuns) != 0 {
				t.Fatal("unproven PR supervision retired the seat")
			}
		})
	}
}

func TestCompletionMismatchRetainsOwnerAndDeadlineSupervisionAcrossRestart(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(strconv.FormatBool(mismatch), func(t *testing.T) {
			ghLog, sshLog := completionSupervisionCommands(t)
			c, j, _, _, _ := completionFixture(t)
			h := c.hosts[j.Host]
			h.SSH = "fixture-host"
			c.hosts[j.Host] = h
			c.actions.completed = func(context.Context, Host, *Job, string) (bool, error) { return false, nil }
			// Deadline teardown retains its existing independent capture semantics.
			c.actions.stopProcess = func(context.Context, Host, string, string) (*actionStopProcess, error) {
				return &actionStopProcess{SchemaVersion: 1, RootPID: 301, GroupID: 301, Members: []actionProcessIdentity{{PID: 301, Start: 9}}}, nil
			}
			ref := completionRef(j)
			ref.StateChangeSeq = 42
			if mismatch {
				ref.Pane = "replacement-pane"
				ref.Workspace = "replacement-workspace"
			}
			var logs bytes.Buffer
			oldLog := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(oldLog)
			c.supervise(context.Background(), 7, j, map[int]agentRef{7: ref}, Issue{Number: 7})
			c.st.save()
			c.st = loadState(c.st.path)
			j = c.st.Jobs[7]
			c.supervise(context.Background(), 7, j, map[int]agentRef{7: ref}, Issue{Number: 7})
			if mismatch && strings.Count(logs.String(), "owner-mismatch") != 1 {
				t.Fatal("mismatch notice not durable/deduplicated")
			}
			if j.Pane != "pane-fixture" || j.Workspace != "workspace-fixture" {
				t.Fatal("mismatch replaced recorded owner")
			}
			j.Deadline = time.Now().Add(-time.Second)
			c.supervise(context.Background(), 7, j, map[int]agentRef{7: ref}, Issue{Number: 7})
			calls, _ := os.ReadFile(ghLog)
			remote, _ := os.ReadFile(sshLog)
			if !strings.Contains(string(calls), "issue close 7") || !strings.Contains(string(remote), "workspace' 'close' 'workspace-fixture") {
				t.Fatalf("deadline supervision stopped: %s %s", calls, remote)
			}
			if strings.Contains(string(remote), "replacement-") || strings.Contains(string(remote), "'send'") {
				t.Fatal("deadline used replacement owner or sent continuation")
			}
			if c.st.Jobs[7] != nil {
				t.Fatal("deadline did not finish existing supervision")
			}
		})
	}
}

func TestCompletionPreAdmissionObservationIsNotRepeatedBySupervision(t *testing.T) {
	ghLog, _ := completionSupervisionCommands(t)
	c, j, _, _, _ := completionFixture(t)
	h := c.hosts[j.Host]
	h.SSH = "fixture-host"
	c.hosts[j.Host] = h
	reads := 0
	c.actions.completed = func(context.Context, Host, *Job, string) (bool, error) { reads++; return false, nil }
	ref := completionRef(j)
	if c.retireCompleted(context.Background(), 7, j, ref, true) {
		t.Fatal("unproven prepass retired")
	}
	c.superviseActive(context.Background(), 7, j, map[int]agentRef{7: ref}, Issue{Number: 7})
	if reads != 1 {
		t.Fatal("completion was read again during supervision")
	}
	if calls, _ := os.ReadFile(ghLog); !strings.Contains(string(calls), "pr list") {
		t.Fatal("cached unproven completion stopped PR polling")
	}
}

func TestCompletionMismatchedIdleSeatNeverReceivesInput(t *testing.T) {
	for _, field := range []string{"host", "pane", "workspace", "agent", "stranded"} {
		t.Run(field, func(t *testing.T) {
			ghLog, sshLog := completionSupervisionCommands(t)
			c, j, _, _, _ := completionFixture(t)
			h := c.hosts[j.Host]
			h.SSH = "fixture-host"
			c.hosts[j.Host] = h
			j.PR = 8
			if field == "stranded" {
				j.PR = 0
			}
			j.LastPoke = time.Now().Add(-time.Hour)
			t.Setenv("COMPLETION_PR_VIEW", `{"number":8,"state":"OPEN","mergeable":"MERGEABLE","isDraft":true,"statusCheckRollup":[{"name":"check","conclusion":"SUCCESS"}]}`)
			ref := completionRef(j)
			ref.Status = "idle"
			switch field {
			case "host":
				ref.Host = "other"
			case "pane", "stranded":
				ref.Pane = "other"
			case "workspace":
				ref.Workspace = "other"
			case "agent":
				ref.Agent = "claude"
			}
			c.supervise(context.Background(), 7, j, map[int]agentRef{7: ref}, Issue{Number: 7})
			if calls, _ := os.ReadFile(ghLog); !(strings.Contains(string(calls), "pr view") || strings.Contains(string(calls), "pr list")) {
				t.Fatal("mismatch stopped PR polling")
			}
			if calls, _ := os.ReadFile(sshLog); len(calls) != 0 {
				t.Fatalf("mismatched seat received effects: %s", calls)
			}
			if j.Pane != "pane-fixture" || j.Workspace != "workspace-fixture" {
				t.Fatal("mismatch changed recorded owner")
			}
		})
	}
}
