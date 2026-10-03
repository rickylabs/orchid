package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func finalSourceFixture(t *testing.T, agent string) (*Coord, *Job, finalReportScope, finalSourceCalls) {
	t.Helper()
	c, j, _, _, _ := completionFixture(t)
	j.Agent = agent
	j.FinalReportManaged = true
	record := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
	var d dispatchBinding
	if readPrivateActionJSON(filepath.Join(record, "dispatch.json"), &d) != nil {
		t.Fatal("fixture dispatch")
	}
	d.Source = agent
	b, _ := json.Marshal(d)
	if os.WriteFile(filepath.Join(record, "dispatch.json"), b, 0600) != nil {
		t.Fatal("fixture dispatch write")
	}
	native := "thread-fixture"
	if agent == "opencode" {
		native = "ses_fixture"
	}
	if agent == "agy" {
		native = "11111111-1111-1111-1111-111111111111"
	}
	binding := map[string]any{"Repo": j.Repo, "NativeSessionID": native, "BriefDigest": strings.Repeat("b", 64), "Host": j.Host, "Route": map[string]string{"Transport": agent, "Provider": "fixture-provider", "Model": "fixture-model"}}
	if agent == "agy" {
		directory, _ := agyStoreDirectory("/fixture/issue-7", d.RunID)
		binding["NativeStore"] = &nativeStore{Source: "agy", Directory: directory}
	}
	b, _ = json.Marshal(binding)
	if os.WriteFile(filepath.Join(record, "binding.json"), b, 0600) != nil {
		t.Fatal("fixture private binding")
	}
	digest, err := finalBindingDigest(record, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	s := finalReportScope{SchemaVersion: 1, Key: j.DispatchKey, Destination: d.Issue, Host: j.Host, Source: agent, Repo: j.Repo, Cwd: "/fixture/issue-7", BindingDigest: digest, DispatchDigest: finalDispatchDigest(d)}
	a := completionAgent(j)
	a.AgentStatus = "working"
	a.AgentSession, _ = json.Marshal(map[string]string{"source": "herdr:" + agent, "agent": agent, "kind": "id", "value": native})
	if agent == "opencode" {
		j.OpenCode = &openCodeRun{Cwd: s.Cwd, SessionID: native}
	}
	calls := finalSourceCalls{agent: func(context.Context, string) (AgentInfo, error) { return a, nil }, openCode: func(context.Context, *Job) (bool, bool, error) { return true, false, nil }, agy: func(context.Context, *nativeStore) (string, nativeIdentityReason) { return native, "" }}
	return c, j, s, calls
}

func TestFinalReportAllNativeSourcesUseCertifiedBindingWithoutTerminalClaim(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "agy", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			c, j, s, calls := finalSourceFixture(t, agent)
			if err := c.finalSourceProof(context.Background(), j, s, calls); err != nil {
				t.Fatal("certified source unavailable", err)
			}
			// None of these fake native readers proves a final stop; publication
			// can still honor an explicit handoff while the worker is working.
			dir, err := c.finalPublicationDir(j.DispatchKey, true)
			if err != nil {
				t.Fatal(err)
			}
			if actionImmutableJSON(dir, "scope.json", s) != nil {
				t.Fatal("scope staging")
			}
			c.cfg.BotLogin = "fixture-bot"
			posts := 0
			c.finalCalls.proof = func(ctx context.Context, _ Host, j *Job, s finalReportScope) error {
				return c.finalSourceProof(ctx, j, s, calls)
			}
			c.finalCalls.report = func(context.Context, Host, finalReportScope) (string, error) { return "VISIBLE_REPORT_OK", nil }
			body := ""
			c.finalCalls.post = func(_ context.Context, d dispatchIssue, b string) (int64, error) {
				if d != s.Destination {
					t.Fatal("mirror title/target replaced bound inbox")
				}
				body = b
				posts++
				return 31, nil
			}
			c.finalCalls.comment = func(context.Context, dispatchIssue, int64) (finalPostedComment, error) {
				v := finalPostedComment{ID: 31, Body: body, IssueURL: "https://api.github.com/repos/" + s.Destination.Repo + "/issues/7", CreatedAt: time.Now().Truncate(time.Second)}
				v.User.Login = c.cfg.BotLogin
				return v, nil
			}
			if id, err := c.publishFinalReport(context.Background(), j); err != nil || id != 31 || posts != 1 {
				t.Fatal("native file handoff did not publish", err)
			}
			if len(c.st.CompletedRuns) != 0 {
				t.Fatal("report manufactured native Done")
			}
		})
	}
}

