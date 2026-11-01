package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const githubFixtureSecret = "fixture webhook signing material"

// Exercise the producer constructor used by Coord.run with no Herdr producers.
func githubWakeFixture(t *testing.T, c *Coord, timing ...supervisionWakeTiming) (*http.Client, context.Context) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	socket, secret := filepath.Join(root, "hook"), filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte(githubFixtureSecret), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORCHID_GITHUB_WEBHOOK_SOCKET", socket)
	t.Setenv("ORCHID_GITHUB_WEBHOOK_SECRET_FILE", secret)
	ctx, cancel := context.WithCancel(context.Background())
	c.startHerdrFleet(ctx, timing...)
	fleet := c.fleet
	t.Cleanup(func() { cancel(); <-fleet.wakes.done })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 2 * time.Second}, ctx
}

func githubSignedRequest(t *testing.T, kind, delivery, payload string) *http.Request {
	t.Helper()
	// The transport above always dials the private fixture socket.
	r, err := http.NewRequest(http.MethodPost, "http://fixture/", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(githubFixtureSecret))
	_, _ = io.WriteString(mac, payload)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-GitHub-Event", kind)
	r.Header.Set("X-GitHub-Delivery", delivery)
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return r
}

func githubDeliver(t *testing.T, client *http.Client, kind, delivery, payload string) {
	t.Helper()
	response, err := client.Do(githubSignedRequest(t, kind, delivery, payload))
	if err != nil {
		t.Fatalf("native GitHub ingress unavailable: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("native GitHub ingress status=%d", response.StatusCode)
	}
}

func githubPickup(t *testing.T, c *Coord) time.Time {
	t.Helper()
	select {
	case <-c.fleet.wake:
		return time.Now()
	case <-time.After(2 * time.Second):
		t.Fatal("GitHub change did not wake the existing supervision owner")
		return time.Time{}
	}
}

func TestGitHubEventProductionWakeQuietFleet(t *testing.T) {
	c := newCoord(&Config{Inbox: "example/inbox"})
	c.st.Jobs[7] = &Job{Issue: 7}
	client, _ := githubWakeFixture(t, c)
	created := time.Now()
	githubDeliver(t, client, "issue_comment", "fixture-delivery", `{"action":"created","repository":{"full_name":"example/inbox"},"issue":{"number":7},"comment":{"id":701}}`)
	picked := githubPickup(t, c)
	t.Logf("controlled signed comment to wake pickup=%s; no hosts or Herdr events", picked.Sub(created))
	// A replay under a new delivery header is still the same signed payload.
	githubDeliver(t, client, "issue_comment", "fixture-replay", `{"action":"created","repository":{"full_name":"example/inbox"},"issue":{"number":7},"comment":{"id":701}}`)
	select {
	case <-c.fleet.wake:
		t.Fatal("replayed native delivery woke supervision twice")
	default:
	}
}

func TestGitHubEventCoalescesAndRetainsEventsDuringWork(t *testing.T) {
	c := newCoord(&Config{Inbox: "example/inbox"})
	c.st.Jobs[7] = &Job{Issue: 7, Repo: "example/inbox", Branch: "owned-branch"}
	client, _ := githubWakeFixture(t, c, githubTestWakeTiming())
	for n := range 20 {
		githubDeliver(t, client, "check_run", strings.Repeat("a", n+1), `{"repository":{"full_name":"example/inbox"},"check_run":{"id":`+strings.Repeat("1", n+1)+`,"check_suite":{"head_branch":"owned-branch"}}}`)
	}
	githubPickup(t, c)
	// The serial owner has consumed the bit and is now doing authoritative work.
	// A new event must queue the next tick even while the current work is running.
	githubDeliver(t, client, "issues", "during-work", `{"action":"closed","repository":{"full_name":"example/inbox"},"issue":{"number":7}}`)
	githubPickup(t, c)
	select {
	case <-c.fleet.wake:
		t.Fatal("event burst accumulated extra ticks")
	default:
	}
}

func TestGitHubEventSourceReconciliation(t *testing.T) {
	c, source, _ := sourceFixture(t)
	startSource(t, c)
	client, ctx := githubWakeFixture(t, c, githubTestWakeTiming())
	created := time.Now().UTC()
	c.st.SourceStarts[sourceRepo], c.st.SourceCursors[sourceRepo] = created.Add(-time.Second), created.Add(-time.Second)
	source.post(sourceRepo, 2063, 3100000701, ownerGitHubID, sourceTrigger, created)
	githubDeliver(t, client, "issue_comment", "source-comment", `{"action":"created","repository":{"full_name":"example/netscript"},"issue":{"number":2063},"comment":{"id":3100000701,"body":"/swarm\nSynthetic task","user":{"id":129366361}}}`)
	pickup := githubPickup(t, c)
	work := time.Now()
	c.sourceTick(ctx)
	open, all := map[int]Issue{}, map[int]bool{}
	if !c.pollSourceBindings(ctx, open, all) || len(open) != 1 {
		t.Fatal("native comment did not reach the existing granted binding reader")
	}
	t.Logf("controlled comment→pickup=%s; queue/work clocks separated; source reconciliation=%s", pickup.Sub(created), time.Since(work))
	// Payload order never overrides the current GitHub response or the unknown guard.
	source.failIssue = true
	githubDeliver(t, client, "issues", "close-with-read-failure", `{"action":"closed","repository":{"full_name":"example/netscript"},"issue":{"number":2063}}`)
	githubPickup(t, c)
	open, all = map[int]Issue{}, map[int]bool{}
	if c.pollSourceBindings(ctx, open, all) || !all[3100000701] || len(open) != 1 {
		t.Fatal("failed GitHub read discarded the existing binding or reported a reliable close")
	}
	source.failIssue = false
	source.issues[sourceRepo+"#2063"] = sourceIssueView{NodeID: "I_source_2063", State: "CLOSED"}
	// An older reopened payload arrives after closed: only the fresh read decides.
	githubDeliver(t, client, "issues", "reordered-open", `{"action":"reopened","repository":{"full_name":"example/netscript"},"issue":{"number":2063}}`)
	githubPickup(t, c)
	open, all = map[int]Issue{}, map[int]bool{}
	if !c.pollSourceBindings(ctx, open, all) || all[3100000701] {
		t.Fatal("stale event payload overrode the authoritative closed issue")
	}
	// Lost events are repaired by exactly the same reader the five-minute tick owns.
	source.issues[sourceRepo+"#2063"] = sourceIssueView{NodeID: "I_source_2063", State: "OPEN"}
	source.post(sourceRepo, 2063, 3100000702, ownerGitHubID, sourceTrigger, time.Now().UTC())
	c.sourceTick(ctx)
	if c.st.SourceBindings[3100000702] == nil {
		t.Fatal("missed-event recovery did not discover the source comment")
	}
}

func TestGitHubEventSourceSingleEffect(t *testing.T) {
	store, request, issue := ownerPortFixture(t, "claude")
	_ = os.Remove(filepath.Join(store.options.ApprovalRoot, request.ApprovalRef+".json"))
	body := "/swarm\ntier: feature\nrole: implementation\nrepo: example/project\n\nSynthetic task"
	request.Trigger, request.ExpectedBriefDigest = ownerNativeCommentTrigger, shaText([]byte(body))
	if ack := store.install(context.Background(), request); ack.State != "LIVE" {
		t.Fatal("fixture grant not live")
	}
	c := &Coord{cfg: store.cfg, st: loadState(filepath.Join(t.TempDir(), "state.json")), ownerGrants: store}
	c.cfg.OwnerNativeGrantPort = &store.options
	source := newFakeSource()
	c.source = source
	source.issues["example/project#7"] = sourceIssueView{NodeID: issue.ID, Title: issue.Title, State: "OPEN"}
	now := time.Now()
	c.cfg.Governor.WeeklyCeiling = 92
	c.gov.q = map[string]quota{"claude": {ok: true, at: now, five: RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}, seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()}}}
	c.sourceTick(context.Background())
	c.st.SourceStarts["example/project"], c.st.SourceCursors["example/project"] = now.Add(-time.Second), now.Add(-time.Second)
	client, ctx := githubWakeFixture(t, c, githubTestWakeTiming())
	launches := 0
	deps := matrixAttemptDeps{
		report: func(r matrixRefusal) { t.Logf("fixture admission verdict: %+v", r) },
		read: func(context.Context, string, string, string) (string, error) {
			return "| `routing` | matrix `implementation` row |", nil
		},
		resolve: func(context.Context, MatrixConfig, matrixRequest) (matrixRoute, error) { return syntheticRoute(), nil },
		host:    func(Target, string) (Host, bool) { return Host{Name: "fixture-node"}, true },
		persist: persistMatrixReceipt,
		launch: func(_ context.Context, _ int, _ Issue, _ Host, agent string, overrides Overrides, receipt *durableMatrixReceipt) error {
			if !receipt.claim(mustBuildAgentCmd(t, agent, overrides)) {
				t.Fatal("launch bypassed its durable receipt")
			}
			launches++
			return nil
		},
	}
	reconcile := func() {
		c.sourceTick(ctx)
		open := map[int]Issue{}
		if !c.pollSourceBindings(ctx, open, map[int]bool{}) {
			t.Fatal("fixture authoritative read failed")
		}
		if is, ok := open[3100000703]; ok {
			c.matrixAttempt(ctx, is.Number, is, c.cfg.Targets[0], map[string]int{"claude": 1}, deps)
		}
	}
	created := time.Now().UTC()
	source.post("example/project", 7, 3100000703, ownerGitHubID, body, created)
	payload := `{"action":"created","repository":{"full_name":"example/project"},"issue":{"number":7},"comment":{"id":3100000703,"body":"/swarm\nSynthetic task","user":{"id":129366361}}}`
	githubDeliver(t, client, "issue_comment", "source-launch", payload)
	pickup := githubPickup(t, c)
	work := time.Now()
	reconcile()
	if launches != 1 {
		t.Fatal("comment wake did not reach the existing launch fence")
	}
	t.Logf("controlled source comment→pickup=%s; reconcile→launch callback=%s; native runtime start not measured", pickup.Sub(created), time.Since(work))
	githubDeliver(t, client, "issue_comment", "source-replay", payload)
	if len(c.fleet.wake) != 0 {
		t.Fatal("signed body replay queued another wake")
	}
	// Restore state and construct a new receiver with an empty ingress cache.
	// The identical signed payload now wakes, but still cannot bypass durable admission.
	c.st = loadState(c.st.path)
	client, ctx = githubWakeFixture(t, c, githubTestWakeTiming())
	githubDeliver(t, client, "issue_comment", "source-launch", payload)
	githubPickup(t, c)
	reconcile()
	reconcile() // same reader is the repair poll, including a missed or evicted event
	if launches != 1 {
		t.Fatal("restart or repair admitted the same source comment twice")
	}
}

