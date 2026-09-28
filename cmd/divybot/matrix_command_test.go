package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &logs
}

// A subprocess killed by the resolution deadline used to be reported as command.exit,
// indistinguishable from one that failed on its own.
func TestMatrixCommandNamesDeadlineKill(t *testing.T) {
	captureLog(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := matrixCommand(ctx, t.TempDir(), "sh", nil, "-c", "sleep 20")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("deadline kill hung for %s", elapsed)
	}
	if got := matrixCause(err); got != "command.timeout" {
		t.Fatalf("deadline kill reported as %q", got)
	}
	if r := refusalFor(matrixSite("resolve.command", err)); r.Cause != "command.timeout" || !validMatrixRefusal(r) {
		t.Fatal("timeout cause did not survive to the public refusal")
	}
}

func TestMatrixResolveDeadlineAllowsSlowSourceButRemainsBounded(t *testing.T) {
	// The #397 source status command exhausted 90 s under disk pressure. Preserve
	// enough room for that check and the bridge, without allowing an unbounded poll.
	if matrixResolveTimeout < 150*time.Second || matrixResolveTimeout > 5*time.Minute {
		t.Fatalf("matrix resolution budget %s cannot cover slow source inspection safely", matrixResolveTimeout)
	}
}

func TestMatrixCommandClassifiesFailures(t *testing.T) {
	captureLog(t)
	cases := map[string][]string{
		"command.exit":   {"sh", "-c", "exit 3"},
		"command.signal": {"sh", "-c", "kill -9 $$"},
		"command.start":  {filepath.Join(t.TempDir(), "absent-binary")},
	}
	for want, argv := range cases {
		_, err := matrixCommand(context.Background(), t.TempDir(), argv[0], nil, argv[1:]...)
		if got := matrixCause(err); got != want {
			t.Fatalf("%v classified as %q, want %q", argv, got, want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := matrixCommand(ctx, t.TempDir(), "sh", nil, "-c", "sleep 20"); matrixCause(err) != "command.canceled" {
		t.Fatal("parent cancellation misreported")
	}
	// Positive control: success still returns stdout and logs nothing.
	logs := captureLog(t)
	if out, err := matrixCommand(context.Background(), t.TempDir(), "sh", nil, "-c", "echo ok; echo ignored >&2"); err != nil || strings.TrimSpace(string(out)) != "ok" || logs.Len() != 0 {
		t.Fatal("successful command changed behaviour")
	}
}

func TestMatrixCommandLogsScrubbedStderrTail(t *testing.T) {
	logs := captureLog(t)
	script := `echo "early noise that must not appear" >&2
echo "error: Module not found /private/tree/runtime.ts token=abc ghp_0123456789abcdefXYZ https://host.example/x user@host" >&2
exit 4`
	_, err := matrixCommand(context.Background(), t.TempDir(), "sh", nil, "-c", script)
	if matrixCause(err) != "command.exit" {
		t.Fatal("exit misclassified")
	}
	got := logs.String()
	if !strings.Contains(got, "matrix subprocess sh failed: cause=command.exit exit=4") || !strings.Contains(got, "error: Module not found") {
		t.Fatalf("diagnostic lost: %q", got)
	}
	for _, secret := range []string{"/private", "tree", "token=abc", "ghp_0123456789", "host.example", "user@host", "early noise"} {
		if strings.Contains(got, secret) {
			t.Fatalf("stderr scrub leaked %q", secret)
		}
	}
	logs.Reset()
	matrixCommand(context.Background(), t.TempDir(), "sh", nil, "-c", "kill -9 $$")
	if !strings.Contains(logs.String(), "signal=killed") {
		t.Fatalf("signal not named: %q", logs.String())
	}
}

func TestStderrTailIsBounded(t *testing.T) {
	var tail stderrTail
	for i := 0; i < 1000; i++ {
		tail.Write(bytes.Repeat([]byte("x"), 100))
	}
	tail.Write([]byte("\nlast line\n"))
	if len(tail.Bytes()) > stderrTailLimit || scrubStderr(tail.Bytes()) != "last line" {
		t.Fatal("stderr tail unbounded or lost its last line")
	}
	if len(scrubStderr(bytes.Repeat([]byte("word "), 200))) > 160 {
		t.Fatal("scrubbed line unbounded")
	}
}

// The bridge's nested-CLI deadline must fire before divybot's own bound, or a slow nested CLI
// kills the whole bridge from outside and the refusal loses its JSON reason.
func TestResolveBudgetCoversBridgeDeadline(t *testing.T) {
	m := regexp.MustCompile(`child\.kill\(\); \} catch \{ /\* already exited \*/ \} \}, ([0-9_]+)\);`).FindStringSubmatch(matrixBridge)
	if m == nil {
		t.Fatal("bridge nested-CLI deadline not found")
	}
	ms, err := strconv.Atoi(strings.ReplaceAll(m[1], "_", ""))
	if err != nil {
		t.Fatal("bridge deadline unreadable")
	}
	bridge := time.Duration(ms) * time.Millisecond
	if matrixResolveTimeout < 60*time.Second || bridge+15*time.Second > matrixResolveTimeout {
		t.Fatalf("resolve bound %s too tight for bridge deadline %s", matrixResolveTimeout, bridge)
	}
}

func TestRefusalNoticeIsSupersededByLaterLaunch(t *testing.T) {
	captureLog(t)
	statePath := filepath.Join(privateTestRoot(t), "state.json")
	c := &Coord{cfg: &Config{Inbox: "example/inbox"}, st: loadState(statePath)}
	is := Issue{ID: "synthetic", Number: 5, Title: "Synthetic", Body: "Synthetic"}
	var bodies []string
	post := func(_ context.Context, _ string, _ int, body string) error { bodies = append(bodies, body); return nil }

	c.reportMatrixLaunchAfterRefusal(context.Background(), 5, post)
	if len(bodies) != 0 {
		t.Fatal("follow-up posted without a prior refusal")
	}
	c.reportIssueMatrixRefusal(context.Background(), 5, is, refusalFor(matrixSite("resolve.command", matrixSite("command.timeout", errMatrix))), post)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "No agent was launched") || !strings.Contains(bodies[0], matrixRetryNote) || !strings.Contains(bodies[0], "Cause: `command.timeout`") {
		t.Fatalf("refusal notice does not say it will be retried: %q", bodies)
	}
	c.reportMatrixLaunchAfterRefusal(context.Background(), 5, post)
	if len(bodies) != 2 || !strings.Contains(bodies[1], "superseded") {
		t.Fatal("later launch did not supersede the refusal notice")
	}
	c.st = loadState(statePath)
	c.reportMatrixLaunchAfterRefusal(context.Background(), 5, post)
	if len(bodies) != 2 {
		t.Fatal("superseded notice repeated after restart")
	}
	// Fenced outcomes are not retried, so they must not promise a retry.
	bodies = nil
	c.reportIssueMatrixRefusal(context.Background(), 6, is, refusalWithReason(errMatrix, "launch-failed"), post)
	if len(bodies) != 1 || strings.Contains(bodies[0], matrixRetryNote) {
		t.Fatal("fenced launch outcome promised a retry")
	}
}

func TestLaunchPathSupersedesRefusalNotice(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal("dispatcher source unavailable")
	}
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatal("dispatcher AST unavailable")
	}
	wired := false
	ast.Inspect(f, func(n ast.Node) bool {
		s, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if id, ok := s.Cond.(*ast.Ident); !ok || id.Name != "launched" {
			return true
		}
		ast.Inspect(s.Body, func(m ast.Node) bool {
			if call, ok := m.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "reportMatrixLaunchAfterRefusal" {
					wired = true
				}
			}
			return true
		})
		return true
	})
	if !wired {
		t.Fatal("successful matrix launch does not supersede the refusal notice")
	}
}
