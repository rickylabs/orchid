package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFinalReportInstructionDropsVisibleReportWithoutModelMarker(t *testing.T) {
	instruction := finalReportInstruction(dispatchIssue{Repo: "fixture/inbox", Number: 7}, strings.Repeat("d", 64))
	if !strings.Contains(instruction, ".divybot-final-report.md") || !strings.Contains(instruction, "fixture/inbox#7") ||
		strings.Contains(instruction, "gh issue comment") || strings.Contains(instruction, ".divybot-final-comment.sh") || strings.Contains(instruction, "<!-- orchid-run") {
		t.Fatal("final publication still depends on the model running the formatter or copying a marker")
	}
}

func finalPublicationFixture(t *testing.T) (*Coord, *Job, *string, *int, *finalPostedComment) {
	t.Helper()
	c, j, _, _, _ := completionFixture(t)
	j.FinalReportManaged = true
	j.SpawnedAt = time.Now().Add(-time.Minute)
	dir := filepath.Join(filepath.Dir(c.st.path), "final-publication", j.DispatchKey)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s := finalReportScope{SchemaVersion: 1, Key: j.DispatchKey, Destination: dispatchIssue{Repo: c.cfg.Inbox, Number: j.Issue}, Host: j.Host, Source: j.Agent, Repo: j.Repo, Cwd: "/fixture/issue-7", BindingDigest: strings.Repeat("b", 64), DispatchDigest: strings.Repeat("e", 64)}
	b, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, "scope.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	c.cfg.BotLogin = "fixture-bot"
	report := "VISIBLE_REPORT_OK\n"
	posts := 0
	comment := finalPostedComment{ID: 31, IssueURL: "https://api.github.com/repos/" + c.cfg.Inbox + "/issues/7", CreatedAt: time.Now().Truncate(time.Second)}
	comment.User.Login = c.cfg.BotLogin
	c.finalCalls.proof = func(context.Context, Host, *Job, finalReportScope) error { return nil }
	c.finalCalls.report = func(context.Context, Host, finalReportScope) (string, error) { return report, nil }
	c.finalCalls.post = func(ctx context.Context, dest dispatchIssue, body string) (int64, error) {
		if dest != s.Destination {
			t.Fatal("post escaped bound dispatch destination")
		}
		if !strings.Contains(body, report) || strings.Count(body, finalCommentMarker(j.DispatchKey)) != 1 {
			t.Fatal("producer did not construct the exact marked visible report")
		}
		posts++
		comment.Body = body
		// GitHub stamps a comment when it is posted, never before the publication began.
		comment.CreatedAt = time.Now().Truncate(time.Second)
		return comment.ID, nil
	}
	c.finalCalls.comment = func(context.Context, dispatchIssue, int64) (finalPostedComment, error) { return comment, nil }
	return c, j, &report, &posts, &comment
}

func TestFinalReportProducerPostsWithoutModelHelperAndRestartDoesNotRepeat(t *testing.T) {
	c, j, _, posts, _ := finalPublicationFixture(t)
	id, err := c.publishFinalReport(context.Background(), j)
	if err != nil || id != 31 || *posts != 1 {
		t.Fatalf("file handoff did not produce one verified final: id=%d posts=%d err=%v", id, *posts, err)
	}
	c.st = loadState(c.st.path)
	id, err = c.publishFinalReport(context.Background(), j)
	if err != nil || id != 31 || *posts != 1 {
		t.Fatalf("restart duplicated or lost acceptance: id=%d posts=%d err=%v", id, *posts, err)
	}
}

