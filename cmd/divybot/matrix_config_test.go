package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatrixGrantUsesCompleteGoBriefDigest(t *testing.T) {
	is := Issue{ID: "synthetic-node", Title: "<&>", Body: "before\n```\ncomplete after fence\n\u2028"}
	cfg := &Config{}
	if p := addMatrixGrant(cfg, is, "example/project", []byte(`{"tier":"feature","role":"implementation"}`)); len(p) > 0 {
		t.Fatal("grant builder failed")
	}
	want := shaText([]byte(`{"Title":"\u003c\u0026\u003e","Body":"before\n` + "```" + `\ncomplete after fence\n\u2028"}`))
	if cfg.Matrix.Grants[0].BriefDigest != want {
		t.Fatal("digest differs from exact Go title/body encoding")
	}
	before := cfg.Matrix.Grants[0]
	is.Body += "changed"
	if p := addMatrixGrant(cfg, is, "example/project", []byte(`{"tier":"feature","role":"implementation"}`)); len(p) > 0 {
		t.Fatal("new brief grant rejected")
	}
	if cfg.Matrix.Grants[0] != before || cfg.Matrix.Grants[1].BriefDigest == before.BriefDigest {
		t.Fatal("history changed or complete body ignored")
	}
	if p := addMatrixGrant(cfg, is, "example/project", []byte(`{"tier":"feature"}`)); len(p) == 0 {
		t.Fatal("duplicate current grant accepted")
	}
	if p := addMatrixGrant(cfg, is, "example/project", []byte(`{"issue_id":"synthetic-spoof"}`)); len(p) == 0 {
		t.Fatal("caller supplied generated binding")
	}
}

