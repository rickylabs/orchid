package main

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMatrixQuotaRefusalAttribution(t *testing.T) {
	cases := []struct {
		name             string
		setup            func(now time.Time, budget map[string]int, govQ map[string]quota)
		wantCondition    string
		negativeControl  string
	}{
		{
			name: "no budget",
			setup: func(now time.Time, budget map[string]int, govQ map[string]quota) {
				budget["claude"] = 0
			},
			wantCondition: "claude: blocked by capacity",
		},
		{
			name: "meter never sampled",
			setup: func(now time.Time, budget map[string]int, govQ map[string]quota) {
				delete(govQ, "claude")
			},
			wantCondition:   "claude: absent",
			negativeControl: "stale",
		},
		{
			name: "meter stale",
			setup: func(now time.Time, budget map[string]int, govQ map[string]quota) {
				q := govQ["claude"]
				q.at = now.Add(-time.Hour) // > 3 * 90s sample interval
				govQ["claude"] = q
			},
			wantCondition:   "claude: stale",
			negativeControl: "absent",
		},
		{
			name: "weekly ceiling exceeded",
			setup: func(now time.Time, budget map[string]int, govQ map[string]quota) {
				q := govQ["claude"]
				q.seven.UsedPct = 95.0 // ceiling is 92.0
				govQ["claude"] = q
			},
			wantCondition: "claude: over ceiling",
		},
		{
			name: "expired window",
			setup: func(now time.Time, budget map[string]int, govQ map[string]quota) {
				q := govQ["claude"]
				q.five.ResetsAt = now.Add(-time.Second).Unix()
				govQ["claude"] = q
			},
			wantCondition: "claude: expired",
		},
		{
			name: "mixed published windows five healthy seven exhausted",
			setup: func(now time.Time, budget map[string]int, govQ map[string]quota) {
				q := govQ["claude"]
				q.five = RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}
				q.seven = RateLimit{UsedPct: 95, ResetsAt: now.Add(time.Hour).Unix()}
				govQ["claude"] = q
			},
			wantCondition: "claude: over ceiling",
		},
		{
			name: "mixed published windows five exhausted seven healthy",
			setup: func(now time.Time, budget map[string]int, govQ map[string]quota) {
				q := govQ["claude"]
				q.five = RateLimit{UsedPct: 95, ResetsAt: now.Add(time.Hour).Unix()}
				q.seven = RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()}
				govQ["claude"] = q
			},
			wantCondition: "claude: over ceiling",
		},
		{
			name: "empty meter unpublished windows",
			setup: func(now time.Time, budget map[string]int, govQ map[string]quota) {
				q := govQ["claude"]
				q.five = RateLimit{}
				q.seven = RateLimit{}
				govQ["claude"] = q
			},
			wantCondition: "claude: absent",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTestRoot(t)
			cfg := &Config{
				Inbox: "example/inbox",
				Matrix: MatrixConfig{
					Source:          root,
					ReceiptRoot:     root,
					Revision:        strings.Repeat("b", 40),
					TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)},
				},
				Governor: Gov{WeeklyCeiling: 92},
			}
			c := &Coord{cfg: cfg}
			now := time.Now()
			q := quota{
				ok:    true,
				at:    now,
				five:  RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()},
				seven: RateLimit{UsedPct: 20, ResetsAt: now.Add(2 * time.Hour).Unix()},
			}
			c.gov.q = map[string]quota{"claude": q}
			budget := map[string]int{"claude": 1}

			tc.setup(now, budget, c.gov.q)

			is := Issue{
				ID:     "synthetic-node",
				Number: 1,
				Title:  "Synthetic",
				Body:   "/swarm\ntier: feature\nrole: implementation\n\nSynthetic task",
			}
			var refusals []matrixRefusal
			deps := matrixAttemptDeps{
				report: func(r matrixRefusal) { refusals = append(refusals, r) },
				read: func(context.Context, string, string, string) (string, error) {
					return "| `routing` | matrix `implementation` row |", nil
				},
				resolve: func(_ context.Context, _ MatrixConfig, req matrixRequest) (matrixRoute, error) {
					if !containsString(req.Available, "claude") {
						return matrixRoute{}, errMatrix
					}
					return syntheticRoute(), nil
				},
				host: func(Target, string) (Host, bool) { return Host{}, true },
				persist: func(root, key, command string, r matrixReceipt, binding any, owners ...*receiptOwner) (*durableMatrixReceipt, error) {
					return persistMatrixReceipt(root, key, command, r, binding, owners...)
				},
				launch: func(context.Context, int, Issue, Host, string, Overrides, *durableMatrixReceipt) error {
					return nil
				},
			}

			_, ok := c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, budget, deps)
			if ok {
				t.Fatal("dispatch was admitted despite quota failure mode")
			}
			if len(refusals) != 1 {
				t.Fatalf("expected 1 refusal, got %d", len(refusals))
			}
			r := refusals[0]
			if r.ReasonCode != "quota-unavailable" {
				t.Fatalf("expected ReasonCode quota-unavailable, got %q", r.ReasonCode)
			}
			if !strings.Contains(r.Detail, tc.wantCondition) {
				t.Fatalf("expected Detail to contain %q, got %q", tc.wantCondition, r.Detail)
			}
			if tc.negativeControl != "" && strings.Contains(r.Detail, tc.negativeControl) {
				t.Fatalf("Detail should not contain negative control %q, got %q", tc.negativeControl, r.Detail)
			}
		})
	}
}