func TestFinalReportPublishesBeforeSupervisionAndTickRetirement(t *testing.T) {
	for _, path := range []string{"supervise", "tick"} {
		t.Run(path, func(t *testing.T) {
			c, j, _, posts, _ := finalPublicationFixture(t)
			close := c.actions.close
			closes := 0
			c.actions.close = func(ctx context.Context, h Host, ws string) error {
				if *posts != 1 {
					t.Fatal("retirement preceded report publication")
				}
				closes++
				return close(ctx, h, ws)
			}
			bin := t.TempDir()
			fleet, _ := json.Marshal(map[string]any{"result": map[string]any{"agents": []AgentInfo{completionAgent(j)}}})
			t.Setenv("FINAL_FLEET", string(fleet))
			for name, script := range map[string]string{
				"gh":  "#!/bin/sh\nprintf '[{\"number\":7,\"title\":\"fixture\",\"state\":\"OPEN\",\"body\":\"\",\"labels\":[]}]\\n'\n",
				"ssh": "#!/bin/sh\nprintf '%s\\n' \"$FINAL_FLEET\"\n",
			} {
				if os.WriteFile(filepath.Join(bin, name), []byte(script), 0700) != nil {
					t.Fatal("fixture command")
				}
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			h := c.hosts[j.Host]
			h.SSH = "fixture-host"
			c.hosts[j.Host] = h
			if path == "tick" {
				c.tick(context.Background())
			} else {
				c.supervise(context.Background(), j.Issue, j, map[int]agentRef{j.Issue: completionRef(j)}, Issue{Number: j.Issue})
			}
			if *posts != 1 || closes != 1 || c.st.CompletedRuns[j.Issue].DispatchKey != j.DispatchKey {
				t.Fatal("actual coordinator path lost publication or completion fencing")
			}
		})
	}
}

func TestFinalReportRejectsInvalidOrChangedReport(t *testing.T) {
	for name, body := range map[string]string{"blank": " \n\t", "oversize": strings.Repeat("x", 60001), "invalid-utf8": string([]byte{0xff}), "marker": "text\n<!-- orchid-run foreign -->"} {
		t.Run(name, func(t *testing.T) {
			c, j, report, posts, _ := finalPublicationFixture(t)
			*report = body
			if id, err := c.publishFinalReport(context.Background(), j); err == nil || id != 0 || *posts != 0 {
				t.Fatal("invalid report published")
			}
		})
	}
	t.Run("changed-before-post", func(t *testing.T) {
		c, j, _, posts, _ := finalPublicationFixture(t)
		calls := 0
		c.finalCalls.report = func(context.Context, Host, finalReportScope) (string, error) {
			calls++
			if calls == 1 {
				return "first", nil
			}
			return "changed", nil
		}
		if id, err := c.publishFinalReport(context.Background(), j); err == nil || id != 0 || *posts != 0 {
			t.Fatal("changed report published")
		}
	})
	t.Run("changed-after-accepted", func(t *testing.T) {
		c, j, report, posts, _ := finalPublicationFixture(t)
		if _, err := c.publishFinalReport(context.Background(), j); err != nil {
			t.Fatal(err)
		}
		*report = "different visible report"
		if id, err := c.publishFinalReport(context.Background(), j); err == nil || id != 0 || *posts != 1 {
			t.Fatal("edited report replaced accepted comment")
		}
	})
}

func TestFinalReportReadbackIsIndependentAndExact(t *testing.T) {
	for _, name := range []string{"id", "issue", "author", "empty-bot", "body", "marker", "old", "future", "missing", "error"} {
		t.Run(name, func(t *testing.T) {
			c, j, _, posts, _ := finalPublicationFixture(t)
			original := c.finalCalls.comment
			c.finalCalls.comment = func(ctx context.Context, d dispatchIssue, id int64) (finalPostedComment, error) {
				v, e := original(ctx, d, id)
				switch name {
				case "id":
					v.ID++
				case "issue":
					v.IssueURL += "8"
				case "author":
					v.User.Login = "foreign"
				case "empty-bot":
					c.cfg.BotLogin = ""
					v.User.Login = ""
				case "body":
					v.Body = "other"
				case "marker":
					v.Body += finalCommentMarker(j.DispatchKey)
				case "old":
					v.CreatedAt = j.SpawnedAt.Add(-time.Minute)
				case "future":
					v.CreatedAt = time.Now().Add(time.Minute)
				case "missing":
					v = finalPostedComment{}
				case "error":
					e = errors.New("fixture")
				}
				return v, e
			}
			if id, err := c.publishFinalReport(context.Background(), j); err == nil || id != 0 || *posts != 1 {
				t.Fatal("unverified returned comment accepted")
			}
			dir, _, _ := c.finalScope(j)
			if _, err := os.Lstat(filepath.Join(dir, "accepted.json")); !os.IsNotExist(err) {
				t.Fatal("unverified receipt exists")
			}
		})
	}
}

func TestFinalReportLostReplyAndInterruptedWritesNeverRepeatPost(t *testing.T) {
	for _, stage := range []string{"intent", "post", "reply", "readback", "accepted"} {
		t.Run(stage, func(t *testing.T) {
			c, j, _, posts, _ := finalPublicationFixture(t)
			if stage == "reply" {
				original := c.finalCalls.post
				c.finalCalls.post = func(ctx context.Context, d dispatchIssue, b string) (int64, error) {
					id, _ := original(ctx, d, b)
					return id, errors.New("lost reply")
				}
			}
			if stage == "readback" {
				c.finalCalls.comment = func(context.Context, dispatchIssue, int64) (finalPostedComment, error) {
					return finalPostedComment{}, errors.New("read failed")
				}
			}
			c.finalCalls.write = func(dir, name string, v any) error {
				err := actionImmutableJSON(dir, name, v)
				if name == stage+".json" {
					return errors.New("sync failed")
				}
				return err
			}
			if id, err := c.publishFinalReport(context.Background(), j); err == nil || id != 0 {
				t.Fatal("interrupted publication accepted")
			}
			first := *posts
			c.st = loadState(c.st.path)
			c.finalCalls.write = nil
			_, _ = c.publishFinalReport(context.Background(), j)
			if *posts != first {
				t.Fatal("restart retried a fenced POST")
			}
			if stage == "intent" && first != 0 || stage != "intent" && first != 1 {
				t.Fatal("write order did not surround one effect")
			}
		})
	}
}

func TestFinalReportDeadlineFenceAtEveryEffect(t *testing.T) {
	for _, stage := range []string{"entry", "report", "proof", "intent", "post", "result", "comment", "accepted"} {
		t.Run(stage, func(t *testing.T) {
			c, j, _, posts, _ := finalPublicationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "entry" {
				cancel()
			}
			if stage == "report" {
				original := c.finalCalls.report
				c.finalCalls.report = func(ctx context.Context, h Host, s finalReportScope) (string, error) {
					v, e := original(ctx, h, s)
					cancel()
					return v, e
				}
			}
			if stage == "proof" {
				c.finalCalls.proof = func(context.Context, Host, *Job, finalReportScope) error { cancel(); return nil }
			}
			if stage == "post" {
				original := c.finalCalls.post
				c.finalCalls.post = func(ctx context.Context, d dispatchIssue, b string) (int64, error) {
					v, e := original(ctx, d, b)
					cancel()
					return v, e
				}
			}
			if stage == "comment" {
				original := c.finalCalls.comment
				c.finalCalls.comment = func(ctx context.Context, d dispatchIssue, id int64) (finalPostedComment, error) {
					v, e := original(ctx, d, id)
					cancel()
					return v, e
				}
			}
			c.finalCalls.write = func(dir, name string, v any) error {
				err := actionImmutableJSON(dir, name, v)
				if name == stage+".json" || stage == "result" && name == "post.json" {
					cancel()
				}
				return err
			}
			if id, err := c.publishFinalReport(ctx, j); err == nil || id != 0 {
				t.Fatal("late effect certified acceptance")
			}
			if stage == "entry" || stage == "report" || stage == "proof" || stage == "intent" {
				if *posts != 0 {
					t.Fatal("expired context reached POST")
				}
			}
			if stage == "accepted" {
				dir, _, _ := c.finalScope(j)
				if _, err := os.Lstat(filepath.Join(dir, "accepted.json")); !os.IsNotExist(err) {
					t.Fatal("late accepted receipt retained")
				}
			}
		})
	}
}

func TestFinalReportPrivateAuthorityCannotBeReplaced(t *testing.T) {
	for _, name := range []string{"scope-destination", "scope-destination-repo", "scope-repo", "scope-source", "scope-host", "scope-key", "scope-digest", "scope-dispatch-digest", "scope-schema", "scope-cwd", "scope-cwd-dot", "scope-mode", "scope-symlink", "scope-unknown", "intent-body", "intent-digest", "intent-body-digest", "intent-time", "intent-zero-time", "intent-old-time", "post-id", "post-digest", "accepted-id"} {
		t.Run(name, func(t *testing.T) {
			c, j, _, posts, _ := finalPublicationFixture(t)
			dir, s, err := c.finalScope(j)
			if err != nil {
				t.Fatal(err)
			}
			file := "scope.json"
			if strings.HasPrefix(name, "scope-") {
				switch name {
				case "scope-destination":
					s.Destination.Number++
				case "scope-destination-repo":
					s.Destination.Repo = "fixture/foreign"
				case "scope-repo":
					s.Repo = "fixture/foreign"
				case "scope-source":
					s.Source = "other"
				case "scope-host":
					s.Host = "other"
				case "scope-key":
					s.Key = strings.Repeat("a", 64)
				case "scope-digest":
					s.BindingDigest = "invalid"
				case "scope-dispatch-digest":
					s.DispatchDigest = "invalid"
				case "scope-schema":
					s.SchemaVersion++
				case "scope-cwd":
					s.Cwd = "relative"
				case "scope-cwd-dot":
					s.Cwd = "/fixture/../changed"
				}
				b, _ := json.Marshal(s)
				if name == "scope-unknown" {
					b = append(b[:len(b)-1], []byte(`,"foreign":true}`)...)
				}
				if err := os.WriteFile(filepath.Join(dir, file), b, 0600); err != nil {
					t.Fatal(err)
				}
				if name == "scope-mode" {
					_ = os.Chmod(filepath.Join(dir, file), 0644)
				}
				if name == "scope-symlink" {
					path := filepath.Join(dir, file)
					_ = os.Rename(path, filepath.Join(dir, "other.json"))
					_ = os.Symlink(filepath.Join(dir, "other.json"), path)
				}
			} else {
				if _, err := c.publishFinalReport(context.Background(), j); err != nil {
					t.Fatal(err)
				}
				parts := strings.Split(name, "-")
				file = parts[0] + ".json"
				var value map[string]any
				b, _ := os.ReadFile(filepath.Join(dir, file))
				_ = json.Unmarshal(b, &value)
				switch name {
				case "intent-body":
					value["body"] = "different"
				case "intent-digest":
					value["scopeDigest"] = "foreign"
				case "intent-body-digest":
					value["bodyDigest"] = "foreign"
				case "intent-time":
					value["startedAt"] = time.Now().Add(time.Hour)
				case "intent-zero-time":
					value["startedAt"] = time.Time{}
				case "intent-old-time":
					value["startedAt"] = j.SpawnedAt.Add(-time.Minute)
				case "post-id", "accepted-id":
					value["commentId"] = 0
				case "post-digest":
					value["intentDigest"] = "foreign"
				}
				b, _ = json.Marshal(value)
				_ = os.WriteFile(filepath.Join(dir, file), b, 0600)
				// Keep later result pairing internally consistent so it cannot
				// mask the intent field under test.
				if parts[0] == "intent" {
					var changed finalPublicationIntent
					_ = json.Unmarshal(b, &changed)
					for _, record := range []string{"post.json", "accepted.json"} {
						var result finalPublicationResult
						_ = finalPrivateRead(dir, record, &result)
						result.IntentDigest = finalJSONDigest(changed)
						data, _ := json.Marshal(result)
						_ = os.WriteFile(filepath.Join(dir, record), data, 0600)
					}
				}
				if name == "post-id" || name == "post-digest" {
					_ = os.WriteFile(filepath.Join(dir, "accepted.json"), b, 0600)
					if name == "post-id" {
						c.finalCalls.comment = func(context.Context, dispatchIssue, int64) (finalPostedComment, error) {
							v := finalPostedComment{}
							v.Body, _ = finalRenderedBody("VISIBLE_REPORT_OK\n", j.DispatchKey)
							v.IssueURL = "https://api.github.com/repos/" + c.cfg.Inbox + "/issues/7"
							v.User.Login = c.cfg.BotLogin
							v.CreatedAt = time.Now().Truncate(time.Second)
							return v, nil
						}
					}
				}
			}
			before := *posts
			if id, err := c.publishFinalReport(context.Background(), j); err == nil || id != 0 || *posts != before {
				t.Fatal("tampered private work granted acceptance or another post")
			}
		})
	}
}

func TestFinalReportChangeDuringPostCannotGrantAcceptance(t *testing.T) {
	for _, name := range []string{"report", "scope", "goal"} {
		t.Run(name, func(t *testing.T) {
			c, j, report, posts, _ := finalPublicationFixture(t)
			original := c.finalCalls.post
			c.finalCalls.post = func(ctx context.Context, d dispatchIssue, b string) (int64, error) {
				id, e := original(ctx, d, b)
				switch name {
				case "report":
					*report = "replaced after POST"
				case "scope":
					dir, s, _ := c.finalScope(j)
					s.Cwd = "/fixture/changed"
					raw, _ := json.Marshal(s)
					_ = os.WriteFile(filepath.Join(dir, "scope.json"), raw, 0600)
				case "goal":
					j.GoalDelivery = "blocked"
				}
				return id, e
			}
			if id, err := c.publishFinalReport(context.Background(), j); err == nil || id != 0 || *posts != 1 {
				t.Fatal("changed post context granted acceptance")
			}
		})
	}
}

func TestFinalReportMatchedUnfencedConfirmedOnly(t *testing.T) {
	for _, name := range []string{"dry", "legacy", "run-mode", "pending", "unknown", "host", "pane", "workspace", "agent", "fenced"} {
		t.Run(name, func(t *testing.T) {
			c, j, _, posts, _ := finalPublicationFixture(t)
			ref := completionRef(j)
			known := true
			switch name {
			case "dry":
				c.dry = true
			case "legacy":
				j.FinalReportManaged = false
			case "run-mode":
				j.RunMode = true
			case "pending":
				j.GoalDelivery = "pending"
			case "unknown":
				known = false
			case "host":
				ref.Host = "foreign"
			case "pane":
				ref.Pane = "foreign"
			case "workspace":
				ref.Workspace = "foreign"
			case "agent":
				ref.Agent = "foreign"
			case "fenced":
				c.st.CompletedRuns = map[int]completedRun{j.Issue: {DispatchKey: j.DispatchKey}}
			}
			c.observeFinalReport(context.Background(), j, ref, known)
			if *posts != 0 {
				t.Fatal("ineligible worker published")
			}
		})
	}
}

// claudeCoordinatorFixture is a Claude run whose turn has ended (seat done)
// while its process tree still holds a live background lane.
func claudeCoordinatorFixture(t *testing.T) (*Coord, *Job, *bool, *bool, *int) {
	t.Helper()
	c, j, seatGone, processGone, closes := completionFixture(t)
	j.Agent, j.Label = "claude", "claude-fixture"
	j.FinalReportManaged = true
	record := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
	var d dispatchBinding
	_ = readPrivateActionJSON(filepath.Join(record, "dispatch.json"), &d)
	d.Source = "claude"
	b, _ := json.Marshal(d)
	_ = os.WriteFile(filepath.Join(record, "dispatch.json"), b, 0600)
	c.actions.list = func(context.Context, Host) ([]AgentInfo, error) {
		if *seatGone {
			return nil, nil
		}
		a := completionAgent(j)
		a.AgentSession = json.RawMessage(`{"source":"herdr:claude","agent":"claude","kind":"id","value":"thread-fixture"}`)
		return []AgentInfo{a}, nil
	}
	c.cfg.Targets = []Target{{Agent: "claude"}}
	return c, j, seatGone, processGone, closes
}

func TestCompletionEndedClaudeTurnWithLiveChildIsNeverRetired(t *testing.T) {
	c, j, _, _, closes := claudeCoordinatorFixture(t)
	c.actions.completed = nil // no final report, no goal: the turn merely ended
	ref := completionRef(j)
	var logs bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldLog)
	for i := 0; i < 3; i++ {
		if c.retireCompleted(context.Background(), 7, j, ref, true) {
			t.Fatal("an ended turn retired the run")
		}
		j.FinalDoneAt, j.FinalDoneSeq = time.Now().Add(-time.Hour), ref.StateChangeSeq // legacy grace state grants nothing
	}
	if *closes != 0 || c.st.Jobs[7] != j || len(c.st.CompletedRuns) != 0 {
		t.Fatal("waiting coordinator was torn down or fenced")
	}
	if strings.Contains(logs.String(), "publication unconfirmed") {
		t.Fatal("ended turn reported as a failed publication")
	}
	if c.admissionBudget(map[int]agentRef{7: ref})["claude"] != 0 {
		t.Fatal("seat idle between turns released capacity")
	}
}