func TestMatrixConfigCLI(t *testing.T) {
	source := syntheticSource(t)
	private := privateTestRoot(t)
	cfgPath := filepath.Join(private, "base.json")
	issueFile := filepath.Join(private, "issue.json")
	is := Issue{ID: "synthetic-private-canary", Number: 1, Title: "Synthetic", Body: "/swarm\nprofile: leaf\ntier: feature\n\nComplete synthetic brief <&>"}
	issueJSON, _ := json.Marshal(is)
	writeFixture(t, issueFile, string(issueJSON))
	writeFixture(t, cfgPath, `{"inbox":"example/inbox","targets":[{"label":"synthetic-label","repo":"example/project"}],"extension":{"preserve":"synthetic-private-canary"}}`)
	bin := filepath.Join(private, "bin")
	if os.Mkdir(bin, 0700) != nil {
		t.Fatal("fixture setup")
	}
	script := "#!/bin/sh\ncase \"$*\" in\n 'issue view 1 --repo example/inbox --json id,number,title,body') cat " + shq(issueFile) + ";;\n 'api repos/example/project/commits/HEAD --jq .sha') printf '%s\\n' '" + strings.Repeat("a", 40) + "';;\n 'api repos/example/project/contents/profiles/leaf.md?ref=" + strings.Repeat("a", 40) + "') printf '%s' '{\"content\":\"fCBgcm91dGluZ2AgfCBtYXRyaXggYGltcGxlbWVudGF0aW9uYCByb3cgfA==\",\"encoding\":\"base64\",\"type\":\"file\"}';;\n *) exit 90;;\nesac\n"
	writeFixture(t, filepath.Join(bin, "gh"), script)
	os.Chmod(filepath.Join(bin, "gh"), 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	grant := filepath.Join(private, "grant.json")
	writeFixture(t, grant, `{"tier":"feature","role":"implementation"}`)
	candidate := filepath.Join(private, "candidate.json")
	args := []string{"build", "-config", cfgPath, "-source", source.Source, "-receipt-root", private, "-issue", "1", "-target", "example/project", "-grant-input", grant, "-out", candidate}
	var out, diagnostic bytes.Buffer
	if code := invokeMatrixConfigCLI(args, &out, &diagnostic); code != 0 {
		t.Fatalf("build exit %d: %s", code, diagnostic.String())
	}
	cfg, raw, p := readMatrixConfigFile(candidate)
	if len(p) > 0 || cfg.Matrix.Revision != source.Revision || cfg.Matrix.Grants[0].BriefDigest != briefDigest(is) || !strings.Contains(string(raw["extension"]), "synthetic-private-canary") {
		t.Fatal("candidate lost resolved pins, grant or unrelated config")
	}
	st, _ := os.Stat(candidate)
	if st.Mode().Perm() != 0600 {
		t.Fatal("candidate is not private")
	}
	if code := invokeMatrixConfigCLI([]string{"validate", "-config", candidate, "-issue", "1", "-target", "example/project"}, &out, &diagnostic); code != 0 {
		t.Fatalf("validation failed: %s", diagnostic.String())
	}
	if strings.Contains(out.String()+diagnostic.String(), "synthetic-private-canary") || strings.Contains(out.String()+diagnostic.String(), private) || !strings.Contains(out.String(), "UNCHECKED") {
		t.Fatal("unsafe or overstated operator output")
	}
	prior, _ := os.ReadFile(candidate)
	if code := invokeMatrixConfigCLI(args, &out, &diagnostic); code == 0 {
		t.Fatal("existing output overwritten")
	}
	after, _ := os.ReadFile(candidate)
	if !bytes.Equal(prior, after) {
		t.Fatal("existing output changed")
	}
	diagnostic.Reset()
	if code := invokeMatrixConfigCLI([]string{"validate", "-config", cfgPath}, &out, &diagnostic); code == 0 || !strings.Contains(diagnostic.String(), "matrix.source") || !strings.Contains(diagnostic.String(), "matrix.revision") || !strings.Contains(diagnostic.String(), "matrix.receipt_root") || !strings.Contains(diagnostic.String(), "matrix.target_revisions") {
		t.Fatal("missing block not explained before coordinator startup")
	}
}

func TestMatrixConfigRequiredFields(t *testing.T) {
	base := syntheticSource(t)
	base.ReceiptRoot = privateTestRoot(t)
	base.TargetRevisions = map[string]string{"example/project": strings.Repeat("c", 40)}
	for _, name := range []string{"valid", "source", "revision", "receipt_root", "target_revisions", "pins", "grant-digest", "grant-authority", "override-pin"} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(base)
			var m MatrixConfig
			json.Unmarshal(raw, &m)
			switch name {
			case "source":
				m.Source = "relative"
			case "revision":
				m.Revision = "invalid"
			case "receipt_root":
				m.ReceiptRoot = "relative"
			case "target_revisions":
				m.TargetRevisions = nil
			case "pins":
				m.Pins = map[string]MatrixPin{"synthetic": {}}
			case "grant-digest":
				m.Grants = []MatrixGrant{{IssueID: "synthetic", Repo: "example/project", BriefDigest: "invalid"}}
			case "grant-authority":
				m.Grants = []MatrixGrant{{IssueID: "synthetic", Repo: "example/project", BriefDigest: strings.Repeat("d", 64), Authorization: &MatrixAuthority{"untrusted", "Synthetic"}}}
			case "override-pin":
				m.Grants = []MatrixGrant{{IssueID: "synthetic", Repo: "example/project", BriefDigest: strings.Repeat("d", 64), Override: &MatrixOverride{Pin: "absent", Authorizer: "owner", Rationale: "Synthetic", WorklogPath: "worklog.md", Route: MatrixPin{"synthetic", "medium"}}}}
			}
			cfg := &Config{Inbox: "example/inbox", Targets: []Target{{Repo: "example/project"}}, Matrix: m}
			p := validateMatrixConfig(context.Background(), cfg)
			if (len(p) == 0) != (name == "valid") {
				t.Fatal("configuration guard verdict incorrect")
			}
		})
	}
}