func TestFinalReportSourceRejectsChangedAuthorityAndOccupant(t *testing.T) {
	for _, name := range []string{"missing-id", "full-brief", "route", "dispatch-destination", "dispatch-model", "dispatch-host", "dispatch-pane", "pending", "identity", "identity-after", "agent", "label", "pane", "workspace", "cwd", "ready", "sequence", "changed-binding", "changed-native-id", "canceled"} {
		t.Run(name, func(t *testing.T) {
			c, j, s, calls := finalSourceFixture(t, "codex")
			record := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
			var binding map[string]any
			b, _ := os.ReadFile(filepath.Join(record, "binding.json"))
			_ = json.Unmarshal(b, &binding)
			if name == "missing-id" {
				delete(binding, "NativeSessionID")
			}
			if name == "full-brief" {
				binding["BriefDigest"] = strings.Repeat("a", 64)
			}
			if name == "route" {
				binding["Route"] = "other"
			}
			b, _ = json.Marshal(binding)
			_ = os.WriteFile(filepath.Join(record, "binding.json"), b, 0600)
			var d dispatchBinding
			_ = readPrivateActionJSON(filepath.Join(record, "dispatch.json"), &d)
			switch name {
			case "dispatch-destination":
				d.Issue.Number++
			case "dispatch-model":
				d.Model = "other"
			case "dispatch-host":
				d.Host = "other"
			case "dispatch-pane":
				d.Location.PaneID = "other"
			case "pending":
				d.State = "reserved"
			}
			b, _ = json.Marshal(d)
			_ = os.WriteFile(filepath.Join(record, "dispatch.json"), b, 0600)
			original := calls.agent
			reads := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls.agent = func(ctx context.Context, p string) (AgentInfo, error) {
				a, e := original(ctx, p)
				reads++
				switch name {
				case "identity":
					a.AgentSession = json.RawMessage(`{"source":"herdr:codex","agent":"codex","kind":"id","value":"other"}`)
				case "identity-after":
					if reads == 2 {
						a.AgentSession = nil
					}
				case "agent":
					a.Agent = "claude"
				case "label":
					a.Name = "other"
				case "pane":
					a.PaneID = "other"
				case "workspace":
					a.WorkspaceID = "other"
				case "cwd":
					a.Cwd = "/fixture/other"
				case "ready":
					a.InteractiveReady = false
				case "sequence":
					a.StateChangeSeq += uint64(reads)
				case "changed-binding":
					if reads == 2 {
						binding["BriefDigest"] = "other"
						b, _ := json.Marshal(binding)
						_ = os.WriteFile(filepath.Join(record, "binding.json"), b, 0600)
					}
				case "changed-native-id":
					if reads == 2 {
						binding["NativeSessionID"] = "other-thread"
						b, _ := json.Marshal(binding)
						_ = os.WriteFile(filepath.Join(record, "binding.json"), b, 0600)
					}
				case "canceled":
					cancel()
				}
				return a, e
			}
			if err := c.finalSourceProof(ctx, j, s, calls); err == nil {
				t.Fatal("changed native scope certified report")
			}
		})
	}
}

