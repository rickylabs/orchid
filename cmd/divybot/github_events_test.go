package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const githubFixturePayload = `{"repository":{"full_name":"example/inbox"}}`

func TestGitHubEventAuthenticationAndBounds(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		status        int
		alter         func(*http.Request)
	}{
		{"valid", githubFixturePayload, http.StatusAccepted, func(*http.Request) {}},
		{"missing-signature", githubFixturePayload, http.StatusUnauthorized, func(r *http.Request) { r.Header.Del("X-Hub-Signature-256") }},
		{"wrong-signature", githubFixturePayload, http.StatusUnauthorized, func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64)) }},
		{"wrong-algorithm", githubFixturePayload, http.StatusUnauthorized, func(r *http.Request) {
			r.Header.Set("X-Hub-Signature-256", strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
		}},
		{"tampered-body", githubFixturePayload, http.StatusUnauthorized, func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha256=00") }},
		{"bad-json", "{", http.StatusBadRequest, func(*http.Request) {}},
		{"oversize", strings.Repeat("x", githubEventBodyLimit+1), http.StatusRequestEntityTooLarge, func(*http.Request) {}},
		{"no-delivery", githubFixturePayload, http.StatusBadRequest, func(r *http.Request) { r.Header.Del("X-GitHub-Delivery") }},
		{"long-delivery", githubFixturePayload, http.StatusBadRequest, func(r *http.Request) { r.Header.Set("X-GitHub-Delivery", strings.Repeat("x", 129)) }},
		{"wrong-method", githubFixturePayload, http.StatusMethodNotAllowed, func(r *http.Request) { r.Method = http.MethodGet }},
		{"wrong-type", githubFixturePayload, http.StatusUnsupportedMediaType, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{"encoded", githubFixturePayload, http.StatusUnsupportedMediaType, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			wakes := 0
			g := newGitHubEventIngress([]byte(githubFixtureSecret), func(repo string) bool { return repo == "example/inbox" }, func() { wakes++ })
			r := githubSignedRequest(t, "issue_comment", "fixture-delivery", test.payload)
			test.alter(r)
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			wantWakes := 0
			if test.name == "valid" {
				wantWakes = 1
			}
			if w.Code != test.status || wakes != wantWakes {
				t.Fatalf("status=%d wakes=%d; want status=%d wakes=%d", w.Code, wakes, test.status, wantWakes)
			}
		})
	}
}

func TestGitHubEventVocabularyAndRepositoryScope(t *testing.T) {
	for index, kind := range []string{"issue_comment", "issues", "pull_request", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread", "check_run", "check_suite", "status", "ping", "push", "workflow_run", "unknown"} {
		for _, repo := range []string{"example/inbox", "foreign/project", ""} {
			wakes := 0
			g := newGitHubEventIngress([]byte(githubFixtureSecret), func(r string) bool { return r == "example/inbox" }, func() { wakes++ })
			payload := fmt.Sprintf(`{"repository":{"full_name":%q}}`, repo)
			g.ServeHTTP(httptest.NewRecorder(), githubSignedRequest(t, kind, "fixture-delivery", payload))
			want := 0
			if index < 9 && repo == "example/inbox" {
				want = 1
			}
			if wakes != want {
				t.Fatalf("event=%s repo=%s wakes=%d want=%d", kind, repo, wakes, want)
			}
		}
	}
	c := newCoord(&Config{Inbox: "example/inbox", Targets: []Target{{Repo: "example/target"}}})
	c.st.Jobs[7] = &Job{Repo: "example/retained", Home: &dispatchIssue{Repo: "example/source", Number: 7}}
	for _, repo := range []string{"Example/Inbox", "example/target", "example/retained", "example/source"} {
		if !c.githubEventRepository(repo) {
			t.Fatalf("lost supervision of %s", repo)
		}
	}
	for _, repo := range []string{"", "example/foreign", "../project"} {
		if c.githubEventRepository(repo) {
			t.Fatal("foreign repository admitted")
		}
	}
	setCockpitTargets(c.cfg, Target{Repo: "example/new-target"})
	if !c.githubEventRepository("example/new-target") || c.githubEventRepository("example/target") || !c.githubEventRepository("example/retained") {
		t.Fatal("repository scope did not follow effective targets while retaining live jobs")
	}
}

func TestGitHubEventDedupIsBoundedAndExpires(t *testing.T) {
	now := time.Now()
	g := newGitHubEventIngress([]byte(githubFixtureSecret), nil, nil)
	g.now = func() time.Time { return now }
	if g.duplicate("one", []byte("first")) || !g.duplicate("one", []byte("changed")) || !g.duplicate("replay", []byte("first")) {
		t.Fatal("delivery identity or signed-body replay was not deduplicated")
	}
	now = now.Add(githubDeliveryTTL)
	if g.duplicate("one", []byte("first")) {
		t.Fatal("expired cache entry prevented repair")
	}
	for n := 0; n < 2*githubDeliveryLimit; n++ {
		if g.duplicate(fmt.Sprint(n), []byte(fmt.Sprint(n))) {
			t.Fatal("unique delivery suppressed")
		}
	}
	if len(g.seen) > 2*githubDeliveryLimit || g.duplicate("one", []byte("first")) {
		t.Fatal("cache grew beyond its bound or failed to evict")
	}
}

func TestGitHubEventConcurrentRedeliveryWakesOnce(t *testing.T) {
	var wakes atomic.Int32
	g := newGitHubEventIngress([]byte(githubFixtureSecret), func(string) bool { return true }, func() { wakes.Add(1) })
	var done sync.WaitGroup
	for n := 0; n < 50; n++ {
		r := githubSignedRequest(t, "issue_comment", fmt.Sprint(n), githubFixturePayload)
		done.Go(func() { g.ServeHTTP(httptest.NewRecorder(), r) })
	}
	done.Wait()
	if wakes.Load() != 1 {
		t.Fatal("concurrent replay admitted more than one invalidation")
	}
}

func TestGitHubEventInvalidRequestsDoNotPoisonDedup(t *testing.T) {
	wakes := 0
	g := newGitHubEventIngress([]byte(githubFixtureSecret), func(string) bool { return true }, func() { wakes++ })
	r := githubSignedRequest(t, "issue_comment", "fixture-delivery", githubFixturePayload)
	r.Header.Del("X-Hub-Signature-256")
	g.ServeHTTP(httptest.NewRecorder(), r)
	g.ServeHTTP(httptest.NewRecorder(), githubSignedRequest(t, "issue_comment", "fixture-delivery", githubFixturePayload))
	if wakes != 1 {
		t.Fatal("an unauthenticated request poisoned delivery deduplication")
	}
}
