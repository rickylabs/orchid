package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestGitHubEventOwnedSubjects(t *testing.T) {
	c := newCoord(&Config{Inbox: "example/inbox", BotLogin: "fixture-bot", Targets: []Target{
		{Repo: "example/project"}, {Repo: "example/mirror", MirrorAssignments: true},
	}})
	c.st.Jobs[7] = &Job{Issue: 7, Repo: "example/project", Branch: "owned-branch"}
	c.st.SourceBindings = map[int]*sourceBinding{}
	c.st.SourceBindings[701] = &sourceBinding{Repo: "example/project", Issue: 11, Comment: 701}
	c.st.SourceBindings[702] = &sourceBinding{Repo: "example/project", Issue: 12, Comment: 702, Closed: "done"}
	c.st.SourceBindings[703] = &sourceBinding{Repo: "example/project", Issue: 13, Comment: 703, Closed: "awaiting-grant"}
	c.sourceNamed = func(repo string, comment, author int64) bool {
		return repo == "example/project" && comment == 801 && author == 1234
	}
	for _, test := range []struct {
		name, kind, repo, detail string
		want                     bool
	}{
		{"tracked inbox", "issues", "example/inbox", `"issue":{"number":7}`, true},
		{"source closure", "issues", "example/project", `"action":"closed","issue":{"number":11}`, true},
		{"waiting source", "issue_comment", "example/project", `"issue":{"number":13}`, true},
		{"retired source", "issues", "example/project", `"issue":{"number":12}`, false},
		{"foreign issue", "issues", "example/project", `"issue":{"number":999}`, false},
		{"launch label", "issues", "example/inbox", `"issue":{"number":999,"labels":[{"name":"harness"}]}`, true},
		{"unlabelled issue", "issues", "example/inbox", `"issue":{"number":999}`, false},
		{"other repository label", "issues", "example/project", `"issue":{"number":999,"labels":[{"name":"harness"}]}`, false},
		{"PR label not launch", "issues", "example/inbox", `"issue":{"number":999,"pull_request":{},"labels":[{"name":"harness"}]}`, false},
		{"mirrored assignment", "issues", "example/mirror", `"issue":{"number":999,"assignees":[{"login":"fixture-bot"}]}`, true},
		{"foreign assignee", "issues", "example/mirror", `"issue":{"number":999,"assignees":[{"login":"other"}]}`, false},
		{"mirror opt-in", "issues", "example/project", `"issue":{"number":999,"assignees":[{"login":"fixture-bot"}]}`, false},
		{"owned PR conversation", "issue_comment", "example/project", `"issue":{"number":42,"pull_request":{},"user":{"login":"fixture-bot"}},"comment":{"body":"READY"}`, true},
		{"foreign PR conversation", "issue_comment", "example/project", `"issue":{"number":42,"pull_request":{},"user":{"login":"other"}},"comment":{"user":{"login":"fixture-bot"},"body":"READY"}`, false},
		{"owner command", "issue_comment", "example/project", fmt.Sprintf(`"action":"created","issue":{"number":41},"comment":{"id":800,"body":"/swarm\nTask","user":{"id":%d}}`, ownerGitHubID), true},
		{"named member", "issue_comment", "example/project", `"action":"created","issue":{"number":41},"comment":{"id":801,"body":"/swarm\nTask","user":{"id":1234}}`, true},
		{"ungranted member", "issue_comment", "example/project", `"action":"created","issue":{"number":41},"comment":{"id":802,"body":"/swarm\nTask","user":{"id":1234}}`, false},
		{"ordinary owner comment", "issue_comment", "example/project", fmt.Sprintf(`"action":"created","issue":{"number":41},"comment":{"id":800,"body":"Hello","user":{"id":%d}}`, ownerGitHubID), false},
		{"edited command", "issue_comment", "example/project", fmt.Sprintf(`"action":"edited","issue":{"number":41},"comment":{"id":800,"body":"/swarm\nTask","user":{"id":%d}}`, ownerGitHubID), false},
		{"bot PR", "pull_request", "example/project", `"pull_request":{"user":{"login":"FIXTURE-BOT"}}`, true},
		{"foreign PR", "pull_request", "example/project", `"pull_request":{"user":{"login":"other"}}`, false},
		{"owned review branch", "pull_request_review", "example/project", `"pull_request":{"head":{"ref":"owned-branch","repo":{"id":1}}}`, true},
		{"foreign fork same branch", "pull_request_review", "example/project", `"pull_request":{"head":{"ref":"owned-branch","repo":{"id":2,"full_name":"foreign/project"}}}`, false},
		{"owned review comment", "pull_request_review_comment", "example/project", `"pull_request":{"user":{"login":"fixture-bot"}}`, true},
		{"owned review thread", "pull_request_review_thread", "example/project", `"pull_request":{"user":{"login":"fixture-bot"}}`, true},
		{"linked owned check", "check_run", "example/project", `"check_run":{"pull_requests":[{"head":{"ref":"owned-branch","repo":{"id":1}}}]}`, true},
		{"unrelated linked check", "check_run", "example/project", `"check_run":{"pull_requests":[{"head":{"ref":"other","repo":{"id":1}}}]}`, false},
		{"empty links owned", "check_run", "example/project", `"check_run":{"check_suite":{"head_branch":"owned-branch"},"pull_requests":[]}`, true},
		{"empty links unknown", "check_run", "example/project", `"check_run":{"pull_requests":[]}`, false},
		{"owned suite", "check_suite", "example/project", `"check_suite":{"head_branch":"owned-branch"}`, true},
		{"unrelated suite", "check_suite", "example/project", `"check_suite":{"head_branch":"foreign-branch"}`, false},
		{"owned status", "status", "example/project", `"branches":[{"name":"owned-branch"}]`, true},
		{"unrelated status", "status", "example/project", `"branches":[{"name":"foreign-branch"}]`, false},
		{"unknown status", "status", "example/project", `"branches":[]`, false},
		{"foreign repository bot", "pull_request", "foreign/project", `"pull_request":{"user":{"login":"fixture-bot"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var event githubSupervisionNotice
			raw := fmt.Sprintf(`{"repository":{"id":1,"full_name":%q},%s}`, test.repo, test.detail)
			if err := json.Unmarshal([]byte(raw), &event); err != nil {
				t.Fatal(err)
			}
			if got := c.githubEventRelevant(test.kind, event); got != test.want {
				t.Fatalf("relevant=%v want=%v", got, test.want)
			}
		})
	}
}

func TestGitHubEventScopeAvoidsMutablePRState(t *testing.T) {
	c := newCoord(&Config{Inbox: "example/inbox"})
	job := &Job{Issue: 7, Repo: "example/inbox", Branch: "owned-branch"}
	c.st.Jobs[7] = job
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n := range 500 {
			job.PR = n
			job.Track.HeadOID = fmt.Sprint(n)
		}
	}()
	for range 500 {
		if !c.githubOwnedBranch("example/inbox", "owned-branch") || !c.githubOwnedIssue("example/inbox", 7) {
			t.Fatal("immutable binding scope lost")
		}
	}
	<-done
}