func TestFinalReportOtherAdapterProofCannotBeSubstituted(t *testing.T) {
	for _, agent := range []string{"opencode", "agy"} {
		for _, name := range []string{"foreign", "unsupported", "unconfirmed", "changed-private-store"} {
			t.Run(agent+"/"+name, func(t *testing.T) {
				c, j, s, calls := finalSourceFixture(t, agent)
				if agent == "opencode" {
					calls.openCode = func(context.Context, *Job) (bool, bool, error) {
						switch name {
						case "foreign":
							j.OpenCode.SessionID = "ses_foreign"
						case "unsupported":
							return false, false, errFinalPublication
						case "unconfirmed":
							return false, false, nil
						case "changed-private-store":
							j.OpenCode.Cwd = "/fixture/foreign"
						}
						return true, false, nil
					}
				}
				if agent == "agy" {
					calls.agy = func(context.Context, *nativeStore) (string, nativeIdentityReason) {
						if name == "unsupported" {
							return "", nativeUnsupported
						}
						if name == "unconfirmed" {
							return "", nativeUnavailable
						}
						return "22222222-2222-2222-2222-222222222222", ""
					}
				}
				if err := c.finalSourceProof(context.Background(), j, s, calls); err == nil {
					t.Fatal("adapter substitution certified report")
				}
			})
		}
	}
}

func finalReportRepository(t *testing.T) (Host, string) {
	t.Helper()
	cwd := t.TempDir()
	if exec.Command("git", "init", "--quiet", cwd).Run() != nil {
		t.Fatal("fixture init")
	}
	return Host{}, cwd
}