func TestMatrixQuotaMultiTransportAttribution(t *testing.T) {
	root := privateTestRoot(t)
	cfg := &Config{
		Inbox: "example/inbox",
		Matrix: MatrixConfig{
			Source:          root,
			ReceiptRoot:     root,
			Revision:        strings.Repeat("b", 40),
			TargetRevisions: map[string]string{"example/project": strings.Repeat("c", 40)},
		},
		Governor: Gov{WeeklyCeiling: 92},
	}
	c := &Coord{cfg: cfg}
	now := time.Now()

	// claude: over ceiling (95% >= 92)
	// codex: absent (never sampled)
	// agy: blocked by capacity (budget = 0)
	c.gov.q = map[string]quota{
		"claude": {
			ok:    true,
			at:    now,
			five:  RateLimit{UsedPct: 10, ResetsAt: now.Add(time.Hour).Unix()},
			seven: RateLimit{UsedPct: 95, ResetsAt: now.Add(2 * time.Hour).Unix()},
		},
		// codex not in map (never metered)
	}
	budget := map[string]int{
		"claude": 1,
		"codex":  1,
		"agy":    0,
	}

	is := Issue{
		ID:     "synthetic-node",
		Number: 1,
		Title:  "Synthetic",
		Body:   "/swarm\ntier: feature\nrole: implementation\n\nSynthetic task",
	}
	var refusals []matrixRefusal
	deps := matrixAttemptDeps{
		report: func(r matrixRefusal) { refusals = append(refusals, r) },
		read: func(context.Context, string, string, string) (string, error) {
			return "| `routing` | matrix `implementation` row |", nil
		},
		resolve: func(_ context.Context, _ MatrixConfig, req matrixRequest) (matrixRoute, error) {
			return matrixRoute{}, errMatrix
		},
		host: func(Target, string) (Host, bool) { return Host{}, true },
		persist: func(root, key, command string, r matrixReceipt, binding any, owners ...*receiptOwner) (*durableMatrixReceipt, error) {
			return persistMatrixReceipt(root, key, command, r, binding, owners...)
		},
		launch: func(context.Context, int, Issue, Host, string, Overrides, *durableMatrixReceipt) error {
			return nil
		},
	}

	_, ok := c.matrixAttempt(context.Background(), 1, is, Target{Repo: "example/project"}, budget, deps)
	if ok {
		t.Fatal("dispatch was admitted despite all transports failing quota gate")
	}
	if len(refusals) != 1 {
		t.Fatalf("expected 1 refusal, got %d", len(refusals))
	}
	r := refusals[0]
	if r.ReasonCode != "quota-unavailable" {
		t.Fatalf("expected ReasonCode quota-unavailable, got %q", r.ReasonCode)
	}
	expected := "claude: over ceiling, codex: absent, agy: blocked by capacity"
	if r.Detail != expected {
		t.Fatalf("expected Detail %q, got %q", expected, r.Detail)
	}
}