func TestMatrixConfigRejectsAmbiguousJSON(t *testing.T) {
	for _, body := range []string{`{"matrix":{"source":"one","source":"two"}}`, `{"matrix":{"invented":"synthetic-private-canary"}}`, `{"matrix":null}`, `{"matrix":[]} {}`} {
		path := filepath.Join(privateTestRoot(t), "config.json")
		writeFixture(t, path, body)
		_, _, p := readMatrixConfigFile(path)
		if len(p) == 0 {
			t.Fatal("ambiguous config accepted")
		}
		b, _ := json.Marshal(p)
		if strings.Contains(string(b), "canary") {
			t.Fatal("invalid config leaked")
		}
	}
}

func TestMatrixPreflightRejectsStaleAuthority(t *testing.T) {
	m := syntheticSource(t)
	m.ReceiptRoot = privateTestRoot(t)
	m.TargetRevisions = map[string]string{"example/project": strings.Repeat("c", 40)}
	cfg := &Config{Inbox: "example/inbox", Targets: []Target{{Repo: "example/project"}}, Matrix: m}
	is := Issue{ID: "synthetic-node", Number: 1, Title: "Synthetic", Body: "/swarm\ntier: architecture\nrole: implementation\n\nSynthetic complete brief"}
	cfg.Matrix.Grants = []MatrixGrant{{IssueID: is.ID, Repo: "example/project", BriefDigest: briefDigest(is), Authorization: &MatrixAuthority{"owner", "Synthetic authority"}}}
	deps := configDeps()
	deps.read = func(context.Context, string, string, string) (string, error) {
		return "| `routing` | matrix `implementation` row |", nil
	}
	if p := validateMatrixIssue(context.Background(), cfg, is, "example/project", deps); len(p) > 0 {
		t.Fatal("matching authority refused")
	}
	is.Body += "changed"
	if p := validateMatrixIssue(context.Background(), cfg, is, "example/project", deps); len(p) != 1 || p[0].Reason != "authorization-required" {
		t.Fatal("stale grant authorized changed issue")
	}
	cfg.Matrix.Revision = strings.Repeat("f", 40)
	before := cfg.Matrix.Revision
	if p := buildMatrixConfig(context.Background(), cfg, "", "", deps); len(p) != 1 || p[0].Reason != "existing-pin-mismatch-not-overwritten" || cfg.Matrix.Revision != before {
		t.Fatal("builder silently changed existing source authority")
	}
}

func TestMatrixConfigNamesInvalidFieldType(t *testing.T) {
	path := filepath.Join(privateTestRoot(t), "config.json")
	writeFixture(t, path, `{"matrix":{"revision":17}}`)
	_, _, p := readMatrixConfigFile(path)
	if len(p) != 1 || p[0].Field != "matrix.revision" {
		t.Fatal("wrong field type is not actionable")
	}
}

func invokeMatrixConfigCLI(args []string, stdout, stderr io.Writer) int {
	if binary := os.Getenv("MATRIX_CONFIG_TEST_BINARY"); binary != "" {
		cmd := exec.Command(binary, append([]string{"matrix"}, args...)...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				return e.ExitCode()
			}
			return -1
		}
		return 0
	}
	return matrixConfigCLI(args, stdout, stderr)
}
func TestMatrixIssueRequiresCompleteFields(t *testing.T) {
	for _, data := range []string{`{"id":"synthetic","number":1,"title":"Synthetic"}`, `{"id":"synthetic","number":1,"title":"Synthetic","body":null}`} {
		d := configDeps()
		d.command = func(context.Context, string, string, []byte, ...string) ([]byte, error) { return []byte(data), nil }
		if _, p := fetchMatrixIssue(context.Background(), &Config{Inbox: "example/inbox"}, 1, d); len(p) == 0 {
			t.Fatal("incomplete issue was accepted for digest")
		}
	}
}