func TestCompletionClaudeFinalReportRetiresRun(t *testing.T) {
	c, j, seatGone, processGone, closes := claudeCoordinatorFixture(t)
	c.actions.completed = func(context.Context, Host, *Job, string) (bool, error) { return true, nil }
	ref := completionRef(j)
	if !c.retireCompleted(context.Background(), 7, j, ref, true) || *closes != 1 || c.st.CompletedRuns[7].PublicationUnconfirmed {
		t.Fatal("final report did not retire the run as completed")
	}
	*seatGone, *processGone = true, true
	c.retireCompleted(context.Background(), 7, j, ref, true)
	if c.st.Jobs[7] != nil || c.st.CompletedRuns[7].Phase != "observed" || c.st.reserveLaunch(7) {
		t.Fatal("completed run kept its job or lost its no-replay fence")
	}
	if c.admissionBudget(map[int]agentRef{})["claude"] != 1 {
		t.Fatal("completed run leaked capacity")
	}
}

func TestCompletionTimeoutRetiresWithLiveChildReason(t *testing.T) {
	for _, name := range []string{"live-children", "no-children", "unreadable", "source-binding"} {
		t.Run(name, func(t *testing.T) {
			ghLog, _ := completionSupervisionCommands(t)
			c, j, _, _, _ := claudeCoordinatorFixture(t)
			h := c.hosts[j.Host]
			h.SSH = "fixture-host"
			c.hosts[j.Host] = h
			c.actions.completed = nil
			reads := 0
			c.actions.stopProcess = func(context.Context, Host, string, string) (*actionStopProcess, error) {
				if reads++; reads > 1 { // the teardown's own anchor, as teardownFixture records it
					return &actionStopProcess{SchemaVersion: 1, GroupID: 301, RootPID: 301, Members: []actionProcessIdentity{{PID: 301, Start: 9}}}, nil
				}
				switch name {
				case "live-children", "source-binding":
					return &actionStopProcess{SchemaVersion: 1, GroupID: 40, RootPID: 41, Members: []actionProcessIdentity{{PID: 41, Start: 1}, {PID: 42, Start: 2}, {PID: 43, Start: 3}}}, nil
				case "no-children":
					return &actionStopProcess{SchemaVersion: 1, GroupID: 40, RootPID: 41, Members: []actionProcessIdentity{{PID: 41, Start: 1}}}, nil
				}
				return nil, errors.New("native_process_unavailable")
			}
			j.Overrides.Timeout = time.Minute
			j.Deadline = time.Now().Add(-time.Second)
			if name == "source-binding" { // the owner's source issue is never closed; the reason goes in its reply
				c.st.SourceBindings = map[int]*sourceBinding{7: {Repo: "fixture/project", Issue: 70, Comment: 71}}
			}
			var logs bytes.Buffer
			oldLog := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(oldLog)
			ref := completionRef(j)
			c.supervise(context.Background(), 7, j, map[int]agentRef{7: ref}, Issue{Number: 7})
			calls, _ := os.ReadFile(ghLog)
			if b, ok := c.sourceBinding(7); ok {
				for _, r := range b.Outbox {
					calls = append(calls, r.Body+"\nissue close 7\n"...)
				}
			}
			want := map[string]string{
				"source-binding": "exceeded; stopping 2 live child processes",
				"live-children":  "exceeded; stopping 2 live child processes",
				"no-children":    "exceeded — ",
				"unreadable":     "exceeded; live child processes unknown",
			}[name]
			if !strings.Contains(logs.String(), "operator timeout (1m0s) "+want) || !strings.Contains(string(calls), want) {
				t.Fatalf("timeout reason not recorded in log and on the issue: %s %s", logs.String(), calls)
			}
			if !strings.Contains(string(calls), "issue close 7") || c.st.Jobs[7] != nil {
				t.Fatal("timeout did not retire the run")
			}
		})
	}
}
func TestFinalReportCompletionFallbackNeedsProducerReceipt(t *testing.T) {
	c, j, _, _, _ := finalPublicationFixture(t)
	j.Agent = "claude"
	c.actions.completed = nil
	bin := t.TempDir()
	comments, _ := json.Marshal(map[string]any{"comments": []any{map[string]any{"body": finalCommentMarker(j.DispatchKey), "createdAt": time.Now().Format(time.RFC3339), "author": map[string]string{"login": c.cfg.BotLogin}}}})
	t.Setenv("FINAL_MODEL_COMMENTS", string(comments))
	if os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$FINAL_MODEL_COMMENTS\"\n"), 0700) != nil {
		t.Fatal("fixture gh")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	complete, err := c.completionEvidence(context.Background(), c.hosts[j.Host], j, "thread-fixture")
	if complete || err == nil {
		t.Fatal("model-owned marker substituted producer acceptance")
	}
	j.FinalReportManaged = false
	if complete, err := c.completionEvidence(context.Background(), c.hosts[j.Host], j, "thread-fixture"); !complete || err != nil {
		t.Fatal("legacy control did not accept the model-owned marked comment", err)
	}
}

func TestFinalReportPrivateRootAndLegacyScopeRemainClosed(t *testing.T) {
	for _, name := range []string{"root-mode", "root-symlink", "legacy", "run-mode", "unknown-host"} {
		t.Run(name, func(t *testing.T) {
			c, j, _, posts, _ := finalPublicationFixture(t)
			dir, _, _ := c.finalScope(j)
			switch name {
			case "root-mode":
				_ = os.Chmod(filepath.Dir(dir), 0755)
			case "root-symlink":
				root := filepath.Dir(dir)
				_ = os.Rename(root, root+"-other")
				_ = os.Symlink(root+"-other", root)
			case "legacy":
				j.FinalReportManaged = false
			case "run-mode":
				j.RunMode = true
			case "unknown-host":
				c.hosts = map[string]Host{}
			}
			if id, err := c.publishFinalReport(context.Background(), j); id != 0 || err == nil || *posts != 0 {
				t.Fatal("unowned/private/legacy scope reached publisher")
			}
		})
	}
}
