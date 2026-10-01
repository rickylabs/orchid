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
		strings.Index(tick, "if completed {") > strings.Index(tick, "c.adopt(") {
		t.Fatal("completion fence lost tick ordering")
	}
	supervise := src[strings.Index(src, "func (c *Coord) supervise("):strings.Index(src, "const strandedPokeInterval")]
	if strings.Index(supervise, "c.retireCompleted(") > strings.Index(supervise, "c.pollPR(") ||
		strings.Index(supervise, "c.retireCompleted(") > strings.Index(supervise, "c.retryBoundGoal(") {
		t.Fatal("completion check followed a continuation effect")
	}
}
