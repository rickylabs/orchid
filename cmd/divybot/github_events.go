package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	githubEventBodyLimit = 2 * 1024 * 1024
	githubDeliveryLimit  = 2048
	githubDeliveryTTL    = 24 * time.Hour
)

// Payloads are invalidations, never issue state, grants, PR verdicts or launch requests.
type githubEventIngress struct {
	secret []byte
	allows func(string, githubSupervisionNotice) bool
	wake   func()
	now    func() time.Time
	mu     sync.Mutex
	seen   map[[sha256.Size]byte]*githubDelivery
	ring   [githubDeliveryLimit]*githubDelivery
	next   int
}

type githubDelivery struct {
	id, body [sha256.Size]byte
	at       time.Time
}

func newGitHubEventIngress(secret []byte, allows func(string, githubSupervisionNotice) bool, wake func()) *githubEventIngress {
	return &githubEventIngress{secret: secret, allows: allows, wake: wake, now: time.Now, seen: map[[sha256.Size]byte]*githubDelivery{}}
}

// Replays with a changed delivery header are suppressed by their signed body as well.
// This bounded cache only saves reads; durable source bindings still own single-effect admission.
func (g *githubEventIngress) duplicate(id string, body []byte) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	d := &githubDelivery{id: sha256.Sum256([]byte(id)), body: sha256.Sum256(body), at: now}
	for _, key := range [][sha256.Size]byte{d.id, d.body} {
		if previous := g.seen[key]; previous != nil && now.Sub(previous.at) < githubDeliveryTTL {
			return true
		}
	}
	if old := g.ring[g.next]; old != nil {
		for _, key := range [][sha256.Size]byte{old.id, old.body} {
			if g.seen[key] == old {
				delete(g.seen, key)
			}
		}
	}
	g.ring[g.next] = d
	g.next = (g.next + 1) % githubDeliveryLimit
	g.seen[d.id], g.seen[d.body] = d, d
	return false
}

func githubSupervisionEvent(kind string) bool {
	switch kind {
	case "issue_comment", "issues", "pull_request", "pull_request_review",
		"pull_request_review_comment", "pull_request_review_thread", "check_run", "check_suite", "status":
		return true
	default:
		return false
	}
}

func (g *githubEventIngress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.Header.Get("Content-Encoding") != "" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, githubEventBodyLimit))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	signature := r.Header.Get("X-Hub-Signature-256")
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	mac := hmac.New(sha256.New, g.secret)
	_, _ = mac.Write(body)
	if len(g.secret) == 0 || !strings.HasPrefix(signature, "sha256=") || err != nil || !hmac.Equal(provided, mac.Sum(nil)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var event githubSupervisionNotice
	if json.Unmarshal(body, &event) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	kind, delivery := r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
	if delivery == "" || len(delivery) > 128 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if kind == "ping" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if !githubSupervisionEvent(kind) || !g.allows(kind, event) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if !g.duplicate(delivery, body) {
		g.wake()
	}
	w.WriteHeader(http.StatusAccepted)
}

// Effective targets are an atomic snapshot. Active jobs retain supervision if a target is paused.
func (c *Coord) githubEventRepository(repo string) bool {
	if !repositoryName.MatchString(repo) {
		return false
	}
	if strings.EqualFold(repo, c.cfg.Inbox) {
		return true
	}
	for _, target := range c.cfg.effectiveTargets() {
		if strings.EqualFold(repo, target.Repo) {
			return true
		}
	}
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	for _, job := range c.st.Jobs {
		if strings.EqualFold(repo, job.Repo) || (job.Home != nil && strings.EqualFold(repo, job.Home.Repo)) {
			return true
		}
	}
	return false
}