func TestFinalReportFileHandoffStagesExcludedFreshFiles(t *testing.T) {
	for _, name := range []string{"clean", "existing", "temp", "tracked", "symlink", "directory", "exclude", "exclude-parent-symlink"} {
		t.Run(name, func(t *testing.T) {
			h, cwd := finalReportRepository(t)
			path := filepath.Join(cwd, finalReportFile)
			switch name {
			case "existing":
				_ = os.WriteFile(path, []byte("stale"), 0600)
			case "temp":
				_ = os.WriteFile(filepath.Join(cwd, finalReportTemp), []byte("stale"), 0600)
			case "tracked":
				_ = os.WriteFile(path, []byte("tracked"), 0600)
				if exec.Command("git", "-C", cwd, "add", finalReportFile).Run() != nil {
					t.Fatal("fixture add")
				}
			case "symlink":
				_ = os.Symlink(filepath.Join(cwd, "absent"), path)
			case "directory":
				_ = os.Mkdir(path, 0700)
			case "exclude":
				exclude := filepath.Join(cwd, ".git", "info", "exclude")
				_ = os.Remove(exclude)
				_ = os.Mkdir(exclude, 0700)
			case "exclude-parent-symlink":
				info := filepath.Join(cwd, ".git", "info")
				_ = os.Rename(info, info+"-other")
				_ = os.Symlink(info+"-other", info)
			}
			err := h.stageFinalReportFiles(context.Background(), cwd)
			if name != "clean" {
				if err == nil {
					t.Fatal("unsafe/stale path reused")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, filename := range []string{finalReportFile, finalReportTemp} {
				if exec.Command("git", "-C", cwd, "check-ignore", "-q", filename).Run() != nil {
					t.Fatal("handoff artifacts enter Git")
				}
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("staging created false ready report")
			}
		})
	}
}

func TestFinalReportFileReadRejectsUnsafeHandoff(t *testing.T) {
	for _, name := range []string{"clean", "missing", "temp-only", "tracked", "unexcluded", "symlink", "fifo", "directory", "hardlink", "writable", "oversize", "invalid-utf8"} {
		t.Run(name, func(t *testing.T) {
			h, cwd := finalReportRepository(t)
			if h.stageFinalReportFiles(context.Background(), cwd) != nil {
				t.Fatal("staging")
			}
			path := filepath.Join(cwd, finalReportFile)
			if name != "missing" && name != "temp-only" {
				_ = os.WriteFile(path, []byte("visible\n"), 0644)
			}
			switch name {
			case "temp-only":
				_ = os.WriteFile(filepath.Join(cwd, finalReportTemp), []byte("visible"), 0644)
			case "tracked":
				if exec.Command("git", "-C", cwd, "add", "-f", finalReportFile).Run() != nil {
					t.Fatal("fixture add")
				}
			case "unexcluded":
				_ = os.WriteFile(filepath.Join(cwd, ".git", "info", "exclude"), nil, 0600)
			case "symlink":
				_ = os.Rename(path, filepath.Join(cwd, "other"))
				_ = os.Symlink(filepath.Join(cwd, "other"), path)
			case "fifo":
				_ = os.Remove(path)
				_ = syscall.Mkfifo(path, 0600)
			case "directory":
				_ = os.Remove(path)
				_ = os.Mkdir(path, 0700)
			case "hardlink":
				_ = os.Link(path, filepath.Join(cwd, "other"))
			case "writable":
				_ = os.Chmod(path, 0666)
			case "oversize":
				_ = os.WriteFile(path, []byte(strings.Repeat("x", 60001)), 0644)
			case "invalid-utf8":
				_ = os.WriteFile(path, []byte{0xff}, 0644)
			}
			body, err := h.readFinalReport(context.Background(), cwd)
			if name == "clean" {
				if err != nil || body != "visible\n" {
					t.Fatal("valid handoff unreadable", err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe handoff read accepted")
			}
		})
	}
}

func TestFinalReportFileReadRejectsReplacementDuringRead(t *testing.T) {
	for _, name := range []string{"opened-file", "path-replaced", "opened-identity"} {
		t.Run(name, func(t *testing.T) {
			h, cwd := finalReportRepository(t)
			if h.stageFinalReportFiles(context.Background(), cwd) != nil {
				t.Fatal("staging")
			}
			path := filepath.Join(cwd, finalReportFile)
			if os.WriteFile(path, []byte("visible report"), 0644) != nil {
				t.Fatal("report")
			}
			// Simulate a concurrent writer at the file-system read boundary.
			// The read-only production script itself remains unchanged.
			prefix := "import os,stat\nreal_read=os.read\ndef changed_read(fd,n):\n if not stat.S_ISREG(os.fstat(fd).st_mode): return real_read(fd,n)\n data=real_read(fd,n)\n"
			switch name {
			case "opened-file":
				prefix += " with open('.divybot-final-report.md','w') as f: f.write('changed')\n"
			case "path-replaced":
				prefix += " os.rename('.divybot-final-report.md','old-report')\n with open('.divybot-final-report.md','w') as f: f.write('visible report')\n"
			case "opened-identity":
				prefix = "import os\nreal_open=os.open\ndef changed_open(p,flags):\n os.rename(p,'old-report')\n with open(p,'w') as f: f.write('visible report')\n return real_open(p,flags)\nos.open=changed_open\n"
			}
			if name != "opened-identity" {
				prefix += " return data\nos.read=changed_read\n"
			}
			cmd := exec.Command("python3", "-c", prefix+finalReportReadScript, cwd)
			if output, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("changed file read accepted: %s", output)
			}
		})
	}
}

func TestFinalReportStageBindsAllFourLaunchSourcesBeforeSeat(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "agy", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			c, j, s, _ := finalSourceFixture(t, agent)
			h, cwd := finalReportRepository(t)
			h.Name = j.Host
			r, _, err := loadNativeBindingReceipt(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, j, c.cfg.Inbox, nil, agent)
			if err != nil {
				t.Fatal(err)
			}
			if c.stageFinalReport(context.Background(), h, cwd, j.DispatchKey, r) != nil {
				t.Fatal("producer launch did not stage source")
			}
			dir, _ := c.finalPublicationDir(j.DispatchKey, false)
			var saved finalReportScope
			if finalPrivateRead(dir, "scope.json", &saved) != nil || saved.Source != agent || saved.Destination != s.Destination || saved.BindingDigest != s.BindingDigest {
				t.Fatal("prelaunch scope lost source, full brief or destination")
			}
			if instruction := finalReportInstruction(saved.Destination, j.DispatchKey); !strings.Contains(instruction, finalReportFile) || strings.Contains(instruction, finalCommentBodyFile) || strings.Contains(instruction, finalCommentMarker(j.DispatchKey)) {
				t.Fatal("producer launch delegated marker ownership")
			}
			if err := c.stageFinalReport(context.Background(), h, cwd, j.DispatchKey, r); err == nil {
				t.Fatal("staging reused a private launch scope")
			}
		})
	}
}

