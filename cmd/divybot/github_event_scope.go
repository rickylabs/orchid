package main

import (
	"encoding/json"
	"strings"
)

// Native payload hints restrict invalidations; only the existing readers authorize effects.
type githubSupervisionNotice struct {
	Action     string                `json:"action"`
	Repository githubEventRepository `json:"repository"`
	Issue      struct {
		Number      int                      `json:"number"`
		PullRequest json.RawMessage          `json:"pull_request"`
		User        struct{ Login string }   `json:"user"`
		Labels      []struct{ Name string }  `json:"labels"`
		Assignees   []struct{ Login string } `json:"assignees"`
	} `json:"issue"`
	Comment struct {
		ID   int64              `json:"id"`
		Body string             `json:"body"`
		User struct{ ID int64 } `json:"user"`
	} `json:"comment"`
	PullRequest githubEventPR `json:"pull_request"`
	CheckRun    struct {
		CheckSuite struct {
			HeadBranch string `json:"head_branch"`
		} `json:"check_suite"`
		PullRequests []githubEventPR `json:"pull_requests"`
	} `json:"check_run"`
	CheckSuite struct {
		HeadBranch   string          `json:"head_branch"`
		PullRequests []githubEventPR `json:"pull_requests"`
	} `json:"check_suite"`
	Branches []struct{ Name string } `json:"branches"`
}

type githubEventRepository struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type githubEventPR struct {
	User struct{ Login string } `json:"user"`
	Head struct {
		Ref  string                `json:"ref"`
		Repo githubEventRepository `json:"repo"`
	} `json:"head"`
}

func (c *Coord) githubEventRelevant(kind string, event githubSupervisionNotice) bool {
	repo := event.Repository.FullName
	if !c.githubEventRepository(repo) {
		return false
	}
	switch kind {
	case "issues", "issue_comment":
		if event.Issue.Number <= 0 {
			return false
		}
		if c.githubOwnedIssue(repo, event.Issue.Number) {
			return true
		}
		isPR := len(event.Issue.PullRequest) > 0 && string(event.Issue.PullRequest) != "null"
		if kind == "issue_comment" && isPR && c.cfg.BotLogin != "" && strings.EqualFold(event.Issue.User.Login, c.cfg.BotLogin) {
			return true // issue.user identifies the PR author; comment.user identifies its commenter
		}
		if kind == "issue_comment" {
			_, trigger, _ := sourceTriggerBody(event.Comment.Body)
			return event.Action == "created" && event.Comment.ID > 0 && trigger &&
				(event.Comment.User.ID == ownerGitHubID || c.sourceGrantNamed(repo, event.Comment.ID, event.Comment.User.ID))
		}
		if isPR {
			return false
		}
		if strings.EqualFold(repo, c.cfg.Inbox) {
			for _, label := range event.Issue.Labels {
				if label.Name == "harness" {
					return true
				}
			}
		}
		for _, target := range c.cfg.effectiveTargets() {
			if !target.MirrorAssignments || !strings.EqualFold(target.Repo, repo) || c.cfg.BotLogin == "" {
				continue
			}
			for _, assignee := range event.Issue.Assignees {
				if strings.EqualFold(assignee.Login, c.cfg.BotLogin) {
					return true
				}
			}
		}
	case "pull_request", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread":
		return c.githubOwnedPR(event.Repository, event.PullRequest)
	case "check_run":
		if c.githubOwnedBranch(repo, event.CheckRun.CheckSuite.HeadBranch) {
			return true
		}
		for _, pr := range event.CheckRun.PullRequests {
			if c.githubOwnedPR(event.Repository, pr) {
				return true
			}
		}
	case "check_suite":
		if c.githubOwnedBranch(repo, event.CheckSuite.HeadBranch) {
			return true
		}
		for _, pr := range event.CheckSuite.PullRequests {
			if c.githubOwnedPR(event.Repository, pr) {
				return true
			}
		}
	case "status":
		for _, branch := range event.Branches {
			if c.githubOwnedBranch(repo, branch.Name) {
				return true
			}
		}
	}
	return false
}

func (c *Coord) githubOwnedIssue(repo string, number int) bool {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	for _, job := range c.st.Jobs {
		home := jobHome(c.cfg.Inbox, job)
		if home.Number == number && strings.EqualFold(home.Repo, repo) {
			return true
		}
	}
	for _, binding := range c.st.SourceBindings {
		if binding.Issue == number && strings.EqualFold(binding.Repo, repo) &&
			(binding.Closed == "" || binding.Closed == "awaiting-grant") {
			return true
		}
	}
	return false
}

// Read immutable launch scope, never the mutable PR pointer or relay tracker from HTTP goroutines.
func (c *Coord) githubOwnedBranch(repo, branch string) bool {
	if branch == "" {
		return false
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	for _, job := range c.st.Jobs {
		if strings.EqualFold(job.Repo, repo) && job.Branch == branch {
			return true
		}
	}
	return false
}

func (c *Coord) githubOwnedPR(repo githubEventRepository, pr githubEventPR) bool {
	if c.cfg.BotLogin != "" && strings.EqualFold(pr.User.Login, c.cfg.BotLogin) {
		return true
	}
	sameRepo := strings.EqualFold(pr.Head.Repo.FullName, repo.FullName) ||
		(repo.ID != 0 && pr.Head.Repo.ID == repo.ID)
	return sameRepo && c.githubOwnedBranch(repo.FullName, pr.Head.Ref)
}