func TestGitHubEventReadyReviewAndChecksReadCurrentHead(t *testing.T) {
	c := newCoord(&Config{Inbox: "example/inbox", Targets: []Target{{Repo: "example/project"}}})
	job := &Job{Issue: 7, Repo: "example/project", Branch: "owned-branch", Home: &dispatchIssue{Repo: "example/project", Number: 7}, PR: 7}
	c.st.Jobs[7] = job
	client, ctx := githubWakeFixture(t, c, githubTestWakeTiming())
	bin := t.TempDir()
	head := strings.Repeat("a", 40)
	view := `{"number":7,"headRefOid":"` + head + `","mergeable":"MERGEABLE","state":"OPEN","isDraft":false,"reviews":[{"id":"fixture-review","author":{"login":"reviewer"},"state":"APPROVED"}],"statusCheckRollup":[{"name":"build","conclusion":"SUCCESS"}]}`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\ncat <<'JSON'\n"+view+"\nJSON\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, kind := range []string{"issue_comment", "pull_request", "pull_request_review", "check_run", "check_suite", "status"} {
		ready := time.Now()
		// READY names the exact current head; delayed reviews/checks can name an old one.
		payload := map[string]any{"repository": map[string]string{"full_name": "example/project"}}
		oldHead := strings.Repeat("b", 40)
		switch kind {
		case "issue_comment":
			payload["action"] = "created"
			payload["issue"] = map[string]int{"number": 7}
			payload["comment"] = map[string]any{"id": 701, "body": "READY " + head, "created_at": ready.UTC().Format(time.RFC3339Nano)}
		case "pull_request":
			payload["action"] = "ready_for_review"
			payload["pull_request"] = map[string]any{"number": 7, "head": map[string]any{"sha": head, "ref": "owned-branch", "repo": map[string]string{"full_name": "example/project"}}}
		case "pull_request_review":
			payload["action"] = "submitted"
			payload["pull_request"] = map[string]any{"head": map[string]any{"ref": "owned-branch", "repo": map[string]string{"full_name": "example/project"}}}
			payload["review"] = map[string]string{"commit_id": oldHead, "state": "approved"}
		case "check_run", "check_suite":
			payload["action"] = "completed"
			payload[kind] = map[string]any{"head_sha": oldHead, "head_branch": "owned-branch", "check_suite": map[string]string{"head_branch": "owned-branch"}, "pull_requests": []any{}}
		case "status":
			payload["sha"], payload["state"] = oldHead, "success"
			payload["branches"] = []any{map[string]string{"name": "owned-branch"}}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		githubDeliver(t, client, kind, kind, string(raw))
		pickup := githubPickup(t, c)
		work := time.Now()
		c.pollPR(ctx, 7, job, Host{}, "working")
		if job.Track.HeadOID != head || job.Track.Checks["build"] != "SUCCESS" || !contains(job.Track.Reviews, "fixture-review") {
			t.Fatal("native review/check/READY wake did not reconcile the current PR head")
		}
		t.Logf("controlled %s READY→pickup=%s; PR work=%s", kind, pickup.Sub(ready), time.Since(work))
	}
}

func githubTestWakeTiming() supervisionWakeTiming {
	return supervisionWakeTiming{debounce: 2 * time.Millisecond, minSpacing: 10 * time.Millisecond}
}