func TestFinalReportOccupantAndLocationForEveryAdapter(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "agy", "opencode"} {
		for _, name := range []string{"agent", "label", "pane", "workspace", "cwd", "ready", "dispatch-pane", "dispatch-workspace", "dispatch-host"} {
			t.Run(agent+"/"+name, func(t *testing.T) {
				c, j, s, calls := finalSourceFixture(t, agent)
				if strings.HasPrefix(name, "dispatch-") {
					record := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record")
					var d dispatchBinding
					_ = readPrivateActionJSON(filepath.Join(record, "dispatch.json"), &d)
					switch name {
					case "dispatch-pane":
						d.Location.PaneID = "other"
					case "dispatch-workspace":
						d.Location.WorkspaceID = "other"
					case "dispatch-host":
						d.Host = "other"
					}
					b, _ := json.Marshal(d)
					_ = os.WriteFile(filepath.Join(record, "dispatch.json"), b, 0600)
				} else {
					original := calls.agent
					calls.agent = func(ctx context.Context, p string) (AgentInfo, error) {
						a, e := original(ctx, p)
						switch name {
						case "agent":
							a.Agent = "other"
						case "label":
							a.Name = "other"
						case "pane":
							a.PaneID = "other"
						case "workspace":
							a.WorkspaceID = "other"
						case "cwd":
							a.Cwd = "/fixture/other"
						case "ready":
							a.InteractiveReady = false
						}
						return a, e
					}
				}
				if c.finalSourceProof(context.Background(), j, s, calls) == nil {
					t.Fatal("foreign adapter occupant/location accepted")
				}
			})
		}
	}
}

func TestFinalReportAGYStoreCannotMoveBeforeOrDuringProof(t *testing.T) {
	for _, when := range []string{"before", "during"} {
		t.Run(when, func(t *testing.T) {
			c, j, s, calls := finalSourceFixture(t, "agy")
			change := func() {
				path := filepath.Join(c.cfg.Matrix.ReceiptRoot, j.DispatchKey, "record", "binding.json")
				var binding map[string]any
				b, _ := os.ReadFile(path)
				_ = json.Unmarshal(b, &binding)
				binding["NativeStore"] = &nativeStore{Source: "agy", Directory: "/fixture/foreign/.divybot-native/" + j.DispatchKey + "/agy"}
				b, _ = json.Marshal(binding)
				_ = os.WriteFile(path, b, 0600)
			}
			if when == "before" {
				change()
			} else {
				original := calls.agy
				calls.agy = func(ctx context.Context, s *nativeStore) (string, nativeIdentityReason) {
					id, reason := original(ctx, s)
					change()
					return id, reason
				}
			}
			if c.finalSourceProof(context.Background(), j, s, calls) == nil {
				t.Fatal("foreign native store certified final")
			}
		})
	}
}

func TestFinalReportOpenCodeIdentityCannotChangeAfterNativeRead(t *testing.T) {
	c, j, s, calls := finalSourceFixture(t, "opencode")
	original := calls.agent
	reads := 0
	calls.agent = func(ctx context.Context, p string) (AgentInfo, error) {
		v, err := original(ctx, p)
		reads++
		if reads == 2 {
			j.OpenCode.SessionID = "ses_foreign"
		}
		return v, err
	}
	if c.finalSourceProof(context.Background(), j, s, calls) == nil {
		t.Fatal("native session changed after its proof")
	}
}