func TestQuotaDetailValidation(t *testing.T) {
	valid := []string{
		"claude: blocked by capacity",
		"claude: absent",
		"claude: stale",
		"claude: expired",
		"claude: over ceiling",
		"claude: over ceiling, codex: absent, agy: blocked by capacity",
		"codex: stale, agy: absent",
	}
	for _, v := range valid {
		if !validQuotaDetail(v) {
			t.Errorf("expected validQuotaDetail(%q) to be true", v)
		}
		r := matrixRefusal{"refused", "quota-unavailable", "", v}
		if !validMatrixRefusal(r) {
			t.Errorf("expected validMatrixRefusal(%+v) to be true", r)
		}
	}

	if validQuotaDetail("") {
		t.Error("validQuotaDetail(\"\") should be false")
	}

	invalid := []string{
		"synthetic-private-canary",
		"claude: 95%",
		"claude: 1800000000",
		"claude: $500",
		"claude: over-ceiling",
		"unknown: absent",
		"claude: invented",
		"claude: absent, claude: stale",
		"claude: absent, codex: 92.5",
		"claude: absent, /opt/operator/spend: leaked",
	}
	for _, inv := range invalid {
		if validQuotaDetail(inv) {
			t.Errorf("expected validQuotaDetail(%q) to be false", inv)
		}
		r := matrixRefusal{"refused", "quota-unavailable", "", inv}
		if validMatrixRefusal(r) {
			t.Errorf("expected validMatrixRefusal(%+v) with invalid detail to be false", r)
		}
	}

	// Non-quota refusal with Detail must be rejected
	if validMatrixRefusal(matrixRefusal{"refused", "source-missing", "", "claude: absent"}) {
		t.Error("non-quota refusal with detail must be rejected")
	}
}

func TestQuotaRefusalCommentAndLog(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)

	statePath := filepath.Join(privateTestRoot(t), "state.json")
	c := &Coord{cfg: &Config{Inbox: "example/inbox"}, st: loadState(statePath)}
	is := Issue{ID: "synthetic-issue", Number: 42, Title: "Synthetic task", Body: "/swarm\ntier: feature"}

	detail := "claude: over ceiling, codex: absent, agy: blocked by capacity"
	r := refusalForQuota(detail)

	var commentBody string
	post := func(_ context.Context, _ string, _ int, body string) error {
		commentBody = body
		return nil
	}

	c.reportIssueMatrixRefusal(context.Background(), 42, is, r, post)

	if !strings.Contains(commentBody, "Detail: `"+detail+"`") {
		t.Fatalf("comment body missing detail, got: %s", commentBody)
	}
	if !strings.Contains(commentBody, "Reason: `quota-unavailable`") {
		t.Fatalf("comment body missing reason code, got: %s", commentBody)
	}
	if !strings.Contains(logs.String(), "detail="+detail) {
		t.Fatalf("log missing detail, got: %s", logs.String())
	}

	// Negative control: Canary in detail must be rejected by validMatrixRefusal and not reach log or comment
	logs.Reset()
	commentBody = ""
	canaryDetail := "synthetic-private-canary"
	badRefusal := matrixRefusal{"refused", "quota-unavailable", "", canaryDetail}
	c.reportIssueMatrixRefusal(context.Background(), 43, is, badRefusal, post)
	if strings.Contains(commentBody, canaryDetail) || strings.Contains(logs.String(), canaryDetail) {
		t.Fatal("untrusted canary in Detail reached comment or log")
	}
}

func TestQuotaRefusalSpendAdjacentScan(t *testing.T) {
	// Assert that valid conditions contain no spend-adjacent numbers, percentages, or timestamps.
	for cond := range validQuotaConditions {
		for _, c := range cond {
			if c >= '0' && c <= '9' {
				t.Fatalf("condition %q contains digit %c", cond, c)
			}
			if c == '%' || c == '$' {
				t.Fatalf("condition %q contains spend symbol %c", cond, c)
			}
		}
	}
}

