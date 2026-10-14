package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDelayedGrantCannotPublishAnEarlyRefusal(t *testing.T) {
	c, f, s, r, at := ownerPolicyGrant(t, 3100000990)
	f.post("example/project", 7, r.CommentID, ownerGitHubID, policyBody, at)
	c.sourceTick(context.Background())
	b := c.st.SourceBindings[int(r.CommentID)]
	if b == nil || b.Closed != "awaiting-grant" || len(b.Outbox) != 0 || refusedMarkers(f) != 0 {
		t.Fatal("grant publication race produced a final refusal")
	}
	restarted := &Coord{cfg: c.cfg, st: loadState(c.st.path), ownerGrants: s, source: f}
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatal(ack.Reason)
	}
	restarted.sourceTick(context.Background())
	restarted.sourceTick(context.Background())
	b = restarted.st.SourceBindings[int(r.CommentID)]
	if b == nil || b.Closed != "" || refusedMarkers(f) != 0 {
		t.Fatal("delayed exact grant did not reconcile before launch")
	}
}

func TestMissingGrantRefusesOnlyAtTheAdmissionWindowEnd(t *testing.T) {
	c, f, _, r, at := ownerPolicyGrant(t, 3100000991)
	f.post("example/project", 7, r.CommentID, ownerGitHubID, policyBody, at)
	c.sourceTick(context.Background())
	key := int(r.CommentID)
	if c.st.SourceBindings[key].Closed != "awaiting-grant" {
		t.Fatal("refused before grant window ended")
	}
	c.recheckSourceGrants(context.Background(), at.Add(sourceTriggerFreshness), sourceCheckRequestsPerTick)
	c.deliverSourceReplies(context.Background())
	b := c.st.SourceBindings[key]
	if b.Closed != "refused" || refusedMarkers(f) != 1 {
		t.Fatal("closed grant window did not produce one terminal refusal")
	}
	c.recheckSourceGrants(context.Background(), at.Add(sourceTriggerFreshness+time.Second), sourceCheckRequestsPerTick)
	c.deliverSourceReplies(context.Background())
	if refusedMarkers(f) != 1 {
		t.Fatal("expired grant refusal was repeated")
	}
}

func TestLateGrantSupersedesALegacyQueuedRefusal(t *testing.T) {
	c, f, s, r, at := ownerPolicyGrant(t, 3100000992)
	f.post("example/project", 7, r.CommentID, ownerGitHubID, policyBody, at)
	c.sourceTick(context.Background())
	key := int(r.CommentID)
	b := c.st.SourceBindings[key]
	b.Closed, b.Refusal = "refused", "source-grant-missing"
	b.Outbox = []sourceReply{sourceReplyFor(b, "refused", "source-grant-missing", "", "legacy refusal")}
	b.Replies = map[string]bool{}
	c.st.MatrixNotices = map[int]string{key: "refused source-grant-missing"}
	c.st.LaunchBlocks = map[int]launchBlock{key: {Reason: "source-grant-missing"}}
	c.st.save()
	if ack := s.install(context.Background(), r); ack.State != "LIVE" {
		t.Fatal(ack.Reason)
	}
	c.sourceTick(context.Background())
	if refusedMarkers(f) != 0 || len(c.st.SourceBindings[key].Outbox) != 0 {
		t.Fatal("queued legacy refusal survived the exact grant")
	}
	if _, present := c.st.MatrixNotices[key]; present {
		t.Fatal("stale pending approval notice survived reconciliation")
	}
	if _, present := c.st.LaunchBlocks[key]; present {
		t.Fatal("missing-grant launch block survived reconciliation")
	}
	b = c.st.SourceBindings[key]
	b.Replies["refused\x00source-grant-missing\x00"] = true
	c.st.Jobs[key] = &Job{Agent: "claude"}
	c.replySourceStarted(context.Background(), key)
	c.deliverSourceReplies(context.Background())
	if len(f.replies) != 1 || !strings.Contains(f.replies[0].body, "supersedes") || !strings.Contains(f.replies[0].body, "no approval request is pending") {
		t.Fatal("started result did not supersede the delivered legacy refusal")
	}
}
